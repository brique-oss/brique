package context

import (
	"strings"
	"testing"
	"time"

	"brique_engine/circulation"
)

// stopping a child that was never started (a zero ContextLoop) panics.
func breakChild(w *controlWorld, name string) {
	w.loop.childMu.Lock()
	w.loop.childLoops[name] = &ContextLoop{}
	w.loop.childMu.Unlock()
}

// A control request whose child operation panics is answered with an internal
// error, without the cause, and does not stop the engine.
func TestControlRequestPanicIsAnsweredAsAnInternalError(t *testing.T) {
	w := newControlWorld(t)
	breakChild(w, "a")

	if stop := w.loop.serveControl(w.restricted(t, "pilot", "context.stop", map[string]any{"child": "a"})); stop {
		t.Fatal("a child stop must not end the control loop")
	}
	resp := w.answer(t)
	if resp.Status != circulation.ValueStatusError || resp.Error == nil || resp.Error.Code != circulation.ValueCodeInternal {
		t.Fatalf("answer = %#v, want an internal error", resp)
	}
	if strings.Contains(strings.ToLower(resp.Error.Message), "nil") || strings.Contains(resp.Error.Message, "channel") {
		t.Fatalf("the cause leaked to the requester: %q", resp.Error.Message)
	}
}

// The lock that serialises child operations is free again after a panic: it would
// otherwise block every later control operation of the context.
func TestChildOperationLockIsReleasedAfterAPanic(t *testing.T) {
	w := newControlWorld(t)
	breakChild(w, "a")
	_, scope, ok := w.loop.controlRequest(w.restricted(t, "pilot", "context.stop", map[string]any{"child": "a"}))
	if !ok {
		t.Fatal("not admitted")
	}
	in := circulation.Intention{IntentionID: "i1", To: circulation.Address{Context: "/root/ctl", Type: circulation.ValueTypeControl, Cap: "context.stop"}, From: circulation.Address{Context: "/root/ctl/w"}}

	func() {
		defer func() { _ = recover() }() // the recovery of the caller stands here
		w.loop.runChildOpScoped(childOpStop, []string{"a"}, in, scope)
	}()

	acquired := make(chan struct{})
	go func() {
		w.loop.opMu.Lock()
		close(acquired)
		w.loop.opMu.Unlock()
	}()
	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("the child operation lock is still held after the panic")
	}
}
