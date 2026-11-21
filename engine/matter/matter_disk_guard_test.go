/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package matter

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"brique_engine/engine/circulation"
	"brique_engine/engine/configuration"
	"brique_engine/engine/junction"
)

func withFreeDisk(t *testing.T, free uint64, err error) {
	t.Helper()
	previous := freeDiskBytes
	freeDiskBytes = func(string) (uint64, error) { return free, err }
	t.Cleanup(func() { freeDiskBytes = previous })
}

func TestMatterDiskGuardConfiguration(t *testing.T) {
	if l := NewMatterLoopWithConfig(nil, map[string]any{}); l.minFreeDiskBytes != 256<<20 {
		t.Fatalf("default floor = %d", l.minFreeDiskBytes)
	}
	if l := NewMatterLoopWithConfig(nil, map[string]any{configuration.KeyMatterMinFreeDiskMiB: int64(0)}); l.minFreeDiskBytes != 0 {
		t.Fatalf("explicit zero must disable the guard, got %d", l.minFreeDiskBytes)
	}
	if l := NewMatterLoopWithConfig(nil, map[string]any{configuration.KeyMatterMinFreeDiskMiB: int64(7)}); l.minFreeDiskBytes != 7<<20 {
		t.Fatalf("configured floor = %d", l.minFreeDiskBytes)
	}
	if l := NewMatterLoopWithConfig(nil, map[string]any{configuration.KeyMatterMinFreeDiskMiB: int64(-3)}); l.minFreeDiskBytes != 256<<20 {
		t.Fatalf("a negative floor must keep the default, got %d", l.minFreeDiskBytes)
	}
}

func TestMatterDiskGuardRefusesEveryMutationUnderTheFloor(t *testing.T) {
	cases := map[string]func(*MatterLoop, circulation.Message){
		"matter.create": (*MatterLoop).capMatterCreate,
		"matter.write":  (*MatterLoop).capMatterWrite,
		"matter.append": (*MatterLoop).capMatterAppend,
		"matter.clone":  (*MatterLoop).capMatterClone,
		"matter.derive": (*MatterLoop).capMatterDerive,
	}
	for name, capability := range cases {
		withFreeDisk(t, 1<<20, nil)
		l, out, _ := newMatterLoopHarness(t)
		l.minFreeDiskBytes = 64 << 20
		capability(l, circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
			IntentionID: "i-" + name, AwaitResponse: true, Params: map[string]any{circulation.KeyMatterID: "m1"},
		}})
		response := recvMatterLoopMsg(t, out, name).Response
		if response.Status == circulation.ValueStatusOK || response.Error.Code != circulation.ValueCodeRefused ||
			response.Error.Details[circulation.KeyReason] != circulation.ValueReasonDiskSpaceLow {
			t.Fatalf("%s under the floor = %+v", name, response)
		}
	}
}

func TestMatterDiskGuardLetsRoomOrDisabledGuardThrough(t *testing.T) {
	l, _, _ := newMatterLoopHarness(t)
	l.minFreeDiskBytes = 64 << 20
	withFreeDisk(t, 64<<20, nil)
	if l.diskLow() {
		t.Fatal("free space equal to the floor must be accepted")
	}
	withFreeDisk(t, 64<<20-1, nil)
	if !l.diskLow() {
		t.Fatal("one byte under the floor must refuse")
	}
	l.minFreeDiskBytes = 0
	withFreeDisk(t, 0, nil)
	if l.diskLow() {
		t.Fatal("a disabled guard refused")
	}
}

func TestMatterDiskGuardFailsClosedWhenSpaceCannotBeMeasured(t *testing.T) {
	l, _, _ := newMatterLoopHarness(t)
	l.minFreeDiskBytes = 64 << 20
	withFreeDisk(t, 0, errors.New("statfs failed"))
	if !l.diskLow() {
		t.Fatal("an unmeasurable disk must refuse mutations")
	}
}

func TestUploadStopsWhenTheDiskFillsDuringTheTransfer(t *testing.T) {
	previousEvery := diskRecheckEveryBytes
	diskRecheckEveryBytes = 1024
	t.Cleanup(func() { diskRecheckEveryBytes = previousEvery })
	// The PUT handler already let this upload open; the space is gone by the time bytes arrive.
	// receiveAndCommit is called directly, so the first measurement is the one made while receiving.
	previous := freeDiskBytes
	freeDiskBytes = func(string) (uint64, error) { return 1 << 20, nil }
	t.Cleanup(func() { freeDiskBytes = previous })

	mid := "m-fill"
	var rev int64 = 5
	ml, ctxDir := newMatterLoopForGetterWithCatalog(t, mid, rev)
	ml.minFreeDiskBytes = 64 << 20
	ml.frame = &junction.ContextRegistry{CtxId: "/ctx/fill", ContextDir: ctxDir}
	g := NewHTTPSubstanceGetter(ml)
	mRoot := filepath.Join(ctxDir, matterDirName)
	tmpPayload := filepath.Join(mRoot, mid+".data"+tmpSuffix)
	finalPayload := filepath.Join(mRoot, mid+".data")
	tmpMatter := filepath.Join(mRoot, mid+matterDescriptorSuffix+tmpSuffix)
	if err := os.WriteFile(tmpMatter, []byte(`{"brique":{"kind":"matter","substance_mode":"brique","revision":6}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	lz := &lease{rid: "w-fill", tok: "tok", mode: leaseWrite, matter: mid, rev: rev, owner: ml, matterRoot: mRoot,
		tmpSubstancePath: tmpPayload, substancePath: finalPayload, tmpMatterPath: tmpMatter,
		newBrique: map[string]any{circulation.KeyKind: circulation.ValueEntryKindMatter, circulation.KeySubstanceMode: circulation.ValueModeBrique,
			circulation.KeyRevision: rev + 1},
		expires: time.Now().Add(time.Minute)}
	g.mu.Lock()
	g.leases[lz.rid] = lz
	g.mu.Unlock()
	if status := g.claimWriteLease(lz.rid, lz); status != "" {
		t.Fatalf("claim write upload: %s", status)
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/substance/w-fill?tok=tok", strings.NewReader(strings.Repeat("x", 8192)))
	g.receiveAndCommit(rr, req, lz)
	if rr.Code != http.StatusInsufficientStorage {
		t.Fatalf("an upload that filled the disk answered %d: %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(finalPayload); err == nil {
		t.Fatal("a partial upload was committed")
	}
	if _, err := os.Stat(tmpPayload); err == nil {
		t.Fatal("the staging file of a refused upload was kept")
	}
	if strings.Contains(rr.Body.String(), ctxDir) {
		t.Fatal("the refusal leaks a local path")
	}
}
