package reflexive

import (
	"strings"
	"testing"

	"brique_engine/engine/circulation"
	"brique_engine/engine/shared"
)

func panicTestIntention(id, capName string) circulation.Message {
	return circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: id,
		To:          circulation.Address{Context: shared.RootContextID, Cap: capName, Type: circulation.ValueTypeReflexive},
		From:        circulation.Address{Context: shared.RootContextID},
	}}
}

// A panicking handler must be answered with an internal error, must not leak
// its text to the caller, and must leave the loop able to serve other requests.
func TestHandlerPanicAnswersInternalError(t *testing.T) {
	l, commCh, _ := newReflexiveSQLiteHarness(t, shared.RootContextID)
	l.caps["test.panic"] = func(*ReflexiveLoop, circulation.Message) { panic("boom") }

	l.handleInboxMessage(panicTestIntention("p-1", "test.panic"))
	resp := recvReflexiveSQLiteMsg(t, commCh, "panic answer").Response
	if resp.Status != circulation.ValueStatusError || resp.Error == nil {
		t.Fatalf("status=%q error=%#v, want an error answer", resp.Status, resp.Error)
	}
	if resp.Error.Code != circulation.ValueCodeInternal {
		t.Fatalf("code=%q, want %q", resp.Error.Code, circulation.ValueCodeInternal)
	}
	if strings.Contains(resp.Error.Message, "boom") {
		t.Fatalf("the panic text leaked to the caller: %q", resp.Error.Message)
	}
	if resp.IntentionID != "p-1" {
		t.Fatalf("answer for %q, want p-1", resp.IntentionID)
	}

	// the loop still serves requests, and the handler slot was released
	l.handleInboxMessage(panicTestIntention("p-2", "no.such.capacity"))
	next := recvReflexiveSQLiteMsg(t, commCh, "answer after panic").Response
	if next.IntentionID != "p-2" || next.Error == nil || next.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("unexpected answer after the panic: %#v", next)
	}
	if got := len(l.handlerSlots); got != 0 {
		t.Fatalf("%d handler slots still held after the panic", got)
	}
}
