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

package wrapper

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Router receives raw messages from the transport and dispatches them.
type Router struct {
	runtime *Runtime
}

func NewRouter(runtime *Runtime) *Router {
	return &Router{runtime: runtime}
}

func (r *Router) Route(raw []byte) {
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		r.runtime.log("router: malformed message: %v", err)
		return
	}
	switch env.Kind {
	case KindIntention:
		if env.Intention != nil {
			go r.handleIntention(env.Intention)
		}
	case KindResponse:
		if env.Response != nil {
			r.runtime.correlator.Resolve(env.Response)
		}
	default:
		r.runtime.log("router: unknown kind %q", env.Kind)
	}
}

func (r *Router) handleIntention(msg *IntentionMsg) {
	cap := msg.To.Cap
	typ := msg.To.Type

	// Control
	if typ == TypeControl || cap == CapWrapperStop {
		r.runtime.requestStop()
		return
	}

	// Matter read
	if typ == TypeMatter && strings.HasSuffix(cap, ".read") {
		relCtx, matterName := r.splitMatterCap(cap, msg)
		result, err := r.runtime.matters.Read(relCtx, matterName)
		r.sendResponse(msg, result, err)
		return
	}

	// Matter write
	if typ == TypeMatter && strings.HasSuffix(cap, ".write") {
		relCtx, matterName := r.splitMatterCap(cap, msg)
		value, _ := extractWriteValue(msg.Params)
		result, err := r.runtime.matters.Write(relCtx, matterName, value)
		r.sendResponse(msg, result, err)
		return
	}

	// Matter subscribe
	if typ == TypeMatter && strings.HasSuffix(cap, ".subscribe") {
		sub := r.buildSubscription(msg)
		r.runtime.matters.AddSubscription(sub)
		r.sendResponse(msg, map[string]any{"ok": true, "subscription_id": sub.ID}, nil)
		return
	}

	// Matter unsubscribe
	if typ == TypeMatter && strings.HasSuffix(cap, ".unsubscribe") {
		id, _ := msg.Params["subscription_id"].(string)
		r.runtime.matters.RemoveSubscription(id)
		r.sendResponse(msg, map[string]any{"ok": true}, nil)
		return
	}

	// Capacity execution
	if typ == TypeExecution {
		relCtx := r.relativeContext(msg.To.Context)
		result, err := r.runtime.executor.Execute(relCtx, cap, msg.Params)
		r.sendResponse(msg, result, err)
		return
	}

	r.sendError(msg, fmt.Sprintf("unhandled intention type=%q cap=%q", typ, cap))
}

func (r *Router) sendResponse(msg *IntentionMsg, payload map[string]any, err error) {
	if !msg.AwaitResponse {
		return
	}
	env := Envelope{Kind: KindResponse, Ts: utcNow()}
	if err != nil {
		env.Response = &ResponseMsg{IntentionID: msg.IntentionID, Ok: false, Error: err.Error()}
	} else {
		env.Response = &ResponseMsg{IntentionID: msg.IntentionID, Ok: true, Payload: payload}
	}
	if err := r.runtime.transport.Send(env); err != nil {
		r.runtime.log("router: send response error: %v", err)
	}
}

func (r *Router) sendError(msg *IntentionMsg, reason string) {
	r.sendResponse(msg, nil, fmt.Errorf("%s", reason))
}

// relativeContext strips the wrapper address prefix from a context path.
func (r *Router) relativeContext(raw string) string {
	prefix := fmt.Sprintf("@wrapper_%s:/", r.runtime.wrapperName)
	if strings.HasPrefix(raw, prefix) {
		return strings.TrimPrefix(raw, prefix)
	}
	return strings.Trim(raw, "/")
}

func (r *Router) splitMatterCap(cap string, msg *IntentionMsg) (relCtx, name string) {
	relCtx = r.relativeContext(msg.To.Context)
	// cap is of the form "<matter_name>.read" / "<matter_name>.write"
	parts := strings.SplitN(cap, ".", 2)
	if len(parts) == 2 {
		name = parts[0]
	} else {
		name = cap
	}
	// Prefer explicit matter_id param
	if id, ok := msg.Params["matter_id"].(string); ok && id != "" {
		name = id
	}
	return
}

func (r *Router) buildSubscription(msg *IntentionMsg) *Subscription {
	matterID, _ := msg.Params["matter_id"].(string)
	relCtx := r.relativeContext(msg.To.Context)
	return &Subscription{
		ID:        newID(),
		MatterID:  matterID,
		Context:   relCtx,
		ToContext: msg.From.Context,
		ToCap:     msg.From.Cap,
		ToType:    msg.From.Type,
	}
}
