package wrapper

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLocalMarshalControlMessageRejectsBulkPayload(t *testing.T) {
	if _, err := marshalControlMessage(map[string]any{"payload": "small"}); err != nil {
		t.Fatalf("small control message rejected: %v", err)
	}
	if _, err := marshalControlMessage(map[string]any{"payload": strings.Repeat("x", ControlMessageLimitBytes)}); err == nil || !strings.Contains(err.Error(), "Matter substance") {
		t.Fatalf("oversized control message should direct caller to Matter, err=%v", err)
	}
}

func TestWrapperFrameCodecIsStrictAndRawCompatible(t *testing.T) {
	raw, _ := json.Marshal(Envelope{Kind: KindResponse, Response: &ResponseMsg{IntentionID: "raw"}})
	envelope, delegation, err := decodeWrapperFrame(raw)
	if err != nil || delegation != "" || envelope.Response == nil || envelope.Response.IntentionID != "raw" {
		t.Fatalf("raw envelope compatibility failed: env=%#v delegation=%q err=%v", envelope, delegation, err)
	}
	handle := strings.Repeat("A", 43)
	framed, _ := json.Marshal(WrapperFrame{
		Format: WrapperFrameFormat, Delegation: handle,
		Message: Envelope{Kind: KindIntention, Intention: &IntentionMsg{IntentionID: "child"}},
	})
	envelope, delegation, err = decodeWrapperFrame(framed)
	if err != nil || delegation != handle || envelope.Intention == nil {
		t.Fatalf("wrapper frame decode failed: env=%#v delegation=%q err=%v", envelope, delegation, err)
	}
	var extra map[string]any
	_ = json.Unmarshal(framed, &extra)
	extra["unexpected"] = true
	invalid, _ := json.Marshal(extra)
	if _, _, err := decodeWrapperFrame(invalid); err == nil {
		t.Fatal("wrapper frame with an unknown field was accepted")
	}
}

func TestScopedOutboundForcesEngineCorrelationAndEmptyIdentity(t *testing.T) {
	origin := &IntentionMsg{
		IntentionID: "parent", Correlation: Correlation{RootIntentionID: "root"},
	}
	base := NewOutboundAPI(nil, NewAwaitCorrelator(), "wrapper", "/root")
	scoped := base.scoped(origin, strings.Repeat("B", 43))
	identity, correlation := scoped.secureFields()
	if len(identity) != 0 || correlation.RootIntentionID != "root" || correlation.ParentIntentionID != "parent" {
		t.Fatalf("scoped security fields = identity %#v correlation %#v", identity, correlation)
	}
	runtime := &Runtime{outbound: base}
	params := newParams(nil, runtime, origin, scoped.delegation)
	if got := params.Outbound(); got == nil || got.delegation != scoped.delegation || got.origin != origin {
		t.Fatal("Params.Outbound did not preserve the exact delegated call scope")
	}
}
