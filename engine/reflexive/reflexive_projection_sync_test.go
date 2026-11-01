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

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"brique_engine/junction"
	"brique_engine/shared"
)

type syncTree struct {
	t    *testing.T
	root string
	l    *ReflexiveLoop
}

func newSyncTree(t *testing.T) *syncTree {
	t.Helper()
	root := t.TempDir()
	st := &syncTree{t: t, root: root}
	st.writeCtx("")
	st.l = &ReflexiveLoop{
		frame: &junction.ContextRegistry{ContextDir: root, CtxId: shared.RootContextID},
		done:  make(chan struct{}),
	}
	t.Cleanup(func() { close(st.l.done) })
	return st
}

func (s *syncTree) abs(rel string) string { return filepath.Join(s.root, filepath.FromSlash(rel)) }

func (s *syncTree) write(rel, content string) {
	s.t.Helper()
	if err := shared.WriteFileAtomic(s.abs(rel), []byte(content), 0o644); err != nil {
		s.t.Fatal(err)
	}
}

// writeBulk creates files quickly (no fsync) for tests that need many.
func (s *syncTree) writeBulk(rel, content string) {
	s.t.Helper()
	if err := os.MkdirAll(filepath.Dir(s.abs(rel)), 0o755); err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(s.abs(rel), []byte(content), 0o644); err != nil {
		s.t.Fatal(err)
	}
}

// writeInPlace edits without replacing the file: the directory mtime stays.
func (s *syncTree) writeInPlace(rel, content string) {
	s.t.Helper()
	if err := os.WriteFile(s.abs(rel), []byte(content), 0o644); err != nil {
		s.t.Fatal(err)
	}
}

func (s *syncTree) writeCtx(rel string) {
	s.t.Helper()
	p := "context.json"
	if rel != "" {
		p = rel + "/context.json"
	}
	s.write(p, `{"brique":{"ctx_name":"c"}}`)
}

func capDoc(name, desc string) string {
	return fmt.Sprintf(`{"brique":{"cap_name":%q},"objective":{"name":%q,"description":%q},"functional":{},"subjective":{}}`, name, name, desc)
}

// age makes every file and directory look old, so the racy-timestamp guard
// (recent writes are distrusted) does not interfere with tests that are not
// about it.
func (s *syncTree) age() {
	s.t.Helper()
	old := time.Now().Add(-time.Minute)
	err := filepath.WalkDir(s.root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(p, old, old)
	})
	if err != nil {
		s.t.Fatal(err)
	}
}

func (s *syncTree) sync(verify bool) syncResult {
	s.t.Helper()
	res, err := s.l.syncProjection(verify)
	if err != nil {
		s.t.Fatalf("syncProjection: %v", err)
	}
	return res
}

func (s *syncTree) query(q string, args ...any) []string {
	s.t.Helper()
	db, err := openSQLite(dbPath(s.root))
	if err != nil {
		s.t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(q, args...)
	if err != nil {
		s.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v sql.NullString
		if err := rows.Scan(&v); err != nil {
			s.t.Fatal(err)
		}
		out = append(out, v.String)
	}
	return out
}

func (s *syncTree) elements() []string {
	return s.query(`SELECT ctx_id||':'||element_type||':'||name FROM elements ORDER BY 1`)
}

// tryElements reads the element list without failing the test: while a sync is
// creating the projection the file exists before its tables do.
func (s *syncTree) tryElements() string {
	db, err := openSQLite(dbPath(s.root))
	if err != nil {
		return ""
	}
	defer db.Close()
	rows, err := db.Query(`SELECT ctx_id||':'||element_type||':'||name FROM elements ORDER BY 1`)
	if err != nil {
		return ""
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if rows.Scan(&v) == nil {
			out = append(out, v)
		}
	}
	return fmt.Sprint(out)
}

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got  %v\nwant %v", got, want)
	}
}

func TestSyncProjection_InitialThenIdempotent(t *testing.T) {
	s := newSyncTree(t)
	s.writeCtx("kid")
	s.write("capacity/a.json", capDoc("a", "first"))
	s.write("kid/capacity/b.json", capDoc("b", "second"))

	s.age()
	r := s.sync(false)
	if r.Added != 2 || r.Changed != 0 || r.Removed != 0 {
		t.Fatalf("first sync: %+v", r)
	}
	eq(t, s.elements(), []string{"/root/kid:capacity:b", "/root:capacity:a"})
	if n := s.query(`SELECT count(*) FROM elements WHERE fingerprint IS NOT NULL AND src_size>0`); n[0] != "2" {
		t.Fatalf("fingerprints missing: %v", n)
	}

	// Nothing changed: no directory is re-examined, nothing is touched.
	r = s.sync(false)
	if r.Added+r.Changed+r.Removed != 0 || r.DirsScanned != 0 || r.DirsSkipped == 0 {
		t.Fatalf("second sync should skip everything: %+v", r)
	}
	// Verify mode re-lists directories but still reindexes nothing.
	r = s.sync(true)
	if r.Added+r.Changed+r.Removed != 0 || r.Unchanged != 2 {
		t.Fatalf("verify sync on unchanged tree: %+v", r)
	}
}

func TestSyncProjection_ChangeAddRemove(t *testing.T) {
	s := newSyncTree(t)
	s.write("capacity/a.json", capDoc("a", "v1"))
	s.write("capacity/gone.json", capDoc("gone", "x"))
	s.age()
	s.sync(false)

	s.write("capacity/a.json", capDoc("a", "v2")) // atomic replace: dir mtime moves
	s.write("capacity/new.json", capDoc("new", "n"))
	if err := os.Remove(s.abs("capacity/gone.json")); err != nil {
		t.Fatal(err)
	}
	r := s.sync(false)
	if r.Added != 1 || r.Changed != 1 || r.Removed != 1 {
		t.Fatalf("sync: %+v", r)
	}
	eq(t, s.elements(), []string{"/root:capacity:a", "/root:capacity:new"})
	if v := s.query(`SELECT v_text FROM kv k JOIN elements e ON e.row_id=k.element_row WHERE e.name='a' AND v_text='v2'`); len(v) != 1 {
		t.Fatalf("new content not indexed: %v", v)
	}
	if v := s.query(`SELECT v_text FROM kv k JOIN elements e ON e.row_id=k.element_row WHERE e.name='a' AND v_text='v1'`); len(v) != 0 {
		t.Fatalf("stale content left behind: %v", v)
	}
}

// A touched file whose content is identical is not reindexed (git checkout,
// editors rewriting the same bytes).
func TestSyncProjection_SameContentNewMtimeIsNotReindexed(t *testing.T) {
	s := newSyncTree(t)
	s.write("capacity/a.json", capDoc("a", "v1"))
	s.age()
	s.sync(false)

	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(s.abs("capacity/a.json"), future, future); err != nil {
		t.Fatal(err)
	}
	// Touch the directory too so the fast pass examines it.
	if err := os.Chtimes(s.abs("capacity"), future, future); err != nil {
		t.Fatal(err)
	}
	r := s.sync(false)
	if r.Changed != 0 || r.Added != 0 || r.Unchanged != 1 {
		t.Fatalf("expected no reindex: %+v", r)
	}
	// The new mtime is remembered, so the next verify pass reads nothing.
	if v := s.query(`SELECT src_mtime_ns FROM elements WHERE name='a'`); v[0] != fmt.Sprint(future.UnixNano()) {
		t.Fatalf("mtime not refreshed: %v", v)
	}
}

// In-place edits leave the directory mtime alone: only verify finds them.
func TestSyncProjection_InPlaceEditNeedsVerify(t *testing.T) {
	s := newSyncTree(t)
	s.write("capacity/a.json", capDoc("a", "v1"))
	s.age()
	s.sync(false)

	s.writeInPlace("capacity/a.json", capDoc("a", "v2-longer"))
	// Pin the directory mtime back, as an in-place edit would leave it.
	st, _ := os.Stat(s.abs("capacity"))
	_ = st
	if r := s.sync(false); r.Changed != 0 {
		t.Fatalf("fast pass should not notice an in-place edit: %+v", r)
	}
	if r := s.sync(true); r.Changed != 1 {
		t.Fatalf("verify pass must find the edit: %+v", r)
	}
	if v := s.query(`SELECT v_text FROM kv k JOIN elements e ON e.row_id=k.element_row WHERE e.name='a' AND v_text='v2-longer'`); len(v) != 1 {
		t.Fatalf("edit not indexed: %v", v)
	}
}

func TestSyncProjection_ContextsComeAndGo(t *testing.T) {
	s := newSyncTree(t)
	s.writeCtx("k1")
	s.writeCtx("k1/deep")
	s.write("k1/capacity/x.json", capDoc("x", "x"))
	s.write("k1/deep/capacity/y.json", capDoc("y", "y"))
	s.write("capacity/r.json", capDoc("r", "r"))
	s.age()
	s.sync(false)
	eq(t, s.elements(), []string{"/root/k1/deep:capacity:y", "/root/k1:capacity:x", "/root:capacity:r"})

	// Whole subtree disappears.
	if err := os.RemoveAll(s.abs("k1")); err != nil {
		t.Fatal(err)
	}
	r := s.sync(false)
	if r.Removed != 2 {
		t.Fatalf("removed = %d, want 2: %+v", r.Removed, r)
	}
	eq(t, s.elements(), []string{"/root:capacity:r"})
	if n := s.query(`SELECT count(*) FROM scan_dirs WHERE ctx_id LIKE '/root/k1%'`); n[0] != "0" {
		t.Fatalf("scan_dirs entries survive their context: %v", n)
	}

	// A new context appears.
	s.writeCtx("k2")
	s.write("k2/capacity/z.json", capDoc("z", "z"))
	if r := s.sync(false); r.Added != 1 {
		t.Fatalf("added = %d: %+v", r.Added, r)
	}
	eq(t, s.elements(), []string{"/root/k2:capacity:z", "/root:capacity:r"})
}

// A sibling whose name merely starts like a removed context must survive.
func TestSyncProjection_DropContextKeepsNameSiblings(t *testing.T) {
	s := newSyncTree(t)
	for _, c := range []string{"a", "a_b", "ab"} {
		s.writeCtx(c)
		s.write(c+"/capacity/e.json", capDoc("e", c))
	}
	s.age()
	s.sync(false)
	if err := os.RemoveAll(s.abs("a")); err != nil {
		t.Fatal(err)
	}
	s.sync(false)
	eq(t, s.elements(), []string{"/root/a_b:capacity:e", "/root/ab:capacity:e"})
}

func TestSyncProjection_ElementTurnsInvalidOrTypeDirVanishes(t *testing.T) {
	s := newSyncTree(t)
	s.write("capacity/a.json", capDoc("a", "ok"))
	s.write("schema/s.json", capDoc("s", "ok"))
	s.age()
	s.sync(false)

	s.write("capacity/a.json", `{"not":"an element"}`)
	if err := os.RemoveAll(s.abs("schema")); err != nil {
		t.Fatal(err)
	}
	r := s.sync(false)
	if r.Removed != 2 {
		t.Fatalf("removed = %d: %+v", r.Removed, r)
	}
	eq(t, s.elements(), nil)
}

// A projection created before fingerprints existed is upgraded in place and
// every element is indexed once.
func TestSyncProjection_UpgradesOldSchema(t *testing.T) {
	s := newSyncTree(t)
	s.write("capacity/a.json", capDoc("a", "v1"))
	if err := ensureProjectionDir(s.root); err != nil {
		t.Fatal(err)
	}
	db, err := openSQLite(dbPath(s.root))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE elements (row_id INTEGER PRIMARY KEY, element_id TEXT NOT NULL UNIQUE,
			element_type TEXT NOT NULL, name TEXT NOT NULL, ctx_id TEXT NOT NULL)`,
		`CREATE UNIQUE INDEX uq_elements_ctx_type_name ON elements(ctx_id, element_type, name)`,
		`INSERT INTO elements(element_id,element_type,name,ctx_id) VALUES('old','capacity','a','/root')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	r := s.sync(false)
	if r.Changed != 1 {
		t.Fatalf("old row should be reindexed once: %+v", r)
	}
	if n := s.query(`SELECT count(*) FROM elements WHERE fingerprint IS NOT NULL`); n[0] != "1" {
		t.Fatalf("not upgraded: %v", n)
	}
}

// A rebuild that swaps the projection during a sync aborts the sync instead
// of writing into the orphaned file.
func TestSyncProjection_AbortsWhenProjectionReplaced(t *testing.T) {
	s := newSyncTree(t)
	for i := 0; i < syncBatchElements+10; i++ {
		s.writeBulk(fmt.Sprintf("capacity/e%04d.json", i), capDoc(fmt.Sprintf("e%04d", i), "x"))
	}
	syncer := &projectionSyncer{l: s.l, gen: s.l.projectionGen.Load()}
	s.l.projectionGen.Add(1)
	syncer.ops = []syncOp{{kind: opMarkDirDel, dirPath: "x"}}
	if err := syncer.flush(); err != errSyncReplaced {
		t.Fatalf("flush = %v, want errSyncReplaced", err)
	}
}

func TestSyncProjection_LargeBatchesStayConsistent(t *testing.T) {
	s := newSyncTree(t)
	const n = 3*syncBatchElements + 7
	for i := 0; i < n; i++ {
		s.writeBulk(fmt.Sprintf("capacity/e%05d.json", i), capDoc(fmt.Sprintf("e%05d", i), "x"))
	}
	r := s.sync(false)
	if r.Added != n {
		t.Fatalf("added = %d, want %d", r.Added, n)
	}
	if got := s.query(`SELECT count(*) FROM elements`); got[0] != fmt.Sprint(n) {
		t.Fatalf("rows = %v", got)
	}
	if r := s.sync(false); r.Added+r.Changed+r.Removed != 0 {
		t.Fatalf("not idempotent: %+v", r)
	}
}

func TestChildContextDirs_DoesNotEnterCodeTrees(t *testing.T) {
	s := newSyncTree(t)
	s.writeCtx("real")
	s.write("code/venv/lib/context.json", `{}`) // must never be treated as a context
	s.write("capacity/context.json", `{}`)
	got := childContextDirs(s.root)
	if fmt.Sprint(got) != "[real]" {
		t.Fatalf("children = %v", got)
	}
}

// The periodic worker picks up disk changes without any explicit call.
func TestProjectionSyncLoop_AppliesChangesPeriodically(t *testing.T) {
	s := newSyncTree(t)
	s.write("capacity/a.json", capDoc("a", "v1"))
	s.l.cfg.ProjectionSyncInterval = 20 * time.Millisecond
	done := make(chan struct{})
	go func() { s.l.projectionSyncLoop(); close(done) }()

	waitRows := func(want string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(dbPath(s.root)); err == nil {
				if fmt.Sprint(s.elements()) == want {
					return
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("projection never reached %s (has %v)", want, s.elements())
	}
	waitRows("[/root:capacity:a]")
	s.write("capacity/b.json", capDoc("b", "v1"))
	waitRows("[/root:capacity:a /root:capacity:b]")
	if err := os.Remove(s.abs("capacity/a.json")); err != nil {
		t.Fatal(err)
	}
	waitRows("[/root:capacity:b]")

	close(s.l.done)
	<-done
	s.l.done = make(chan struct{}) // cleanup closes it again
}

func TestParseReflexiveCfg_ProjectionSync(t *testing.T) {
	cfg := ParseReflexiveCfg(map[string]any{"projection_sync_interval_ms": 2000.0, "projection_verify_interval_ms": 60000.0})
	if cfg.ProjectionSyncInterval != 2*time.Second || cfg.ProjectionVerifyInterval != time.Minute {
		t.Fatalf("cfg = %+v", cfg)
	}
	if off := ParseReflexiveCfg(nil); off.ProjectionSyncInterval != 0 || off.ProjectionVerifyInterval != 0 {
		t.Fatalf("auto-sync must be off by default: %+v", off)
	}
}

// A file written within the racy window cannot be trusted by its timestamps:
// its directory stays unmarked and the file is re-read on the next pass, until
// it is old enough to be believed.
func TestSyncProjection_RacyWritesAreRecheckedUntilOld(t *testing.T) {
	s := newSyncTree(t)
	s.write("capacity/a.json", capDoc("a", "just written")) // mtime = now
	s.sync(false)

	if v := s.query(`SELECT src_racy FROM elements WHERE name='a'`); v[0] != "1" {
		t.Fatalf("fresh file should be recorded as racy: %v", v)
	}
	if n := s.query(`SELECT count(*) FROM scan_dirs WHERE kind='dir'`); n[0] != "0" {
		t.Fatalf("a directory holding racy files must stay unmarked: %v", n)
	}
	// Still racy: examined again, nothing reindexed.
	if r := s.sync(false); r.DirsScanned != 1 || r.Changed+r.Added != 0 {
		t.Fatalf("racy dir should be re-examined without reindexing: %+v", r)
	}

	s.age() // time passes
	if r := s.sync(false); r.Changed+r.Added != 0 {
		t.Fatalf("aged file with the same content must not be reindexed: %+v", r)
	}
	if v := s.query(`SELECT src_racy FROM elements WHERE name='a'`); v[0] != "0" {
		t.Fatalf("racy flag should clear once the file is old: %v", v)
	}
	if r := s.sync(false); r.DirsScanned != 0 || r.DirsSkipped == 0 {
		t.Fatalf("settled tree should be skipped again: %+v", r)
	}
}

// An edit that puts the old mtime back (and keeps the size) is still caught:
// the ctime cannot be forged.
func TestSyncProjection_RestoredMtimeStillDetectedByCtime(t *testing.T) {
	s := newSyncTree(t)
	s.write("capacity/a.json", capDoc("a", "AAAA"))
	s.age()
	s.sync(false)

	path := s.abs("capacity/a.json")
	before, _ := os.Stat(path)
	s.writeInPlace("capacity/a.json", capDoc("a", "BBBB")) // same length
	if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	if r := s.sync(true); r.Changed != 1 {
		t.Fatalf("same size + restored mtime must still be detected: %+v", r)
	}
}

// Replacing a file by another with identical size and mtime is caught by the inode.
func TestSyncProjection_ReplacedFileDetectedByInode(t *testing.T) {
	s := newSyncTree(t)
	s.write("capacity/a.json", capDoc("a", "AAAA"))
	s.age()
	s.sync(false)

	path := s.abs("capacity/a.json")
	before, _ := os.Stat(path)
	other := s.abs("capacity/.swap")
	if err := os.WriteFile(other, []byte(capDoc("a", "BBBB")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(other, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(other, path); err != nil {
		t.Fatal(err)
	}
	if r := s.sync(true); r.Changed != 1 {
		t.Fatalf("replaced file must be detected: %+v", r)
	}
}

// Without any timer, an edit in ANY context reaches the root through the shared
// registry and the projection follows within a debounce.
func TestProjectionSync_EditInChildContextRefreshesRootProjection(t *testing.T) {
	root := t.TempDir()
	reg := junction.NewContextCommRegistry()
	write := func(rel, content string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := shared.WriteFileAtomic(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("context.json", `{"brique":{"ctx_name":"r"}}`)
	write("kid/context.json", `{"brique":{"ctx_name":"k"}}`)

	rootLoop := NewReflexiveLoop(&junction.ContextRegistry{
		ContextDir: root, CtxId: shared.RootContextID, CtxCommReg: reg,
	}, map[string]any{"projection_sync_on_edit": true})
	rootLoop.Start()
	t.Cleanup(rootLoop.Stop)

	// The child's own loop: not root, no sync config of its own.
	kidLoop := NewReflexiveLoop(&junction.ContextRegistry{
		ContextDir: filepath.Join(root, "kid"), CtxId: "/root/kid", CtxCommReg: reg,
	}, nil)

	tree := &syncTree{t: t, root: root, l: rootLoop}
	waitFor := func(want string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if tree.tryElements() == want {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("projection did not reach %s (has %v)", want, tree.elements())
	}

	write("kid/capacity/e.json", capDoc("e", "from the child"))
	kidLoop.notifyIfEdited([]map[string]any{{"ok": false}}, false) // failed edit: no hint
	time.Sleep(3 * editSyncDebounce)
	if _, err := os.Stat(dbPath(root)); err == nil {
		t.Fatal("a failed edit must not trigger a pass")
	}
	if tree.tryElements() != "" { // (tryElements itself would create the file)
		t.Fatal("unexpected projection")
	}
	_ = os.Remove(dbPath(root))
	kidLoop.notifyIfEdited([]map[string]any{{"ok": true}}, false)
	waitFor("[/root/kid:capacity:e]")

	write("kid/capacity/f.json", capDoc("f", "again"))
	kidLoop.notifyIfEdited([]map[string]any{{"ok": true}}, false)
	waitFor("[/root/kid:capacity:e /root/kid:capacity:f]")

	rootLoop.Stop()
	kidLoop.notifyIfEdited([]map[string]any{{"ok": true}}, false) // listener removed: harmless
}

func hint(ctx, typ, name string) junction.ProjectionHint {
	return junction.ProjectionHint{CtxID: ctx, ElementType: typ, Name: name}
}

// Hints refresh exactly the named elements without listing any directory.
func TestSyncHints_TargetNamedElementsOnly(t *testing.T) {
	s := newSyncTree(t)
	for i := 0; i < 50; i++ {
		s.writeBulk(fmt.Sprintf("capacity/c%02d.json", i), capDoc(fmt.Sprintf("c%02d", i), "x"))
	}
	s.age()
	s.sync(false)

	// Edit one, add one, delete one: only those are touched.
	s.write("capacity/c07.json", capDoc("c07", "edited"))
	s.write("capacity/new.json", capDoc("new", "n"))
	if err := os.Remove(s.abs("capacity/c08.json")); err != nil {
		t.Fatal(err)
	}
	res, ok, err := s.l.syncHints([]junction.ProjectionHint{
		hint("/root", "capacity", "c07"), hint("/root", "capacity", "new"), hint("/root", "capacity", "c08"),
		hint("/root", "capacity", "c09"),   // unchanged
		hint("/root", "capacity", "ghost"), // never existed
	})
	if err != nil || !ok {
		t.Fatalf("syncHints ok=%v err=%v", ok, err)
	}
	if res.Changed != 1 || res.Added != 1 || res.Removed != 1 || res.DirsScanned != 0 || res.Unchanged != 1 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if v := s.query(`SELECT count(*) FROM elements`); v[0] != "50" { // 50 -1 +1
		t.Fatalf("rows = %v", v)
	}
	if v := s.query(`SELECT count(*) FROM elements WHERE name='c08'`); v[0] != "0" {
		t.Fatal("deleted element still indexed")
	}
	// Hints did not mark directories as examined: a regular pass still sees the dir as changed.
	if r := s.sync(false); r.DirsScanned != 1 || r.Added+r.Changed+r.Removed != 0 {
		t.Fatalf("regular pass after hints: %+v", r)
	}
}

// An unresolvable hint reports ok=false so the caller falls back to a pass.
func TestSyncHints_UnresolvableFallsBack(t *testing.T) {
	s := newSyncTree(t)
	for _, h := range []junction.ProjectionHint{
		hint("/root/nosuchctx", "capacity", "a"),
		hint("/root", "capacity", "../escape"),
		hint("/root", "weird", "a"),
	} {
		if _, ok, err := s.l.syncHints([]junction.ProjectionHint{h}); err != nil || ok {
			t.Fatalf("%v: ok=%v err=%v, want fallback", h, ok, err)
		}
	}
}

// Queued hints are merged, and anything unnamed or too many degrades to a pass.
func TestKickProjectionSync_MergesAndDegrades(t *testing.T) {
	l := &ReflexiveLoop{syncKick: make(chan struct{}, 1)}
	a := hint("/root", "matter", "m1")
	l.kickProjectionSync(a)
	l.kickProjectionSync(a, hint("/root", "matter", "m2"))
	hints, full := l.takeHints()
	if full || len(hints) != 2 {
		t.Fatalf("hints=%v full=%v", hints, full)
	}
	if h, f := l.takeHints(); len(h) != 0 || f {
		t.Fatal("takeHints must clear the queue")
	}
	l.kickProjectionSync(a)
	l.kickProjectionSync() // someone could not say what changed
	if h, f := l.takeHints(); !f || len(h) != 0 {
		t.Fatalf("unnamed change should force a pass: %v %v", h, f)
	}
	for i := 0; i <= maxPendingHints; i++ {
		l.kickProjectionSync(hint("/root", "matter", fmt.Sprintf("m%d", i)))
	}
	if _, f := l.takeHints(); !f {
		t.Fatal("too many hints should degrade to a pass")
	}
}

// A named hint from any family (here sent straight on the registry, as Matter
// does) makes the root index exactly that descriptor.
func TestProjectionSync_NamedHintFromAnotherFamily(t *testing.T) {
	root := t.TempDir()
	reg := junction.NewContextCommRegistry()
	put := func(rel, content string) {
		if err := shared.WriteFileAtomic(filepath.Join(root, filepath.FromSlash(rel)), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put("context.json", `{"brique":{"ctx_name":"r"}}`)
	put("kid/context.json", `{"brique":{"ctx_name":"k"}}`)

	rootLoop := NewReflexiveLoop(&junction.ContextRegistry{ContextDir: root, CtxId: shared.RootContextID, CtxCommReg: reg},
		map[string]any{"projection_sync_on_edit": true})
	rootLoop.Start()
	t.Cleanup(rootLoop.Stop)

	put("kid/matter/m1.matter.json", capDoc("m1", "a matter"))
	reg.NotifyProjectionChange(junction.ProjectionHint{CtxID: "/root/kid", ElementType: "matter", Name: "m1"})

	tree := &syncTree{t: t, root: root, l: rootLoop}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if tree.tryElements() == "[/root/kid:matter:m1]" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("named hint was not applied (has %s)", tree.tryElements())
}
