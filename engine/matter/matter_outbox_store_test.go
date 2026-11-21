/* Copyright 2026 Nicolas Cassan. Licensed under the Apache License, Version 2.0. */

package matter

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"brique_engine/engine/circulation"
	"brique_engine/engine/junction"
)

// The durable outbox is one file per event (format /3). These tests state its invariants:
//  1. an added event survives a restart, in order;
//  2. an acknowledged event does not come back, and a lost acknowledgement only means a duplicate;
//  3. adding or acknowledging one event writes nothing else, whatever the backlog;
//  4. an interrupted migration is redone from the /2 file, which stays the truth until the commit;
//  5. an engine that only knows /2 refuses the /3 state instead of reading an empty outbox.

func outboxTestSubscription() MatterSubscription {
	return MatterSubscription{ID: "sub", MatterID: "matter", Durable: true,
		Target: circulation.Address{Context: "/root/client", Type: "user", Cap: "consume"}}
}

// outboxStoreWithSubscription returns a loop on a fresh Context whose subscription store is valid.
func outboxStoreWithSubscription(t *testing.T) (*MatterLoop, string, string) {
	t.Helper()
	loop, dir := eventStoreFixture(t)
	writeEventFixture(t, filepath.Join(dir, "subscriptions.json"), matterSubscriptionsStore{
		Format: matterSubscriptionsStoreFormat, Subscriptions: []MatterSubscription{outboxTestSubscription()}})
	return loop, dir, loop.frame.ContextDir
}

// restart builds a new loop over the same Context directory, as a process restart does.
func restartedLoop(t *testing.T, contextDir string) *MatterLoop {
	t.Helper()
	loop := NewMatterLoop(&junction.ContextRegistry{CtxId: "/root", ContextDir: contextDir})
	if err := loop.loadMatterEventState(); err != nil {
		t.Fatalf("restart: %v", err)
	}
	return loop
}

func addOutboxEvent(t *testing.T, loop *MatterLoop, id string) {
	t.Helper()
	subscription := outboxTestSubscription()
	event := MatterPendingEvent{EventID: id, SubID: subscription.ID, MatterID: subscription.MatterID,
		Target: subscription.Target, Params: map[string]any{circulation.KeyEventId: id}, CreatedAt: 1}
	loop.outboxMu.Lock()
	defer loop.outboxMu.Unlock()
	if err := loop.persistOutboxAddLocked(&event); err != nil {
		t.Fatal(err)
	}
	loop.outbox = append(loop.outbox, event)
}

func outboxIDs(loop *MatterLoop) []string {
	loop.outboxMu.Lock()
	defer loop.outboxMu.Unlock()
	ids := make([]string, 0, len(loop.outbox))
	for _, event := range loop.outbox {
		ids = append(ids, event.EventID)
	}
	return ids
}

func TestOutboxEventsSurviveARestartInOrderAndAcknowledgedOnesDoNot(t *testing.T) {
	loop, _, contextDir := outboxStoreWithSubscription(t)
	if err := loop.loadMatterEventState(); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"e1", "e2", "e3", "e4"} {
		addOutboxEvent(t, loop, id)
	}
	if removed, next, err := loop.ackOutbox("sub", "e2"); err != nil || !removed || next == nil || next.EventID != "e1" {
		t.Fatalf("ack e2 = %v, next=%v, err=%v", removed, next, err)
	}
	if removed, _, err := loop.ackOutbox("sub", "e2"); err != nil || removed {
		t.Fatalf("a second acknowledgement of the same event reported %v, %v", removed, err)
	}
	restarted := restartedLoop(t, contextDir)
	if got := strings.Join(outboxIDs(restarted), ","); got != "e1,e3,e4" {
		t.Fatalf("outbox after restart = %s, want e1,e3,e4 in order", got)
	}
	// Positions are never reused: an event added after the restart goes last and takes a new file.
	addOutboxEvent(t, restarted, "e5")
	again := restartedLoop(t, contextDir)
	if got := strings.Join(outboxIDs(again), ","); got != "e1,e3,e4,e5" {
		t.Fatalf("outbox after a second restart = %s", got)
	}
}

func TestAcknowledgingOrAddingOneEventWritesNoOtherEventFile(t *testing.T) {
	loop, dir, _ := outboxStoreWithSubscription(t)
	if err := loop.loadMatterEventState(); err != nil {
		t.Fatal(err)
	}
	const backlog = 300
	for index := 0; index < backlog; index++ {
		addOutboxEvent(t, loop, fmt.Sprintf("e%04d", index))
	}
	directory := filepath.Join(dir, matterOutboxDirName)
	snapshot := func() map[string]os.FileInfo {
		entries, err := os.ReadDir(directory)
		if err != nil {
			t.Fatal(err)
		}
		files := make(map[string]os.FileInfo, len(entries))
		for _, entry := range entries {
			info, err := os.Stat(filepath.Join(directory, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			files[entry.Name()] = info
		}
		return files
	}
	before := snapshot()
	if _, _, err := loop.ackOutbox("sub", "e0150"); err != nil {
		t.Fatal(err)
	}
	addOutboxEvent(t, loop, "late")
	after := snapshot()
	if len(after) != backlog {
		t.Fatalf("%d files after one ack and one add on a backlog of %d", len(after), backlog)
	}
	changed := 0
	for name, info := range before {
		if next, kept := after[name]; kept && !os.SameFile(info, next) {
			changed++
		}
	}
	if changed != 0 {
		t.Fatalf("%d untouched event files were rewritten", changed)
	}
}

func TestOutboxMigrationFromTheOldFileIsRedoneAfterAnInterruption(t *testing.T) {
	loop, dir, contextDir := outboxStoreWithSubscription(t)
	subscription := outboxTestSubscription()
	writeEventFixture(t, filepath.Join(dir, "outbox.json"), matterOutboxStore{Format: matterOutboxStoreFormat, Events: []MatterPendingEvent{
		{EventID: "a", SubID: "sub", MatterID: "matter", Target: subscription.Target},
		{EventID: "b", SubID: "sub", MatterID: "matter", Target: subscription.Target},
	}})
	// A previous run wrote part of the new layout and stopped before the commit.
	stale := filepath.Join(dir, matterOutboxDirName)
	if err := os.MkdirAll(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	writeEventFixture(t, filepath.Join(stale, outboxEventFileName(7)), matterOutboxEventFile{
		Format: matterOutboxEventFormat, Seq: 7, Event: MatterPendingEvent{EventID: "ghost", SubID: "sub", MatterID: "matter", Target: subscription.Target}})
	if err := loop.loadMatterEventState(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(outboxIDs(loop), ","); got != "a,b" {
		t.Fatalf("migrated outbox = %s: the half-written layout leaked in", got)
	}
	if got := readEventFixtureObject(t, filepath.Join(dir, "outbox.json"))["format"]; got != matterOutboxManifestFormat {
		t.Fatalf("manifest format = %v", got)
	}
	if got := strings.Join(outboxIDs(restartedLoop(t, contextDir)), ","); got != "a,b" {
		t.Fatalf("outbox after migration and restart = %s", got)
	}
}

func TestOutboxRefusesADirectoryWithoutItsManifestAndAnEventOfAnotherFormat(t *testing.T) {
	loop, dir, _ := outboxStoreWithSubscription(t)
	if err := os.MkdirAll(filepath.Join(dir, matterOutboxDirName), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := loop.loadMatterEventState(); err == nil || !strings.Contains(err.Error(), "manifest") {
		t.Fatalf("an outbox directory without manifest was accepted: %v", err)
	}

	loop, dir, _ = outboxStoreWithSubscription(t)
	writeEventFixture(t, filepath.Join(dir, "outbox.json"), matterOutboxManifest{Format: matterOutboxManifestFormat, Directory: matterOutboxDirName})
	if err := os.MkdirAll(filepath.Join(dir, matterOutboxDirName), 0o700); err != nil {
		t.Fatal(err)
	}
	writeEventFixture(t, filepath.Join(dir, matterOutboxDirName, outboxEventFileName(1)), matterOutboxEventFile{
		Format: "brique.matter-outbox-event/99", Seq: 1, Event: MatterPendingEvent{EventID: "x"}})
	if err := loop.loadMatterEventState(); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("an event of a future format was accepted: %v", err)
	}
}

func TestOutboxIgnoresAnInterruptedWriteAndKeepsUnknownFiles(t *testing.T) {
	loop, dir, contextDir := outboxStoreWithSubscription(t)
	if err := loop.loadMatterEventState(); err != nil {
		t.Fatal(err)
	}
	addOutboxEvent(t, loop, "kept")
	directory := filepath.Join(dir, matterOutboxDirName)
	interrupted := filepath.Join(directory, "."+outboxEventFileName(2)+".tmp-123456")
	if err := os.WriteFile(interrupted, []byte(`{"format":`), 0o600); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(directory, ".DS_Store")
	if err := os.WriteFile(foreign, []byte("finder"), 0o600); err != nil {
		t.Fatal(err)
	}
	restarted := restartedLoop(t, contextDir)
	if got := strings.Join(outboxIDs(restarted), ","); got != "kept" {
		t.Fatalf("outbox after an interrupted write = %s", got)
	}
	if _, err := os.Stat(interrupted); err == nil {
		t.Fatal("the leftover of an interrupted write was kept")
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatal("a file this engine did not write was deleted")
	}
}

// An engine that only reads /2 must stop rather than start with an empty outbox: the old reader is
// readMatterOutbox, which accepts exactly the /2 format and the legacy array.
func TestAnEngineThatOnlyKnowsTheOldOutboxFormatRefusesTheNewState(t *testing.T) {
	loop, dir, _ := outboxStoreWithSubscription(t)
	if err := loop.loadMatterEventState(); err != nil {
		t.Fatal(err)
	}
	addOutboxEvent(t, loop, "x")
	if _, _, err := readMatterOutbox(filepath.Join(dir, "outbox.json")); err == nil {
		t.Fatal("the /2 reader accepted the /3 manifest")
	}
}

func TestAcknowledgingAnEventWhoseFileCannotBeRemovedKeepsItPending(t *testing.T) {
	loop, dir, _ := outboxStoreWithSubscription(t)
	if err := loop.loadMatterEventState(); err != nil {
		t.Fatal(err)
	}
	addOutboxEvent(t, loop, "stuck")
	// Replace the file by a non-empty directory of the same name: os.Remove fails on it.
	path := filepath.Join(dir, matterOutboxDirName, outboxEventFileName(1))
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(path, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	if removed, _, err := loop.ackOutbox("sub", "stuck"); err == nil || removed {
		t.Fatalf("an acknowledgement that could not be persisted was reported as done: %v, %v", removed, err)
	}
	if got := strings.Join(outboxIDs(loop), ","); got != "stuck" {
		t.Fatalf("memory dropped an event the disk still holds: %q", got)
	}
}
