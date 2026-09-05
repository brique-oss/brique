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

package matter_test

import (
	"testing"
	"time"

	"brique_engine/circulation"
)

func TestEngine_N6_MAT_10_BriqueSubscribeWriteNotifyAndUnsubscribe(t *testing.T) {
	h := newEngineN6MatterHarness(t)

	subResp := callN6MatterCap(t, h, n6MatterWorkspaceID, "n6-mat-sub-brique", "matter.subscribe", map[string]any{
		circulation.KeyMatterID: "m_brique_inline",
	})
	if subResp.Status != circulation.ValueStatusOK {
		t.Fatalf("brique subscribe status=%q want ok payload=%#v error=%#v", subResp.Status, subResp.Payload, subResp.Error)
	}
	subPayload := mustPayloadMapMatter(t, subResp.Payload)
	subID, _ := subPayload[circulation.KeySubID].(string)
	if subID == "" {
		t.Fatalf("brique subscribe should return sub_id: %#v", subPayload)
	}

	writeResp := callN6MatterWrite(t, h, n6MatterWorkspaceID, "n6-mat-write-brique-event", map[string]any{
		circulation.KeyMatterID: "m_brique_inline",
		circulation.KeyFunctional: map[string]any{
			"event_marker": "brique-notify",
		},
	})
	if writeResp.Status != circulation.ValueStatusOK {
		t.Fatalf("brique write status=%q want ok payload=%#v error=%#v", writeResp.Status, writeResp.Payload, writeResp.Error)
	}
	ev := recvN6MatterMsg(t, h.sinkCh, "brique matter event")
	if ev.Kind != circulation.ValueKindIntention {
		t.Fatalf("brique notify kind=%q want intention", ev.Kind)
	}
	if ev.Intention.From.Cap != circulation.ValueCapMatterEvent || ev.Intention.From.Type != circulation.ValueTypeMatter {
		t.Fatalf("brique notify from mismatch: %#v", ev.Intention.From)
	}
	if ev.Intention.Params[circulation.KeyEvent] != circulation.ValueEventMatterWritten {
		t.Fatalf("brique notify event mismatch: %#v", ev.Intention.Params)
	}
	if ev.Intention.Params[circulation.KeyMatterID] != "m_brique_inline" {
		t.Fatalf("brique notify matter mismatch: %#v", ev.Intention.Params)
	}

	unsubResp := callN6MatterCap(t, h, n6MatterWorkspaceID, "n6-mat-unsub-brique", "matter.unsubscribe", map[string]any{
		circulation.KeyMatterID: "m_brique_inline",
		circulation.KeySubID:    subID,
	})
	if unsubResp.Status != circulation.ValueStatusOK {
		t.Fatalf("brique unsubscribe status=%q want ok payload=%#v error=%#v", unsubResp.Status, unsubResp.Payload, unsubResp.Error)
	}

	writeResp = callN6MatterWrite(t, h, n6MatterWorkspaceID, "n6-mat-write-brique-no-event", map[string]any{
		circulation.KeyMatterID: "m_brique_inline",
		circulation.KeyFunctional: map[string]any{
			"event_marker": "brique-no-notify",
		},
	})
	if writeResp.Status != circulation.ValueStatusOK {
		t.Fatalf("second brique write status=%q want ok payload=%#v error=%#v", writeResp.Status, writeResp.Payload, writeResp.Error)
	}
	noN6MatterMsg(t, h.sinkCh, "brique event after unsubscribe")
}

func TestEngine_N6_MAT_11_WrapperSubscribeTouchNotifyAndUnsubscribe(t *testing.T) {
	h := newEngineN6MatterHarness(t)

	bootstrapResp := callN6MatterUserCap(t, h, n6MatterWorkspaceID, "n6-mat-wrapper-bootstrap-event", "wrapper.ping", map[string]any{
		"message": "bootstrap-event",
	})
	if bootstrapResp.Status != circulation.ValueStatusOK {
		t.Fatalf("wrapper bootstrap status=%q want ok payload=%#v error=%#v", bootstrapResp.Status, bootstrapResp.Payload, bootstrapResp.Error)
	}

	subResp := callN6MatterCap(t, h, n6MatterWorkspaceID, "n6-mat-sub-wrapper", "matter.subscribe", map[string]any{
		circulation.KeyMatterID: "m_wrapper",
	})
	if subResp.Status != circulation.ValueStatusOK {
		t.Fatalf("wrapper subscribe status=%q want ok payload=%#v error=%#v", subResp.Status, subResp.Payload, subResp.Error)
	}
	subPayload := mustPayloadMapMatter(t, subResp.Payload)
	subID, _ := subPayload[circulation.KeySubID].(string)
	if subID == "" {
		t.Fatalf("wrapper subscribe should return delegated sub_id: %#v", subPayload)
	}

	sendToN6MatterContext(t, h, n6MatterWorkspaceID, mkN6MatterIntention(
		"n6-mat-wrapper-touch",
		n6MatterWorkspaceID,
		"wrapper.touch",
		circulation.ValueTypeUser,
		map[string]any{
			"value": "wrapper:event:update",
		},
	))
	var touchResp circulation.Response
	var ev circulation.Message
	waitForMatter(t, 5*time.Second, func() bool {
		msg := recvN6MatterMsg(t, h.sinkCh, "wrapper touch ack or event")
		if msg.Kind == circulation.ValueKindResponse && msg.Response.IntentionID == "n6-mat-wrapper-touch" {
			touchResp = msg.Response
		}
		if msg.Kind == circulation.ValueKindIntention && msg.Intention.Params[circulation.KeyEvent] == circulation.ValueEventMatterWritten {
			ev = msg
		}
		return touchResp.IntentionID == "n6-mat-wrapper-touch" && ev.Kind == circulation.ValueKindIntention
	}, "wrapper touch ack and event")
	if touchResp.Status != circulation.ValueStatusOK {
		t.Fatalf("wrapper touch status=%q want ok payload=%#v error=%#v", touchResp.Status, touchResp.Payload, touchResp.Error)
	}
	touchPayload := mustPayloadMapMatter(t, touchResp.Payload)
	if notified, _ := touchPayload["notified"].(float64); notified != 1 {
		t.Fatalf("wrapper touch should report one notification, payload=%#v", touchPayload)
	}
	if ev.Kind != circulation.ValueKindIntention {
		t.Fatalf("wrapper notify kind=%q want intention", ev.Kind)
	}
	if ev.Intention.Params[circulation.KeyEvent] != circulation.ValueEventMatterWritten {
		t.Fatalf("wrapper notify event mismatch: %#v", ev.Intention.Params)
	}
	if ev.Intention.Params[circulation.KeyMatterID] != "m_wrapper" {
		t.Fatalf("wrapper notify matter mismatch: %#v", ev.Intention.Params)
	}
	if ev.Intention.Params[circulation.KeySubstanceMode] != circulation.ValueModeWrapper {
		t.Fatalf("wrapper notify mode mismatch: %#v", ev.Intention.Params)
	}
	readResp := callN6MatterRead(t, h, n6MatterWorkspaceID, "n6-mat-read-wrapper-after-touch", map[string]any{
		circulation.KeyMatterID: "m_wrapper",
	})
	if readResp.Status != circulation.ValueStatusOK {
		t.Fatalf("wrapper read after touch status=%q want ok payload=%#v error=%#v", readResp.Status, readResp.Payload, readResp.Error)
	}
	readPayload := mustPayloadMapMatter(t, readResp.Payload)
	readData := mustPayloadMapMatter(t, readPayload[circulation.KeyData])
	if got := decodeInlineMatterBytes(t, readData); got != "wrapper:event:update" {
		t.Fatalf("wrapper read after touch payload=%q want wrapper:event:update", got)
	}

	unsubResp := callN6MatterCap(t, h, n6MatterWorkspaceID, "n6-mat-unsub-wrapper", "matter.unsubscribe", map[string]any{
		circulation.KeyMatterID: "m_wrapper",
		circulation.KeySubID:    subID,
	})
	if unsubResp.Status != circulation.ValueStatusOK {
		t.Fatalf("wrapper unsubscribe status=%q want ok payload=%#v error=%#v", unsubResp.Status, unsubResp.Payload, unsubResp.Error)
	}

	touchResp = callN6MatterUserCap(t, h, n6MatterWorkspaceID, "n6-mat-wrapper-touch-no-event", "wrapper.touch", map[string]any{
		"value": "wrapper:event:after-unsub",
	})
	if touchResp.Status != circulation.ValueStatusOK {
		t.Fatalf("wrapper touch after unsubscribe status=%q want ok payload=%#v error=%#v", touchResp.Status, touchResp.Payload, touchResp.Error)
	}
	touchPayload = mustPayloadMapMatter(t, touchResp.Payload)
	if notified, _ := touchPayload["notified"].(float64); notified != 0 {
		t.Fatalf("wrapper touch after unsubscribe should report zero notifications, payload=%#v", touchPayload)
	}
	noN6MatterMsg(t, h.sinkCh, "wrapper event after unsubscribe")
}
