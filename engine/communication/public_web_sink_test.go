/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package comm

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"brique_engine/engine/circulation"
	"brique_engine/engine/junction"
	"brique_engine/engine/security/trust"
	"brique_engine/engine/shared"
)

func TestPublicWebSinkAdmitsAndReturnsOnlyToItsExactChannel(t *testing.T) {
	world := newRestrictedWorld(t)
	world.loop.frame.CtxId = shared.RootContextID
	world.loop.ingress = make(chan IngressItem, 2)
	world.loop.browserUI = make(map[string]func(circulation.Message) error)
	world.loop.browserPublicUI = make(map[string]browserPublicUIRoute)
	returned := make(chan []byte, 1)
	closed := 0
	enabled := true
	const channelID = "bpw1_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	sink, err := NewPublicWebSink(PublicWebSinkOptions{
		Loop: world.loop,
		Admit: func() (trust.Admission, error) {
			if !enabled {
				return trust.Admission{}, trust.ErrPublicWebUnavailable
			}
			return world.runtime.AdmitPublicWeb()
		},
		Deliver: func(got string, payload []byte) error {
			if got != channelID {
				t.Errorf("delivery channel = %q", got)
			}
			returned <- append([]byte(nil), payload...)
			return nil
		},
		ChannelClosed: func(got string) {
			if got != channelID {
				t.Errorf("closed channel = %q", got)
			}
			closed++
		},
		NewChannelID: func() (string, error) { return channelID, nil },
		Now:          func() time.Time { return time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	gotChannel, err := sink.Open("root")
	if err != nil || gotChannel != channelID {
		t.Fatalf("Open = %q, %v", gotChannel, err)
	}
	if !world.registry.UIChannelActive("public_web", channelID) {
		t.Fatal("public channel was not activated")
	}

	input := circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "123e4567-e89b-42d3-a456-426614174000", AwaitResponse: true,
		To:       circulation.Address{Context: "/root/catalog", Type: circulation.ValueTypeMatter, Cap: "matter.read"},
		From:     circulation.Address{Context: "/forged", Type: "admin", Cap: "control"},
		Identity: circulation.Identity{ID: "forged", Kind: "instance"},
	}}
	payload, _ := json.Marshal(input)
	if err := sink.Message(channelID, "root", payload); err != nil {
		t.Fatal(err)
	}
	item := <-world.loop.ingress
	admission, public := item.Delivery.PublicWeb()
	if !public || admission.PrincipalID() != trust.PublicWebPrincipalID ||
		item.Msg.Intention.From.Context != circulation.ContextID("@public_web:/"+channelID+"/root") ||
		item.Msg.Intention.Identity != (circulation.Identity{}) {
		t.Fatalf("public ingress = %+v, admission=%+v", item.Msg, admission)
	}

	response := circulation.Message{Kind: circulation.ValueKindResponse, Response: circulation.Response{
		IntentionID: input.Intention.IntentionID,
		To:          circulation.Address{Context: circulation.ContextID("@public_web:/" + channelID + "/root")},
		Status:      circulation.ValueStatusOK,
	}}
	world.loop.routeEgressRoot(response)
	var decoded circulation.Message
	if err := json.Unmarshal(<-returned, &decoded); err != nil || decoded.Response.IntentionID != input.Intention.IntentionID {
		t.Fatalf("public return = %+v, %v", decoded, err)
	}
	if world.loop.sendToPublicBrowserUI(channelID+"/other", response) ||
		world.loop.sendToPublicBrowserUI("bpw1_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB/root", response) {
		t.Fatal("public response escaped its exact channel/UI binding")
	}

	enabled = false
	if err := sink.Message(channelID, "root", payload); !errors.Is(err, ErrPublicWebSink) {
		t.Fatalf("message after policy revocation = %v", err)
	}
	if err := sink.Message(channelID, "other", payload); !errors.Is(err, ErrPublicWebSink) {
		t.Fatalf("message with substituted UI = %v", err)
	}
	sink.Close(channelID)
	if world.registry.UIChannelActive("public_web", channelID) || closed != 1 {
		t.Fatalf("closed state: active=%v callbacks=%d", world.registry.UIChannelActive("public_web", channelID), closed)
	}
	if world.loop.sendToPublicBrowserUI(channelID+"/root", response) {
		t.Fatal("closed public route remained available")
	}
}

func TestPublicWebSinkEnforcesFrameAndConnectionBudgets(t *testing.T) {
	world := newRestrictedWorld(t)
	world.loop.frame.CtxId = shared.RootContextID
	world.loop.ingress = make(chan IngressItem, 1)
	next := 0
	sink, err := NewPublicWebSink(PublicWebSinkOptions{
		Loop:           world.loop,
		Admit:          world.runtime.AdmitPublicWeb,
		Deliver:        func(string, []byte) error { return nil },
		MaxConnections: 2,
		NewChannelID: func() (string, error) {
			next++
			return "channel_" + string(rune('a'+next)), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := sink.Open("root")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sink.Open("root"); err != nil {
		t.Fatal(err)
	}
	if _, err := sink.Open("root"); !errors.Is(err, ErrPublicWebSink) {
		t.Fatalf("third connection = %v", err)
	}
	oversized := []byte(strings.Repeat("x", maxPublicWebFrameBytes+1))
	if err := sink.Message(first, "root", oversized); !errors.Is(err, ErrPublicWebSink) {
		t.Fatalf("oversized frame = %v", err)
	}
}

func TestPublicWebSinkAppliesConfiguredAggregatePrincipalRate(t *testing.T) {
	world := newRestrictedWorld(t)
	world.loop.frame.CtxId = shared.RootContextID
	world.loop.ingress = make(chan IngressItem, 2)
	sink, err := NewPublicWebSink(PublicWebSinkOptions{
		Loop: world.loop, Admit: world.runtime.AdmitPublicWeb,
		Deliver:       func(string, []byte) error { return nil },
		NewChannelID:  func() (string, error) { return "bpw1_aggregate_rate", nil },
		AggregateRate: func() (int, int, bool) { return 1, 1, true },
	})
	if err != nil {
		t.Fatal(err)
	}
	channel, err := sink.Open("root")
	if err != nil {
		t.Fatal(err)
	}
	message := circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "123e4567-e89b-42d3-a456-426614174000",
		To:          circulation.Address{Context: "/root/catalog", Type: circulation.ValueTypeMatter, Cap: "matter.read"},
	}}
	payload, _ := json.Marshal(message)
	if err := sink.Message(channel, "root", payload); err != nil {
		t.Fatalf("first aggregate token: %v", err)
	}
	message.Intention.IntentionID = "123e4567-e89b-42d3-a456-426614174001"
	payload, _ = json.Marshal(message)
	if err := sink.Message(channel, "root", payload); !errors.Is(err, ErrPublicWebSink) {
		t.Fatalf("aggregate overflow = %v", err)
	}
	records := world.journal.Records()
	if len(records) != 1 || records[0].Principal != trust.PublicWebPrincipalID || records[0].Reason != "rate_limited" {
		t.Fatalf("aggregate rate audit = %#v", records)
	}
}

func TestPublicWebSinkRequiresRootAndRuntimeRegistry(t *testing.T) {
	registry := junction.NewContextCommRegistry()
	loop := &CommLoop{frame: &junction.ContextRegistry{CtxId: "/child", CtxCommReg: registry}}
	if _, err := NewPublicWebSink(PublicWebSinkOptions{Loop: loop,
		Admit:   func() (trust.Admission, error) { return trust.Admission{}, nil },
		Deliver: func(string, []byte) error { return nil }}); !errors.Is(err, ErrPublicWebSink) {
		t.Fatalf("non-root sink = %v", err)
	}
}

func BenchmarkPublicWebSinkAdmission(b *testing.B) {
	world := newRestrictedWorld(b)
	world.loop.frame.CtxId = shared.RootContextID
	world.loop.ingress = make(chan IngressItem, 1)
	sink, err := NewPublicWebSink(PublicWebSinkOptions{
		Loop: world.loop, Admit: world.runtime.AdmitPublicWeb,
		Deliver:      func(string, []byte) error { return nil },
		NewChannelID: func() (string, error) { return "bpw1_benchmark_channel", nil },
	})
	if err != nil {
		b.Fatal(err)
	}
	channelID, err := sink.Open("main")
	if err != nil {
		b.Fatal(err)
	}
	payload, err := json.Marshal(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "123e4567-e89b-42d3-a456-426614174000", AwaitResponse: true,
		To: circulation.Address{Context: "/root/catalog", Type: circulation.ValueTypeMatter, Cap: "matter.read"},
	}})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for range b.N {
		if err := sink.Message(channelID, "main", payload); err != nil {
			b.Fatal(err)
		}
		<-world.loop.ingress
	}
}
