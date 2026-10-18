/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package wrapper

import (
	"encoding/json"
	"fmt"
	"strings"
)

type Router struct{ runtime *Runtime }

func NewRouter(runtime *Runtime) *Router { return &Router{runtime: runtime} }

// Route keeps responses and stop control outside the bounded work scheduler.
func (r *Router) Route(raw []byte) {
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
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
	if !r.runtime.scheduler.Submit(key, func() { r.handleIntention(msg) }) {
		r.runtime.log("event=scheduler_overloaded queued=%d key=%s", r.runtime.scheduler.Queued(), key)
		if msg.AwaitResponse {
			r.sendFailure(msg, newPublicError("refused", "wrapper pending-work queue is full", "busy"))
		}
	}
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

func (r *Router) handleIntention(msg *IntentionMsg) {
	switch msg.To.Type {
	case TypeUser:
		params := make(map[string]any, len(msg.Params)+1)
		for key, value := range msg.Params {
			params[key] = value
		}
		matterParams := make(map[string]any)
		for _, matter := range msg.Matters {
			repr, _ := matter["repr"].(string)
			if repr == "" {
				continue
			}
			matterParams[repr] = map[string]any{
				"context": matter["context"], "id": matter["id"], "mode": matter["mode"],
			}
		}
		if len(matterParams) > 0 {
			params["@matter"] = matterParams
		}
		result, err := r.runtime.executor.Execute(r.relativeContext(msg.To.Context), msg.To.Cap, params, msg)
		r.sendResponse(msg, result, err)
	case TypeMatter:
		r.handleMatter(msg)
	default:
		r.sendFailure(msg, newPublicError("invalid", "unsupported wrapper intention", "unsupported_type"))
	}
}

func (r *Router) handleMatter(msg *IntentionMsg) {
	rel := r.relativeContext(msg.To.Context)
	matterID := stringParam(msg.Params, "matter_id")
	switch msg.To.Cap {
	case CapMatterRead:
		result, err := r.runtime.matters.Read(rel, matterID)
		r.sendResponse(msg, result, err)
	case CapMatterWrite:
		value, err := extractWriteValue(msg.Params)
		if err != nil {
			r.sendResponse(msg, nil, err)
			return
		}
		result, err := r.runtime.matters.Write(rel, matterID, value)
		r.sendResponse(msg, result, err)
	case CapMatterSubscribe:
		r.subscribe(msg, rel, matterID)
	case CapMatterUnsubscribe:
		r.unsubscribe(msg, matterID)
	default:
		r.sendFailure(msg, newPublicError("invalid", "unsupported wrapper matter operation", "unsupported_matter_cap"))
	}
}

func (r *Router) subscribe(msg *IntentionMsg, rel, matterID string) {
	id := stringParam(msg.Params, "sub_id")
	if id == "" {
		id = newID()
	}
	target := msg.From
	if raw, ok := msg.Params["target"].(map[string]any); ok {
		target = Address{Context: stringParam(raw, "context"), Cap: stringParam(raw, "cap"), Type: stringParam(raw, "type")}
	}
	if matterID == "" || target.Context == "" || target.Cap == "" || target.Type == "" {
		r.sendFailure(msg, newPublicError("invalid", "matter_id and a complete notification target are required", "invalid_subscription"))
		return
	}
	sub := &Subscription{ID: id, MatterID: matterID, Context: rel, ToContext: target.Context, ToCap: target.Cap, ToType: target.Type, Owner: msg.From}
	if err := r.runtime.matters.AddSubscription(sub); err != nil {
		r.sendFailure(msg, err)
		return
	}
	r.sendResponse(msg, map[string]any{"ok": true, "matter_id": matterID, "sub_id": id, "substance_mode": ModeWrapper}, nil)
}

func (r *Router) unsubscribe(msg *IntentionMsg, matterID string) {
	id := stringParam(msg.Params, "sub_id")
	if matterID == "" || id == "" {
		r.sendFailure(msg, newPublicError("invalid", "matter_id and sub_id are required", "invalid_subscription"))
		return
	}
	removed, err := r.runtime.matters.RemoveSubscription(id, msg.From)
	if err != nil {
		r.sendFailure(msg, err)
		return
	}
	r.sendResponse(msg, map[string]any{"ok": true, "matter_id": matterID, "sub_id": id, "removed": removed, "substance_mode": ModeWrapper}, nil)
}

func (r *Router) sendResponse(msg *IntentionMsg, payload map[string]any, err error) {
	if !msg.AwaitResponse {
		return
	}
	if err != nil {
		r.sendFailure(msg, err)
		return
	}
	resp := &ResponseMsg{
		IntentionID: msg.IntentionID,
		To:          msg.From,
		From:        Address{Context: r.responseFromContext(msg.To.Context), Cap: msg.To.Cap, Type: msg.To.Type},
		Identity:    map[string]any{"id": r.runtime.wrapperName, "kind": "wrapper"},
		Status:      StatusOK,
		Payload:     payload,
	}
	if err := r.runtime.transport.Send(Envelope{Kind: KindResponse, Ts: utcNow(), Response: resp}); err != nil {
		r.runtime.log("event=response_send_failed error=%v", err)
	}
}

func (r *Router) sendFailure(msg *IntentionMsg, err error) {
	if !msg.AwaitResponse {
		return
	}
	public, ok := err.(*PublicError)
	if !ok {
		errorID := newID()
		r.runtime.log("event=work_failed error_id=%s error=%v", errorID, err)
		public = newPublicError("internal", "wrapper operation failed", "wrapper_operation_failed")
		resp := r.errorResponse(msg, public)
		resp.Error.Details["error_id"] = errorID
		_ = r.runtime.transport.Send(Envelope{Kind: KindResponse, Ts: utcNow(), Response: resp})
		return
	}
	if sendErr := r.runtime.transport.Send(Envelope{Kind: KindResponse, Ts: utcNow(), Response: r.errorResponse(msg, public)}); sendErr != nil {
		r.runtime.log("event=response_send_failed error=%v", sendErr)
	}
}

func (r *Router) errorResponse(msg *IntentionMsg, err *PublicError) *ResponseMsg {
	return &ResponseMsg{
		IntentionID: msg.IntentionID,
		To:          msg.From,
		From:        Address{Context: r.responseFromContext(msg.To.Context), Cap: msg.To.Cap, Type: msg.To.Type},
		Identity:    map[string]any{"id": r.runtime.wrapperName, "kind": "wrapper"},
		Status:      StatusError,
		Error:       &ResponseError{Origin: "wrapper", Code: err.Code, Message: err.Message, Details: map[string]any{"reason": err.Reason}},
	}
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
