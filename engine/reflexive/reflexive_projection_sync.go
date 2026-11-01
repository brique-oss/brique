/*
 * Copyright 2026 Nicolas Cassan
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package reflexive

// reflexive_projection_sync.go
//
// Incremental maintenance of the meaning projection from file fingerprints.
//
// Every indexed element row records the size, mtime and SHA-256 of the file it
// came from; every examined directory records its mtime. A sync walks the
// context tree and re-examines only what changed:
//   - a directory whose mtime is unchanged is skipped entirely (no per-file
//     work), which keeps a pass over a very large instance proportional to the
//     number of contexts, not files;
//   - in a changed directory, a file is read only when its size or mtime moved,
//     and reindexed only when its content hash really changed;
//   - files that vanished are removed from the projection, together with whole
//     contexts that vanished.
// Writes are applied in bounded batches (one transaction each) taking the
// projection lock only for the batch, so reads and other writers stay live.
// "verify" ignores directory mtimes (still trusting per-file size+mtime) to
// catch in-place edits that leave a directory untouched.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/shared"
)

const syncBatchElements = 500

var (
	errSyncStopped  = errors.New("projection sync stopped")
	errSyncReplaced = errors.New("projection replaced during sync")
)

// racyWindow is how recent a modification must be to be distrusted. A file
// edited again within the timestamp granularity of the filesystem (1-2 s on
// some) keeps the same mtime, so a stamp taken this soon after a write cannot
// prove the content is unchanged; such a file is re-read on the next pass.
// (This is git's "racily clean" problem.)
const racyWindow = 2 * time.Second

// fileStamp is the cheap change detector kept next to the content hash:
// size, mtime, and the inode and ctime, which user space cannot forge (a
// replaced file has another inode; any write moves the ctime even if the
// mtime was put back).
type fileStamp struct {
	Size    int64
	MtimeNS int64
	CtimeNS int64
	Ino     int64
	Racy    bool
}

func statStamp(path string) (fileStamp, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return fileStamp{}, err
	}
	ctime, ino := fileIdentity(fi)
	age := time.Since(fi.ModTime())
	return fileStamp{
		Size: fi.Size(), MtimeNS: fi.ModTime().UnixNano(), CtimeNS: ctime, Ino: ino,
		Racy: age < racyWindow, // also true for an mtime in the future
	}, nil
}

func fingerprintBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// stampElement records the fingerprint of the file an element row came from.
func stampElement(exec sqlExec, rowID int64, content []byte, st fileStamp) error {
	_, err := exec.Exec(`UPDATE elements SET src_size=?, src_mtime_ns=?, src_ctime_ns=?, src_ino=?, src_racy=?, fingerprint=? WHERE row_id=?`,
		st.Size, st.MtimeNS, st.CtimeNS, st.Ino, boolInt(st.Racy), fingerprintBytes(content), rowID)
	return err
}

// syncResult reports what one pass did.
type syncResult struct {
	Contexts     int
	DirsScanned  int
	DirsSkipped  int
	Added        int
	Changed      int
	Unchanged    int
	Removed      int
	AffectedKeys map[string]struct{}
}

type syncOpKind uint8

const (
	opIndex      syncOpKind = iota // (re)index one element
	opStamp                        // content unchanged: refresh size/mtime only
	opDelete                       // drop one element row
	opDropCtx                      // drop a vanished context subtree
	opDropDir                      // forget a vanished directory (rows of one type)
	opMarkDir                      // record a fully examined directory
	opMarkDirDel                   // forget a scan_dirs entry
)

type syncOp struct {
	kind      syncOpKind
	ctxID     string
	etype     string
	name      string
	rowID     int64
	doc       map[string]any
	content   []byte
	stamp     fileStamp
	isNew     bool
	dirPath   string
	dirKind   string
	parentCtx string
}

type knownElement struct {
	rowID       int64
	size        sql.NullInt64
	mtimeNS     sql.NullInt64
	ctimeNS     sql.NullInt64
	ino         sql.NullInt64
	racy        sql.NullInt64
	fingerprint sql.NullString
}

// matches reports whether the file still looks exactly as it did when it was
// fingerprinted. Anything unknown (NULL, from an older projection) or racy
// does not match, which only costs one re-read: an identical hash is just
// re-stamped.
func (k knownElement) matches(st fileStamp) bool {
	return k.fingerprint.Valid && k.size.Valid && k.mtimeNS.Valid && k.ctimeNS.Valid && k.ino.Valid &&
		k.racy.Valid && k.racy.Int64 == 0 &&
		k.size.Int64 == st.Size && k.mtimeNS.Int64 == st.MtimeNS &&
		k.ctimeNS.Int64 == st.CtimeNS && k.ino.Int64 == st.Ino
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

type projectionSyncer struct {
	l      *ReflexiveLoop
	db     *sql.DB
	root   string
	verify bool
	gen    uint64
	ops    []syncOp
	res    syncResult

	// batchRows are the rows (re)indexed by the batch being applied; vocab
	// accumulates what they observed until the final vocabulary update.
	batchRows []int64
	vocab     map[string]any
}

// syncProjection brings the projection in line with the files on disk. It
// creates the projection when absent. It never blocks other users of the
// projection for longer than one batch.
func (l *ReflexiveLoop) syncProjection(verify bool) (syncResult, error) {
	res := syncResult{AffectedKeys: map[string]struct{}{}}
	if l == nil || l.frame == nil || l.frame.ContextDir == "" {
		return res, errors.New("no context directory")
	}
	if err := ensureProjectionDir(l.frame.ContextDir); err != nil {
		return res, err
	}
	db, err := openSQLite(dbPath(l.frame.ContextDir))
	if err != nil {
		return res, err
	}
	defer db.Close()

	unlock := l.lockProjection()
	gen := l.projectionGen.Load()
	err = createMeaningSchema(db)
	if err == nil {
		err = ensureSyncColumns(db)
	}
	unlock()
	if err != nil {
		return res, err
	}

	s := &projectionSyncer{l: l, db: db, root: l.frame.ContextDir, verify: verify, gen: gen, res: res, vocab: map[string]any{}}
	s.res.AffectedKeys = res.AffectedKeys
	if _, err := os.Stat(filepath.Join(s.root, ContextDescriptorFilename)); err != nil {
		return s.res, fmt.Errorf("root context descriptor not found: %w", err)
	}
	if err := s.walkContext(s.root, shared.RootContextID, ""); err != nil {
		return s.res, err
	}
	if err := s.flush(); err != nil {
		return s.res, err
	}
	if err := s.applyVocabulary(); err != nil {
		return s.res, err
	}
	return s.res, nil
}

func (s *projectionSyncer) stopped() bool {
	select {
	case <-s.l.done:
		return true
	default:
		return false
	}
}

func (s *projectionSyncer) queue(op syncOp) error {
	s.ops = append(s.ops, op)
	if len(s.ops) >= syncBatchElements {
		return s.flush()
	}
	return nil
}

// flush applies the queued operations in one transaction, under the
// projection lock, then releases the lock.
func (s *projectionSyncer) flush() error {
	if len(s.ops) == 0 {
		return nil
	}
	if s.stopped() {
		return errSyncStopped
	}
	unlock := s.l.lockProjection()
	defer unlock()
	if s.l.projectionGen.Load() != s.gen {
		return errSyncReplaced
	}
	tx, err := s.db.BeginTx(context.Background(), &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for i := range s.ops {
		if err := s.applyOp(tx, &s.ops[i]); err != nil {
			return err
		}
	}
	if len(s.batchRows) > 0 {
		patch, err := buildObservedVocabularyForRows(tx, s.batchRows)
		if err != nil {
			return err
		}
		s.vocab = deepMergeJSON(s.vocab, patch)
		s.batchRows = s.batchRows[:0]
	}
	if err := setIndexMeta(tx, map[string]string{
		"last_update_ms": fmt.Sprintf("%d", time.Now().UTC().UnixMilli()),
	}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.ops = s.ops[:0]
	return nil
}

func (s *projectionSyncer) applyOp(tx *sql.Tx, op *syncOp) error {
	switch op.kind {
	case opIndex:
		rowID, err := upsertElement(tx, ElementRow{
			ElementID:   stableElementID(op.ctxID, op.etype, op.name),
			ElementType: op.etype, Name: op.name, CtxID: op.ctxID,
		})
		if err != nil {
			return err
		}
		if err := stampElement(tx, rowID, op.content, op.stamp); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM kv WHERE element_row=?`, rowID); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM links WHERE src_row=?`, rowID); err != nil {
			return err
		}
		kvRows, paths, err := flattenKV(rowID, op.doc)
		if err != nil {
			return err
		}
		if err := ensurePaths(tx, paths); err != nil {
			return err
		}
		if err := insertKV(tx, kvRows); err != nil {
			return err
		}
		for _, p := range paths {
			if p.Kind == "key" {
				s.res.AffectedKeys[p.Text] = struct{}{}
			}
		}
		s.batchRows = append(s.batchRows, rowID)
		if op.isNew {
			s.res.Added++
		} else {
			s.res.Changed++
		}
	case opStamp:
		return stampElement(tx, op.rowID, op.content, op.stamp)
	case opDelete:
		if _, err := tx.Exec(`DELETE FROM elements WHERE row_id=?`, op.rowID); err != nil {
			return err
		}
		s.res.Removed++
	case opDropDir:
		r, err := tx.Exec(`DELETE FROM elements WHERE ctx_id=? AND element_type=?`, op.ctxID, op.etype)
		if err != nil {
			return err
		}
		if n, _ := r.RowsAffected(); n > 0 {
			s.res.Removed += int(n)
		}
		_, err = tx.Exec(`DELETE FROM scan_dirs WHERE dir_path=?`, op.dirPath)
		return err
	case opDropCtx:
		// '/' + 1 == '0': the range covers exactly the ctx and its descendants.
		lo, hi := op.ctxID+"/", op.ctxID+"0"
		r, err := tx.Exec(`DELETE FROM elements WHERE ctx_id=? OR (ctx_id>=? AND ctx_id<?)`, op.ctxID, lo, hi)
		if err != nil {
			return err
		}
		if n, _ := r.RowsAffected(); n > 0 {
			s.res.Removed += int(n)
		}
		_, err = tx.Exec(`DELETE FROM scan_dirs WHERE ctx_id=? OR (ctx_id>=? AND ctx_id<?)`, op.ctxID, lo, hi)
		return err
	case opMarkDir:
		_, err := tx.Exec(`INSERT INTO scan_dirs(dir_path, kind, ctx_id, parent_ctx, mtime_ns) VALUES(?,?,?,?,?)
			ON CONFLICT(dir_path) DO UPDATE SET kind=excluded.kind, ctx_id=excluded.ctx_id,
			parent_ctx=excluded.parent_ctx, mtime_ns=excluded.mtime_ns`,
			op.dirPath, op.dirKind, op.ctxID, op.parentCtx, op.stamp.MtimeNS)
		return err
	case opMarkDirDel:
		_, err := tx.Exec(`DELETE FROM scan_dirs WHERE dir_path=?`, op.dirPath)
		return err
	}
	return nil
}

func (s *projectionSyncer) storedDirMtime(dirPath string) (int64, bool) {
	var m int64
	if err := s.db.QueryRow(`SELECT mtime_ns FROM scan_dirs WHERE dir_path=?`, dirPath).Scan(&m); err != nil {
		return 0, false
	}
	return m, true
}

func parentCtxOf(ctxID string) string {
	if i := strings.LastIndex(ctxID, "/"); i > 0 {
		return ctxID[:i]
	}
	return ""
}

// walkContext examines one context and recurses into its child contexts.
func (s *projectionSyncer) walkContext(dir, ctxID, name string) error {
	if s.stopped() {
		return errSyncStopped
	}
	s.res.Contexts++

	for _, td := range elementTypeDirs {
		abs := filepath.Join(dir, td.sub)
		fi, err := os.Stat(abs)
		if err != nil || !fi.IsDir() {
			// Gone since the last pass? Forget what it held.
			if _, had := s.storedDirMtime(abs); had {
				if err := s.queue(syncOp{kind: opDropDir, ctxID: ctxID, etype: td.typ, dirPath: abs}); err != nil {
					return err
				}
			}
			continue
		}
		mtime := fi.ModTime().UnixNano()
		if stored, had := s.storedDirMtime(abs); had && stored == mtime && !s.verify {
			s.res.DirsSkipped++
			continue
		}
		if err := s.syncTypeDir(ctxID, td.typ, td.ext, abs, mtime); err != nil {
			return err
		}
	}

	// Child set: re-derived only when this context's directory changed.
	dfi, derr := os.Stat(dir)
	if derr != nil {
		return nil
	}
	dmtime := dfi.ModTime().UnixNano()
	children := childContextDirs(dir)
	if stored, had := s.storedDirMtime(dir); !had || stored != dmtime || s.verify {
		onDisk := make(map[string]struct{}, len(children))
		for _, c := range children {
			onDisk[ctxID+"/"+c] = struct{}{}
		}
		known, err := s.knownChildContexts(ctxID)
		if err != nil {
			return err
		}
		for _, kid := range known {
			if _, ok := onDisk[kid]; !ok {
				if err := s.queue(syncOp{kind: opDropCtx, ctxID: kid}); err != nil {
					return err
				}
			}
		}
		if err := s.queue(syncOp{kind: opMarkDir, dirPath: dir, dirKind: "ctx", ctxID: ctxID,
			parentCtx: parentCtxOf(ctxID), stamp: fileStamp{MtimeNS: dmtime}}); err != nil {
			return err
		}
	}
	for _, c := range children {
		if err := s.walkContext(filepath.Join(dir, c), ctxID+"/"+c, c); err != nil {
			return err
		}
	}
	return nil
}

func (s *projectionSyncer) knownChildContexts(ctxID string) ([]string, error) {
	rows, err := s.db.Query(`SELECT ctx_id FROM scan_dirs WHERE kind='ctx' AND parent_ctx=?`, ctxID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// syncTypeDir compares one element directory with the rows it produced.
func (s *projectionSyncer) syncTypeDir(ctxID, etype, ext, abs string, dirMtime int64) error {
	s.res.DirsScanned++
	known := map[string]knownElement{}
	rows, err := s.db.Query(`SELECT name,row_id,src_size,src_mtime_ns,src_ctime_ns,src_ino,src_racy,fingerprint FROM elements WHERE ctx_id=? AND element_type=?`, ctxID, etype)
	if err != nil {
		return err
	}
	for rows.Next() {
		var name string
		var k knownElement
		if err := rows.Scan(&name, &k.rowID, &k.size, &k.mtimeNS, &k.ctimeNS, &k.ino, &k.racy, &k.fingerprint); err != nil {
			_ = rows.Close()
			return err
		}
		known[name] = k
	}
	if err := rows.Close(); err != nil {
		return err
	}

	ents, err := os.ReadDir(abs)
	if err != nil {
		return nil // unreadable now: leave the directory unmarked, retry next pass
	}
	seen := make(map[string]struct{}, len(ents))
	racyDir := false // some file was modified too recently to be trusted
	for _, e := range ents {
		fn := strings.TrimSpace(e.Name())
		if e.IsDir() || fn == "" || strings.HasPrefix(fn, ".") ||
			!strings.HasSuffix(strings.ToLower(fn), strings.ToLower(ext)) {
			continue
		}
		name := strings.TrimSuffix(fn, ext)
		if name == "" {
			continue
		}
		seen[name] = struct{}{}
		path := filepath.Join(abs, fn)
		stamp, err := statStamp(path)
		if err != nil {
			continue
		}
		if stamp.Racy {
			racyDir = true
		}
		k, isKnown := known[name]
		if err := s.examineFile(ctxID, etype, name, path, stamp, k, isKnown); err != nil {
			return err
		}
	}

	names := make([]string, 0, len(known))
	for n := range known {
		if _, ok := seen[n]; !ok {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		if err := s.queue(syncOp{kind: opDelete, rowID: known[n].rowID}); err != nil {
			return err
		}
	}
	if racyDir {
		// Leave the directory unmarked: the next pass looks at it again, by
		// which time the racy files are old enough to be trusted.
		return nil
	}
	return s.queue(syncOp{kind: opMarkDir, dirPath: abs, dirKind: "dir", ctxID: ctxID,
		parentCtx: parentCtxOf(ctxID), stamp: fileStamp{MtimeNS: dirMtime}})
}

// examineFile decides what one descriptor file needs: nothing (it still looks
// like what was fingerprinted), a re-stamp (touched but same bytes), a
// reindex (new or changed content), or removal (it stopped being an element).
func (s *projectionSyncer) examineFile(ctxID, etype, name, path string, stamp fileStamp, k knownElement, isKnown bool) error {
	if isKnown && k.matches(stamp) {
		s.res.Unchanged++
		return nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var doc map[string]any
	if json.Unmarshal(content, &doc) != nil || doc == nil || !isBriqueElementDocument(doc) {
		if isKnown { // became something that is not an element
			return s.queue(syncOp{kind: opDelete, rowID: k.rowID})
		}
		return nil
	}
	if isKnown && k.fingerprint.Valid && k.fingerprint.String == fingerprintBytes(content) {
		s.res.Unchanged++
		return s.queue(syncOp{kind: opStamp, rowID: k.rowID, stamp: stamp, content: content})
	}
	return s.queue(syncOp{kind: opIndex, ctxID: ctxID, etype: etype, name: name,
		doc: doc, content: content, stamp: stamp, isNew: !isKnown})
}

// syncHints refreshes exactly the named elements: one stat, and one read only
// if the file moved. It never lists a directory, so an edit inside a huge
// matter/ folder costs the same as in an empty one. ok is false when a hint
// cannot be resolved (unknown context, invalid name): the caller then falls
// back to a regular pass.
func (l *ReflexiveLoop) syncHints(hints []junction.ProjectionHint) (res syncResult, ok bool, err error) {
	res = syncResult{AffectedKeys: map[string]struct{}{}}
	if err := ensureProjectionDir(l.frame.ContextDir); err != nil {
		return res, false, err
	}
	db, err := openSQLite(dbPath(l.frame.ContextDir))
	if err != nil {
		return res, false, err
	}
	defer db.Close()
	unlock := l.lockProjection()
	gen := l.projectionGen.Load()
	err = createMeaningSchema(db)
	if err == nil {
		err = ensureSyncColumns(db)
	}
	unlock()
	if err != nil {
		return res, false, err
	}
	s := &projectionSyncer{l: l, db: db, root: l.frame.ContextDir, gen: gen, res: res, vocab: map[string]any{}}
	s.res.AffectedKeys = res.AffectedKeys
	for _, h := range hints {
		path, rerr := resolveDescriptorAbsFromRoot(s.root, h.CtxID, h.ElementType, h.Name)
		if rerr != nil {
			return s.res, false, nil
		}
		var k knownElement
		isKnown := true
		qerr := db.QueryRow(`SELECT row_id,src_size,src_mtime_ns,src_ctime_ns,src_ino,src_racy,fingerprint FROM elements WHERE ctx_id=? AND element_type=? AND name=?`,
			h.CtxID, h.ElementType, h.Name).Scan(&k.rowID, &k.size, &k.mtimeNS, &k.ctimeNS, &k.ino, &k.racy, &k.fingerprint)
		if qerr == sql.ErrNoRows {
			isKnown = false
		} else if qerr != nil {
			return s.res, false, qerr
		}
		stamp, serr := statStamp(path)
		if serr != nil { // gone
			if isKnown {
				if err := s.queue(syncOp{kind: opDelete, rowID: k.rowID}); err != nil {
					return s.res, false, err
				}
			}
			continue
		}
		if err := s.examineFile(h.CtxID, h.ElementType, h.Name, path, stamp, k, isKnown); err != nil {
			return s.res, false, err
		}
	}
	if err := s.flush(); err != nil {
		return s.res, false, err
	}
	if err := s.applyVocabulary(); err != nil {
		return s.res, false, err
	}
	return s.res, true, nil
}

// applyVocabulary merges what the reindexed elements observed into the
// vocabulary, add-only like meaning.update, and writes the vocabulary files
// only when something is actually new.
func (s *projectionSyncer) applyVocabulary() error {
	if len(s.vocab) == 0 {
		return nil
	}
	unlock := s.l.lockProjection()
	defer unlock()
	if s.l.projectionGen.Load() != s.gen {
		return errSyncReplaced
	}
	cur, _ := loadVocabulary(s.root)
	curNorm := normalizeVocabularyLeafLists(cur)
	next := normalizeVocabularyLeafLists(deepMergeJSON(cur, s.vocab))
	if len(cur) > 0 && reflect.DeepEqual(curNorm, next) {
		return nil // every observed path and value is already known
	}
	tx, err := s.db.BeginTx(context.Background(), &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := createVocabularySchema(tx); err != nil {
		return err
	}
	if _, err := vocabularyApplyAddOnly(tx, s.vocab); err != nil {
		return err
	}
	if err := writeVocabularyFiles(s.root, next, 0o644); err != nil {
		return err
	}
	return tx.Commit()
}

// syncRequested reports whether a meaning.update asks for change discovery
// (params.sync: true) instead of refreshing explicitly named elements.
func syncRequested(params map[string]any) bool {
	v, _ := params[circulation.KeySync].(bool)
	return v
}

// capSQLiteSync serves meaning.update {sync: true, verify?: bool}: apply to the
// projection whatever changed on disk since it was last examined.
func (l *ReflexiveLoop) capSQLiteSync(in circulation.Intention) {
	start := time.Now()
	verify, _ := in.Params[circulation.KeyVerify].(bool)
	res, err := l.syncProjection(verify)
	if err != nil {
		reason := "sync_failed"
		code := circulation.ValueCodeInternal
		switch {
		case errors.Is(err, errSyncReplaced):
			reason, code = "projection_replaced", circulation.ValueCodeUnavailable
		case errors.Is(err, errSyncStopped):
			reason, code = "stopped", circulation.ValueCodeUnavailable
		}
		l.emitResponseError(errorResp(in, code, map[string]any{circulation.KeyReason: reason}, err.Error()))
		return
	}
	l.emitResponseOK(in, map[string]any{
		"contexts_visited":    res.Contexts,
		"directories_scanned": res.DirsScanned,
		"directories_skipped": res.DirsSkipped,
		"added":               res.Added,
		"changed":             res.Changed,
		"removed":             res.Removed,
		"unchanged":           res.Unchanged,
		"verify":              verify,
		"duration_ms":         time.Since(start).Milliseconds(),
	})
}

// notifyProjectionChange forwards a "descriptors changed" hint to the root of
// the instance through the shared registry. It never blocks. With no hint it
// means "something changed": the root runs a regular fast pass.
func (l *ReflexiveLoop) notifyProjectionChange(hints ...junction.ProjectionHint) {
	if l == nil || l.frame == nil {
		return
	}
	if n, ok := l.frame.CtxCommReg.(junction.ProjectionNotifier); ok {
		n.NotifyProjectionChange(hints...)
	}
}

// maxPendingHints bounds the memory of unprocessed hints; past it the root
// simply does a regular pass, which covers any number of changes.
const maxPendingHints = 4096

// kickProjectionSync queues hints for the sync loop and wakes it. Requests
// arriving while one is pending are merged.
func (l *ReflexiveLoop) kickProjectionSync(hints ...junction.ProjectionHint) {
	l.hintMu.Lock()
	if len(hints) == 0 || l.hintFull {
		l.hintFull = true
	} else {
		if l.hints == nil {
			l.hints = make(map[junction.ProjectionHint]struct{})
		}
		for _, h := range hints {
			l.hints[h] = struct{}{}
		}
		if len(l.hints) > maxPendingHints {
			l.hintFull = true
		}
	}
	if l.hintFull {
		l.hints = nil
	}
	l.hintMu.Unlock()
	select {
	case l.syncKick <- struct{}{}:
	default:
	}
}

// takeHints returns and clears what was queued since the last pass.
func (l *ReflexiveLoop) takeHints() (hints []junction.ProjectionHint, full bool) {
	l.hintMu.Lock()
	defer l.hintMu.Unlock()
	full = l.hintFull
	for h := range l.hints {
		hints = append(hints, h)
	}
	l.hints, l.hintFull = nil, false
	return hints, full
}

// editSyncDebounce lets a burst of edits collapse into one pass.
const editSyncDebounce = 100 * time.Millisecond

// projectionSyncLoop keeps the projection up to date when configured: a fast
// pass every ProjectionSyncInterval, a slower verifying pass every
// ProjectionVerifyInterval, and a prompt refresh after descriptors changed
// (ProjectionSyncOnEdit): precisely the named elements when the writer said
// which, a regular fast pass otherwise. It runs on the root only (the
// projection lives there) and never reports failures to callers: a pass that
// cannot complete (rebuild in progress, shutdown) is retried on the next tick
// or edit.
func (l *ReflexiveLoop) projectionSyncLoop() {
	var fastC, verifyC <-chan time.Time
	if l.cfg.ProjectionSyncInterval > 0 {
		t := time.NewTicker(l.cfg.ProjectionSyncInterval)
		defer t.Stop()
		fastC = t.C
	}
	if l.cfg.ProjectionVerifyInterval > 0 && l.cfg.ProjectionSyncInterval > 0 {
		t := time.NewTicker(l.cfg.ProjectionVerifyInterval)
		defer t.Stop()
		verifyC = t.C
	}
	var kickC <-chan struct{}
	if l.cfg.ProjectionSyncOnEdit {
		kickC = l.syncKick
	}
	for {
		verify, kicked := false, false
		select {
		case <-l.done:
			return
		case <-fastC:
		case <-verifyC:
			verify = true
		case <-kickC:
			kicked = true
			select { // debounce: absorb the rest of the burst
			case <-time.After(editSyncDebounce):
			case <-l.done:
				return
			}
		}
		var err error
		if kicked {
			err = l.runKickedSync()
		} else {
			_, err = l.syncProjection(verify)
		}
		if err != nil && !errors.Is(err, errSyncStopped) {
			l.sendToTrace("", "", circulation.ValueTraceFamilyError)
		}
	}
}

// runKickedSync serves queued hints precisely, or with a fast pass when any
// writer could not name what it changed (or a hint could not be resolved).
func (l *ReflexiveLoop) runKickedSync() error {
	hints, full := l.takeHints()
	if !full && len(hints) > 0 {
		_, ok, err := l.syncHints(hints)
		if err != nil || ok {
			return err
		}
	}
	_, err := l.syncProjection(false)
	return err
}
