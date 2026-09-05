/*
 * Copyright 2026 Nicolas Cassan
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package execution

import (
	"testing"
	"time"

	"brique_engine/circulation"
)

// =============================================================================
// N1 — pure helpers: condition eval, address parsing, retry/timeout policy,
//      validate guards not yet covered, invokeNoWait
// =============================================================================

func TestExecutionDSL_N1_ConditionOpsAllOperators(t *testing.T) {
	st := newDSLExecState(circulation.Intention{
		IntentionID: "cond-test",
		Params: map[string]any{
			"score": 7,
			"name":  "alice",
			"tags":  []any{"a", "b", "c"},
		},
	}, nil)

	cases := []struct {
		label  string
		cond   map[string]any
		expect bool
	}{
		{"eq int", map[string]any{"left": "$input.score", "op": "eq", "right": 7}, true},
		{"neq", map[string]any{"left": "$input.score", "op": "neq", "right": 9}, true},
		{"gt", map[string]any{"left": "$input.score", "op": "gt", "right": 5}, true},
		{"gte equal", map[string]any{"left": "$input.score", "op": "gte", "right": 7}, true},
		{"lt false", map[string]any{"left": "$input.score", "op": "lt", "right": 5}, false},
		{"lte", map[string]any{"left": "$input.score", "op": "lte", "right": 7}, true},
		{"exists present", map[string]any{"left": "$input.name", "op": "exists"}, true},
		{"exists absent", map[string]any{"left": "$input.missing", "op": "exists"}, false},
		{"in found", map[string]any{"left": "$input.name", "op": "in", "right": []any{"alice", "bob"}}, true},
		{"in not found", map[string]any{"left": "$input.name", "op": "in", "right": []any{"bob", "carol"}}, false},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			got, err := evalDSLCondition(st, tc.cond)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.expect {
				t.Fatalf("got %v want %v", got, tc.expect)
			}
		})
	}
}

func TestExecutionDSL_N1_ConditionAndOr(t *testing.T) {
	st := newDSLExecState(circulation.Intention{
		IntentionID: "cond-and-or",
		Params:      map[string]any{"x": 5, "y": 10},
	}, nil)

	andCond := map[string]any{
		"and": []any{
			map[string]any{"left": "$input.x", "op": "gt", "right": 3},
			map[string]any{"left": "$input.y", "op": "lt", "right": 20},
		},
	}
	if ok, err := evalDSLCondition(st, andCond); err != nil || !ok {
		t.Fatalf("and condition: got=%v err=%v", ok, err)
	}

	andFail := map[string]any{
		"and": []any{
			map[string]any{"left": "$input.x", "op": "gt", "right": 3},
			map[string]any{"left": "$input.y", "op": "gt", "right": 100},
		},
	}
	if ok, err := evalDSLCondition(st, andFail); err != nil || ok {
		t.Fatalf("and false condition: got=%v err=%v", ok, err)
	}

	orCond := map[string]any{
		"or": []any{
			map[string]any{"left": "$input.x", "op": "gt", "right": 100},
			map[string]any{"left": "$input.y", "op": "lt", "right": 20},
		},
	}
	if ok, err := evalDSLCondition(st, orCond); err != nil || !ok {
		t.Fatalf("or condition: got=%v err=%v", ok, err)
	}
}

func TestExecutionDSL_N1_AddressParsing(t *testing.T) {
	base := circulation.ContextID("/root/child")

	cases := []struct {
		ref     string
		wantCtx string
		wantCap string
		wantErr bool
	}{
		// valid absolute forms supported by Communication
		{"/root/other/my.cap", "/root/other", "my.cap", false},
		{"/root/my.cap", "/root", "my.cap", false},
		{"@ext_abc123:/remote/my.cap", "@ext_abc123:/remote", "my.cap", false},
		// forbidden forms
		{"@wrapper_x", "", "", true},     // @wrapper_ forbidden in DSL descriptors
		{"./my.cap", "", "", true},        // relative refs are wrapper-internal only, not DSL
		{"", "", "", true},               // empty
		{"relative_no_slash", "", "", true}, // no leading slash
	}

	for _, tc := range cases {
		t.Run(tc.ref, func(t *testing.T) {
			addr, err := parseDSLAddressRef(base, tc.ref, circulation.ValueTypeUser)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for ref %q", tc.ref)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(addr.Context) != tc.wantCtx || addr.Cap != tc.wantCap {
				t.Fatalf("got ctx=%q cap=%q want ctx=%q cap=%q", addr.Context, addr.Cap, tc.wantCtx, tc.wantCap)
			}
		})
	}
}

func TestExecutionDSL_N1_ValidateGuards(t *testing.T) {
	// missing role
	desc := DSLDescriptor{
		CapName:  "cap.bad",
		RootName: "#root",
		RootSection: DSLSection{
			Name:    "#root",
			Role:    "",
			Inputs:  map[string]any{},
			Outputs: map[string]any{},
			TransformationContract: DSLTransformationContract{Morphing: "x"},
		},
	}
	if err := validateDSLPlan(desc); err == nil {
		t.Fatal("expected error on missing role")
	}

	// missing morphing
	desc.RootSection.Role = "some role"
	desc.RootSection.TransformationContract.Morphing = ""
	if err := validateDSLPlan(desc); err == nil {
		t.Fatal("expected error on missing morphing")
	}

	// missing inputs
	desc.RootSection.TransformationContract.Morphing = "x"
	desc.RootSection.Inputs = nil
	if err := validateDSLPlan(desc); err == nil {
		t.Fatal("expected error on nil inputs")
	}

	// invoke retry when await_response=false
	desc.RootSection.Inputs = map[string]any{}
	desc.RootSection.Resolution = map[string]any{
		"invoke": map[string]any{
			"@capacity":      "/ctx/cap",
			"await_response": false,
			"retry":          map[string]any{"max": 2},
		},
	}
	if err := validateDSLPlan(desc); err == nil {
		t.Fatal("expected error: retry forbidden with await_response=false")
	}
}

func TestExecutionDSL_N1_InvokeNoWait(t *testing.T) {
	l, commCh, _ := newExecutionLoopHarness(nil)
	in := circulation.Intention{
		IntentionID: "nowait-root",
		To:          circulation.Address{Context: "/ctx/root", Cap: "cap.dsl", Type: circulation.ValueTypeUser},
		From:        circulation.Address{Context: "/ctx/caller", Cap: "caller", Type: circulation.ValueTypeUser},
	}

	if err := l.invokeNoWait(in); err != nil {
		t.Fatalf("invokeNoWait error: %v", err)
	}

	select {
	case msg := <-commCh:
		if msg.Kind != circulation.ValueKindIntention {
			t.Fatalf("expected intention kind, got %q", msg.Kind)
		}
		if msg.Intention.AwaitResponse {
			t.Fatalf("await_response must be false for no-wait invoke")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timeout waiting for no-wait emission")
	}
}

func TestExecutionDSL_N1_RetryPolicySucceedsOnSecondAttempt(t *testing.T) {
	l, commCh, _ := newExecutionLoopHarness(nil)

	attempt := 0
	go func() {
		for {
			msg, ok := <-commCh
			if !ok {
				return
			}
			if msg.Kind != circulation.ValueKindIntention {
				continue
			}
			attempt++
			status := circulation.ValueStatusError
			if attempt >= 2 {
				status = circulation.ValueStatusOK
			}
			l.dispatchResponse(circulation.Message{
				Kind: circulation.ValueKindResponse,
				Response: circulation.Response{
					IntentionID: msg.Intention.IntentionID,
					Status:      status,
					To:          msg.Intention.From,
					From:        msg.Intention.To,
					Payload:     map[string]any{"attempt": attempt},
				},
			})
		}
	}()

	node := map[string]any{
		"@capacity": "/ctx/root/retry.cap",
		"retry":     map[string]any{"max": 2, "delay_ms": 0},
	}
	sub := circulation.Intention{
		IntentionID: "retry-root",
		To:          circulation.Address{Context: "/ctx/root", Cap: "cap.dsl", Type: circulation.ValueTypeUser},
		From:        circulation.Address{Context: "/ctx/caller"},
	}
	resp, err := l.evalDSLInvokeWithPolicy(node, sub)
	if err != nil {
		t.Fatalf("evalDSLInvokeWithPolicy error: %v", err)
	}
	if resp.Status != circulation.ValueStatusOK {
		t.Fatalf("expected ok after retry, got %q", resp.Status)
	}
	if attempt != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempt)
	}
}

func TestExecutionDSL_N1_TimeoutFailsClosed(t *testing.T) {
	l, _, _ := newExecutionLoopHarness(nil)

	sub := circulation.Intention{
		IntentionID: "timeout-root",
		To:          circulation.Address{Context: "/ctx/root", Cap: "cap.dsl", Type: circulation.ValueTypeUser},
		From:        circulation.Address{Context: "/ctx/caller"},
	}
	resp, err := l.invokeAndAwaitWithTimeout(sub, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != circulation.ValueStatusError {
		t.Fatalf("expected error status on timeout, got %q", resp.Status)
	}
}

// =============================================================================
// N2 — node evaluators in isolation with stubbed comm
// =============================================================================

func newDSLN2State(params map[string]any) *dslExecState {
	return newDSLExecState(circulation.Intention{
		IntentionID: "n2-root",
		To:          circulation.Address{Context: "/ctx/root", Cap: "cap.dsl", Type: circulation.ValueTypeUser},
		From:        circulation.Address{Context: "/ctx/caller", Cap: "caller", Type: circulation.ValueTypeUser},
		Params:      params,
	}, nil)
}

func stubDSLComm(l *ExecutionLoop, commCh <-chan circulation.Message, responses map[string]circulation.Response) {
	go func() {
		for msg := range commCh {
			if msg.Kind != circulation.ValueKindIntention {
				continue
			}
			cap := msg.Intention.To.Cap
			resp, ok := responses[cap]
			if !ok {
				resp = circulation.Response{
					Status:  circulation.ValueStatusOK,
					Payload: map[string]any{"cap": cap},
				}
			}
			resp.IntentionID = msg.Intention.IntentionID
			resp.To = msg.Intention.From
			resp.From = msg.Intention.To
			l.dispatchResponse(circulation.Message{Kind: circulation.ValueKindResponse, Response: resp})
		}
	}()
}

func TestExecutionDSL_N2_IfNoElseFalseConditionReturnsEmptyOK(t *testing.T) {
	l, commCh, _ := newExecutionLoopHarness(nil)
	stubDSLComm(l, commCh, nil)
	st := newDSLN2State(map[string]any{"flag": false})

	resp, err := l.evalDSLNode(st, map[string]any{
		">if": map[string]any{
			"when": map[string]any{"left": "$input.flag", "op": "eq", "right": true},
			">then": map[string]any{
				"invoke": map[string]any{"@capacity": "/ctx/root/should.not.run"},
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != circulation.ValueStatusOK {
		t.Fatalf("expected ok, got %q", resp.Status)
	}
	if len(resp.Payload) != 0 {
		t.Fatalf("expected empty payload, got %#v", resp.Payload)
	}
}

func TestExecutionDSL_N2_SwitchDefaultBranch(t *testing.T) {
	l, commCh, _ := newExecutionLoopHarness(nil)
	stubDSLComm(l, commCh, map[string]circulation.Response{
		"default.cap": {Status: circulation.ValueStatusOK, Payload: map[string]any{"branch": "default"}},
	})
	st := newDSLN2State(map[string]any{"kind": "unknown"})

	resp, err := l.evalDSLNode(st, map[string]any{
		">switch": map[string]any{
			"on": "$input.kind",
			"cases": []any{
				map[string]any{"value": "known", ">then": map[string]any{
					"invoke": map[string]any{"@capacity": "/ctx/root/known.cap"},
				}},
			},
			">default": map[string]any{
				"invoke": map[string]any{"@capacity": "/ctx/root/default.cap"},
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != circulation.ValueStatusOK || resp.Payload["branch"] != "default" {
		t.Fatalf("expected default branch response, got %#v", resp)
	}
}

func TestExecutionDSL_N2_SwitchNoMatchNoDefaultReturnsEmptyOK(t *testing.T) {
	l, commCh, _ := newExecutionLoopHarness(nil)
	stubDSLComm(l, commCh, nil)
	st := newDSLN2State(map[string]any{"kind": "unknown"})

	resp, err := l.evalDSLNode(st, map[string]any{
		">switch": map[string]any{
			"on": "$input.kind",
			"cases": []any{
				map[string]any{"value": "known", ">then": map[string]any{
					"invoke": map[string]any{"@capacity": "/ctx/root/known.cap"},
				}},
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != circulation.ValueStatusOK || len(resp.Payload) != 0 {
		t.Fatalf("expected empty ok, got %#v", resp)
	}
}

func TestExecutionDSL_N2_SequenceStopsOnFirstError(t *testing.T) {
	l, commCh, _ := newExecutionLoopHarness(nil)

	// Track which caps were invoked inside the stub — no second reader on commCh.
	var invokedCaps []string
	go func() {
		for msg := range commCh {
			if msg.Kind != circulation.ValueKindIntention {
				continue
			}
			cap := msg.Intention.To.Cap
			invokedCaps = append(invokedCaps, cap)
			var resp circulation.Response
			if cap == "step.one" {
				resp = circulation.Response{
					Status: circulation.ValueStatusError,
					Error:  &circulation.ResponseProblem{Origin: "test", Code: "forced_error"},
				}
			} else {
				resp = circulation.Response{
					Status:  circulation.ValueStatusOK,
					Payload: map[string]any{"cap": cap},
				}
			}
			resp.IntentionID = msg.Intention.IntentionID
			resp.To = msg.Intention.From
			resp.From = msg.Intention.To
			l.dispatchResponse(circulation.Message{Kind: circulation.ValueKindResponse, Response: resp})
		}
	}()

	st := newDSLN2State(nil)
	resp, err := l.evalDSLNode(st, map[string]any{
		">sequence": map[string]any{
			"items": []any{
				map[string]any{"invoke": map[string]any{"@capacity": "/ctx/root/step.one"}},
				map[string]any{"invoke": map[string]any{"@capacity": "/ctx/root/step.two"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != circulation.ValueStatusError {
		t.Fatalf("expected error status from sequence, got %q", resp.Status)
	}
	for _, cap := range invokedCaps {
		if cap == "step.two" {
			t.Fatal("step.two must not be called after step.one returns error")
		}
	}
}

func TestExecutionDSL_N2_ParallelOneBranchErrorPropagates(t *testing.T) {
	l, commCh, _ := newExecutionLoopHarness(nil)
	stubDSLComm(l, commCh, map[string]circulation.Response{
		"p.ok":  {Status: circulation.ValueStatusOK, Payload: map[string]any{"v": "ok"}},
		"p.err": {Status: circulation.ValueStatusError, Error: &circulation.ResponseProblem{Origin: "test", Code: "fail"}},
	})
	st := newDSLN2State(nil)

	resp, err := l.evalDSLNode(st, map[string]any{
		">parallel": map[string]any{
			"items": []any{
				map[string]any{"invoke": map[string]any{"@capacity": "/ctx/root/p.ok"}},
				map[string]any{"invoke": map[string]any{"@capacity": "/ctx/root/p.err"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != circulation.ValueStatusError {
		t.Fatalf("expected error propagation from parallel, got %q", resp.Status)
	}
}

func TestExecutionDSL_N2_ForEachOnErrorContinueSkipsFailedItems(t *testing.T) {
	l, commCh, _ := newExecutionLoopHarness(nil)

	go func() {
		for msg := range commCh {
			if msg.Kind != circulation.ValueKindIntention {
				continue
			}
			item, _ := msg.Intention.Params["item"].(string)
			var resp circulation.Response
			if item == "bad" {
				resp = circulation.Response{
					Status: circulation.ValueStatusError,
					Error:  &circulation.ResponseProblem{Origin: "test", Code: "bad_item"},
				}
			} else {
				resp = circulation.Response{
					Status:  circulation.ValueStatusOK,
					Payload: map[string]any{"item": item},
				}
			}
			resp.IntentionID = msg.Intention.IntentionID
			resp.To = msg.Intention.From
			resp.From = msg.Intention.To
			l.dispatchResponse(circulation.Message{Kind: circulation.ValueKindResponse, Response: resp})
		}
	}()

	st := newDSLN2State(map[string]any{"items": []any{"a", "bad", "b"}})

	resp, err := l.evalDSLNode(st, map[string]any{
		">for_each": map[string]any{
			"collection": "$input.items",
			"on_error":   "continue",
			">do": map[string]any{
				"invoke": map[string]any{
					"@capacity": "/ctx/root/process.item",
					"with":      map[string]any{"item": "$item"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != circulation.ValueStatusOK {
		t.Fatalf("expected ok with continue, got %q", resp.Status)
	}
	items, _ := resp.Payload["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("expected 2 successful items (bad skipped), got %d", len(items))
	}
}

func TestExecutionDSL_N2_InvokeNoAwaitDoesNotBindAlias(t *testing.T) {
	l, commCh, _ := newExecutionLoopHarness(nil)
	stubDSLComm(l, commCh, nil)
	st := newDSLN2State(nil)

	_, err := l.evalDSLNode(st, map[string]any{
		"invoke": map[string]any{
			"@capacity":      "/ctx/root/fire.forget",
			"await_response": false,
			"as":             "", // empty as — no alias expected
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := st.alias("fire.forget"); ok {
		t.Fatal("alias must not be set for no-wait invoke")
	}
}

// =============================================================================
// N3 — runDSLOrchestrator end-to-end through the ExecutionLoop
// =============================================================================

func writeDSLOrchestratorCap(t *testing.T, dir, capName string, resolution map[string]any) {
	t.Helper()
	writeDSLTestCap(t, dir, capName, map[string]any{
		"brique": map[string]any{
			"cap_name": capName,
			"kind":     "dsl",
		},
		"functional": map[string]any{
			"#root": map[string]any{
				"role":    "test orchestration",
				"inputs":  map[string]any{},
				"outputs": map[string]any{},
				"effects": map[string]any{},
				"transformation_contract": map[string]any{
					"from":      []any{},
					"to":        []any{},
					"affecting": []any{},
					"morphing":  "Input becomes orchestrated result.",
				},
				"resolution": resolution,
			},
		},
	})
}

func TestExecutionDSL_N3_OrchestratorEndToEndSequence(t *testing.T) {
	l, commCh, _ := newExecutionLoopHarness(nil)
	dir := t.TempDir()
	l.capRoot = dir

	writeDSLOrchestratorCap(t, dir, "cap.orch.seq", map[string]any{
		">sequence": map[string]any{
			"items": []any{
				map[string]any{"invoke": map[string]any{
					"@capacity": "/ctx/root/step.a",
					"as":        "result",
				}},
			},
		},
	})

	responses := make(chan circulation.Message, 4)
	go func() {
		for msg := range commCh {
			if msg.Kind == circulation.ValueKindIntention && msg.Intention.To.Cap != "cap.orch.seq" {
				l.dispatchResponse(circulation.Message{
					Kind: circulation.ValueKindResponse,
					Response: circulation.Response{
						IntentionID: msg.Intention.IntentionID,
						Status:      circulation.ValueStatusOK,
						To:          msg.Intention.From,
						From:        msg.Intention.To,
						Payload:     map[string]any{"step": "a"},
					},
				})
			} else if msg.Kind == circulation.ValueKindResponse {
				responses <- msg
			}
		}
	}()

	in := circulation.Intention{
		IntentionID: "orch-e2e-seq",
		To:          circulation.Address{Context: "/ctx/root", Cap: "cap.orch.seq", Type: circulation.ValueTypeUser},
		From:        circulation.Address{Context: "/ctx/caller", Cap: "caller", Type: circulation.ValueTypeUser},
	}
	l.runDSLOrchestrator(circulation.Message{Kind: circulation.ValueKindIntention, Intention: in})

	select {
	case msg := <-responses:
		if msg.Response.Status != circulation.ValueStatusOK {
			t.Fatalf("expected ok, got %q payload=%#v", msg.Response.Status, msg.Response.Payload)
		}
		if msg.Response.Payload["step"] != "a" {
			t.Fatalf("unexpected payload: %#v", msg.Response.Payload)
		}
		if msg.Response.IntentionID != "orch-e2e-seq" {
			t.Fatalf("terminal response must carry root intention_id, got %q", msg.Response.IntentionID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for orchestrator terminal response")
	}
}

func TestExecutionDSL_N3_OrchestratorMissingDescriptorFailsClosed(t *testing.T) {
	l, commCh, _ := newExecutionLoopHarness(nil)
	dir := t.TempDir()
	l.capRoot = dir

	responses := make(chan circulation.Message, 4)
	go func() {
		for msg := range commCh {
			if msg.Kind == circulation.ValueKindResponse {
				responses <- msg
			}
		}
	}()

	in := circulation.Intention{
		IntentionID: "orch-missing",
		To:          circulation.Address{Context: "/ctx/root", Cap: "cap.does.not.exist", Type: circulation.ValueTypeUser},
		From:        circulation.Address{Context: "/ctx/caller", Cap: "caller", Type: circulation.ValueTypeUser},
	}
	l.runDSLOrchestrator(circulation.Message{Kind: circulation.ValueKindIntention, Intention: in})

	select {
	case msg := <-responses:
		if msg.Response.Status != circulation.ValueStatusError {
			t.Fatalf("expected error status on missing descriptor, got %q", msg.Response.Status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for fail-closed response")
	}
}

func TestExecutionDSL_N3_OrchestratorValidationFailureFailsClosed(t *testing.T) {
	l, commCh, _ := newExecutionLoopHarness(nil)
	dir := t.TempDir()
	l.capRoot = dir

	// write a cap with missing morphing — will fail validation
	writeDSLTestCap(t, dir, "cap.invalid.plan", map[string]any{
		"brique": map[string]any{
			"cap_name": "cap.invalid.plan",
			"kind":     "dsl",
		},
		"functional": map[string]any{
			"#root": map[string]any{
				"role":    "bad",
				"inputs":  map[string]any{},
				"outputs": map[string]any{},
				"transformation_contract": map[string]any{
					"morphing": "",
				},
				"resolution": map[string]any{
					"invoke": map[string]any{"@capacity": "/ctx/root/x"},
				},
			},
		},
	})

	responses := make(chan circulation.Message, 4)
	go func() {
		for msg := range commCh {
			if msg.Kind == circulation.ValueKindResponse {
				responses <- msg
			}
		}
	}()

	in := circulation.Intention{
		IntentionID: "orch-invalid",
		To:          circulation.Address{Context: "/ctx/root", Cap: "cap.invalid.plan", Type: circulation.ValueTypeUser},
		From:        circulation.Address{Context: "/ctx/caller", Cap: "caller", Type: circulation.ValueTypeUser},
	}
	l.runDSLOrchestrator(circulation.Message{Kind: circulation.ValueKindIntention, Intention: in})

	select {
	case msg := <-responses:
		if msg.Response.Status != circulation.ValueStatusError {
			t.Fatalf("expected error status on invalid plan, got %q", msg.Response.Status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for fail-closed response")
	}
}

func TestExecutionDSL_N3_OrchestratorTerminalResponseIDMatchesRootIntention(t *testing.T) {
	l, commCh, _ := newExecutionLoopHarness(nil)
	dir := t.TempDir()
	l.capRoot = dir

	writeDSLOrchestratorCap(t, dir, "cap.orch.corr", map[string]any{
		">sequence": map[string]any{
			"items": []any{
				map[string]any{"invoke": map[string]any{"@capacity": "/ctx/root/corr.step"}},
			},
		},
	})

	responses := make(chan circulation.Message, 4)
	go func() {
		for msg := range commCh {
			if msg.Kind == circulation.ValueKindIntention && msg.Intention.To.Cap != "cap.orch.corr" {
				l.dispatchResponse(circulation.Message{
					Kind: circulation.ValueKindResponse,
					Response: circulation.Response{
						IntentionID: msg.Intention.IntentionID,
						Status:      circulation.ValueStatusOK,
						To:          msg.Intention.From,
						From:        msg.Intention.To,
						Payload:     map[string]any{},
					},
				})
			} else if msg.Kind == circulation.ValueKindResponse {
				responses <- msg
			}
		}
	}()

	rootID := "orch-corr-check"
	in := circulation.Intention{
		IntentionID: rootID,
		To:          circulation.Address{Context: "/ctx/root", Cap: "cap.orch.corr", Type: circulation.ValueTypeUser},
		From:        circulation.Address{Context: "/ctx/caller", Cap: "caller", Type: circulation.ValueTypeUser},
	}
	l.runDSLOrchestrator(circulation.Message{Kind: circulation.ValueKindIntention, Intention: in})

	select {
	case msg := <-responses:
		if msg.Response.IntentionID != rootID {
			t.Fatalf("terminal intention_id=%q want %q", msg.Response.IntentionID, rootID)
		}
		if string(msg.Response.To.Context) != string(in.From.Context) {
			t.Fatalf("terminal to.context=%q want %q", msg.Response.To.Context, in.From.Context)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout")
	}
}

// =============================================================================
// N4 — ElementRef and @capacity-as-$ref new feature tests
// =============================================================================

// Test A: @capacity resolved from $input ref
func TestExecutionDSL_N4_CapacityRefFromInputParam(t *testing.T) {
	st := newDSLExecState(circulation.Intention{
		IntentionID: "cap-ref-test",
		To:          circulation.Address{Context: "/root/ctx", Cap: "orch.cap", Type: "user"},
		Params:      map[string]any{"handler": "/root/ctx/my.cap"},
	}, nil)

	ref, err := resolveDSLCapacityRef(st, "$input.handler")
	if err != nil {
		t.Fatalf("resolveDSLCapacityRef error: %v", err)
	}
	if ref != "/root/ctx/my.cap" {
		t.Fatalf("expected /root/ctx/my.cap, got %q", ref)
	}

	// Verify it parses into the correct Address components
	addr, err := parseDSLAddressRef(st.in.To.Context, ref, "user")
	if err != nil {
		t.Fatalf("parseDSLAddressRef error: %v", err)
	}
	if addr.Cap != "my.cap" {
		t.Fatalf("expected cap=my.cap, got %q", addr.Cap)
	}
	if string(addr.Context) != "/root/ctx" {
		t.Fatalf("expected context=/root/ctx, got %q", addr.Context)
	}
}

// Test A2: @capacity as $input ref through evalDSLInvokeNode (end-to-end)
func TestExecutionDSL_N4_CapacityRefFromInputParamEndToEnd(t *testing.T) {
	l, commCh, _ := newExecutionLoopHarness(nil)

	var capturedCap string
	go func() {
		for msg := range commCh {
			if msg.Kind != circulation.ValueKindIntention {
				continue
			}
			capturedCap = msg.Intention.To.Cap
			l.dispatchResponse(circulation.Message{
				Kind: circulation.ValueKindResponse,
				Response: circulation.Response{
					IntentionID: msg.Intention.IntentionID,
					Status:      circulation.ValueStatusOK,
					To:          msg.Intention.From,
					From:        msg.Intention.To,
					Payload:     map[string]any{},
				},
			})
		}
	}()

	st := newDSLExecState(circulation.Intention{
		IntentionID: "cap-ref-e2e",
		To:          circulation.Address{Context: "/root/ctx", Cap: "orch.cap", Type: "user"},
		Params:      map[string]any{"handler": "/root/ctx/my.cap"},
	}, nil)

	node := map[string]any{
		"@capacity": "$input.handler",
		"with":      map[string]any{},
	}
	resp, err := l.evalDSLInvokeNode(st, node)
	if err != nil {
		t.Fatalf("evalDSLInvokeNode error: %v", err)
	}
	if resp.Status != circulation.ValueStatusOK {
		t.Fatalf("expected ok, got %q", resp.Status)
	}
	if capturedCap != "my.cap" {
		t.Fatalf("expected invoked cap=my.cap, got %q", capturedCap)
	}
}

// Test B: @schema, @structure, @document in invoke.with produce ElementRefs
func TestExecutionDSL_N4_ElementRefsInWithBlock(t *testing.T) {
	l, _, _ := newExecutionLoopHarness(nil)
	st := newDSLExecState(circulation.Intention{
		IntentionID: "elemrefs-test",
		To:          circulation.Address{Context: "/root/workflows", Cap: "orch.cap", Type: "user"},
	}, nil)

	withBlock := map[string]any{
		"@schema":    "/root/workflows/schema/profile",
		"@structure": "/root/workflows/structure/execution_plan",
		"@document":  "/root/workflows/document/runbook",
	}

	_, _, elemRefs, err := l.resolveDSLInvokeInput(st, withBlock)
	if err != nil {
		t.Fatalf("resolveDSLInvokeInput error: %v", err)
	}
	if len(elemRefs) != 3 {
		t.Fatalf("expected 3 elemRefs, got %d: %#v", len(elemRefs), elemRefs)
	}

	byKind := make(map[string]circulation.ElementRef)
	for _, r := range elemRefs {
		byKind[r.Kind] = r
	}

	schema, ok := byKind["schema"]
	if !ok {
		t.Fatal("missing schema ElementRef")
	}
	if schema.ID != "profile" || string(schema.Context) != "/root/workflows/schema" {
		t.Fatalf("schema ref wrong: %#v", schema)
	}

	structure, ok := byKind["structure"]
	if !ok {
		t.Fatal("missing structure ElementRef")
	}
	if structure.ID != "execution_plan" || string(structure.Context) != "/root/workflows/structure" {
		t.Fatalf("structure ref wrong: %#v", structure)
	}

	document, ok := byKind["document"]
	if !ok {
		t.Fatal("missing document ElementRef")
	}
	if document.ID != "runbook" || string(document.Context) != "/root/workflows/document" {
		t.Fatalf("document ref wrong: %#v", document)
	}
}

// Test C: incoming ElementRefs exposed via $input by Repr name
func TestExecutionDSL_N4_IncomingElementRefsExposedViaInput(t *testing.T) {
	st := newDSLExecState(circulation.Intention{
		IntentionID: "elemrefs-input",
		To:          circulation.Address{Context: "/root/workflows", Cap: "orch.cap", Type: "user"},
		ElementRefs: []circulation.ElementRef{
			{Context: "/root/workflows", ID: "execution_plan", Repr: "execution_plan", Kind: "structure"},
		},
	}, nil)

	got, err := st.resolveValueRef("$input.execution_plan")
	if err != nil {
		t.Fatalf("resolveValueRef error: %v", err)
	}
	if got != "/root/workflows/execution_plan" {
		t.Fatalf("expected /root/workflows/execution_plan, got %q", got)
	}
}

// Test D: incoming Matters exposed via $input by Repr name
func TestExecutionDSL_N4_IncomingMattersExposedViaInput(t *testing.T) {
	st := newDSLExecState(circulation.Intention{
		IntentionID: "matters-input",
		To:          circulation.Address{Context: "/root", Cap: "orch.cap", Type: "user"},
		Matters: []circulation.MatterRef{
			{Context: "/root", ID: "m_profile", Repr: "source_profile"},
		},
	}, nil)

	got, err := st.resolveValueRef("$input.source_profile")
	if err != nil {
		t.Fatalf("resolveValueRef error: %v", err)
	}
	if got != "/root/m_profile" {
		t.Fatalf("expected /root/m_profile, got %q", got)
	}
}

// Test N5: on_error on invoke node — collect and continue swallow the error
func TestExecutionDSL_N5_InvokeOnErrorCollectSwallowsError(t *testing.T) {
	l, commCh, _ := newExecutionLoopHarness(nil)
	go func() {
		for msg := range commCh {
			if msg.Kind != circulation.ValueKindIntention {
				continue
			}
			l.dispatchResponse(circulation.Message{
				Kind: circulation.ValueKindResponse,
				Response: circulation.Response{
					IntentionID: msg.Intention.IntentionID,
					Status:      circulation.ValueStatusError,
					To:          msg.Intention.From,
					From:        msg.Intention.To,
					Error:       &circulation.ResponseProblem{Code: "cap_error", Origin: "test"},
				},
			})
		}
	}()

	st := newDSLN2State(nil)
	resp, err := l.evalDSLNode(st, map[string]any{
		"invoke": map[string]any{
			"@capacity": "/ctx/root/fail.cap",
			"on_error":  "collect",
			"as":        "step",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != circulation.ValueStatusOK {
		t.Fatalf("on_error=collect must swallow error, got status %q", resp.Status)
	}
	if resp.Payload["collected_error"] == nil {
		t.Fatal("on_error=collect must expose collected_error in payload")
	}
}

func TestExecutionDSL_N5_InvokeOnErrorFailPropagatesError(t *testing.T) {
	l, commCh, _ := newExecutionLoopHarness(nil)
	go func() {
		for msg := range commCh {
			if msg.Kind != circulation.ValueKindIntention {
				continue
			}
			l.dispatchResponse(circulation.Message{
				Kind: circulation.ValueKindResponse,
				Response: circulation.Response{
					IntentionID: msg.Intention.IntentionID,
					Status:      circulation.ValueStatusError,
					To:          msg.Intention.From,
					From:        msg.Intention.To,
					Error:       &circulation.ResponseProblem{Code: "cap_error", Origin: "test"},
				},
			})
		}
	}()

	st := newDSLN2State(nil)
	resp, err := l.evalDSLNode(st, map[string]any{
		"invoke": map[string]any{
			"@capacity": "/ctx/root/fail.cap",
			"on_error":  "fail",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != circulation.ValueStatusError {
		t.Fatalf("on_error=fail must propagate error status, got %q", resp.Status)
	}
}

// Test N6: sub-section (#name) in sequence items — resolution is evaluated
func TestExecutionDSL_N6_SubSectionInSequenceIsEvaluated(t *testing.T) {
	l, commCh, _ := newExecutionLoopHarness(nil)
	go func() {
		for msg := range commCh {
			if msg.Kind != circulation.ValueKindIntention {
				continue
			}
			l.dispatchResponse(circulation.Message{
				Kind: circulation.ValueKindResponse,
				Response: circulation.Response{
					IntentionID: msg.Intention.IntentionID,
					Status:      circulation.ValueStatusOK,
					To:          msg.Intention.From,
					From:        msg.Intention.To,
					Payload:     map[string]any{"result": "from_sub"},
				},
			})
		}
	}()

	st := newDSLN2State(nil)
	resp, err := l.evalDSLNode(st, map[string]any{
		">sequence": map[string]any{
			"items": []any{
				map[string]any{
					"#my_sub_section": map[string]any{
						"role": "a sub-section",
						"resolution": map[string]any{
							"invoke": map[string]any{
								"@capacity": "/ctx/root/sub.cap",
								"as":        "sub_result",
							},
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != circulation.ValueStatusOK {
		t.Fatalf("expected ok from sub-section, got %q", resp.Status)
	}
	if resp.Payload["result"] != "from_sub" {
		t.Fatalf("expected sub-section result payload, got %#v", resp.Payload)
	}
}

// Test N5b: >switch case "then" (spec key, no > prefix) is handled
func TestExecutionDSL_N5_SwitchCaseThenWithoutPrefixIsHandled(t *testing.T) {
	l, commCh, _ := newExecutionLoopHarness(nil)
	go func() {
		for msg := range commCh {
			if msg.Kind != circulation.ValueKindIntention {
				continue
			}
			l.dispatchResponse(circulation.Message{
				Kind: circulation.ValueKindResponse,
				Response: circulation.Response{
					IntentionID: msg.Intention.IntentionID,
					Status:      circulation.ValueStatusOK,
					To:          msg.Intention.From,
					From:        msg.Intention.To,
					Payload:     map[string]any{"branch": "matched"},
				},
			})
		}
	}()

	st := newDSLExecState(circulation.Intention{
		IntentionID: "switch-then-test",
		To:          circulation.Address{Context: "/ctx/root", Cap: "orch.cap", Type: "user"},
		Params:      map[string]any{"mode": "fast"},
	}, nil)

	// "then" without > prefix — matches the spec and showcase format
	resp, err := l.evalDSLNode(st, map[string]any{
		">switch": map[string]any{
			"on": "$input.mode",
			"cases": []any{
				map[string]any{
					"value": "fast",
					"then": map[string]any{
						"invoke": map[string]any{
							"@capacity": "/ctx/root/fast.cap",
							"as":        "fast_result",
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != circulation.ValueStatusOK {
		t.Fatalf("expected ok, got %q", resp.Status)
	}
	if resp.Payload["branch"] != "matched" {
		t.Fatalf("expected matched branch payload, got %#v", resp.Payload)
	}
}

func TestExecutionDSL_N6_SubSectionWithNoResolutionReturnsEmptyOK(t *testing.T) {
	l, _, _ := newExecutionLoopHarness(nil)
	st := newDSLN2State(nil)

	resp, err := l.evalDSLNode(st, map[string]any{
		">sequence": map[string]any{
			"items": []any{
				map[string]any{
					"#empty_section": map[string]any{
						"role": "no resolution",
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != circulation.ValueStatusOK {
		t.Fatalf("expected ok for section with no resolution, got %q", resp.Status)
	}
}

// Test E: named map form for @schema in invoke.with
func TestExecutionDSL_N4_NamedMapElementRefInWithBlock(t *testing.T) {
	l, _, _ := newExecutionLoopHarness(nil)
	st := newDSLExecState(circulation.Intention{
		IntentionID: "named-map-test",
		To:          circulation.Address{Context: "/root/workflows", Cap: "orch.cap", Type: "user"},
	}, nil)

	withBlock := map[string]any{
		"@schema": map[string]any{
			"validation_shape": "/root/workflows/schema/profile",
		},
	}

	_, _, elemRefs, err := l.resolveDSLInvokeInput(st, withBlock)
	if err != nil {
		t.Fatalf("resolveDSLInvokeInput error: %v", err)
	}
	if len(elemRefs) != 1 {
		t.Fatalf("expected 1 elemRef, got %d: %#v", len(elemRefs), elemRefs)
	}
	r := elemRefs[0]
	if r.Repr != "validation_shape" {
		t.Fatalf("expected Repr=validation_shape, got %q", r.Repr)
	}
	if r.Kind != "schema" {
		t.Fatalf("expected Kind=schema, got %q", r.Kind)
	}
	if r.ID != "profile" {
		t.Fatalf("expected ID=profile, got %q", r.ID)
	}
	if string(r.Context) != "/root/workflows/schema" {
		t.Fatalf("expected Context=/root/workflows/schema, got %q", r.Context)
	}
}

// =============================================================================
// N7 — output node
// =============================================================================

// output node resolves $-bindings from execution state and returns them as payload.
func TestExecutionDSL_N7_OutputNodeResolvesBindings(t *testing.T) {
	l, _, _ := newExecutionLoopHarness(nil)
	st := newDSLExecState(circulation.Intention{
		IntentionID: "output-test",
		To:          circulation.Address{Context: "/ctx/root", Cap: "orch.cap", Type: "user"},
		Params:      map[string]any{"label": "hello"},
	}, nil)
	st.setAlias("result_data", circulation.Response{
		IntentionID: "output-test",
		Status:      circulation.ValueStatusOK,
		Payload:     map[string]any{"ok": true},
	})

	resp, err := l.evalDSLNode(st, map[string]any{
		"output": map[string]any{
			"label":  "$input.label",
			"result": "$result_data",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != circulation.ValueStatusOK {
		t.Fatalf("expected ok, got %q", resp.Status)
	}
	if resp.Payload["label"] != "hello" {
		t.Fatalf("expected label=hello, got %#v", resp.Payload["label"])
	}
	// $result_data resolves to {status, payload} — check payload.ok
	resultMap, ok := resp.Payload["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result to be a map, got %T", resp.Payload["result"])
	}
	resultPayload, ok := resultMap["payload"].(map[string]any)
	if !ok || resultPayload["ok"] != true {
		t.Fatalf("expected result.payload={ok:true}, got %#v", resultMap)
	}
}

// output node at end of a sequence produces the final response payload.
func TestExecutionDSL_N7_OutputNodeAsSequenceTerminal(t *testing.T) {
	l, commCh, _ := newExecutionLoopHarness(nil)
	go func() {
		for msg := range commCh {
			if msg.Kind != circulation.ValueKindIntention {
				continue
			}
			l.dispatchResponse(circulation.Message{
				Kind: circulation.ValueKindResponse,
				Response: circulation.Response{
					IntentionID: msg.Intention.IntentionID,
					Status:      circulation.ValueStatusOK,
					To:          msg.Intention.From,
					From:        msg.Intention.To,
					Payload:     map[string]any{"value": 42},
				},
			})
		}
	}()

	st := newDSLExecState(circulation.Intention{
		IntentionID: "output-seq-test",
		To:          circulation.Address{Context: "/ctx/root", Cap: "orch.cap", Type: "user"},
	}, nil)

	resp, err := l.evalDSLNode(st, map[string]any{
		">sequence": map[string]any{
			"items": []any{
				map[string]any{
					"invoke": map[string]any{
						"@capacity": "/ctx/root/compute.cap",
						"as":        "computed",
					},
				},
				map[string]any{
					"output": map[string]any{
						"final": "$computed",
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != circulation.ValueStatusOK {
		t.Fatalf("expected ok, got %q", resp.Status)
	}
	// $computed resolves to {status, payload} — check payload.value
	finalMap, ok := resp.Payload["final"].(map[string]any)
	if !ok {
		t.Fatalf("expected final to be a map, got %T", resp.Payload["final"])
	}
	finalPayload, ok := finalMap["payload"].(map[string]any)
	if !ok || finalPayload["value"] != 42 {
		t.Fatalf("expected final.payload={value:42}, got %#v", finalMap)
	}
}
