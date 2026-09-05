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
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"brique_engine/circulation"
)

func writeDSLTestCap(t *testing.T, dir, capName string, doc map[string]any) {
	t.Helper()
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal dsl test cap: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, capName+".json"), b, 0o644); err != nil {
		t.Fatalf("write dsl test cap: %v", err)
	}
}

func TestExecutionDSL_N1_LoadDescriptorAndValidate(t *testing.T) {
	l := newExecUtilitiesLoop(nil)
	dir := t.TempDir()
	l.capRoot = dir

	writeDSLTestCap(t, dir, "cap.dsl.ok", map[string]any{
		"brique": map[string]any{
			"cap_name": "cap.dsl.ok",
			"kind":     "dsl",
		},
		"functional": map[string]any{
			"#root": map[string]any{
				"role":   "test root",
				"inputs": map[string]any{},
				"outputs": map[string]any{
					"done": map[string]any{"type": "boolean"},
				},
				"effects": map[string]any{},
				"transformation_contract": map[string]any{
					"from":      []any{"params"},
					"to":        []any{"done"},
					"affecting": []any{},
					"morphing":  "A request becomes a done result.",
				},
				"resolution": map[string]any{
					">if": map[string]any{
						"when": map[string]any{
							"left":  "$input.flag",
							"op":    "eq",
							"right": true,
						},
						">then": map[string]any{
							">switch": map[string]any{
								"on": "$input.kind",
								"cases": []any{
									map[string]any{
										"value": "x",
										">then": map[string]any{
											">sequence": map[string]any{
												"items": []any{},
											},
										},
									},
								},
							},
						},
						">else": map[string]any{
							"invoke": map[string]any{
								"@capacity": "/ctx/child/cap.user",
							},
						},
					},
				},
			},
		},
	})

	desc, err := l.loadDSLDescriptor("cap.dsl.ok")
	if err != nil {
		t.Fatalf("loadDSLDescriptor error: %v", err)
	}
	if desc.CapName != "cap.dsl.ok" || desc.RootName != "#root" {
		t.Fatalf("unexpected descriptor identity: %#v", desc)
	}
	if err := validateDSLPlan(desc); err != nil {
		t.Fatalf("validateDSLPlan error: %v", err)
	}
}

func TestExecutionDSL_N1_ValidateRejectsInvalidInvokeQualifiers(t *testing.T) {
	desc := DSLDescriptor{
		CapName:  "cap.bad",
		RootName: "#root",
		RootSection: DSLSection{
			Name:   "#root",
			Role:   "bad",
			Inputs: map[string]any{},
			Outputs: map[string]any{
				"done": map[string]any{"type": "boolean"},
			},
			TransformationContract: DSLTransformationContract{Morphing: "x"},
			Resolution: map[string]any{
				"invoke": map[string]any{
					"@capacity":      "/ctx/child/cap.user",
					"await_response": false,
					"as":             "bad_alias",
				},
			},
		},
	}
	if err := validateDSLPlan(desc); err == nil {
		t.Fatalf("expected invalid invoke qualifier validation error")
	}
}

func TestExecutionDSL_N1_RuntimeRefsAndPaths(t *testing.T) {
	st := newDSLExecState(circulation.Intention{
		IntentionID: "root",
		Params: map[string]any{
			"message": "hello",
			"nested": map[string]any{
				"count": 3,
			},
		},
	}, nil)
	st.setAlias("echo", circulation.Response{
		Status: circulation.ValueStatusOK,
		Payload: map[string]any{
			"message": "world",
			"items":   []any{"a", "b"},
		},
	})
	st.pushScope(map[string]any{
		dslLoopItemAlias:  map[string]any{"name": "item-a"},
		dslLoopIndexAlias: 2,
	})
	defer st.popScope()

	if got, err := st.resolveValueRef("$input.message"); err != nil || got != "hello" {
		t.Fatalf("$input.message got=%#v err=%v", got, err)
	}
	if got, err := st.resolveValueRef("$input.nested.count"); err != nil || got != 3 {
		t.Fatalf("$input.nested.count got=%#v err=%v", got, err)
	}
	if got, err := st.resolveValueRef("$echo.payload.message"); err != nil || got != "world" {
		t.Fatalf("$echo.payload.message got=%#v err=%v", got, err)
	}
	if got, err := st.resolveValueRef("$echo.payload.items.1"); err != nil || got != "b" {
		t.Fatalf("$echo.payload.items.1 got=%#v err=%v", got, err)
	}
	if got, err := st.resolveValueRef("$item.name"); err != nil || got != "item-a" {
		t.Fatalf("$item.name got=%#v err=%v", got, err)
	}
	if got, err := st.resolveValueRef("$index"); err != nil || got != 2 {
		t.Fatalf("$index got=%#v err=%v", got, err)
	}
}

func TestExecutionDSL_N1_InvokeAndAwaitInternal(t *testing.T) {
	l, commCh, _ := newExecutionLoopHarness(nil)
	in := circulation.Intention{
		IntentionID: "root",
		To:          circulation.Address{Context: "/ctx/root", Cap: "cap.dsl", Type: circulation.ValueTypeUser},
		From:        circulation.Address{Context: "/ctx/caller", Cap: "caller", Type: circulation.ValueTypeUser},
	}

	go func() {
		msg := recvExecMsg(t, commCh, "internal dsl sub-intention")
		if msg.Kind != circulation.ValueKindIntention {
			t.Errorf("unexpected emitted kind: %q", msg.Kind)
			return
		}
		l.dispatchResponse(circulation.Message{
			Kind: circulation.ValueKindResponse,
			Response: circulation.Response{
				IntentionID: msg.Intention.IntentionID,
				Status:      circulation.ValueStatusOK,
				To:          msg.Intention.From,
				From:        msg.Intention.To,
				Payload: map[string]any{
					"echo": "ok",
				},
			},
		})
	}()

	resp, err := l.invokeAndAwait(in)
	if err != nil {
		t.Fatalf("invokeAndAwait error: %v", err)
	}
	if resp.Status != circulation.ValueStatusOK || resp.Payload["echo"] != "ok" {
		t.Fatalf("unexpected invokeAndAwait response: %#v", resp)
	}
}

func TestExecutionDSL_N1_FlowSequenceIfSwitch(t *testing.T) {
	l, commCh, _ := newExecutionLoopHarness(nil)
	st := newDSLExecState(circulation.Intention{
		IntentionID: "root-flow",
		To:          circulation.Address{Context: "/ctx/root", Cap: "cap.dsl", Type: circulation.ValueTypeUser},
		From:        circulation.Address{Context: "/ctx/caller", Cap: "caller", Type: circulation.ValueTypeUser},
		Params: map[string]any{
			"enabled": true,
			"kind":    "go",
		},
	}, nil)

	go func() {
		for i := 0; i < 2; i++ {
			msg := recvExecMsg(t, commCh, "dsl flow sub-intention")
			l.dispatchResponse(circulation.Message{
				Kind: circulation.ValueKindResponse,
				Response: circulation.Response{
					IntentionID: msg.Intention.IntentionID,
					Status:      circulation.ValueStatusOK,
					To:          msg.Intention.From,
					From:        msg.Intention.To,
					Payload: map[string]any{
						"cap": msg.Intention.To.Cap,
					},
				},
			})
		}
	}()

	resp, err := l.evalDSLNode(st, map[string]any{
		">sequence": map[string]any{
			"items": []any{
				map[string]any{
					"invoke": map[string]any{
						"@capacity": "/ctx/root/step.first",
						"as":        "first",
					},
				},
				map[string]any{
					">if": map[string]any{
						"when": map[string]any{
							"left":  "$input.enabled",
							"op":    "eq",
							"right": true,
						},
						">then": map[string]any{
							">switch": map[string]any{
								"on": "$input.kind",
								"cases": []any{
									map[string]any{
										"value": "go",
										">then": map[string]any{
											"invoke": map[string]any{
												"@capacity": "/ctx/root/step.second",
												"as":        "second",
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("evalDSLNode error: %v", err)
	}
	if resp.Status != circulation.ValueStatusOK || resp.Payload["cap"] != "step.second" {
		t.Fatalf("unexpected flow response: %#v", resp)
	}
	if _, ok := st.alias("first"); !ok {
		t.Fatalf("expected first alias stored")
	}
	if _, ok := st.alias("second"); !ok {
		t.Fatalf("expected second alias stored")
	}
}

func TestExecutionDSL_N1_ParallelAndForEach(t *testing.T) {
	l, commCh, _ := newExecutionLoopHarness(nil)
	st := newDSLExecState(circulation.Intention{
		IntentionID: "root-par",
		To:          circulation.Address{Context: "/ctx/root", Cap: "cap.dsl", Type: circulation.ValueTypeUser},
		Params: map[string]any{
			"items": []any{"a", "b", "c"},
		},
	}, nil)

	go func() {
		timeout := time.After(500 * time.Millisecond)
		for handled := 0; handled < 5; {
			select {
			case msg := <-commCh:
				if msg.Kind != circulation.ValueKindIntention {
					continue
				}
				handled++
				payload := map[string]any{"cap": msg.Intention.To.Cap}
				if msg.Intention.Params != nil {
					if item, ok := msg.Intention.Params["item"]; ok {
						payload["item"] = item
					}
				}
				l.dispatchResponse(circulation.Message{
					Kind: circulation.ValueKindResponse,
					Response: circulation.Response{
						IntentionID: msg.Intention.IntentionID,
						Status:      circulation.ValueStatusOK,
						To:          msg.Intention.From,
						From:        msg.Intention.To,
						Payload:     payload,
					},
				})
			case <-timeout:
				return
			}
		}
	}()

	parResp, err := l.evalDSLNode(st, map[string]any{
		">parallel": map[string]any{
			"limit": 2,
			"items": []any{
				map[string]any{"invoke": map[string]any{"@capacity": "/ctx/root/p.one", "as": "one"}},
				map[string]any{"invoke": map[string]any{"@capacity": "/ctx/root/p.two", "as": "two"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("parallel eval error: %v", err)
	}
	items, _ := parResp.Payload["items"].([]any)
	if parResp.Status != circulation.ValueStatusOK || len(items) != 2 {
		t.Fatalf("unexpected parallel response: %#v", parResp)
	}

	loopResp, err := l.evalDSLNode(st, map[string]any{
		">for_each": map[string]any{
			"collection": "$input.items",
			"limit":      2,
			">do": map[string]any{
				"invoke": map[string]any{
					"@capacity": "/ctx/root/p.each",
					"with": map[string]any{
						"item": "$item",
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("for_each eval error: %v", err)
	}
	loopItems, _ := loopResp.Payload["items"].([]any)
	if loopResp.Status != circulation.ValueStatusOK || len(loopItems) != 3 {
		t.Fatalf("unexpected for_each response: %#v", loopResp)
	}
}

func TestExecutionDSL_N1_WhileConvergesWithinLimit(t *testing.T) {
	l, _, _ := newExecutionLoopHarness(nil)
	st := newDSLExecState(circulation.Intention{
		IntentionID: "root-while",
		To:          circulation.Address{Context: "/ctx/root", Cap: "cap.dsl", Type: circulation.ValueTypeUser},
	}, nil)

	resp, err := l.evalDSLNode(st, map[string]any{
		">while": map[string]any{
			"when":           map[string]any{"left": "$index", "op": "lt", "right": 3},
			"max_iterations": 5,
			">do":            map[string]any{"output": map[string]any{"tick": "$index"}},
		},
	})
	if err != nil {
		t.Fatalf("while eval error: %v", err)
	}
	items, _ := resp.Payload["items"].([]any)
	if resp.Status != circulation.ValueStatusOK || len(items) != 3 {
		t.Fatalf("unexpected while response: %#v", resp)
	}
	if resp.Payload["iterations"] != 3 {
		t.Fatalf("unexpected iterations count: %#v", resp.Payload["iterations"])
	}
}

func TestExecutionDSL_N1_WhileFalseConditionNeverRuns(t *testing.T) {
	l, _, _ := newExecutionLoopHarness(nil)
	st := newDSLExecState(circulation.Intention{
		IntentionID: "root-while-skip",
		To:          circulation.Address{Context: "/ctx/root", Cap: "cap.dsl", Type: circulation.ValueTypeUser},
	}, nil)

	resp, err := l.evalDSLNode(st, map[string]any{
		">while": map[string]any{
			"when":           map[string]any{"left": false, "op": "eq", "right": true},
			"max_iterations": 5,
			">do":            map[string]any{"output": map[string]any{"tick": "$index"}},
		},
	})
	if err != nil {
		t.Fatalf("while eval error: %v", err)
	}
	items, _ := resp.Payload["items"].([]any)
	if resp.Status != circulation.ValueStatusOK || len(items) != 0 {
		t.Fatalf("unexpected while response for false condition: %#v", resp)
	}
}

func TestExecutionDSL_N1_WhileExceedsMaxIterations(t *testing.T) {
	l, _, _ := newExecutionLoopHarness(nil)
	st := newDSLExecState(circulation.Intention{
		IntentionID: "root-while-overflow",
		To:          circulation.Address{Context: "/ctx/root", Cap: "cap.dsl", Type: circulation.ValueTypeUser},
	}, nil)

	_, err := l.evalDSLNode(st, map[string]any{
		">while": map[string]any{
			"when":           map[string]any{"left": true, "op": "eq", "right": true},
			"max_iterations": 3,
			">do":            map[string]any{"output": map[string]any{"tick": "$index"}},
		},
	})
	if err == nil {
		t.Fatalf("expected error when max_iterations is exceeded")
	}
}

func TestExecutionDSL_N1_WhileRequiresMaxIterations(t *testing.T) {
	l, _, _ := newExecutionLoopHarness(nil)
	st := newDSLExecState(circulation.Intention{
		IntentionID: "root-while-no-limit",
		To:          circulation.Address{Context: "/ctx/root", Cap: "cap.dsl", Type: circulation.ValueTypeUser},
	}, nil)

	_, err := l.evalDSLNode(st, map[string]any{
		">while": map[string]any{
			"when": map[string]any{"left": true, "op": "eq", "right": true},
			">do":  map[string]any{"output": map[string]any{"tick": "$index"}},
		},
	})
	if err == nil {
		t.Fatalf("expected error when max_iterations is missing")
	}
}

// raceResponder answers invokes on commCh according to per-cap behavior:
// delay before responding, and ok/error status.
func raceResponder(t *testing.T, l *ExecutionLoop, commCh chan circulation.Message, behavior map[string]struct {
	delay time.Duration
	ok    bool
}) {
	t.Helper()
	go func() {
		timeout := time.After(2 * time.Second)
		for {
			select {
			case msg := <-commCh:
				if msg.Kind != circulation.ValueKindIntention {
					continue
				}
				b, known := behavior[msg.Intention.To.Cap]
				if !known {
					continue
				}
				go func(in circulation.Intention, delay time.Duration, ok bool) {
					time.Sleep(delay)
					resp := circulation.Response{
						IntentionID: in.IntentionID,
						To:          in.From,
						From:        in.To,
					}
					if ok {
						resp.Status = circulation.ValueStatusOK
						resp.Payload = map[string]any{"who": in.To.Cap}
					} else {
						resp.Status = circulation.ValueStatusError
						resp.Error = &circulation.ResponseProblem{Code: circulation.ValueCodeRefused, Message: "branch failed"}
					}
					l.dispatchResponse(circulation.Message{Kind: circulation.ValueKindResponse, Response: resp})
				}(msg.Intention, b.delay, b.ok)
			case <-timeout:
				return
			}
		}
	}()
}

func TestExecutionDSL_N1_RaceFirstSuccessWins(t *testing.T) {
	l, commCh, _ := newExecutionLoopHarness(nil)
	st := newDSLExecState(circulation.Intention{
		IntentionID: "root-race",
		To:          circulation.Address{Context: "/ctx/root", Cap: "cap.dsl", Type: circulation.ValueTypeUser},
	}, nil)

	raceResponder(t, l, commCh, map[string]struct {
		delay time.Duration
		ok    bool
	}{
		"fast.fail": {delay: 0, ok: false},
		"mid.ok":    {delay: 30 * time.Millisecond, ok: true},
		"slow.ok":   {delay: 300 * time.Millisecond, ok: true},
	})

	resp, err := l.evalDSLNode(st, map[string]any{
		">race": map[string]any{
			"items": []any{
				map[string]any{"invoke": map[string]any{"@capacity": "/ctx/root/fast.fail", "as": "fast"}},
				map[string]any{"invoke": map[string]any{"@capacity": "/ctx/root/mid.ok", "as": "mid"}},
				map[string]any{"invoke": map[string]any{"@capacity": "/ctx/root/slow.ok", "as": "slow"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("race eval error: %v", err)
	}
	if resp.Status != circulation.ValueStatusOK || resp.Payload["who"] != "mid.ok" {
		t.Fatalf("expected mid.ok to win the race, got %#v", resp)
	}
	if _, ok := st.alias("mid"); !ok {
		t.Fatalf("winner alias should be merged into parent scope")
	}
	if _, ok := st.alias("slow"); ok {
		t.Fatalf("losing branch alias must not be merged into parent scope")
	}
}

func TestExecutionDSL_N1_RaceAllFailReturnsError(t *testing.T) {
	l, commCh, _ := newExecutionLoopHarness(nil)
	st := newDSLExecState(circulation.Intention{
		IntentionID: "root-race-fail",
		To:          circulation.Address{Context: "/ctx/root", Cap: "cap.dsl", Type: circulation.ValueTypeUser},
	}, nil)

	raceResponder(t, l, commCh, map[string]struct {
		delay time.Duration
		ok    bool
	}{
		"fail.a": {delay: 0, ok: false},
		"fail.b": {delay: 10 * time.Millisecond, ok: false},
	})

	resp, err := l.evalDSLNode(st, map[string]any{
		">race": map[string]any{
			"items": []any{
				map[string]any{"invoke": map[string]any{"@capacity": "/ctx/root/fail.a"}},
				map[string]any{"invoke": map[string]any{"@capacity": "/ctx/root/fail.b"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("race eval error: %v", err)
	}
	if resp.Status != circulation.ValueStatusError || resp.Error == nil {
		t.Fatalf("expected error response when every branch fails, got %#v", resp)
	}
}

func TestExecutionDSL_N1_RaceRequiresItems(t *testing.T) {
	l, _, _ := newExecutionLoopHarness(nil)
	st := newDSLExecState(circulation.Intention{
		IntentionID: "root-race-empty",
		To:          circulation.Address{Context: "/ctx/root", Cap: "cap.dsl", Type: circulation.ValueTypeUser},
	}, nil)

	if _, err := l.evalDSLNode(st, map[string]any{">race": map[string]any{"items": []any{}}}); err == nil {
		t.Fatalf("expected error for empty race items")
	}
	if _, err := l.evalDSLNode(st, map[string]any{">race": map[string]any{}}); err == nil {
		t.Fatalf("expected error for missing race items")
	}
}
