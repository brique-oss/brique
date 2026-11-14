package matter

import (
	"strings"
	"testing"

	"brique_engine/circulation"
)

func panicIntention(id, capName string) circulation.Message {
	return circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: id,
		To:          circulation.Address{Context: "/ctx/matter-a", Cap: capName, Type: circulation.ValueTypeMatter},
		From:        circulation.Address{Context: "/ctx/other"},
	}}
}

// A panicking operation is answered with an internal error, without its text,
// and leaves the loop serving requests with its handler slot released.
func TestMatterHandlerPanicAnswersInternalError(t *testing.T) {
	l, commCh, _ := newMatterLoopHarness(t)
	l.caps["test.panic"] = func(*MatterLoop, circulation.Message) { panic("boom") }

	l.handleInboxMessage(panicIntention("mp-1", "test.panic"))
	resp := recvMatterLoopMsg(t, commCh, "panic answer").Response
	if resp.Status != circulation.ValueStatusError || resp.Error == nil {
		t.Fatalf("status=%q error=%#v, want an error answer", resp.Status, resp.Error)
	}
	if resp.Error.Code != circulation.ValueCodeInternal {
		t.Fatalf("code=%q, want %q", resp.Error.Code, circulation.ValueCodeInternal)
	}
	if strings.Contains(resp.Error.Message, "boom") {
		t.Fatalf("the panic text leaked to the caller: %q", resp.Error.Message)
	}
	if resp.IntentionID != "mp-1" {
		t.Fatalf("answer for %q, want mp-1", resp.IntentionID)
	}

	l.handleInboxMessage(panicIntention("mp-2", "no.such.capacity"))
	next := recvMatterLoopMsg(t, commCh, "answer after the panic").Response
	if next.IntentionID != "mp-2" || next.Error == nil || next.Error.Code != circulation.ValueCodeInvalid {
		t.Fatalf("unexpected answer after the panic: %#v", next)
	}
	if got := len(l.handlerSlots); got != 0 {
		t.Fatalf("%d handler slots still held after the panic", got)
	}
}
