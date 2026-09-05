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

package junction_test

import (
	"testing"

	"brique_engine/circulation"
	"brique_engine/configuration"
	"brique_engine/junction"
	"brique_engine/shared"
)

func frameForTraceTest(enabled bool, level string, ch chan circulation.Message) *junction.ContextRegistry {
	fam := junction.FamiliesInChanRegistry{}
	if ch != nil {
		fam[shared.FamilyTrace] = ch
	}
	return &junction.ContextRegistry{
		CtxId:        "/ctx/test",
		TraceEnabled: enabled,
		TraceLevel:   level,
		FamIn:        fam,
	}
}

func TestTraceGateEnabled_N1_TG_01_NilFrame(t *testing.T) {
	ok := junction.TraceGateEnabled(nil, circulation.TraceWire{TraceKind: circulation.ValueTraceCommIngress})
	if ok {
		t.Fatalf("expected false for nil frame")
	}
}

func TestTraceGateEnabled_N1_TG_02_Disabled(t *testing.T) {
	f := frameForTraceTest(false, configuration.ValueConfigTraceLevelNormal, nil)
	ok := junction.TraceGateEnabled(f, circulation.TraceWire{TraceKind: circulation.ValueTraceCommIngress})
	if ok {
		t.Fatalf("expected false when trace disabled")
	}
}

func TestTraceGateEnabled_N1_TG_03_UserBypass(t *testing.T) {
	f := frameForTraceTest(true, configuration.ValueConfigTraceLevelMinimal, nil)
	ok := junction.TraceGateEnabled(f, circulation.TraceWire{TraceKind: configuration.ValueConfigTraceLevelUser})
	if !ok {
		t.Fatalf("expected true for user trace kind")
	}
}

func TestTraceGateEnabled_N1_TG_04_MinimalWhitelist(t *testing.T) {
	f := frameForTraceTest(true, configuration.ValueConfigTraceLevelMinimal, nil)
	ok := junction.TraceGateEnabled(f, circulation.TraceWire{TraceKind: circulation.ValueTraceCommIngress})
	if !ok {
		t.Fatalf("expected true for minimal whitelisted kind")
	}
}

func TestTraceGateEnabled_N1_TG_05_MinimalRejectWithoutReason(t *testing.T) {
	f := frameForTraceTest(true, configuration.ValueConfigTraceLevelMinimal, nil)
	ok := junction.TraceGateEnabled(f, circulation.TraceWire{TraceKind: circulation.ValueTraceCommEgress})
	if ok {
		t.Fatalf("expected false for non-whitelisted kind without reason")
	}
}

func TestTraceGateEnabled_N1_TG_06_MinimalReasonFallback(t *testing.T) {
	f := frameForTraceTest(true, configuration.ValueConfigTraceLevelMinimal, nil)
	ok := junction.TraceGateEnabled(f, circulation.TraceWire{
		TraceKind:  circulation.ValueTraceCommEgress,
		ReasonCode: "ERR",
	})
	if !ok {
		t.Fatalf("expected true for reason fallback in minimal")
	}
}

func TestTraceGateEnabled_N1_TG_07_DebugAcceptAll(t *testing.T) {
	f := frameForTraceTest(true, configuration.ValueConfigTraceLevelDebug, nil)
	ok := junction.TraceGateEnabled(f, circulation.TraceWire{TraceKind: "anything"})
	if !ok {
		t.Fatalf("expected true for debug level")
	}
}

func TestTraceGateEnabled_N1_TG_08_NormalWhitelist(t *testing.T) {
	f := frameForTraceTest(true, configuration.ValueConfigTraceLevelNormal, nil)
	ok := junction.TraceGateEnabled(f, circulation.TraceWire{TraceKind: circulation.ValueTraceCommEgress})
	if !ok {
		t.Fatalf("expected true for normal whitelisted kind")
	}
}

func TestTraceGateEnabled_N1_TG_09_EmptyLevelDefaultsToNormal(t *testing.T) {
	f := frameForTraceTest(true, "", nil)
	ok := junction.TraceGateEnabled(f, circulation.TraceWire{TraceKind: circulation.ValueTraceCommEgress})
	if !ok {
		t.Fatalf("expected true for empty level defaulting to normal")
	}
}

func TestTraceGateEnabled_N1_TG_10_UnknownLevelDefaultsToNormal(t *testing.T) {
	f := frameForTraceTest(true, "unknown", nil)
	ok := junction.TraceGateEnabled(f, circulation.TraceWire{TraceKind: circulation.ValueTraceCommEgress})
	if !ok {
		t.Fatalf("expected true for unknown level defaulting to normal")
	}
}

func TestTraceEmit_N1_TG_11_GateDenied(t *testing.T) {
	ch := make(chan circulation.Message, 1)
	f := frameForTraceTest(false, configuration.ValueConfigTraceLevelNormal, ch)

	ok := junction.TraceEmit(f, circulation.TraceWire{TraceKind: circulation.ValueTraceCommIngress})
	if ok {
		t.Fatalf("expected false when gate denies")
	}
	if len(ch) != 0 {
		t.Fatalf("expected no emitted message when gate denies")
	}
}

func TestTraceEmit_N1_TG_12_MissingTraceChannel(t *testing.T) {
	f := frameForTraceTest(true, configuration.ValueConfigTraceLevelNormal, nil)
	ok := junction.TraceEmit(f, circulation.TraceWire{TraceKind: circulation.ValueTraceCommIngress})
	if ok {
		t.Fatalf("expected false when trace channel missing")
	}
}

func TestTraceEmit_N1_TG_13_SendSuccess(t *testing.T) {
	ch := make(chan circulation.Message, 1)
	f := frameForTraceTest(true, configuration.ValueConfigTraceLevelNormal, ch)

	tw := circulation.TraceWire{
		Timestamp: "2026-03-07T12:00:00Z",
		TraceKind: circulation.ValueTraceCommIngress,
	}

	ok := junction.TraceEmit(f, tw)
	if !ok {
		t.Fatalf("expected true when send succeeds")
	}

	select {
	case msg := <-ch:
		if msg.Kind != circulation.ValueKindTrace {
			t.Fatalf("message kind = %q, want %q", msg.Kind, circulation.ValueKindTrace)
		}
		if msg.TS != tw.Timestamp {
			t.Fatalf("message ts = %q, want %q", msg.TS, tw.Timestamp)
		}
		if msg.Trace.ContextID != f.CtxId {
			t.Fatalf("trace context_id = %q, want %q", msg.Trace.ContextID, f.CtxId)
		}
		if msg.Trace.TraceKind != tw.TraceKind {
			t.Fatalf("trace kind = %q, want %q", msg.Trace.TraceKind, tw.TraceKind)
		}
	default:
		t.Fatalf("expected one emitted message")
	}
}

func TestTraceEmit_N1_TG_14_ChannelFullNonBlockingDrop(t *testing.T) {
	ch := make(chan circulation.Message, 1)
	ch <- circulation.Message{Kind: circulation.ValueKindTrace}
	f := frameForTraceTest(true, configuration.ValueConfigTraceLevelNormal, ch)

	ok := junction.TraceEmit(f, circulation.TraceWire{TraceKind: circulation.ValueTraceCommIngress})
	if ok {
		t.Fatalf("expected false when channel is full")
	}
	if len(ch) != 1 {
		t.Fatalf("channel should remain full with one message")
	}
}

func TestTraceEmit_N1_TG_15_NilFramePanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic for nil frame")
		}
	}()

	_ = junction.TraceEmit(nil, circulation.TraceWire{TraceKind: circulation.ValueTraceCommIngress})
}
