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
	"fmt"
	"strings"
	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/shared"
	"time"
)

// msgType
//
// Functional role (Brique DSL):
// - extract logical destination family type from intention or response envelope.
//
//
// Expected Message Fields:
// - message fields consumed directly:
//   - `kind`
//   - `intention.to.type`
//   - `response.to.type`
//
// Expected Params Keys/values:
// - none (no `msg.Intention.Params[...]` key consumed directly in this function).
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - none.
//
// Inputs:
// - msg *circulation.Message.
//
//
// Outputs:
// - returns string.
//
//
// Contract:
// - Nil message or unsupported kind yields empty string.

func msgType(msg *circulation.Message) string {
	if msg == nil {
		return ""
	}
	switch msg.Kind {
	case circulation.ValueKindIntention:
		return msg.Intention.To.Type
	case circulation.ValueKindResponse:
		return msg.Response.To.Type
	default:
		return ""
	}
}

// msgToContext
//
// Functional role (Brique DSL):
// - extract destination context from intention or response envelope.
//
//
// Expected Message Fields:
// - message fields consumed directly:
//   - `kind`
//   - `intention.to.context`
//   - `response.to.context`
//
// Expected Params Keys/values:
// - none (no `msg.Intention.Params[...]` key consumed directly in this function).
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - none.
//
// Inputs:
// - msg *circulation.Message.
//
//
// Outputs:
// - returns `circulation.ContextID`.
//
//
// Contract:
// - Nil message or unsupported kind yields empty context id.

func msgToContext(msg *circulation.Message) circulation.ContextID {
	if msg == nil {
		return ""
	}
	switch msg.Kind {
	case circulation.ValueKindIntention:
		return msg.Intention.To.Context
	case circulation.ValueKindResponse:
		return msg.Response.To.Context
	default:
		return ""
	}
}

func msgFromContext(msg *circulation.Message) circulation.ContextID {
	if msg == nil {
		return ""
	}
	switch msg.Kind {
	case circulation.ValueKindIntention:
		return msg.Intention.From.Context
	case circulation.ValueKindResponse:
		return msg.Response.From.Context
	default:
		return ""
	}
}

// stampFromContext
//
// Functional role (Brique DSL):
// - stamp message source context with provided internal context id while preserving wrapper-relative suffixes.
//
//
// Expected Message Fields:
// - message fields consumed directly:
//   - `kind`
//   - `intention.from.context`
//   - `response.from.context`
//
// Expected Params Keys/values:
// - none (no `msg.Intention.Params[...]` key consumed directly in this function).
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - mutates `msg.Intention.From.Context` or `msg.Response.From.Context` in place.
//
// Inputs:
// - msg *circulation.Message, ctx circulation.ContextID.
//
//
// Outputs:
// - no direct return value; observable outputs are in-place message mutations.
//
//
// Contract:
// - Wrapper transport origins of the form `@wrapper_*:/rel` are rewritten to `<ctx>/rel`.
// - An empty origin is stamped with `ctx` (the wrapper's own boundary context).
// - Any other already-explicit internal origin (set by EndpointWrapper ingress
//   projection from a non-empty from_context supplied by the wrapper's own
//   code) is preserved as-is: a wrapper shared across several contexts relies
//   on this to route sub-intentions and their responses back to the actual
//   caller context, not unconditionally to the wrapper's boundary.

func stampFromContext(msg *circulation.Message, ctx circulation.ContextID) {
	if msg == nil {
		return
	}
	replaceWrapperFrom := func(cur circulation.ContextID) circulation.ContextID {
		s := string(cur)
		if s == "" {
			return ctx
		}
		if !strings.HasPrefix(s, "@wrapper_") {
			return cur
		}
		i := strings.Index(s, ":/")
		if i < 0 {
			return ctx
		}

		rel := s[i+2:]
		rel = strings.TrimPrefix(rel, "/")

		if rel == "" {
			return ctx
		}
		return circulation.ContextID(string(ctx) + "/" + rel)
	}
	switch msg.Kind {
	case circulation.ValueKindIntention:
		msg.Intention.From.Context = replaceWrapperFrom(msg.Intention.From.Context)
	case circulation.ValueKindResponse:
		msg.Response.From.Context = replaceWrapperFrom(msg.Response.From.Context)
	}
}

func stampFromUIContext(msg *circulation.Message, ifaceName string) {
	if msg == nil {
		return
	}
	base := circulation.ContextID("@ui_" + strings.TrimSpace(ifaceName))
	preservePath := func(cur circulation.ContextID) circulation.ContextID {
		s := strings.TrimSpace(string(cur))
		if !strings.HasPrefix(s, "@ui_") {
			return base
		}
		i := strings.Index(s, ":/")
		if i < 0 {
			return base
		}
		return circulation.ContextID(string(base) + s[i:])
	}
	switch msg.Kind {
	case circulation.ValueKindIntention:
		msg.Intention.From.Context = preservePath(msg.Intention.From.Context)
	case circulation.ValueKindResponse:
		msg.Response.From.Context = preservePath(msg.Response.From.Context)
	}
}

// canonicalInternalCtxID
//
// Functional role (Brique DSL):
// - canonicalize internal context identifiers into absolute `/...` form and leave `@...` addresses unchanged.
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none (no `msg.Intention.Params[...]` key consumed directly in this function).
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - none.
//
// Inputs:
// - s string.
//
//
// Outputs:
// - returns string.
//
//
// Contract:
// - Empty or blank input yields empty string.

func canonicalInternalCtxID(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// interface/external addressing is not an internal ctx id
	if s[0] == '@' {
		return s
	}
	// canonical internal absolute id
	if s[0] == '/' {
		return s
	}
	// accept "root/..." from older callers and canonicalize.
	return "/" + s
}

// resolveExtContext
//
// Functional role (Brique DSL):
// - resolve external/public context name to internal context id through registry and rewrite destination in place.
//
//
// Expected Message Fields:
// - message fields consumed directly:
//   - `kind`
//   - `intention.to.context`
//   - `response.to.context`
//
// Expected Params Keys/values:
// - none (no `msg.Intention.Params[...]` key consumed directly in this function).
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - mutates destination context in the provided message on successful resolution.
//
// Inputs:
// - receiver `l *CommLoop`.
// - msg *circulation.Message.
// - frame.CtxCommReg: communication registry.
//
//
// Outputs:
// - returns bool.
//
//
// Contract:
// - On success, destination context is rewritten from external name to internal context id.
// - Nil message, missing registry, unknown external name, and unsupported message kinds return `false` and leave the message unchanged.

func (l *CommLoop) resolveExtContext(msg *circulation.Message) bool {
	if l.frame == nil || l.frame.CtxCommReg == nil || msg == nil {
		return false
	}

	ext := string(msgToContext(msg))
	if ext == "" {
		return false
	}

	reg := l.frame.CtxCommReg
	if reg == nil {
		return false
	}

	addr, ok := reg.ResolveExtName(ext)
	if !ok {
		return false
	}

	switch msg.Kind {
	case circulation.ValueKindIntention:
		msg.Intention.To.Context = circulation.ContextID(string(addr))
	case circulation.ValueKindResponse:
		msg.Response.To.Context = circulation.ContextID(string(addr))
	default:
		return false
	}

	return true
}

// extractPublicKey
//
// Functional role (Brique DSL):
// - extract sender public key from intention or response identity envelope.
//
//
// Expected Message Fields:
// - message fields consumed directly:
//   - `kind`
//   - `intention.identity.pubkey`
//   - `response.identity.pubkey`
//
// Expected Params Keys/values:
// - none (no `msg.Intention.Params[...]` key consumed directly in this function).
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - none.
//
// Inputs:
// - receiver `l *CommLoop`.
// - msg *circulation.Message.
//
//
// Outputs:
// - returns string.
//
//
// Contract:
// - Nil message or unsupported kind yields empty string.

func extractPublicKey(msg *circulation.Message) string {
	if msg == nil {
		return ""
	}

	switch msg.Kind {
	case circulation.ValueKindIntention:
		return msg.Intention.Identity.PubKey
	case circulation.ValueKindResponse:
		return msg.Response.Identity.PubKey
	default:
		return ""
	}
}

// fromContext
//
// Functional role (Brique DSL):
// - extract source context from intention or response envelope.
//
//
// Expected Message Fields:
// - message fields consumed directly:
//   - `kind`
//   - `intention.from.context`
//   - `response.from.context`
//
// Expected Params Keys/values:
// - none (no `msg.Intention.Params[...]` key consumed directly in this function).
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - none.
//
// Inputs:
// - msg *circulation.Message.
//
//
// Outputs:
// - returns `circulation.ContextID`.
//
//
// Contract:
// - Nil message or unsupported kind yields empty context id.

func (l *CommLoop) fromContext(msg *circulation.Message) circulation.ContextID {
	if msg == nil {
		return ""
	}
	switch msg.Kind {
	case circulation.ValueKindIntention:
		return msg.Intention.From.Context
	case circulation.ValueKindResponse:
		return msg.Response.From.Context
	default:
		return ""
	}
}

// publicFromContext
//
// Functional role (Brique DSL):
// - compute public sender context for external egress by resolving internal source context through registry when possible.
//
//
// Expected Message Fields:
// - message fields consumed indirectly via `fromContext(msg)`:
//   - `kind`
//   - `intention.from.context`
//   - `response.from.context`
//
// Expected Params Keys/values:
// - none (no `msg.Intention.Params[...]` key consumed directly in this function).
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - none.
//
// Inputs:
// - receiver `l *CommLoop`.
// - msg *circulation.Message.
// - frame.CtxCommReg: communication registry.
//
//
// Outputs:
// - returns `shared.ContextAddr`.
//
//
// Contract:
// - Falls back to raw source context when no public-name mapping exists.

func (l *CommLoop) publicFromContext(msg *circulation.Message) shared.ContextAddr {
	if l.frame == nil {
		return ""
	}

	// We require a public name for any external egress.
	reg := l.frame.CtxCommReg
	if reg == nil {
		return ""
	}

	from := string(l.fromContext(msg))
	if from == "" {
		//external egress must always have an explicit emitter
		return ""
	}

	// Forward: must be able to resolve a public name for the internal from.context.
	if ext, ok := reg.ResolveIDToExtName(shared.ContextAddr(from)); ok && ext != "" {
		return shared.ContextAddr(ext)
	}

	return shared.ContextAddr(from)
}

// stampFromPublicContext
//
// Functional role (Brique DSL):
// - stamp outbound source context with public context identity.
//
//
// Expected Message Fields:
// - message fields consumed directly:
//   - `kind`
//   - `intention.from.context`
//   - `response.from.context`
//
// Expected Params Keys/values:
// - none (no `msg.Intention.Params[...]` key consumed directly in this function).
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - mutates `msg.Intention.From.Context` or `msg.Response.From.Context` in place.
//
// Inputs:
// - receiver `l *CommLoop`.
// - msg *circulation.Message, pub shared.ContextAddr.
//
//
// Outputs:
// - no direct return value; observable outputs are in-place message mutations.
//
//
// Contract:
// - Nil message is ignored.

func (l *CommLoop) stampFromPublicContext(msg *circulation.Message, pub shared.ContextAddr) {
	if msg == nil {
		return
	}
	switch msg.Kind {
	case circulation.ValueKindIntention:
		msg.Intention.From.Context = circulation.ContextID(string(pub))
	case circulation.ValueKindResponse:
		msg.Response.From.Context = circulation.ContextID(string(pub))
	}
}

// parseAtAddress
//
// Functional role (Brique DSL):
// - parse interface/external address grammar `@tag:/path`.
//
//
// Expected Message Fields:
// - none (function does not read message envelope fields directly).
//
// Expected Params Keys/values:
// - none (no `msg.Intention.Params[...]` key consumed directly in this function).
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - none.
//
// Inputs:
// - s string.
//
//
// Outputs:
// - returns (tag string, path string, ok bool).
//
//
// Contract:
// - Accepts both `@tag:/path` and bare `@tag`.
// - Missing tag yields `ok=false`.

func parseAtAddress(s string) (tag string, path string, ok bool) {
	// s must start with '@'
	if len(s) < 3 || s[0] != '@' {
		return "", "", false
	}
	// find the first occurrence of ":/"
	for i := 1; i < len(s)-1; i++ {
		if s[i] == ':' && s[i+1] == '/' {
			tag = s[1:i]
			path = s[i+2:]
			if tag == "" {
				return "", "", false
			}
			return tag, path, true
		}
	}
	tag = s[1:]
	if tag == "" {
		return "", "", false
	}
	return tag, "", true
}

// forwardToRoot
//
// Functional role (Brique DSL):
// - forward message to root context ingress channel through internal context forwarding primitive.
//
//
// Expected Message Fields:
// - message fields consumed indirectly via `forwardToContext(..., msg)`:
//   - `kind`
//   - `to.type`
//   - `intention.intentionid` / `response.intentionid`
//   - `intention.correlation.parent_intention_id`
//   - `intention.correlation.root_intention_id`
//
// Expected Params Keys/values:
// - none (no `msg.Intention.Params[...]` key consumed directly in this function).
//
// Produced Response Fields:
// - Valid:
//   - none directly from this function.
// - On error:
//   - none directly from this function.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none directly; delegated forwarding may emit reject traces through `forwardToContext`.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - forwards the message to the root context Comm ingress channel indirectly via `forwardToContext`.
// - On error:
//   - none.
//
// State/Storage Effects:
// - may enqueue the message on the root context comm ingress channel.
//
// Inputs:
// - receiver `l *CommLoop`.
// - msg circulation.Message.
//
//
// Outputs:
// - no direct return value; observable outputs are delegated forwarding side effects.
//
//
// Contract:
// - Convenience wrapper for root forwarding.

func (l *CommLoop) forwardToRoot(msg circulation.Message) {
	l.forwardToContext(shared.ContextAddr(shared.RootContextID), msg)
}

// forwardToContext
//
// Functional role (Brique DSL):
// - deliver message to internal context communication ingress via registry-resolved channel.
//
//
// Expected Message Fields:
// - message fields consumed indirectly via reject trace path:
//   - `kind`
//   - `to.type`
//   - `intention.intentionid` / `response.intentionid`
//   - `intention.correlation.parent_intention_id`
//   - `intention.correlation.root_intention_id`
//
// Expected Params Keys/values:
// - none (no `msg.Intention.Params[...]` key consumed directly in this function).
//
// Produced Response Fields:
// - Valid:
//   - none directly from this function.
// - On error:
//   - none directly from this function.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - emits one reject trace when the target context cannot be resolved.
//
// Produced Outbound Message:
// - Valid:
//   - forwards the message to the resolved internal context Comm ingress channel.
// - On error:
//   - emits a reject trace instead of forwarding when the target context is unknown.
//
// State/Storage Effects:
// - may block until the resolved context channel accepts `msg` or `l.done` is closed.
// - may enqueue the message on an internal context comm ingress channel.
//
// Inputs:
// - receiver `l *CommLoop`.
// - addr shared.ContextAddr, msg circulation.Message.
//
//
// Outputs:
// - no direct return value; observable outputs are channel send or reject/trace side effects.
//
//
// Contract:
// - Unknown target context fails closed with reject trace.
// - Nil `frame` or nil `frame.CtxCommReg` produces a silent no-op.
// - Shutdown while waiting to send aborts forwarding without emitting an additional trace.

func (l *CommLoop) forwardToContext(addr shared.ContextAddr, msg circulation.Message) {
	if l.frame == nil || l.frame.CtxCommReg == nil {
		return
	}

	ch, ok := l.frame.CtxCommReg.ResolveCh(addr)
	if !ok || ch == nil {
		l.sendToTrace(msg, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidToContextName, fmt.Sprintf("unknown context addr: %s", addr))
		if msg.Kind == circulation.ValueKindIntention && msg.Intention.AwaitResponse {
			l.sendContextUnreachableAck(&msg, addr)
		}
		return
	}
	select {
	case ch <- msg:
		return
	case <-l.done:
		return
	}
}

// sendContextUnreachableAck
//
// Functional role (Brique DSL):
// - reply immediately to an awaited intention whose target context could not be resolved
//   (never created, or created on disk but never started), instead of letting the caller
//   block until its generic timeout.
//
// Contract:
// - No-op if `req` is nil or not an intention.
// - Reuses the requester's own address as the reply's `From` context, since the failure is
//   local to routing and does not belong to any specific resolved context.

func (l *CommLoop) sendContextUnreachableAck(req *circulation.Message, addr shared.ContextAddr) {
	if req == nil || req.Kind != circulation.ValueKindIntention {
		return
	}
	ack := circulation.Message{
		Kind: circulation.ValueKindResponse,
		Response: circulation.Response{
			IntentionID: req.Intention.IntentionID,
			Status:      "error",
			To:          req.Intention.From,
			From: circulation.Address{
				Context: circulation.ContextID(addr),
				Type:    circulation.ValueTypeCommunication,
				Cap:     req.Intention.To.Cap,
			},
			Identity: req.Intention.Identity,
			Error: &circulation.ResponseProblem{
				Origin:  circulation.ValueOriginCommunication,
				Code:    circulation.ValueCodeUnavailable,
				Message: fmt.Sprintf("target context is unreachable: %s (never created, or created but not started)", addr),
				Details: map[string]any{circulation.KeyReason: circulation.ValueReasonContextUnreachable},
			},
		},
	}
	l.routeEgress(ack)
}

// sendToIface
//
// Functional role (Brique DSL):
// - relay message to named interface egress channel.
//
//
// Expected Message Fields:
// - none (function does not read message envelope fields directly).
//
// Expected Params Keys/values:
// - none (no `msg.Intention.Params[...]` key consumed directly in this function).
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - forwards the message to the named interface egress channel.
// - On error:
//   - none.
//
// State/Storage Effects:
// - may block until the interface egress channel accepts `msg` or `l.done` is closed.
// - may enqueue the message on an interface egress channel.
//
// Inputs:
// - receiver `l *CommLoop`.
// - name string, msg circulation.Message.
//
//
// Outputs:
// - returns error.
//
//
// Contract:
// - Unknown interface returns error; loop shutdown returns nil without sending.
// - Does not emit traces on send failure paths; tracing is the caller's responsibility.

func (l *CommLoop) sendToIface(name string, msg circulation.Message) error {
	rt, ok := l.ifaces[name]
	if !ok || rt == nil || rt.Egress == nil {
		return fmt.Errorf("unknown iface: %s", name)
	}
	select {
	case rt.Egress <- msg:
		return nil
	case <-l.done:
		return nil
	}
}

// sendToTrace
//
// Functional role (Brique DSL):
// - emit communication trace event with optional message projection while suppressing reflexive-family traces.
//
//
// Expected Message Fields:
// - message fields consumed directly:
//   - `kind`
//   - `intention.intentionid`
//   - `intention.correlation.parent_intention_id`
//   - `intention.correlation.root_intention_id`
//   - `intention.to.type`
//   - `response.intentionid`
//   - `response.to.type`
//
// Expected Params Keys/values:
// - none (no `msg.Intention.Params[...]` key consumed directly in this function).
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - emits one communication trace event of the requested kind.
//   - attaches projected intention/response payload only for `CommIngress` and `CommEgress`.
//   - includes routing/correlation context when available from the input message.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - emits one trace message through junction tracing for all traffic including reflexive-bound.
// - On error:
//   - none.
//
// State/Storage Effects:
// - emits one trace event unless suppressed for reflexive-family traffic.
//
// Inputs:
// - receiver `l *CommLoop`.
// - msg circulation.Message, traceKind string, reason string, userText string.
//
//
// Outputs:
// - no direct return value; observable outputs are delegated trace emission side effects.
//
//
// Contract:
// - Full intention/response payload is attached only for `CommIngress` and `CommEgress` trace kinds.

func (l *CommLoop) sendToTrace(msg circulation.Message, traceKind string, reason string, userText string) {
	tw := circulation.TraceWire{
		Timestamp:  time.Now().UTC().Format(time.RFC3339Nano),
		TraceKind:  traceKind,
		Family:     circulation.ValueOriginCommunication,
		ReasonCode: reason,
		UserText:   userText,
	}
	if msg.Kind == "" {
		junction.TraceEmit(l.frame, tw)
		return
	}
	tw.MsgKind = msg.Kind
	if msg.Kind == circulation.ValueKindIntention {
		tw.IntentionId = msg.Intention.IntentionID
		if msg.Intention.Correlation != nil {
			tw.ParentIntentionId = msg.Intention.Correlation.ParentIntentionID
			tw.RootIntentionId = msg.Intention.Correlation.RootIntentionID
		}
	} else {
		tw.IntentionId = msg.Response.IntentionID
	}
	if traceKind == circulation.ValueTraceCommIngress || traceKind == circulation.ValueTraceCommEgress {
		if msg.Kind == circulation.ValueKindIntention {
			in := msg.Intention
			tw.Intention = &in
		} else {
			resp := msg.Response
			tw.Response = &resp
		}
	}
	junction.TraceEmit(l.frame, tw)
}
