/* Copyright 2026 Nicolas Cassan. Licensed under the Apache License, Version 2.0. */

package matter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"brique_engine/circulation"
	"brique_engine/junction"
)

func eventStoreFixture(t *testing.T) (*MatterLoop, string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, matterDirName, matterEventDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return NewMatterLoop(&junction.ContextRegistry{CtxId: "/root", ContextDir: root}), dir
}

func writeEventFixture(t *testing.T, path string, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readEventFixtureObject(t *testing.T, path string) map[string]any {
	t.Helper()
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatal(err)
	}
	return object
}

func TestLegacyMatterEventArraysMigrateWithDemonstrableOwner(t *testing.T) {
	loop, dir := eventStoreFixture(t)
	subscription := MatterSubscription{
		ID: "sub", MatterID: "matter", Durable: true, Owner: "partner-a", OwnerActor: "reader",
		Target: circulation.Address{Context: "@ext_partner-a:/root/client", Type: "user", Cap: "consume"},
	}
	event := MatterPendingEvent{
		EventID: "event", SubID: "sub", MatterID: "matter", Target: subscription.Target,
		Params: map[string]any{circulation.KeyEventId: "event"},
	}
	writeEventFixture(t, filepath.Join(dir, "subscriptions.json"), []MatterSubscription{subscription})
	writeEventFixture(t, filepath.Join(dir, "outbox.json"), []MatterPendingEvent{event})

	if err := loop.loadMatterEventState(); err != nil {
		t.Fatal(err)
	}
	if len(loop.outbox) != 1 || loop.outbox[0].Owner != "partner-a" || loop.outbox[0].OwnerActor != "reader" {
		t.Fatalf("migrated outbox = %#v", loop.outbox)
	}
	if got := readEventFixtureObject(t, filepath.Join(dir, "subscriptions.json"))["format"]; got != matterSubscriptionsStoreFormat {
		t.Fatalf("subscription format = %v", got)
	}
	if got := readEventFixtureObject(t, filepath.Join(dir, "outbox.json"))["format"]; got != matterOutboxManifestFormat {
		t.Fatalf("outbox format = %v", got)
	}
	if files, _ := filepath.Glob(filepath.Join(dir, matterOutboxDirName, "*.json")); len(files) != 1 {
		t.Fatalf("the migrated event was not written as its own file: %v", files)
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "quarantine-*.json")); len(matches) != 0 {
		t.Fatalf("valid legacy records were quarantined: %v", matches)
	}
}

func TestInterruptedMatterEventMigrationResumesFromVersionedSubscriptions(t *testing.T) {
	loop, dir := eventStoreFixture(t)
	subscription := MatterSubscription{
		ID: "local", MatterID: "matter", Durable: true,
		Target: circulation.Address{Context: "/root/client", Type: "user", Cap: "consume"},
	}
	writeEventFixture(t, filepath.Join(dir, "subscriptions.json"), matterSubscriptionsStore{
		Format: matterSubscriptionsStoreFormat, Subscriptions: []MatterSubscription{subscription},
	})
	writeEventFixture(t, filepath.Join(dir, "outbox.json"), []MatterPendingEvent{{
		EventID: "event", SubID: "local", MatterID: "matter", Target: subscription.Target,
	}})
	if err := loop.loadMatterEventState(); err != nil {
		t.Fatal(err)
	}
	if len(loop.outbox) != 1 || loop.outbox[0].Owner != "" {
		t.Fatalf("resumed outbox = %#v", loop.outbox)
	}
	if got := readEventFixtureObject(t, filepath.Join(dir, "outbox.json"))["format"]; got != matterOutboxManifestFormat {
		t.Fatalf("outbox was not migrated: %v", got)
	}
}

func TestAmbiguousLegacyMatterEventStateIsQuarantined(t *testing.T) {
	loop, dir := eventStoreFixture(t)
	ambiguous := MatterSubscription{
		ID: "ambiguous", MatterID: "matter", Durable: true,
		Target: circulation.Address{Context: "@ext_partner-a:/root/client", Type: "user", Cap: "consume"},
	}
	writeEventFixture(t, filepath.Join(dir, "subscriptions.json"), []MatterSubscription{ambiguous})
	writeEventFixture(t, filepath.Join(dir, "outbox.json"), []MatterPendingEvent{{
		EventID: "event", SubID: "ambiguous", MatterID: "matter", Target: ambiguous.Target,
	}})
	if err := loop.loadMatterEventState(); err != nil {
		t.Fatal(err)
	}
	if len(loop.listSubs("matter")) != 0 || len(loop.outbox) != 0 {
		t.Fatalf("ambiguous state became active: subs=%#v outbox=%#v", loop.listSubs("matter"), loop.outbox)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "quarantine-*.json"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("quarantine files = %v, err=%v", matches, err)
	}
	quarantine := readEventFixtureObject(t, matches[0])
	if quarantine["format"] != matterQuarantineFormat || !strings.Contains(quarantine["reason"].(string), "ambiguous") {
		t.Fatalf("quarantine = %#v", quarantine)
	}
	if subscriptions, ok := readEventFixtureObject(t, filepath.Join(dir, "subscriptions.json"))["subscriptions"].([]any); !ok || len(subscriptions) != 0 {
		t.Fatalf("active subscription store retained ambiguous entry: %#v", subscriptions)
	}
	if err := loop.quarantineMatterEventState([]MatterSubscription{ambiguous}, []MatterPendingEvent{{
		EventID: "event", SubID: "ambiguous", MatterID: "matter", Target: ambiguous.Target,
	}}); err != nil {
		t.Fatal(err)
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "quarantine-*.json")); len(matches) != 1 {
		t.Fatalf("the same evidence created repeated quarantines: %v", matches)
	}
}

func TestMatterEventStoreRefusesUnknownOrMismatchedVersionedState(t *testing.T) {
	t.Run("future format", func(t *testing.T) {
		loop, dir := eventStoreFixture(t)
		writeEventFixture(t, filepath.Join(dir, "subscriptions.json"), matterSubscriptionsStore{Format: "brique.matter-subscriptions/99"})
		if err := loop.loadMatterEventState(); err == nil || !strings.Contains(err.Error(), "unsupported") {
			t.Fatalf("future format error = %v", err)
		}
	})

	t.Run("owner mismatch", func(t *testing.T) {
		loop, dir := eventStoreFixture(t)
		subscription := MatterSubscription{ID: "sub", MatterID: "matter", Durable: true, Owner: "partner-a",
			Target: circulation.Address{Context: "@ext_partner-a:/root/client", Type: "user", Cap: "consume"}}
		writeEventFixture(t, filepath.Join(dir, "subscriptions.json"), matterSubscriptionsStore{
			Format: matterSubscriptionsStoreFormat, Subscriptions: []MatterSubscription{subscription},
		})
		writeEventFixture(t, filepath.Join(dir, "outbox.json"), matterOutboxStore{
			Format: matterOutboxStoreFormat, Events: []MatterPendingEvent{{
				EventID: "event", SubID: "sub", MatterID: "matter", Target: subscription.Target, Owner: "partner-b",
			}},
		})
		if err := loop.loadMatterEventState(); err != nil {
			t.Fatal(err)
		}
		if len(loop.outbox) != 0 {
			t.Fatalf("mismatched owner became active: %#v", loop.outbox)
		}
		if matches, _ := filepath.Glob(filepath.Join(dir, "quarantine-*.json")); len(matches) != 1 {
			t.Fatalf("owner mismatch quarantine = %v", matches)
		}
	})
}

func TestMatterEventMarkersMigrateQuarantineAndRefuseFutureFormats(t *testing.T) {
	valid := MatterSubscription{ID: "local", MatterID: "matter", Durable: true,
		Target: circulation.Address{Context: "/root/client", Type: "user", Cap: "consume"}}

	t.Run("legacy marker", func(t *testing.T) {
		loop, dir := eventStoreFixture(t)
		path := filepath.Join(dir, "event-legacy.json")
		writeEventFixture(t, path, struct {
			ID            string               `json:"id"`
			MatterID      string               `json:"matter_id"`
			Revision      int64                `json:"revision"`
			Params        map[string]any       `json:"params"`
			Subscriptions []MatterSubscription `json:"subscriptions"`
		}{ID: "legacy", MatterID: "matter", Revision: 2, Params: map[string]any{}, Subscriptions: []MatterSubscription{valid}})
		if err := loop.reconcileMatterEventMarkers(false); err != nil {
			t.Fatal(err)
		}
		if got := readEventFixtureObject(t, path)["format"]; got != matterEventMarkerFormat {
			t.Fatalf("marker format = %v", got)
		}
	})

	t.Run("ambiguous marker", func(t *testing.T) {
		loop, dir := eventStoreFixture(t)
		ambiguous := valid
		ambiguous.Owner = ""
		ambiguous.Target.Context = "@ext_partner-a:/root/client"
		path := filepath.Join(dir, "event-ambiguous.json")
		writeEventFixture(t, path, matterEventMarker{Format: matterEventMarkerFormat, ID: "ambiguous", MatterID: "matter",
			Params: map[string]any{}, Subscriptions: []MatterSubscription{ambiguous}})
		if err := loop.reconcileMatterEventMarkers(false); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("ambiguous marker remained active: %v", err)
		}
		if matches, _ := filepath.Glob(filepath.Join(dir, "quarantine-*.json")); len(matches) != 1 {
			t.Fatalf("ambiguous marker quarantine = %v", matches)
		}
	})

	t.Run("future marker", func(t *testing.T) {
		loop, dir := eventStoreFixture(t)
		writeEventFixture(t, filepath.Join(dir, "event-future.json"), matterEventMarker{
			Format: "brique.matter-event-marker/99", ID: "future", MatterID: "matter",
			Params: map[string]any{}, Subscriptions: []MatterSubscription{valid},
		})
		if err := loop.reconcileMatterEventMarkers(false); err == nil || !strings.Contains(err.Error(), "unsupported") {
			t.Fatalf("future marker error = %v", err)
		}
	})
}

func TestConcurrentDurableSubscriptionSnapshotsNeverPublishAnOlderStateLast(t *testing.T) {
	loop, _ := eventStoreFixture(t)
	const count = 64
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			id := "sub-" + strconv.Itoa(index)
			loop.addSub("matter", MatterSubscription{ID: id, MatterID: "matter", Durable: true,
				Target: circulation.Address{Context: "/ctx/client", Type: "user", Cap: "consume"}})
			if err := loop.persistMatterSubscriptions(); err != nil {
				t.Errorf("persist %d: %v", index, err)
			}
		}(i)
	}
	wg.Wait()
	reloaded := NewMatterLoop(&junction.ContextRegistry{CtxId: "/root", ContextDir: loop.frame.ContextDir})
	if err := reloaded.loadMatterEventState(); err != nil {
		t.Fatal(err)
	}
	if subscriptions := reloaded.listSubs("matter"); len(subscriptions) != count {
		t.Fatalf("persisted subscriptions = %d, want %d", len(subscriptions), count)
	}
}
