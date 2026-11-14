package execution

import (
	"strings"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/configuration"
)

func panicTestEngineCfg() map[string]any {
	return map[string]any{
		configuration.KeyWrappers: []any{map[string]any{configuration.KeyWrpName: "w1"}},
	}
}

// An entry of the wrapper table that is nil makes the code that uses it panic.
func breakWrapperTable(l *ExecutionLoop) { l.frame.Wrappers["w1"] = nil }

func recvExecMsgT(t *testing.T, ch <-chan circulation.Message, label string) circulation.Message {
	t.Helper()
	select {
	case m := <-ch:
		return m
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting %s", label)
		return circulation.Message{}
	}
}

func assertInternalAnswer(t *testing.T, m circulation.Message, id string) {
	t.Helper()
	if m.Kind != circulation.ValueKindResponse {
		t.Fatalf("expected a response, got %q", m.Kind)
	}
	r := m.Response
	if r.IntentionID != id || r.Status != circulation.ValueStatusError || r.Error == nil {
		t.Fatalf("answer = %#v, want an error for %s", r, id)
	}
	if r.Error.Code != circulation.ValueCodeInternal {
		t.Fatalf("code = %q, want %q (error %#v)", r.Error.Code, circulation.ValueCodeInternal, r.Error)
	}
	if strings.Contains(strings.ToLower(r.Error.Message), "panic") || strings.Contains(r.Error.Message, "nil pointer") {
		t.Fatalf("the cause leaked to the caller: %q", r.Error.Message)
	}
}

// A user job that panics is answered with an internal error, its slot is freed,
// and the family serves the next request.
func TestExecutionJobPanicAnswersInternalError(t *testing.T) {
	l, commCh, _ := newExecutionLoopHarness(panicTestEngineCfg())
	breakWrapperTable(l)

	l.dispatchIntention(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "ep-1",
		To:          circulation.Address{Context: "@wrapper_w1:/x", Cap: "do.it", Type: circulation.ValueTypeUser},
		From:        circulation.Address{Context: "/ctx/other"},
	}})
	assertInternalAnswer(t, recvExecMsgT(t, commCh, "job panic answer"), "ep-1")

	deadline := time.Now().Add(2 * time.Second)
	for len(l.jobSem) != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := len(l.jobSem); got != 0 {
		t.Fatalf("%d job slots still held after the panic", got)
	}

	// still serving
	l.dispatchIntention(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "ep-2",
		To:          circulation.Address{Context: "/ctx/a", Cap: "nothing", Type: "no-such-type"},
		From:        circulation.Address{Context: "/ctx/other"},
	}})
	if r := recvExecMsgT(t, commCh, "answer after the panic").Response; r.IntentionID != "ep-2" {
		t.Fatalf("unexpected answer after the panic: %#v", r)
	}
}

// A panic while the inbox loop itself serves an internal capacity costs that
// delivery only: the loop goes on.
func TestExecutionInboxPanicIsAnsweredAndTheLoopGoesOn(t *testing.T) {
	l, commCh, _ := newExecutionLoopHarness(panicTestEngineCfg())
	breakWrapperTable(l)

	l.handleInboxDeliverySafely(localTestDelivery(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "ep-3",
		To:          circulation.Address{Context: "/ctx/a", Cap: execCapWrapperReady, Type: circulation.ValueTypeExecution},
		From:        circulation.Address{Context: "@wrapper_w1:/x"},
	}}))
	assertInternalAnswer(t, recvExecMsgT(t, commCh, "inbox panic answer"), "ep-3")
}

// evalWithTimeout evaluates a DSL node and fails the test if it does not return:
// a branch that died silently would leave its parent waiting for ever.
func evalWithTimeout(t *testing.T, l *ExecutionLoop, node map[string]any) (circulation.Response, error) {
	t.Helper()
	type result struct {
		resp circulation.Response
		err  error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := l.evalDSLNode(newDSLN2State(map[string]any{"items": []any{"a", "b"}}), node)
		done <- result{resp, err}
	}()
	select {
	case r := <-done:
		return r.resp, r.err
	case <-time.After(5 * time.Second):
		t.Fatal("the DSL node never returned: a branch that panicked did not report")
		return circulation.Response{}, nil
	}
}

func invokeNode(capacity string) map[string]any {
	return map[string]any{"invoke": map[string]any{"@capacity": "/ctx/root/" + capacity}}
}

// A DSL branch that panics reports an error to its parent instead of stopping the
// engine, in each of the three fan-out forms.
func TestExecutionDSLBranchPanicReportsAnError(t *testing.T) {
	for name, node := range map[string]map[string]any{
		">parallel": {">parallel": map[string]any{"items": []any{invokeNode("p.one"), invokeNode("p.two")}}},
		">race":     {">race": map[string]any{"items": []any{invokeNode("p.one"), invokeNode("p.two")}}},
		">for_each": {">for_each": map[string]any{
			"collection": "$input.items",
			">do":        invokeNode("p.one"),
		}},
	} {
		t.Run(name, func(t *testing.T) {
			l, _, _ := newExecutionLoopHarness(nil)
			l.pending = nil // registering the call of a branch now writes to a nil map
			resp, err := evalWithTimeout(t, l, node)
			if err == nil && resp.Status != circulation.ValueStatusError {
				t.Fatalf("expected an error from the branch that panicked, got %#v", resp)
			}
			if err != nil && !strings.Contains(err.Error(), "internal error") {
				t.Fatalf("error = %q, want the branch's internal error", err)
			}
		})
	}
}

// ensureWrapperReady takes and releases the wrapper state lock several times. A
// panic while it is held must not leave it taken: it would block every later call
// for that wrapper. Here the readiness channel is closed already, so closing it
// again panics with the lock held.
func TestEnsureWrapperReadyReleasesTheStateLockOnPanic(t *testing.T) {
	cfg := map[string]any{
		configuration.KeyWrappers: []any{map[string]any{
			configuration.KeyWrpName: "w1",
			configuration.KeyWrpMode: configuration.ValueConfigStyleInterpreted,
		}},
	}
	l, _, _ := newExecutionLoopHarness(cfg)
	st := l.frame.Wrappers["w1"]
	st.Lock()
	st.SetReadyCh()
	close(st.GetReadyCh()) // a second close will panic
	st.Unlock()

	func() {
		defer func() { _ = recover() }() // the family's own recovery stands here
		l.ensureWrapperReady("w1", circulation.Intention{
			IntentionID: "lk-1",
			To:          circulation.Address{Context: "@wrapper_w1:/x", Cap: "do.it", Type: circulation.ValueTypeUser},
			From:        circulation.Address{Context: "/ctx/other"},
		})
	}()

	acquired := make(chan struct{})
	go func() {
		st.Lock()
		close(acquired)
		st.Unlock()
	}()
	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("the wrapper state lock is still held after the panic: every later call would block")
	}
}
