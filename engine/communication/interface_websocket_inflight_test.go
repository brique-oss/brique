package comm

import (
	"testing"

	"brique_engine/circulation"
	"brique_engine/junction"
)

func awaited(id string) circulation.Message {
	return circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: id, AwaitResponse: true, Params: map[string]any{"big": "payload"},
	}}
}

func answer(id, status string) circulation.Message {
	return circulation.Message{Kind: circulation.ValueKindResponse, Response: circulation.Response{IntentionID: id, Status: status}}
}

func TestInflightLosingAConnectionAnswersOnlyItsOwnAwaitedIntentions(t *testing.T) {
	tracker := newInflightTracker()
	a, _ := junction.NewWrapperConnection()
	b, _ := junction.NewWrapperConnection()
	tracker.add(b, awaited("other-connection"))
	tracker.add(a, awaited("one"))
	tracker.add(a, awaited("two"))
	tracker.add(a, circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{IntentionID: "fire", AwaitResponse: false}})
	tracker.add(a, answer("not-an-intention", "ok"))
	if tracker.size() != 3 {
		t.Fatalf("only awaited intentions are recorded, got %d", tracker.size())
	}
	lost := tracker.lose(a)
	if len(lost) != 2 || tracker.size() != 1 {
		t.Fatalf("losing the connection must return its two intentions and keep the other connection's: %d lost, %d left", len(lost), tracker.size())
	}
	for _, request := range lost {
		if request.Intention.Params != nil {
			t.Fatalf("an entry must not keep the parameters of the request: %v", request.Intention.Params)
		}
	}
	if again := tracker.lose(a); len(again) != 0 {
		t.Fatal("a connection is answered once")
	}
	if tracker.size() != 1 || len(tracker.lose(b)) != 1 {
		t.Fatal("the intentions of another connection are untouched")
	}
}

func TestInflightTerminalResponseSettlesButRunningDoesNot(t *testing.T) {
	tracker := newInflightTracker()
	c, _ := junction.NewWrapperConnection()
	tracker.add(c, awaited("x"))
	tracker.settle(answer("x", circulation.ValueStatusRunning))
	if tracker.size() != 1 {
		t.Fatal("a running response must not settle the intention")
	}
	tracker.settle(answer("x", "ok"))
	if tracker.size() != 0 {
		t.Fatal("a terminal response settles the intention")
	}
	if got := tracker.lose(c); len(got) != 0 {
		t.Fatal("a settled intention is not answered connection_lost")
	}
}

func TestInflightNilTrackerIsHarmless(t *testing.T) {
	var tracker *inflightTracker
	tracker.add(junction.WrapperConnection{}, awaited("x"))
	tracker.settle(answer("x", "ok"))
	if tracker.lose(junction.WrapperConnection{}) != nil {
		t.Fatal("a nil tracker has nothing to lose")
	}
}
