/* Copyright 2026 Nicolas Cassan. Licensed under the Apache License, Version 2.0. */

package matter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/shared"
)

func TestDurableSubscriptionPersistsDeliversAndAcknowledges(t *testing.T) {
	l, comm, root, _ := createAppendFixture(t, circulation.ValueFramingBytes, "base")
	target := map[string]any{circulation.KeyContext: "/ctx/consumer", circulation.KeyType: circulation.ValueTypeUser, circulation.KeyCap: "consume"}
	l.capMatterSubscribe(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "subscribe-durable", From: circulation.Address{Context: "/caller", Type: circulation.ValueTypeUser, Cap: "caller"},
		Params: map[string]any{circulation.KeyMatterID: "log", circulation.KeySubID: "sub-1", circulation.KeyDurable: true, circulation.KeyTarget: target},
	}})
	response := recvMatterLoopMsg(t, comm, "durable subscribe")
	if response.Response.Status != circulation.ValueStatusOK || response.Response.Payload[circulation.KeyDurable] != true {
		t.Fatalf("subscribe response: %#v", response)
	}

	l.capMatterAppend(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "append-durable", Params: map[string]any{circulation.KeyMatterID: "log", circulation.KeyData: "!"},
	}})
	_ = recvMatterLoopMsg(t, comm, "append response")
	event := recvMatterLoopMsg(t, comm, "durable event")
	if event.Intention.To.Context != "/ctx/consumer" || event.Intention.Params[circulation.KeyEventId] == nil {
		t.Fatalf("event: %#v", event)
	}
	eventID := event.Intention.Params[circulation.KeyEventId].(string)

	frame := &junction.ContextRegistry{CtxId: "/ctx/matter-a", ContextDir: root, FamIn: junction.FamiliesInChanRegistry{shared.FamilyComm: make(chan circulation.Message, 16)}}
	reloaded := NewMatterLoop(frame)
	if err := reloaded.loadMatterEventState(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if subscriptions := reloaded.listSubs("log"); len(subscriptions) != 1 || !subscriptions[0].Durable {
		t.Fatalf("subscriptions after reload: %#v", subscriptions)
	}
	if len(reloaded.outbox) != 1 || reloaded.outbox[0].EventID != eventID {
		t.Fatalf("outbox after reload: %#v", reloaded.outbox)
	}
	removed, err := reloaded.ackPendingMatterEvent("sub-1", eventID)
	if err != nil || !removed || len(reloaded.outbox) != 0 {
		t.Fatalf("ack removed=%v err=%v outbox=%#v", removed, err, reloaded.outbox)
	}
	removed, err = reloaded.ackPendingMatterEvent("sub-1", eventID)
	if err != nil || removed {
		t.Fatalf("idempotent ack removed=%v err=%v", removed, err)
	}
}

func TestDurableEventMarkerReconcilesCommittedMutationAfterRestart(t *testing.T) {
	l, comm, root, revision := createAppendFixture(t, circulation.ValueFramingBytes, "base")
	l.capMatterSubscribe(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "subscribe-marker", From: circulation.Address{Context: "/ctx/consumer", Type: circulation.ValueTypeUser, Cap: "consume"},
		Params: map[string]any{circulation.KeyMatterID: "log", circulation.KeySubID: "sub-marker", circulation.KeyDurable: true},
	}})
	if response := recvMatterLoopMsg(t, comm, "marker subscription"); response.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("subscribe failed: %#v", response)
	}
	nextRevision := revision + 1
	marker, err := l.prepareMatterEventMarker("log", nextRevision, false, matterWrittenEventParams("log", nextRevision, "source"))
	if err != nil || marker == nil {
		t.Fatalf("prepare marker=%#v err=%v", marker, err)
	}
	markerPath := filepath.Join(root, matterDirName, matterEventDirName, "event-"+marker.ID+".json")
	if err := l.reconcileMatterEventMarkers(false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(markerPath); err != nil || len(l.outbox) != 0 {
		t.Fatalf("live reconciliation removed a pre-commit marker: stat=%v outbox=%#v", err, l.outbox)
	}
	descriptorPath := filepath.Join(root, matterDirName, "log"+matterDescriptorSuffix)
	doc, err := readJSONMapFile(descriptorPath)
	if err != nil {
		t.Fatal(err)
	}
	setBriqueRev(doc, nextRevision)
	encoded, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(descriptorPath, encoded, 0o644); err != nil {
		t.Fatal(err)
	}

	replayComm := make(chan circulation.Message, 16)
	reloaded := NewMatterLoop(&junction.ContextRegistry{CtxId: "/ctx/matter-a", ContextDir: root, FamIn: junction.FamiliesInChanRegistry{shared.FamilyComm: replayComm}})
	if err := reloaded.loadMatterEventState(); err != nil {
		t.Fatal(err)
	}
	if err := reloaded.reconcileMatterEventMarkers(true); err != nil {
		t.Fatal(err)
	}
	event := recvMatterLoopMsg(t, replayComm, "reconciled durable event")
	if event.Intention.Params[circulation.KeyEvent] != circulation.ValueEventMatterWritten || len(reloaded.outbox) != 1 {
		t.Fatalf("reconciled event=%#v outbox=%#v", event, reloaded.outbox)
	}
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Fatalf("completed marker still exists: %v", err)
	}
}

func TestDurableSubscriptionDowngradeAndUnsubscribeClearPersistentState(t *testing.T) {
	for _, mode := range []string{"downgrade", "unsubscribe"} {
		t.Run(mode, func(t *testing.T) {
			l, comm, root, _ := createAppendFixture(t, circulation.ValueFramingBytes, "base")
			subscribe := func(durable bool) {
				l.capMatterSubscribe(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
					IntentionID: "subscribe-" + mode, From: circulation.Address{Context: "/ctx/consumer", Type: circulation.ValueTypeUser, Cap: "consume"},
					Params: map[string]any{circulation.KeyMatterID: "log", circulation.KeySubID: "sub", circulation.KeyDurable: durable},
				}})
				if response := recvMatterLoopMsg(t, comm, "subscription"); response.Response.Status != circulation.ValueStatusOK {
					t.Fatalf("subscribe failed: %#v", response)
				}
			}
			subscribe(true)
			l.capMatterAppend(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{IntentionID: "append", Params: map[string]any{circulation.KeyMatterID: "log", circulation.KeyData: "!"}}})
			_ = recvMatterLoopMsg(t, comm, "append response")
			_ = recvMatterLoopMsg(t, comm, "pending event")
			if mode == "downgrade" {
				subscribe(false)
			} else {
				l.capMatterUnsubscribe(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
					IntentionID: "unsubscribe", Params: map[string]any{circulation.KeyMatterID: "log", circulation.KeySubID: "sub"},
				}})
				if response := recvMatterLoopMsg(t, comm, "unsubscribe"); response.Response.Status != circulation.ValueStatusOK {
					t.Fatalf("unsubscribe failed: %#v", response)
				}
			}
			if len(l.outbox) != 0 {
				t.Fatalf("outbox not cleared: %#v", l.outbox)
			}
			reloaded := NewMatterLoop(&junction.ContextRegistry{CtxId: "/ctx/matter-a", ContextDir: root, FamIn: junction.FamiliesInChanRegistry{shared.FamilyComm: make(chan circulation.Message, 4)}})
			if err := reloaded.loadMatterEventState(); err != nil {
				t.Fatal(err)
			}
			if subscriptions := reloaded.listSubs("log"); len(subscriptions) != 0 {
				t.Fatalf("durable subscription resurrected: %#v", subscriptions)
			}
		})
	}
}
