/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package execution

import (
	"testing"
	"time"

	"brique_engine/engine/circulation"
	"brique_engine/engine/security/refusal"
	"brique_engine/engine/security/trust"
)

func protectedRuntime(t *testing.T, entries ...trust.ProtectedCapacity) *trust.Runtime {
	t.Helper()
	return activePolicyRuntime(t, func(policy *trust.Policy) { policy.ProtectedCapacities = entries })
}

func protectSubtree(path string) trust.ProtectedCapacity {
	descendants := true
	return trust.ProtectedCapacity{Context: trust.ContextSelector{Path: path, IncludeDescendants: &descendants}, Capacity: "*"}
}

func protectOne(path, name string) trust.ProtectedCapacity {
	descendants := false
	return trust.ProtectedCapacity{Context: trust.ContextSelector{Path: path, IncludeDescendants: &descendants}, Capacity: name}
}

func protectedLoop(t *testing.T, runtime *trust.Runtime) (*ExecutionLoop, chan circulation.Message) {
	t.Helper()
	l, commCh, _ := newProcTestLoop(t, map[string]any{})
	l.frame.CtxCommReg = secretsCommReg{mockExecCommReg: l.frame.CtxCommReg.(*mockExecCommReg), runtime: runtime}
	return l, commCh
}

func userCall(context, capacity string, await bool) circulation.Message {
	return circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "i-" + capacity, AwaitResponse: await,
		From: circulation.Address{Context: "/root/other", Type: circulation.ValueTypeExecution, Cap: "caller"},
		To:   circulation.Address{Context: circulation.ContextID(context), Type: circulation.ValueTypeUser, Cap: capacity},
	}}
}

func refusalOf(t *testing.T, commCh chan circulation.Message, intentionID string) *circulation.Message {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case msg := <-commCh:
			if msg.Kind == circulation.ValueKindResponse && msg.Response.IntentionID == intentionID {
				return &msg
			}
		case <-deadline:
			return nil
		}
	}
}

func TestProtected_AnUnadmittedCallToAProtectedCapacityIsRefusedCanonically(t *testing.T) {
	l, commCh := protectedLoop(t, protectedRuntime(t, protectSubtree("/root/mcp/client")))
	msg := userCall("/root/mcp/client", "call", true)
	if !l.refuseUnadmittedProtected(msg) {
		t.Fatal("a protected capacity was not refused to a caller with no admission")
	}
	response := refusalOf(t, commCh, msg.Intention.IntentionID)
	if response == nil {
		t.Fatal("the awaited call got no answer")
	}
	problem := response.Response.Error
	if response.Response.Status != circulation.ValueStatusError || problem == nil || problem.Code != circulation.ValueCodeUnauthorized ||
		problem.Message != refusal.MessageCapabilityDenied || problem.Details[circulation.KeyReason] != refusal.ReasonCapabilityDenied {
		t.Fatalf("the refusal is not the canonical capability refusal: %+v", response.Response)
	}
}

func TestProtected_TheSubtreeCoversDescendantsAndOnlyThem(t *testing.T) {
	l, _ := protectedLoop(t, protectedRuntime(t, protectSubtree("/root/mcp/client"), protectOne("/root/llm", "infer")))
	cases := []struct {
		context, capacity string
		refused           bool
	}{
		{"/root/mcp/client", "tools", true},
		{"/root/mcp/client/deeper", "x", true},
		{"/root/mcp", "brique.intention", false},
		{"/root/mcp/clientele", "call", false},
		{"/root/llm", "infer", true},
		{"/root/llm", "infer.anthropic", false},
		{"/root/llm/sub", "infer", false},
	}
	for _, c := range cases {
		if got := l.refuseUnadmittedProtected(userCall(c.context, c.capacity, false)); got != c.refused {
			t.Errorf("%s#%s: refused=%v, want %v", c.context, c.capacity, got, c.refused)
		}
	}
}

func TestProtected_NothingIsRefusedWithoutAPolicyOrForAnotherType(t *testing.T) {
	l, _ := protectedLoop(t, nil)
	if l.refuseUnadmittedProtected(userCall("/root/mcp/client", "call", true)) {
		t.Fatal("with no policy nothing is protected")
	}
	l, _ = protectedLoop(t, protectedRuntime(t, protectSubtree("/root/mcp/client")))
	msg := userCall("/root/mcp/client", "call", true)
	msg.Intention.To.Type = circulation.ValueTypeExecution
	if l.refuseUnadmittedProtected(msg) {
		t.Fatal("only user capacities are protected")
	}
	response := circulation.Message{Kind: circulation.ValueKindResponse, Response: circulation.Response{IntentionID: "r"}}
	if l.refuseUnadmittedProtected(response) {
		t.Fatal("a response is not an intention")
	}
}

func TestProtected_AFireAndForgetCallIsRefusedWithoutAnswer(t *testing.T) {
	l, commCh := protectedLoop(t, protectedRuntime(t, protectSubtree("/root/mcp/client")))
	msg := userCall("/root/mcp/client", "call", false)
	if !l.refuseUnadmittedProtected(msg) {
		t.Fatal("not refused")
	}
	if refusalOf(t, commCh, msg.Intention.IntentionID) != nil {
		t.Fatal("a call that awaits no response must not be answered")
	}
}

func TestProtected_TheInboxRefusesAnUnadmittedDeliveryBeforeDispatch(t *testing.T) {
	l, commCh := protectedLoop(t, protectedRuntime(t, protectSubtree("/root/mcp/client")))
	msg := userCall("/root/mcp/client", "anything", true)
	l.handleInboxDelivery(l.frame.MustDelivery(msg))
	response := refusalOf(t, commCh, msg.Intention.IntentionID)
	if response == nil || response.Response.Error == nil || response.Response.Error.Details[circulation.KeyReason] != refusal.ReasonCapabilityDenied {
		t.Fatalf("the inbox did not refuse the protected capacity: %+v", response)
	}
}
