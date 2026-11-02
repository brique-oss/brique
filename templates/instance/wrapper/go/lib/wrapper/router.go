/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package wrapper

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type Router struct{ runtime *Runtime }

func NewRouter(runtime *Runtime) *Router { return &Router{runtime: runtime} }

// Route keeps responses and stop control outside the bounded work scheduler.
func (r *Router) Route(raw []byte) {
	env, delegation, err := decodeWrapperFrame(raw)
	if err != nil {
		r.runtime.log("event=incoming_message_rejected reason=invalid_json error=%v", err)
		return
	}
	if env.Kind == KindResponse && env.Response != nil {
		if !r.runtime.correlator.Resolve(env.Response) {
			r.runtime.log("event=orphan_response intention_id=%s", env.Response.IntentionID)
		}
		return
	}
	if env.Kind != KindIntention || env.Intention == nil {
		r.runtime.log("event=incoming_message_rejected reason=invalid_envelope")
		return
	}
	msg := env.Intention
	if msg.To.Type == TypeExecution && msg.To.Cap == CapWrapperStop {
		r.runtime.requestStop()
		return
	}
	key := r.workKey(msg)
	work := func() { r.handleIntention(msg, delegation) }
	if !r.runtime.scheduler.SubmitFor(msg, key, r.ordered(msg), work) {
		r.runtime.log("event=scheduler_overloaded queued=%d key=%s", r.runtime.scheduler.Queued(), key)
		if msg.AwaitResponse {
			r.sendFailure(msg, delegation, newPublicError("refused", "wrapper pending-work queue is full", "busy"))
		}
	}
}

func decodeWrapperFrame(raw []byte) (Envelope, string, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return Envelope{}, "", err
	}
	if _, framed := probe["brique_wrapper_frame"]; !framed {
		var env Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			return Envelope{}, "", err
		}
		return env, "", nil
	}
	var frame WrapperFrame
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&frame); err != nil {
		return Envelope{}, "", err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Envelope{}, "", fmt.Errorf("wrapper frame has trailing JSON")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(frame.Delegation)
	if err != nil || len(decoded) != 32 || frame.Format != WrapperFrameFormat || (frame.Message.Kind != KindIntention && frame.Message.Kind != KindResponse) {
		return Envelope{}, "", fmt.Errorf("invalid wrapper frame")
	}
	return frame.Message, frame.Delegation, nil
}

// ordered says whether the work of a message must wait for the earlier work of its key. Matters
// are ordered per matter. A capacity runs in parallel unless it is declared Serial.
func (r *Router) ordered(msg *IntentionMsg) bool {
	if msg.To.Type != TypeUser {
		return true
	}
	binding, err := r.runtime.registry.ResolveCapacity(r.relativeContext(msg.To.Context), msg.To.Cap)
	return err == nil && binding.Serial
}

func (r *Router) workKey(msg *IntentionMsg) string {
	rel := r.relativeContext(msg.To.Context)
	name := msg.To.Cap
	if msg.To.Type == TypeMatter {
		if msg.To.Cap == CapMatterSubscribe || msg.To.Cap == CapMatterUnsubscribe {
			name = "subscription:" + stringParam(msg.Params, "sub_id")
		} else {
			name = stringParam(msg.Params, "matter_id")
		}
	}
	return msg.To.Type + ":" + rel + ":" + name
}

// incomingMatterParams turns intention.matters into the "@matter" parameter map,
// keyed by repr (or by id when repr is missing). An entry without context or id
// is not a reference and is ignored. Every wrapper language applies this rule.
func incomingMatterParams(matters []map[string]any) map[string]any {
	out := make(map[string]any)
	for _, matter := range matters {
		ref, valid := newMatterRef(matter["context"], matter["id"], matter["mode"])
		if !valid {
			continue
		}
		repr, _ := matter["repr"].(string)
		if repr = strings.TrimSpace(repr); repr == "" {
			repr = ref.ID
		}
		entry := map[string]any{"context": ref.Context, "id": ref.ID}
		if ref.Mode != "" {
			entry["mode"] = ref.Mode
		}
		out[repr] = entry
	}
	return out
}

func (r *Router) handleIntention(msg *IntentionMsg, delegation string) {
	switch msg.To.Type {
	case TypeUser:
		params := make(map[string]any, len(msg.Params)+1)
		for key, value := range msg.Params {
			params[key] = value
		}
		matterParams := incomingMatterParams(msg.Matters)
		if len(matterParams) > 0 {
			params["@matter"] = matterParams
		}
		result, err := r.runtime.executor.Execute(r.relativeContext(msg.To.Context), msg.To.Cap, params, msg, delegation)
		r.sendResponse(msg, delegation, result, err)
	case TypeMatter:
		r.handleMatter(msg, delegation)
	default:
		r.sendFailure(msg, delegation, newPublicError("invalid", "unsupported wrapper intention", "unsupported_type"))
	}
}

func (r *Router) handleMatter(msg *IntentionMsg, delegation string) {
	rel := r.relativeContext(msg.To.Context)
	matterID := stringParam(msg.Params, "matter_id")
	switch msg.To.Cap {
	case CapMatterRead:
		result, err := r.runtime.matters.Read(rel, matterID)
		r.sendResponse(msg, delegation, result, err)
	case CapMatterWrite:
		value, err := extractWriteValue(msg.Params)
		if err != nil {
			r.sendResponse(msg, delegation, nil, err)
			return
		}
		result, err := r.runtime.matters.Write(rel, matterID, value, r.runtime.outbound.scoped(msg, delegation))
		r.sendResponse(msg, delegation, result, err)
	case CapMatterSubscribe:
		r.subscribe(msg, delegation, rel, matterID)
	case CapMatterUnsubscribe:
		r.unsubscribe(msg, delegation, matterID)
	default:
		r.sendFailure(msg, delegation, newPublicError("invalid", "unsupported wrapper matter operation", "unsupported_matter_cap"))
	}
}

func (r *Router) subscribe(msg *IntentionMsg, delegation, rel, matterID string) {
	id := stringParam(msg.Params, "sub_id")
	if id == "" {
		id = newID()
	}
	target := msg.From
	if raw, ok := msg.Params["target"].(map[string]any); ok {
		target = Address{Context: stringParam(raw, "context"), Cap: stringParam(raw, "cap"), Type: stringParam(raw, "type")}
	}
	if matterID == "" || target.Context == "" || target.Cap == "" || target.Type == "" {
		r.sendFailure(msg, delegation, newPublicError("invalid", "matter_id and a complete notification target are required", "invalid_subscription"))
		return
	}
	sub := &Subscription{ID: id, MatterID: matterID, Context: rel, ToContext: target.Context, ToCap: target.Cap, ToType: target.Type, Owner: msg.From}
	if err := r.runtime.matters.AddSubscription(sub); err != nil {
		r.sendFailure(msg, delegation, err)
		return
	}
	r.sendResponse(msg, delegation, map[string]any{"ok": true, "matter_id": matterID, "sub_id": id, "substance_mode": ModeWrapper}, nil)
}

func (r *Router) unsubscribe(msg *IntentionMsg, delegation, matterID string) {
	id := stringParam(msg.Params, "sub_id")
	if matterID == "" || id == "" {
		r.sendFailure(msg, delegation, newPublicError("invalid", "matter_id and sub_id are required", "invalid_subscription"))
		return
	}
	removed, err := r.runtime.matters.RemoveSubscription(id, msg.From)
	if err != nil {
		r.sendFailure(msg, delegation, err)
		return
	}
	r.sendResponse(msg, delegation, map[string]any{"ok": true, "matter_id": matterID, "sub_id": id, "removed": removed, "substance_mode": ModeWrapper}, nil)
}

func (r *Router) sendResponse(msg *IntentionMsg, delegation string, payload map[string]any, err error) {
	if !msg.AwaitResponse {
		return
	}
	if err != nil {
		r.sendFailure(msg, delegation, err)
		return
	}
	resp := &ResponseMsg{
		IntentionID: msg.IntentionID,
		To:          msg.From,
		From:        Address{Context: r.responseFromContext(msg.To.Context), Cap: msg.To.Cap, Type: msg.To.Type},
		Identity:    responseIdentity(r.runtime.wrapperName, delegation),
		Status:      StatusOK,
		Payload:     payload,
	}
	if err := r.runtime.transport.SendWithDelegation(Envelope{Kind: KindResponse, Ts: utcNow(), Response: resp}, delegation); err != nil {
		r.runtime.log("event=response_send_failed error=%v", err)
	}
}

func (r *Router) sendFailure(msg *IntentionMsg, delegation string, err error) {
	if !msg.AwaitResponse {
		return
	}
	public, ok := err.(*PublicError)
	if !ok {
		errorID := newID()
		r.runtime.log("event=work_failed error_id=%s error=%v", errorID, err)
		public = newPublicError("internal", "wrapper operation failed", "wrapper_operation_failed")
		resp := r.errorResponse(msg, delegation, public)
		resp.Error.Details["error_id"] = errorID
		_ = r.runtime.transport.SendWithDelegation(Envelope{Kind: KindResponse, Ts: utcNow(), Response: resp}, delegation)
		return
	}
	if sendErr := r.runtime.transport.SendWithDelegation(Envelope{Kind: KindResponse, Ts: utcNow(), Response: r.errorResponse(msg, delegation, public)}, delegation); sendErr != nil {
		r.runtime.log("event=response_send_failed error=%v", sendErr)
	}
}

func (r *Router) errorResponse(msg *IntentionMsg, delegation string, err *PublicError) *ResponseMsg {
	return &ResponseMsg{
		IntentionID: msg.IntentionID,
		To:          msg.From,
		From:        Address{Context: r.responseFromContext(msg.To.Context), Cap: msg.To.Cap, Type: msg.To.Type},
		Identity:    responseIdentity(r.runtime.wrapperName, delegation),
		Status:      StatusError,
		Error:       &ResponseError{Origin: "wrapper", Code: err.Code, Message: err.Message, Details: map[string]any{"reason": err.Reason}},
	}
}

func responseIdentity(wrapperName, delegation string) map[string]any {
	if delegation != "" {
		return map[string]any{}
	}
	return map[string]any{"id": wrapperName, "kind": "wrapper"}
}

func (r *Router) relativeContext(raw string) string {
	prefix := fmt.Sprintf("@wrapper_%s:/", r.runtime.wrapperName)
	if strings.HasPrefix(raw, prefix) {
		return normalizeContext(strings.TrimPrefix(raw, prefix))
	}
	return normalizeContext(raw)
}

func (r *Router) responseFromContext(raw string) string {
	prefix := fmt.Sprintf("@wrapper_%s:/", r.runtime.wrapperName)
	if strings.HasPrefix(raw, prefix) {
		return normalizeContext(strings.TrimPrefix(raw, prefix))
	}
	return ""
}

func stringParam(params map[string]any, key string) string {
	value, _ := params[key].(string)
	return strings.TrimSpace(value)
}
