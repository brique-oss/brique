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

package matter

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"brique_engine/circulation"
)

func newMatterLoopForGetter() *MatterLoop {
	return &MatterLoop{
		catalogMatter: map[string]CatalogEntry{},
		locks:         map[string]*sync.Mutex{},
	}
}

func parseBodyMap(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &m); err != nil {
		t.Fatalf("invalid json body: %v", err)
	}
	return m
}

func TestHTTPSubstanceGetter_N1_HSG_01_NewGetterDefaults(t *testing.T) {
	ml := newMatterLoopForGetter()
	g := NewHTTPSubstanceGetter(ml)
	if g == nil || g.ml != ml {
		t.Fatalf("getter init failed")
	}
	if g.leases == nil || g.done == nil {
		t.Fatalf("expected initialized lease map and done channel")
	}
	if g.defaultTTL <= 0 || g.sweep <= 0 {
		t.Fatalf("expected positive ttl/sweep defaults")
	}
	if g.BaseURL() != "" {
		t.Fatalf("baseURL should be empty before start")
	}
}

func TestHTTPSubstanceGetter_N1_HSG_02_OpenLeaseGuards(t *testing.T) {
	g := NewHTTPSubstanceGetter(newMatterLoopForGetter())
	if _, err := g.OpenReadLease("/ctx", "", "data", 1, time.Second, 0); err == nil {
		t.Fatalf("OpenReadLease should fail on empty matter id")
	}
	if _, err := g.OpenWriteLease("/ctx", "", "data", 1, "i", nil, nil, time.Second); err == nil {
		t.Fatalf("OpenWriteLease should fail on empty matter id")
	}
	if _, err := g.OpenReadLease("/ctx", "m1", "data", 1, time.Second, 0); err == nil {
		t.Fatalf("OpenReadLease should fail when getter not running")
	}
	if _, err := g.OpenWriteLease("/ctx", "m1", "data", 1, "i", nil, []byte("{}"), time.Second); err == nil {
		t.Fatalf("OpenWriteLease should fail when getter not running")
	}
}

func TestHTTPSubstanceGetter_N1_HSG_03_OpenLeaseSuccess(t *testing.T) {
	g := NewHTTPSubstanceGetter(newMatterLoopForGetter())
	ctxDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ctxDir, matterDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	g.srvMu.Lock()
	g.baseURL = "http://127.0.0.1:1"
	g.srvMu.Unlock()

	hr, err := g.OpenReadLease(ctxDir, "m1", "data", 3, time.Second, 12)
	if err != nil {
		t.Fatalf("OpenReadLease error: %v", err)
	}
	if hr.Method != http.MethodGet || hr.RID == "" || hr.Tok == "" || hr.Path == "" {
		t.Fatalf("unexpected read handle: %#v", hr)
	}
	if g.getLease(hr.RID) == nil {
		t.Fatalf("read lease should be stored")
	}

	hw, err := g.OpenWriteLease(ctxDir, "m2", "data", 7, "i1", map[string]any{"x": 1}, []byte("{}"), time.Second)
	if err != nil {
		t.Fatalf("OpenWriteLease error: %v", err)
	}
	if hw.Method != http.MethodPut || hw.RID == "" || hw.Tok == "" {
		t.Fatalf("unexpected write handle: %#v", hw)
	}
	if g.getLease(hw.RID) == nil {
		t.Fatalf("write lease should be stored")
	}
	first := g.getLease(hw.RID)
	hw2, err := g.OpenWriteLease(ctxDir, "m2", "data", 7, "i2", map[string]any{"x": 2}, []byte("{}"), time.Second)
	if err != nil {
		t.Fatalf("second OpenWriteLease error: %v", err)
	}
	second := g.getLease(hw2.RID)
	if first == nil || second == nil || first.tmpSubstancePath == second.tmpSubstancePath || first.tmpMatterPath == second.tmpMatterPath {
		t.Fatalf("each write lease must own unique temp files: first=%#v second=%#v", first, second)
	}
}

func TestHTTPSubstanceGetter_N1_HSG_04_CloseLeaseAndStopCleanup(t *testing.T) {
	g := NewHTTPSubstanceGetter(newMatterLoopForGetter())
	dir := t.TempDir()
	tmpPayload := filepath.Join(dir, "payload.tmp")
	tmpMatter := filepath.Join(dir, "matter.tmp")
	if err := os.WriteFile(tmpPayload, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmpMatter, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	lz := &lease{rid: "r1", mode: leaseWrite, tmpSubstancePath: tmpPayload, tmpMatterPath: tmpMatter}
	g.mu.Lock()
	g.leases[lz.rid] = lz
	g.mu.Unlock()

	g.closeLease("r1", "done")
	if g.getLease("r1") != nil {
		t.Fatalf("lease should be removed")
	}
	if _, err := os.Stat(tmpPayload); !os.IsNotExist(err) {
		t.Fatalf("payload temp should be removed, err=%v", err)
	}
	if _, err := os.Stat(tmpMatter); !os.IsNotExist(err) {
		t.Fatalf("matter temp should be removed, err=%v", err)
	}

	lz2 := &lease{rid: "r2"}
	g.mu.Lock()
	g.leases[lz2.rid] = lz2
	g.mu.Unlock()
	if err := g.Stop(); err != nil {
		t.Fatalf("Stop error: %v", err)
	}
}

func TestHTTPSubstanceGetter_N1_HSG_05_SweeperExpiresLease(t *testing.T) {
	g := NewHTTPSubstanceGetter(newMatterLoopForGetter())
	g.sweep = 10 * time.Millisecond
	g.mu.Lock()
	g.leases["r-exp"] = &lease{rid: "r-exp", expires: time.Now().Add(-time.Second)}
	g.leases["r-active"] = &lease{rid: "r-active", expires: time.Now().Add(-time.Second), uploading: true}
	g.mu.Unlock()

	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		g.sweeperLoop()
	}()

	time.Sleep(40 * time.Millisecond)
	if g.getLease("r-exp") != nil {
		t.Fatalf("expired lease should be removed by sweeper")
	}
	if g.getLease("r-active") == nil {
		t.Fatalf("active upload must not expire while its body is streaming")
	}
	g.mu.Lock()
	g.leases["r-active"].uploading = false
	g.mu.Unlock()
	time.Sleep(30 * time.Millisecond)
	if g.getLease("r-active") != nil {
		t.Fatalf("inactive expired upload should be removed by sweeper")
	}
	g.once.Do(func() { close(g.done) })
	g.wg.Wait()
}

func TestHTTPSubstanceGetter_N1_HSG_05b_WriteLeaseCanBeClaimedOnce(t *testing.T) {
	g := NewHTTPSubstanceGetter(newMatterLoopForGetter())
	lz := &lease{rid: "r-write", mode: leaseWrite, expires: time.Now().Add(time.Minute)}
	g.mu.Lock()
	g.leases[lz.rid] = lz
	g.mu.Unlock()
	if status := g.claimWriteLease(lz.rid, lz); status != "" {
		t.Fatalf("first claim status=%q", status)
	}
	if status := g.claimWriteLease(lz.rid, lz); status != "upload_in_progress" {
		t.Fatalf("second claim status=%q want upload_in_progress", status)
	}
}

func TestHTTPSubstanceGetter_N1_HSG_06_HandleSubstanceGuardErrors(t *testing.T) {
	g := NewHTTPSubstanceGetter(newMatterLoopForGetter())

	// bad rid
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/substance/", nil)
	g.handleSubstance(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bad rid status=%d", rr.Code)
	}

	// missing tok
	g.mu.Lock()
	g.leases["rid1"] = &lease{rid: "rid1", tok: "tok", mode: leaseRead, matter: "m1", rev: 1, expires: time.Now().Add(time.Second)}
	g.mu.Unlock()
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/substance/rid1", nil)
	g.handleSubstance(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("missing tok status=%d", rr.Code)
	}

	// unknown rid
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/substance/unknown?tok=x", nil)
	g.handleSubstance(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("unknown rid status=%d", rr.Code)
	}

	// invalid tok
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/substance/rid1?tok=bad", nil)
	g.handleSubstance(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("invalid tok status=%d", rr.Code)
	}
}

func TestHTTPSubstanceGetter_N1_HSG_07_HandleSubstanceReadBranches(t *testing.T) {
	ml := newMatterLoopForGetter()
	ml.catalogMatter["m1"] = CatalogEntry{Brique: map[string]any{circulation.KeyRevision: int64(5)}}
	g := NewHTTPSubstanceGetter(ml)

	dir := t.TempDir()
	mRoot := filepath.Join(dir, matterDirName)
	if err := os.MkdirAll(mRoot, 0o755); err != nil {
		t.Fatalf("mkdir matter root: %v", err)
	}
	dataPath := filepath.Join(mRoot, "m1.data")

	// method not allowed on read lease
	g.mu.Lock()
	g.leases["r-method"] = &lease{rid: "r-method", tok: "t", mode: leaseRead, matter: "m1", rev: 5, expires: time.Now().Add(time.Second), substancePath: dataPath}
	g.mu.Unlock()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/substance/r-method?tok=t", nil)
	g.handleSubstance(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("method mismatch status=%d", rr.Code)
	}

	// stale rev
	g.mu.Lock()
	g.leases["r-stale"] = &lease{rid: "r-stale", tok: "t", mode: leaseRead, matter: "m1", rev: 99, expires: time.Now().Add(time.Second), substancePath: dataPath}
	g.mu.Unlock()
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/substance/r-stale?tok=t", nil)
	g.handleSubstance(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("stale status=%d", rr.Code)
	}

	// data missing
	g.mu.Lock()
	g.leases["r-missing"] = &lease{rid: "r-missing", tok: "t", mode: leaseRead, matter: "m1", rev: 5, expires: time.Now().Add(time.Second), substancePath: dataPath}
	g.mu.Unlock()
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/substance/r-missing?tok=t", nil)
	g.handleSubstance(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("data_missing status=%d", rr.Code)
	}

	// success stream
	if err := os.WriteFile(dataPath, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write data file: %v", err)
	}
	g.mu.Lock()
	g.leases["r-ok"] = &lease{rid: "r-ok", tok: "t", mode: leaseRead, matter: "m1", rev: 5, expires: time.Now().Add(time.Second), substancePath: dataPath}
	g.mu.Unlock()
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/substance/r-ok?tok=t", nil)
	g.handleSubstance(rr, req)
	if rr.Code != http.StatusOK || rr.Body.String() != "hello" {
		t.Fatalf("read success status/body: %d %q", rr.Code, rr.Body.String())
	}
	if g.getLease("r-ok") != nil {
		t.Fatalf("read lease should close after successful GET")
	}

	// one-shot byte range
	g.mu.Lock()
	g.leases["r-range"] = &lease{rid: "r-range", tok: "t", mode: leaseRead, matter: "m1", rev: 5, expires: time.Now().Add(time.Second), substancePath: dataPath}
	g.mu.Unlock()
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/substance/r-range?tok=t", nil)
	req.Header.Set("Range", "bytes=1-3")
	g.handleSubstance(rr, req)
	if rr.Code != http.StatusPartialContent || rr.Body.String() != "ell" {
		t.Fatalf("range read status/body: %d %q", rr.Code, rr.Body.String())
	}
}

func TestHTTPSubstanceGetter_N1_HSG_08_RevStillValid(t *testing.T) {
	g := NewHTTPSubstanceGetter(nil)
	if g.revStillValid("m1", 1) {
		t.Fatalf("nil matter loop should be invalid")
	}
	g.ml = newMatterLoopForGetter()
	if g.revStillValid("", 1) {
		t.Fatalf("empty matter id should be invalid")
	}
	if g.revStillValid("m1", 1) {
		t.Fatalf("missing catalog entry should be invalid")
	}
	g.ml.catalogMatter["m1"] = CatalogEntry{Brique: map[string]any{}}
	if g.revStillValid("m1", 1) {
		t.Fatalf("missing rev field should be invalid")
	}
	g.ml.catalogMatter["m1"] = CatalogEntry{Brique: map[string]any{circulation.KeyRevision: int64(2)}}
	if g.revStillValid("m1", 1) {
		t.Fatalf("mismatched rev should be invalid")
	}
	if !g.revStillValid("m1", 2) {
		t.Fatalf("matching rev should be valid")
	}
}

func TestHTTPSubstanceGetter_N1_HSG_09_UtilityHelpers(t *testing.T) {
	if subtleStringNe("abc", "abc") {
		t.Fatalf("equal strings should compare equal")
	}
	if !subtleStringNe("abc", "abd") || !subtleStringNe("a", "ab") {
		t.Fatalf("different strings should compare not equal")
	}
	if tok := randTok(0); tok == "" {
		t.Fatalf("randTok should produce non-empty token")
	}

	rr := httptest.NewRecorder()
	writeJSON(rr, http.StatusCreated, map[string]any{"ok": true})
	if rr.Code != http.StatusCreated {
		t.Fatalf("writeJSON status=%d", rr.Code)
	}
	body := parseBodyMap(t, rr)
	if v, ok := body["ok"].(bool); !ok || !v {
		t.Fatalf("writeJSON payload unexpected: %#v", body)
	}

	rr = httptest.NewRecorder()
	writeProblem(rr, http.StatusBadRequest, "bad", "rid1", http.MethodGet, map[string]any{"x": 1})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("writeProblem status=%d", rr.Code)
	}
	pb := parseBodyMap(t, rr)
	if okv, ok := pb[circulation.KeyOK].(bool); !ok || okv {
		t.Fatalf("writeProblem ok flag unexpected: %#v", pb)
	}
	if pb[circulation.KeyReason] != "bad" {
		t.Fatalf("writeProblem reason unexpected: %#v", pb)
	}
	detail, _ := pb[circulation.KeyDetail].(map[string]any)
	if detail[circulation.KeyRID] != "rid1" || detail[circulation.KeyExpectedMethod] != http.MethodGet {
		t.Fatalf("writeProblem details unexpected: %#v", detail)
	}
}

func newMatterLoopForGetterWithCatalog(t *testing.T, mid string, rev int64) (*MatterLoop, string) {
	t.Helper()
	dir := t.TempDir()
	ml := newMatterLoopForGetter()
	mRoot := filepath.Join(dir, matterDirName)
	if err := os.MkdirAll(mRoot, 0o755); err != nil {
		t.Fatalf("mkdir matter: %v", err)
	}
	doc := map[string]any{circulation.KeyBrique: map[string]any{
		circulation.KeyKind:          circulation.ValueEntryKindMatter,
		circulation.KeySubstanceMode: circulation.ValueModeBrique,
		circulation.KeyRevision:      rev,
	}}
	mb, _ := json.Marshal(doc)
	if err := os.WriteFile(filepath.Join(mRoot, mid+matterDescriptorSuffix), mb, 0o644); err != nil {
		t.Fatalf("write matter.json: %v", err)
	}
	ml.catalogMatter[mid] = CatalogEntry{Brique: map[string]any{
		circulation.KeyKind:          circulation.ValueEntryKindMatter,
		circulation.KeySubstanceMode: circulation.ValueModeBrique,
		circulation.KeyRevision:      rev,
	}}
	return ml, dir
}

func TestHTTPSubstanceGetter_N1_HSG_10_ReceiveAndCommitSuccess(t *testing.T) {
	mid := "m-rac"
	var rev int64 = 5
	ml, ctxDir := newMatterLoopForGetterWithCatalog(t, mid, rev)
	g := NewHTTPSubstanceGetter(ml)
	g.srvMu.Lock()
	g.baseURL = "http://127.0.0.1:1"
	g.srvMu.Unlock()

	mRoot := filepath.Join(ctxDir, matterDirName)
	tmpPayload := filepath.Join(mRoot, mid+".data"+tmpSuffix)
	finalPayload := filepath.Join(mRoot, mid+".data")
	tmpMatter := filepath.Join(mRoot, mid+matterDescriptorSuffix+tmpSuffix)

	newDoc := map[string]any{circulation.KeyBrique: map[string]any{
		circulation.KeyKind:          circulation.ValueEntryKindMatter,
		circulation.KeySubstanceMode: circulation.ValueModeBrique,
		circulation.KeyRevision:      rev + 1,
	}}
	mb, _ := json.Marshal(newDoc)
	if err := os.WriteFile(tmpMatter, mb, 0o644); err != nil {
		t.Fatalf("write tmp matter.json: %v", err)
	}

	lz := &lease{
		rid:              "w-ok",
		tok:              "tok",
		mode:             leaseWrite,
		matter:           mid,
		rev:              rev,
		matterRoot:       mRoot,
		tmpSubstancePath: tmpPayload,
		substancePath:    finalPayload,
		newBrique: map[string]any{
			circulation.KeyKind:          circulation.ValueEntryKindMatter,
			circulation.KeySubstanceMode: circulation.ValueModeBrique,
			circulation.KeyRevision:      rev + 1,
		},
		tmpMatterPath: tmpMatter,
		expires:       time.Now().Add(time.Minute),
	}
	g.mu.Lock()
	g.leases[lz.rid] = lz
	g.mu.Unlock()

	body := strings.NewReader("payload-data")
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/substance/w-ok?tok=tok", body)
	g.receiveAndCommit(rr, req, lz)

	if rr.Code != http.StatusOK {
		t.Fatalf("receiveAndCommit success status=%d body=%s", rr.Code, rr.Body.String())
	}
	result := parseBodyMap(t, rr)
	if ok, _ := result[circulation.KeyOK].(bool); !ok {
		t.Fatalf("receiveAndCommit success response ok=%v: %#v", result[circulation.KeyOK], result)
	}
	if b, err := os.ReadFile(finalPayload); err != nil || string(b) != "payload-data" {
		t.Fatalf("final payload should be committed, err=%v payload=%q", err, string(b))
	}
}

func TestHTTPSubstanceGetter_N1_HSG_11_ReceiveAndCommitStaleRev(t *testing.T) {
	mid := "m-stale-rac"
	var rev int64 = 3
	ml, ctxDir := newMatterLoopForGetterWithCatalog(t, mid, rev)
	g := NewHTTPSubstanceGetter(ml)
	g.srvMu.Lock()
	g.baseURL = "http://127.0.0.1:1"
	g.srvMu.Unlock()

	mRoot := filepath.Join(ctxDir, matterDirName)
	tmpPayload := filepath.Join(mRoot, mid+".data"+tmpSuffix)
	finalPayload := filepath.Join(mRoot, mid+".data")

	lz := &lease{
		rid:              "w-stale",
		tok:              "tok",
		mode:             leaseWrite,
		matter:           mid,
		rev:              rev + 10,
		matterRoot:       mRoot,
		tmpSubstancePath: tmpPayload,
		substancePath:    finalPayload,
		newBrique:        map[string]any{circulation.KeyRevision: rev + 11},
		expires:          time.Now().Add(time.Minute),
		tmpMatterPath:    filepath.Join(mRoot, mid+matterDescriptorSuffix+".w-stale"+tmpSuffix),
	}
	g.mu.Lock()
	g.leases[lz.rid] = lz
	g.mu.Unlock()

	body := strings.NewReader("stale-payload")
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/substance/w-stale?tok=tok", body)
	g.receiveAndCommit(rr, req, lz)

	if rr.Code != http.StatusConflict {
		t.Fatalf("stale rev should return conflict, got status=%d body=%s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(tmpPayload); !os.IsNotExist(err) {
		t.Fatalf("tmp payload should be cleaned up on stale rev, stat err=%v", err)
	}
}
