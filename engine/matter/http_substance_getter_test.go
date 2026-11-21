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
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"brique_engine/engine/circulation"
	"brique_engine/engine/junction"
)

type stalledUploadBody struct {
	mu      sync.Mutex
	sent    bool
	release chan struct{}
	once    sync.Once
}

type readTrackingBody struct{ read bool }

type deadlineResponseRecorder struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
}

func (recorder *deadlineResponseRecorder) SetWriteDeadline(deadline time.Time) error {
	recorder.deadlines = append(recorder.deadlines, deadline)
	return nil
}

func (body *readTrackingBody) Read([]byte) (int, error) {
	body.read = true
	return 0, errors.New("body must not be read")
}
func (*readTrackingBody) Close() error { return nil }

func newStalledUploadBody() *stalledUploadBody {
	return &stalledUploadBody{release: make(chan struct{})}
}

func (body *stalledUploadBody) Read(buffer []byte) (int, error) {
	body.mu.Lock()
	if !body.sent {
		body.sent = true
		body.mu.Unlock()
		return copy(buffer, "partial"), nil
	}
	body.mu.Unlock()
	<-body.release
	return 0, io.EOF
}

func (body *stalledUploadBody) Close() error {
	body.once.Do(func() { close(body.release) })
	return nil
}

func newMatterLoopForGetter() *MatterLoop {
	return &MatterLoop{
		catalogMatter: map[string]CatalogEntry{},
		locks:         map[string]*sync.RWMutex{},
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
	timedOutUpload := filepath.Join(t.TempDir(), "timed-out-upload.tmp")
	if err := os.WriteFile(timedOutUpload, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	timedOutBody := newStalledUploadBody()
	g.mu.Lock()
	g.leases["r-exp"] = &lease{rid: "r-exp", expires: time.Now().Add(-time.Second)}
	g.leases["r-active"] = &lease{rid: "r-active", expires: time.Now().Add(-time.Second), uploading: true}
	g.leases["r-timeout"] = &lease{rid: "r-timeout", mode: leaseWrite, expires: time.Now().Add(time.Minute),
		uploading: true, uploadDeadline: time.Now().Add(-time.Second), uploadBody: timedOutBody,
		tmpSubstancePath: timedOutUpload}
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
	if g.getLease("r-timeout") != nil {
		t.Fatal("upload beyond its maximum duration was retained")
	}
	if _, err := os.Stat(timedOutUpload); !os.IsNotExist(err) {
		t.Fatalf("timed-out upload retained its temporary file: %v", err)
	}
	select {
	case <-timedOutBody.release:
	default:
		t.Fatal("timed-out upload body was not closed")
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

func TestHTTPSubstanceGetter_N1_HSG_05c_StalledUploadReleasesLeaseAndFile(t *testing.T) {
	loop := newMatterLoopForGetter()
	loop.catalogMatter["slow"] = CatalogEntry{Brique: map[string]any{circulation.KeyRevision: int64(1)}}
	g := NewHTTPSubstanceGetter(loop)
	g.uploadIdleTimeout = 100 * time.Millisecond
	g.uploadMaxDuration = time.Second
	g.sweep = 20 * time.Millisecond
	if err := g.Start(); err != nil {
		t.Fatal(err)
	}
	defer g.Stop()
	matterRoot := filepath.Join(t.TempDir(), matterDirName)
	if err := os.MkdirAll(matterRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	temporary := filepath.Join(matterRoot, "slow.data.tmp")
	lz := &lease{rid: "slow", tok: "token", mode: leaseWrite, matter: "slow", rev: 1,
		expires: time.Now().Add(time.Minute), matterRoot: matterRoot, tmpSubstancePath: temporary,
		substancePath: filepath.Join(matterRoot, "slow.data"), stagedMatter: []byte("{}"),
		tmpMatterPath: filepath.Join(matterRoot, "slow.json.tmp")}
	g.mu.Lock()
	g.leases[lz.rid] = lz
	g.mu.Unlock()
	body := newStalledUploadBody()
	request, err := http.NewRequest(http.MethodPut, g.BaseURL()+"/substance/slow?tok=token", body)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		response, requestErr := http.DefaultClient.Do(request)
		if response != nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if requestErr == nil && response.StatusCode != http.StatusBadRequest {
				requestErr = fmt.Errorf("stalled upload status = %d", response.StatusCode)
			}
		}
		result <- requestErr
	}()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		_ = body.Close()
		t.Fatal("stalled upload did not terminate")
	}
	if g.getLease(lz.rid) != nil {
		t.Fatal("stalled upload retained its lease")
	}
	if _, err := os.Stat(temporary); !os.IsNotExist(err) {
		t.Fatalf("stalled upload retained its temporary file: %v", err)
	}
}

func TestHTTPSubstanceGetter_M0_HardensEveryHTTPResponse(t *testing.T) {
	g := NewHTTPSubstanceGetter(newMatterLoopForGetter())
	if err := g.Start(); err != nil {
		t.Fatal(err)
	}
	defer g.Stop()
	for _, path := range []string{"/healthz", "/substance/unknown?tok=invalid"} {
		response, err := http.Get(g.BaseURL() + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.Header.Get("X-Content-Type-Options") != "nosniff" ||
			response.Header.Get("Content-Security-Policy") == "" || response.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("security headers for %s = %#v", path, response.Header)
		}
		if strings.HasPrefix(path, substancePathPrefix) && response.Header.Get("Content-Disposition") != "attachment" {
			t.Fatalf("substance disposition = %q", response.Header.Get("Content-Disposition"))
		}
	}
}

func TestHTTPSubstanceGetter_M0_RejectsStaleWriteBeforeReadingOrStaging(t *testing.T) {
	loop, ctxDir := newMatterLoopForGetterWithCatalog(t, "stale-before-body", 5)
	g := NewHTTPSubstanceGetter(loop)
	g.srvMu.Lock()
	g.baseURL = "http://127.0.0.1:1"
	g.srvMu.Unlock()
	handle, err := g.OpenWriteLease(ctxDir, "stale-before-body", "data", 6, "intent", nil, []byte("{}"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	lz := g.getLease(handle.RID)
	if _, err := os.Stat(lz.tmpMatterPath); !os.IsNotExist(err) {
		t.Fatalf("descriptor was staged before first use: %v", err)
	}
	body := &readTrackingBody{}
	request := httptest.NewRequest(http.MethodPut, handle.Path+"?tok="+handle.Tok, body)
	response := httptest.NewRecorder()
	g.handleSubstance(response, request)
	if response.Code != http.StatusConflict || body.read {
		t.Fatalf("stale precheck: status=%d body_read=%v payload=%s", response.Code, body.read, response.Body.String())
	}
	if _, err := os.Stat(lz.tmpMatterPath); !os.IsNotExist(err) {
		t.Fatalf("stale write created descriptor: %v", err)
	}
	if _, err := os.Stat(lz.tmpSubstancePath); !os.IsNotExist(err) {
		t.Fatalf("stale write created substance: %v", err)
	}
}

func TestHTTPSubstanceGetter_M0_WriteFailureDoesNotExposeInternalError(t *testing.T) {
	g := NewHTTPSubstanceGetter(newMatterLoopForGetter())
	lz := &lease{rid: "failure", matter: "m", rev: 1}
	g.mu.Lock()
	g.leases[lz.rid] = lz
	g.mu.Unlock()
	response := httptest.NewRecorder()
	g.failWrite(response, lz, http.StatusInternalServerError, "internal_failure", errors.New("/secret/path/private.db"))
	if strings.Contains(response.Body.String(), "/secret/path") || strings.Contains(response.Body.String(), "private.db") {
		t.Fatalf("internal error escaped in HTTP response: %s", response.Body.String())
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

	// A scoped lease exposes only its selected representation. An HTTP Range is
	// relative to that scope, never to bytes outside it.
	g.mu.Lock()
	g.leases["r-scoped"] = &lease{rid: "r-scoped", tok: "t", mode: leaseRead, matter: "m1", rev: 5, expires: time.Now().Add(time.Second), substancePath: dataPath, readRanged: true, readOffset: 1, readLength: 3}
	g.mu.Unlock()
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/substance/r-scoped?tok=t", nil)
	req.Header.Set("Range", "bytes=1-1")
	g.handleSubstance(rr, req)
	if rr.Code != http.StatusPartialContent || rr.Body.String() != "l" {
		t.Fatalf("scoped range status/body: %d %q", rr.Code, rr.Body.String())
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
	if tok, err := randTok(0); err != nil || tok == "" {
		t.Fatalf("randTok should produce non-empty token: %q, %v", tok, err)
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

	expectedDigest := fmt.Sprintf("%x", sha256.Sum256([]byte("payload-data")))
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
		tmpMatterPath:  tmpMatter,
		expires:        time.Now().Add(time.Minute),
		expectedSHA256: expectedDigest,
	}
	g.mu.Lock()
	g.leases[lz.rid] = lz
	g.mu.Unlock()
	if status := g.claimWriteLease(lz.rid, lz); status != "" {
		t.Fatalf("claim write upload: %s", status)
	}

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
	if payload := parseBodyMap(t, rr); payload[circulation.KeyMessageSHA256] != expectedDigest {
		t.Fatalf("PUT digest=%v want %s", payload[circulation.KeyMessageSHA256], expectedDigest)
	}
}

func TestHTTPSubstanceGetterM2RejectsDigestMismatchWithoutCommit(t *testing.T) {
	const mid = "digest-guard"
	const revision int64 = 3
	ml, ctxDir := newMatterLoopForGetterWithCatalog(t, mid, revision)
	g := NewHTTPSubstanceGetter(ml)
	root := filepath.Join(ctxDir, matterDirName)
	finalPayload := filepath.Join(root, mid+".data")
	if err := os.WriteFile(finalPayload, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	lz := &lease{
		owner: ml, rid: "digest", tok: "token", mode: leaseWrite, matter: mid, rev: revision,
		expires: time.Now().Add(time.Minute), matterRoot: root, substancePath: finalPayload,
		tmpSubstancePath: finalPayload + ".digest" + tmpSuffix,
		tmpMatterPath:    filepath.Join(root, mid+matterDescriptorSuffix+".digest"+tmpSuffix),
		stagedMatter:     []byte(`{"brique":{"revision":4}}`), expectedSHA256: strings.Repeat("0", 64),
	}
	g.leases[lz.rid] = lz
	if status := g.claimWriteLease(lz.rid, lz); status != "" {
		t.Fatal(status)
	}
	rr := httptest.NewRecorder()
	g.receiveAndCommit(rr, httptest.NewRequest(http.MethodPut, "/substance/digest", strings.NewReader("new")), lz)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	content, err := os.ReadFile(finalPayload)
	if err != nil || string(content) != "old" {
		t.Fatalf("committed content=%q err=%v", content, err)
	}
	if g.getLease(lz.rid) != nil {
		t.Fatal("digest-mismatched lease remained open")
	}
}

func TestHTTPSubstanceGetterM2ResumesRangesAndBindsIfRangeToRevision(t *testing.T) {
	const mid = "resumable"
	const revision int64 = 9
	ml, ctxDir := newMatterLoopForGetterWithCatalog(t, mid, revision)
	g := NewHTTPSubstanceGetter(ml)
	g.srvMu.Lock()
	g.baseURL = "http://127.0.0.1:1"
	g.srvMu.Unlock()
	root := filepath.Join(ctxDir, matterDirName)
	if err := os.WriteFile(filepath.Join(root, mid+".data"), []byte("abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	handle, err := g.OpenReadLease(ctxDir, mid, "data", revision, time.Minute, 6)
	if err != nil {
		t.Fatal(err)
	}
	requestRange := func(byteRange, ifRange string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, handle.Path, nil)
		req.Header.Set("Authorization", "Bearer "+handle.Tok)
		req.Header.Set("Range", byteRange)
		if ifRange != "" {
			req.Header.Set("If-Range", ifRange)
		}
		rr := httptest.NewRecorder()
		g.handleSubstance(rr, req)
		return rr
	}
	if rr := requestRange("bytes=0-2", `"brique-rev-8"`); rr.Code != http.StatusConflict {
		t.Fatalf("wrong If-Range status=%d", rr.Code)
	}
	first := requestRange("bytes=0-2", `"brique-rev-9"`)
	if first.Code != http.StatusPartialContent || first.Body.String() != "abc" || first.Header().Get("ETag") != `"brique-rev-9"` {
		t.Fatalf("first range status=%d body=%q headers=%v", first.Code, first.Body.String(), first.Header())
	}
	if g.getLease(handle.RID) == nil {
		t.Fatal("lease closed before representation was completely delivered")
	}
	second := requestRange("bytes=3-5", `"brique-rev-9"`)
	if second.Code != http.StatusPartialContent || second.Body.String() != "def" {
		t.Fatalf("second range status=%d body=%q", second.Code, second.Body.String())
	}
	if g.getLease(handle.RID) != nil {
		t.Fatal("lease remained after all byte ranges were delivered")
	}
}

func TestHTTPSubstanceGetterM3BoundsConcurrentTransfers(t *testing.T) {
	const mid = "bounded-transfer"
	ml, _ := newMatterLoopForGetterWithCatalog(t, mid, 1)
	g := NewHTTPSubstanceGetter(ml)
	g.transferSlots = make(chan struct{}, 2)
	g.transferSlots <- struct{}{}
	g.transferSlots <- struct{}{}
	g.leases["rid"] = &lease{owner: ml, rid: "rid", tok: "token", mode: leaseRead, matter: mid, rev: 1, expires: time.Now().Add(time.Minute)}
	req := httptest.NewRequest(http.MethodGet, "/substance/rid", nil)
	req.Header.Set("Authorization", "Bearer token")
	rr := httptest.NewRecorder()
	g.handleSubstance(rr, req)
	if rr.Code != http.StatusServiceUnavailable || rr.Header().Get("Retry-After") != "1" {
		t.Fatalf("saturated transfer response=%d headers=%v", rr.Code, rr.Header())
	}
	if g.getLease("rid") == nil {
		t.Fatal("capacity refusal consumed the lease")
	}
}

func TestHTTPSubstanceGetterM2RenewsLeaseWithoutExceedingMaximumLife(t *testing.T) {
	base := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	now := base
	g := NewHTTPSubstanceGetter(newMatterLoopForGetter())
	g.now = func() time.Time { return now }
	g.defaultTTL = 30 * time.Second
	g.leaseMaxLife = time.Minute
	lz := &lease{rid: "renew", expires: base.Add(30 * time.Second), maxExpires: base.Add(time.Minute)}
	g.leases[lz.rid] = lz

	now = base.Add(20 * time.Second)
	if !g.renewLease(lz) || !lz.expires.Equal(base.Add(50*time.Second)) {
		t.Fatalf("first renewal expires=%s", lz.expires)
	}
	now = base.Add(45 * time.Second)
	if !g.renewLease(lz) || !lz.expires.Equal(base.Add(time.Minute)) {
		t.Fatalf("capped renewal expires=%s", lz.expires)
	}
	writeLease := &lease{rid: "write-deadline", mode: leaseWrite, expires: base.Add(time.Minute), maxExpires: base.Add(time.Minute)}
	g.leases[writeLease.rid] = writeLease
	g.uploadMaxDuration = 2 * time.Minute
	if status := g.claimWriteLease(writeLease.rid, writeLease); status != "" {
		t.Fatalf("claim write lease status=%q", status)
	}
	if !writeLease.uploadDeadline.Equal(base.Add(time.Minute)) {
		t.Fatalf("upload deadline=%s exceeds lease maximum", writeLease.uploadDeadline)
	}
	now = base.Add(time.Minute)
	if g.renewLease(lz) {
		t.Fatal("lease renewed beyond maximum lifetime")
	}
}

func TestHTTPSubstanceGetterM2RechecksAuthorizationDuringLongRead(t *testing.T) {
	const mid = "long-read"
	ml, ctxDir := newMatterLoopForGetterWithCatalog(t, mid, 1)
	g := NewHTTPSubstanceGetter(ml)
	root := filepath.Join(ctxDir, matterDirName)
	path := filepath.Join(root, mid+".data")
	content := strings.Repeat("x", 64<<10)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 11, 21, 11, 0, 0, 0, time.UTC)
	clock := base
	g.now = func() time.Time {
		clock = clock.Add(time.Millisecond)
		return clock
	}
	g.authorizationRecheck = time.Microsecond
	checks := 0
	g.authorizeLease = func(*lease) bool {
		checks++
		return checks == 1
	}
	lz := &lease{owner: ml, rid: "long", tok: "token", mode: leaseRead, matter: mid, rev: 1,
		expires: base.Add(time.Minute), maxExpires: base.Add(2 * time.Minute), substancePath: path}
	g.leases[lz.rid] = lz
	req := httptest.NewRequest(http.MethodGet, "/substance/long", nil)
	req.Header.Set("Authorization", "Bearer token")
	rr := httptest.NewRecorder()
	g.handleSubstance(rr, req)
	if checks < 2 {
		t.Fatalf("authorization checks=%d want initial and in-stream", checks)
	}
	if g.getLease(lz.rid) != nil {
		t.Fatal("revoked long read retained its lease")
	}
	if rr.Body.Len() == len(content) {
		t.Fatal("revoked long read delivered the complete substance")
	}
}

func TestHTTPSubstanceGetterM2BoundsSlowReadAndClearsConnectionDeadline(t *testing.T) {
	const mid = "read-deadline"
	ml, ctxDir := newMatterLoopForGetterWithCatalog(t, mid, 1)
	g := NewHTTPSubstanceGetter(ml)
	path := filepath.Join(ctxDir, matterDirName, mid+".data")
	content := []byte("bounded read")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	g.readIdleTimeout = 3 * time.Second
	lz := &lease{owner: ml, rid: "deadline", tok: "token", mode: leaseRead, matter: mid, rev: 1,
		expires: time.Now().Add(time.Minute), maxExpires: time.Now().Add(2 * time.Minute), substancePath: path}
	g.leases[lz.rid] = lz
	recorder := &deadlineResponseRecorder{ResponseRecorder: httptest.NewRecorder()}
	request := httptest.NewRequest(http.MethodGet, "/substance/deadline", nil)
	request.Header.Set("Authorization", "Bearer token")
	g.handleSubstance(recorder, request)
	if recorder.Code != http.StatusOK || !bytes.Equal(recorder.Body.Bytes(), content) {
		t.Fatalf("read response status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if len(recorder.deadlines) < 2 || recorder.deadlines[0].IsZero() || !recorder.deadlines[len(recorder.deadlines)-1].IsZero() {
		t.Fatalf("write deadlines were not armed then cleared: %v", recorder.deadlines)
	}
}

func TestHTTPSubstanceGetterPublishesConfiguredHTTPSBaseAndHidesHealth(t *testing.T) {
	g := NewHTTPSubstanceGetter(newMatterLoopForGetter())
	g.publicURL = "https://engine.example/brique/"
	g.listenAddr = "127.0.0.1:0"
	if err := g.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.Stop() })
	if got := g.BaseURL(); got != "https://engine.example/brique" {
		t.Fatalf("BaseURL=%q", got)
	}
	response, err := http.Get("http://" + g.ln.Addr().String() + healthzPath)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("public health status=%d want 404", response.StatusCode)
	}
}

func TestHTTPSubstanceGetterPublicModeRequiresBearerAndKeepsLocalTransition(t *testing.T) {
	makeGetter := func(public bool) *HTTPSubstanceGetter {
		g := NewHTTPSubstanceGetter(newMatterLoopForGetter())
		if public {
			g.publicURL = "https://engine.example/brique"
		}
		g.leases["rid"] = &lease{rid: "rid", tok: "secret", mode: leaseRead, matter: "m", expires: time.Now().Add(time.Minute)}
		return g
	}
	request := func(g *HTTPSubstanceGetter, target, authorization string) int {
		req := httptest.NewRequest(http.MethodPost, target, nil)
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		rr := httptest.NewRecorder()
		g.handleSubstance(rr, req)
		return rr.Code
	}

	public := makeGetter(true)
	for _, tc := range []struct{ target, authorization string }{
		{"/substance/rid?tok=secret", ""},
		{"/substance/rid", "Bearer wrong"},
		{"/substance/unknown", "Bearer secret"},
	} {
		if status := request(public, tc.target, tc.authorization); status != http.StatusNotFound {
			t.Fatalf("public unauthorized status=%d want neutral 404", status)
		}
	}
	if status := request(public, "/substance/rid", "Bearer secret"); status != http.StatusMethodNotAllowed {
		t.Fatalf("public bearer status=%d want authenticated method response", status)
	}

	local := makeGetter(false)
	if status := request(local, "/substance/rid?tok=secret", ""); status != http.StatusMethodNotAllowed {
		t.Fatalf("local query transition status=%d", status)
	}
	if status := request(local, "/substance/rid", "Bearer secret"); status != http.StatusMethodNotAllowed {
		t.Fatalf("local bearer transition status=%d", status)
	}
}

func TestHTTPSubstanceGetterRefusesUnsafePublicConfiguration(t *testing.T) {
	for _, tc := range []struct{ publicURL, listen string }{
		{"http://engine.example/brique", "127.0.0.1:0"},
		{"", "0.0.0.0:0"},
	} {
		g := NewHTTPSubstanceGetter(newMatterLoopForGetter())
		g.publicURL, g.listenAddr = tc.publicURL, tc.listen
		if err := g.Start(); err == nil {
			_ = g.Stop()
			t.Fatalf("Start(%q, %q) succeeded", tc.publicURL, tc.listen)
		}
	}
}

func TestHTTPSubstanceGetterSharesOneListenerAcrossMatterLoops(t *testing.T) {
	newOwner := func(contextID string) (*MatterLoop, string) {
		directory := t.TempDir()
		frame := &junction.ContextRegistry{CtxId: contextID, ContextDir: directory}
		return NewMatterLoop(frame), directory
	}
	ownerA, dirA := newOwner("/root/a")
	sharedGetter := ownerA.SubstanceHTTP()
	frameB := &junction.ContextRegistry{CtxId: "/root/b", ContextDir: t.TempDir()}
	ownerB := NewMatterLoopWithSharedHTTP(frameB, nil, sharedGetter)
	dirB := frameB.ContextDir
	if ownerB.SubstanceHTTP() != sharedGetter {
		t.Fatal("second Matter loop did not join the instance data plane")
	}
	if err := sharedGetter.Acquire(ownerA); err != nil {
		t.Fatal(err)
	}
	if err := sharedGetter.Acquire(ownerB); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = sharedGetter.Release(ownerA)
		_ = sharedGetter.Release(ownerB)
	})

	prepare := func(owner *MatterLoop, directory, content string, revision int64) SubstanceHTTPHandle {
		root := filepath.Join(directory, matterDirName)
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "same.data"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		owner.catalogMatter["same"] = CatalogEntry{Brique: map[string]any{circulation.KeyRevision: revision}}
		handle, err := sharedGetter.OpenReadRangeLeaseFor(owner, directory, "same", "data", revision, time.Minute, 0, int64(len(content)))
		if err != nil {
			t.Fatal(err)
		}
		return handle
	}
	handleA := prepare(ownerA, dirA, "context-a", 1)
	handleB := prepare(ownerB, dirB, "context-b", 7)
	if handleA.BaseURL != handleB.BaseURL || handleA.BaseURL != sharedGetter.BaseURL() {
		t.Fatalf("lease origins differ: A=%q B=%q shared=%q", handleA.BaseURL, handleB.BaseURL, sharedGetter.BaseURL())
	}

	if err := sharedGetter.Release(ownerA); err != nil {
		t.Fatal(err)
	}
	request := func(handle SubstanceHTTPHandle) (int, string) {
		req, err := http.NewRequest(http.MethodGet, handle.BaseURL+handle.Path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+handle.Tok)
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, string(body)
	}
	if status, _ := request(handleA); status != http.StatusNotFound {
		t.Fatalf("stopped Context lease status=%d want 404", status)
	}
	if status, body := request(handleB); status != http.StatusOK || body != "context-b" {
		t.Fatalf("live Context lease status=%d body=%q", status, body)
	}
	if sharedGetter.BaseURL() == "" {
		t.Fatal("stopping one Context stopped the shared listener")
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
		stagedMatter:     []byte("{}"),
	}
	g.mu.Lock()
	g.leases[lz.rid] = lz
	g.mu.Unlock()
	if status := g.claimWriteLease(lz.rid, lz); status != "" {
		t.Fatalf("claim stale upload: %s", status)
	}

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
