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

package comm

import (
	"testing"

	"brique_engine/circulation"
	"brique_engine/configuration"
	"brique_engine/junction"
	"brique_engine/shared"
)

func newScatteredLoop(ctxID string, reg *mockCommReg) (*CommLoop, chan circulation.Message) {
	traceCh := make(chan circulation.Message, 128)
	l := &CommLoop{
		frame: &junction.ContextRegistry{
			CtxId:        ctxID,
			CtxCommReg:   reg,
			FamIn:        junction.FamiliesInChanRegistry{shared.FamilyTrace: traceCh},
			TraceEnabled: true,
			TraceLevel:   configuration.ValueConfigTraceLevelDebug,
		},
		cfg:  CommCfg{AllowedScatter: map[string]map[string]bool{}, MaxScatterItems: 0},
		done: make(chan struct{}),
	}
	return l, traceCh
}

func mkScatterReq(toCtx, typ, cap, from string, scattered any) circulation.Message {
	m := mkIntentionMsg(toCtx, typ, from)
	m.Intention.To.Cap = cap
	if scattered != nil {
		m.Intention.Params = map[string]any{circulation.KeyScatteredParam: scattered}
	}
	return m
}

func recvResponse(t *testing.T, ch <-chan circulation.Message, label string) circulation.Response {
	t.Helper()
	m := recvMsg(t, ch, label)
	if m.Kind != circulation.ValueKindResponse {
		t.Fatalf("expected response message on %s, got kind=%q", label, m.Kind)
	}
	return m.Response
}

func TestCommScattered_N1_CMSC_01_IsScatteredIntentionPredicate(t *testing.T) {
	l, _ := newScatteredLoop(shared.RootContextID, newMockCommReg())

	if l.isScatteredIntention(nil) {
		t.Fatalf("nil message should be false")
	}
	if l.isScatteredIntention(&circulation.Message{Kind: circulation.ValueKindResponse}) {
		t.Fatalf("non-intention should be false")
	}
	mNoParams := mkIntentionMsg(shared.RootContextID, circulation.ValueTypeMatter, "/ctx/a")
	if l.isScatteredIntention(&mNoParams) {
		t.Fatalf("intention without params should be false")
	}
	mWith := mkScatterReq(shared.RootContextID, circulation.ValueTypeMatter, "cap", "/ctx/a", []any{})
	if !l.isScatteredIntention(&mWith) {
		t.Fatalf("intention with scattered key should be true")
	}
}

func TestCommScattered_N1_CMSC_02_ScatterAllowedPredicate(t *testing.T) {
	l, _ := newScatteredLoop(shared.RootContextID, newMockCommReg())
	l.cfg.AllowedScatter = map[string]map[string]bool{
		"matter": {"read": true},
	}

	if !l.scatterAllowed(" matter ", " read ") {
		t.Fatalf("trimmed allowed pair should be true")
	}
	if l.scatterAllowed("matter", "write") {
		t.Fatalf("non-whitelisted cap should be false")
	}
	if l.scatterAllowed("", "read") {
		t.Fatalf("empty family should be false")
	}
}

func TestCommScattered_N1_CMSC_03_HandleOutsideRootRejects(t *testing.T) {
	l, traceCh := newScatteredLoop("/ctx/local", newMockCommReg())
	msg := mkScatterReq(shared.RootContextID, circulation.ValueTypeMatter, "read", "/ctx/rq", []any{})
	l.handleScatteredIngress(msg, EndpointInnerCtx, "")
	if !traceContains(traceCh, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidEndPoint) {
		t.Fatalf("expected invalid_endpoint reject trace")
	}
}

func TestCommScattered_N1_CMSC_04_HandleNonIntentionRejects(t *testing.T) {
	l, traceCh := newScatteredLoop(shared.RootContextID, newMockCommReg())
	msg := circulation.Message{Kind: circulation.ValueKindResponse}
	l.handleScatteredIngress(msg, EndpointInnerCtx, "")
	if !traceContains(traceCh, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidMsgType) {
		t.Fatalf("expected invalid_msg_type reject trace")
	}
}

func TestCommScattered_N1_CMSC_05_HandleMissingTypeCapAckError(t *testing.T) {
	reg := newMockCommReg()
	requester := make(chan circulation.Message, 1)
	reg.chByID["/ctx/rq"] = requester
	l, traceCh := newScatteredLoop(shared.RootContextID, reg)
	msg := mkScatterReq(shared.RootContextID, "", "", "/ctx/rq", []any{})

	l.handleScatteredIngress(msg, EndpointInnerCtx, "")
	resp := recvResponse(t, requester, "scatter missing type/cap ack")
	if resp.Status != "error" || resp.Error == nil || resp.Error.Code != "invalid" {
		t.Fatalf("expected error ack code=invalid, got status=%q err=%#v", resp.Status, resp.Error)
	}
	if !traceContains(traceCh, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidMsgType) {
		t.Fatalf("expected invalid_msg_type reject trace")
	}
}

func TestCommScattered_N1_CMSC_06_HandleNotAllowedAckRefused(t *testing.T) {
	reg := newMockCommReg()
	requester := make(chan circulation.Message, 1)
	reg.chByID["/ctx/rq"] = requester
	l, traceCh := newScatteredLoop(shared.RootContextID, reg)
	l.cfg.AllowedScatter = map[string]map[string]bool{"matter": {"allowed": true}}
	msg := mkScatterReq(shared.RootContextID, circulation.ValueTypeMatter, "forbidden", "/ctx/rq", []any{})

	l.handleScatteredIngress(msg, EndpointInnerCtx, "")
	resp := recvResponse(t, requester, "scatter refused ack")
	if resp.Status != "error" || resp.Error == nil || resp.Error.Code != "refused" {
		t.Fatalf("expected refused error ack, got status=%q err=%#v", resp.Status, resp.Error)
	}
	if !traceContains(traceCh, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidCapacityType) {
		t.Fatalf("expected invalid_capacity_type reject trace")
	}
}

func TestCommScattered_N1_CMSC_07_HandleInvalidScatteredTypeAckError(t *testing.T) {
	reg := newMockCommReg()
	requester := make(chan circulation.Message, 1)
	reg.chByID["/ctx/rq"] = requester
	l, traceCh := newScatteredLoop(shared.RootContextID, reg)
	l.cfg.AllowedScatter = map[string]map[string]bool{"matter": {"read": true}}
	msg := mkScatterReq(shared.RootContextID, circulation.ValueTypeMatter, "read", "/ctx/rq", "not-array")

	l.handleScatteredIngress(msg, EndpointInnerCtx, "")
	resp := recvResponse(t, requester, "scatter invalid type ack")
	if resp.Status != "error" || resp.Error == nil || resp.Error.Code != "invalid" {
		t.Fatalf("expected invalid error ack, got status=%q err=%#v", resp.Status, resp.Error)
	}
	if !traceContains(traceCh, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidMsgType) {
		t.Fatalf("expected invalid_msg_type reject trace")
	}
}

func TestCommScattered_N1_CMSC_08_HandleTooManyItemsAckError(t *testing.T) {
	reg := newMockCommReg()
	requester := make(chan circulation.Message, 1)
	reg.chByID["/ctx/rq"] = requester
	l, traceCh := newScatteredLoop(shared.RootContextID, reg)
	l.cfg.AllowedScatter = map[string]map[string]bool{"matter": {"read": true}}
	l.cfg.MaxScatterItems = 1
	list := []any{map[string]any{circulation.KeyTo: map[string]any{circulation.KeyContext: "/ctx/a"}}, map[string]any{circulation.KeyTo: map[string]any{circulation.KeyContext: "/ctx/b"}}}
	msg := mkScatterReq(shared.RootContextID, circulation.ValueTypeMatter, "read", "/ctx/rq", list)

	l.handleScatteredIngress(msg, EndpointInnerCtx, "")
	resp := recvResponse(t, requester, "scatter too many items ack")
	if resp.Status != "error" || resp.Error == nil || resp.Error.Code != "invalid" {
		t.Fatalf("expected invalid error ack, got status=%q err=%#v", resp.Status, resp.Error)
	}
	if !traceContains(traceCh, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidMsgType) {
		t.Fatalf("expected invalid_msg_type reject trace")
	}
}

func TestCommScattered_N1_CMSC_09_HandleValidFanoutAndAckOk(t *testing.T) {
	reg := newMockCommReg()
	requester := make(chan circulation.Message, 1)
	target := make(chan circulation.Message, 1)
	reg.chByID["/ctx/rq"] = requester
	reg.chByID["/ctx/target"] = target
	l, _ := newScatteredLoop(shared.RootContextID, reg)
	l.cfg.AllowedScatter = map[string]map[string]bool{"matter": {"read": true}}

	list := []any{map[string]any{
		circulation.KeyTo:     map[string]any{circulation.KeyContext: "/ctx/target"},
		circulation.KeyParams: map[string]any{"k": "v"},
	}}
	msg := mkScatterReq(shared.RootContextID, circulation.ValueTypeMatter, "read", "/ctx/rq", list)
	msg.Intention.Correlation = &circulation.Correlation{RootIntentionID: "root-1", ParentIntentionID: "parent-1"}

	l.handleScatteredIngress(msg, EndpointInnerCtx, "")

	sub := recvMsg(t, target, "scatter sub-intention")
	if sub.Kind != circulation.ValueKindIntention {
		t.Fatalf("expected forwarded intention, got kind=%q", sub.Kind)
	}
	if sub.Intention.To.Type != circulation.ValueTypeMatter || sub.Intention.To.Cap != "read" {
		t.Fatalf("expected propagated type/cap, got type=%q cap=%q", sub.Intention.To.Type, sub.Intention.To.Cap)
	}
	if sub.Intention.Params["k"] != "v" {
		t.Fatalf("expected propagated params in sub-intention")
	}

	ack := recvResponse(t, requester, "scatter ack ok")
	if ack.Status != "ok" {
		t.Fatalf("expected ok ack, got status=%q", ack.Status)
	}
	stream, _ := ack.Payload["stream"].(map[string]any)
	if stream == nil {
		t.Fatalf("expected stream payload in ack")
	}
}

func TestCommScattered_N1_CMSC_10_HandleScatterOfScatterSkipped(t *testing.T) {
	reg := newMockCommReg()
	requester := make(chan circulation.Message, 1)
	target := make(chan circulation.Message, 1)
	reg.chByID["/ctx/rq"] = requester
	reg.chByID["/ctx/target"] = target
	l, traceCh := newScatteredLoop(shared.RootContextID, reg)
	l.cfg.AllowedScatter = map[string]map[string]bool{"matter": {"read": true}}

	list := []any{map[string]any{
		circulation.KeyTo:     map[string]any{circulation.KeyContext: "/ctx/target"},
		circulation.KeyParams: map[string]any{circulation.KeyScatteredParam: []any{}},
	}}
	msg := mkScatterReq(shared.RootContextID, circulation.ValueTypeMatter, "read", "/ctx/rq", list)

	l.handleScatteredIngress(msg, EndpointInnerCtx, "")
	noMsg(t, target, "target should receive no sub on scatter-of-scatter")
	ack := recvResponse(t, requester, "scatter ack ok with skipped item")
	if ack.Status != "ok" {
		t.Fatalf("expected ok ack despite skipped item, got status=%q", ack.Status)
	}
	if !traceContains(traceCh, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidMsgType) {
		t.Fatalf("expected reject trace for scatter-of-scatter forbidden")
	}
}
