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
	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/shared"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
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
		// Wire destinations carry a leading slash; ext names are stored without.
		addr, ok = reg.ResolveExtName(strings.TrimPrefix(ext, "/"))
	}
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
//   - awaited intentions receive a correlated Communication error when the
//     destination is unavailable or invalid.
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

func (l *CommLoop) forwardToRoot(msg circulation.Message, source ...junction.Delivery) {
	l.forwardToContext(shared.ContextAddr(shared.RootContextID), msg, source...)
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
//   - emits one reject trace when the target cannot accept the message.
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
// - returns whether the destination queue accepted the message, was unavailable,
//   or communication stopped before acceptance.
//
//
// Contract:
// - A full destination queue applies blocking backpressure and never drops.
// - Unknown targets fail closed with a reject trace and an explicit result.
// - Nil `frame` or registry returns `deliveryInvalid` and is reported as a delivery failure.
// - Shutdown while waiting returns `deliveryStopped`.

func (l *CommLoop) forwardToContext(addr shared.ContextAddr, msg circulation.Message, source ...junction.Delivery) deliveryResult {
	target := string(addr)
	if l.frame == nil || l.frame.CtxCommReg == nil {
		result := newDeliveryResult(deliveryInvalid, target, fmt.Errorf("context registry unavailable"))
		l.reportDeliveryFailure(msg, result)
		return result
	}

	ch, ok := l.frame.CtxCommReg.ResolveCh(addr)
	if !ok || ch == nil {
		result := newDeliveryResult(deliveryUnavailable, target, fmt.Errorf("unknown context addr: %s", addr))
		l.reportDeliveryFailure(msg, result)
		return result
	}
	delivery, err := l.deliveryForMessage(msg, source...)
	if err != nil {
		result := newDeliveryResult(deliveryInvalid, target, err)
		l.reportDeliveryFailure(msg, result)
		return result
	}
	select {
	case ch <- delivery:
		return newDeliveryResult(deliveryAccepted, target, nil)
	case <-l.done:
		result := newDeliveryResult(deliveryStopped, target, fmt.Errorf("communication stopped before delivery to %s", addr))
		l.reportDeliveryFailure(msg, result)
		return result
	}
}

func (l *CommLoop) reportDeliveryFailure(msg circulation.Message, result deliveryResult) {
	if l == nil || result.accepted() {
		return
	}
	reason := circulation.ValueReasonContextUnreachable
	if result.Status == deliveryStopped {
		reason = circulation.ValueReasonContextStopped
	}
	detail := fmt.Sprintf("delivery failed: target=%s status=%s", result.Target, result.Status)
	if result.Err != nil {
		detail = result.Err.Error()
	}
	l.sendToTrace(msg, circulation.ValueTraceCommReject, reason, detail)
	if result.Status != deliveryStopped && msg.Kind == circulation.ValueKindIntention && msg.Intention.AwaitResponse {
		l.sendDeliveryFailureAck(&msg, result.Target, reason)
	}
}

// reportInterfaceEgressOutcome handles failures discovered after the local
// interface queue accepted a message. An explicit non-2xx peer response is a
// definitive rejection. A network error has an unknown delivery outcome and
// therefore cannot safely manufacture a terminal response.
func (l *CommLoop) reportInterfaceEgressOutcome(msg circulation.Message, outcome InterfaceEgressOutcome) {
	if l == nil || outcome.Err == nil {
		return
	}
	target := string(msgToContext(&msg))
	if target == "" {
		target = "@external"
	}
	if !outcome.Definitive {
		l.sendToTrace(msg, circulation.ValueTraceCommDrop, "delivery_outcome_unknown", outcome.Err.Error())
		return
	}
	l.reportDeliveryFailure(msg, newDeliveryResult(deliveryUnavailable, target, outcome.Err))
}

func (l *CommLoop) enqueueInterfaceEgressOutcome(msg circulation.Message, outcome InterfaceEgressOutcome) {
	if l == nil || l.interfaceOutcomes == nil {
		return
	}
	select {
	case l.interfaceOutcomes <- interfaceEgressReport{Message: msg, Outcome: outcome}:
	case <-l.done:
	}
}

func (l *CommLoop) loopInterfaceEgressOutcomes() {
	for {
		select {
		case <-l.done:
			return
		case report, ok := <-l.interfaceOutcomes:
			if !ok {
				return
			}
			l.reportInterfaceEgressOutcome(report.Message, report.Outcome)
		}
	}
}

// sendDeliveryFailureAck
//
// Functional role (Brique DSL):
// - reply immediately to an awaited intention whose local communication target
//   could not accept the message.
//
// Contract:
// - No-op if `req` is nil or not an intention.
// - Reuses the requester's own address as the reply's `From` context, since the failure is
//   local to routing and does not belong to any specific resolved context.

func (l *CommLoop) sendDeliveryFailureAck(req *circulation.Message, target string, reason string) {
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
				Context: circulation.ContextID(target),
				Type:    circulation.ValueTypeCommunication,
				Cap:     req.Intention.To.Cap,
			},
			Identity: req.Intention.Identity,
			Error: &circulation.ResponseProblem{
				Origin:  circulation.ValueOriginCommunication,
				Code:    circulation.ValueCodeUnavailable,
				Message: fmt.Sprintf("delivery target is unavailable: %s", target),
				Details: map[string]any{circulation.KeyReason: reason},
			},
		},
	}
	l.routeEgress(ack)
}

// sendInvalidMessageTypeAck answers an awaited intention that cannot be
// dispatched because its target family is missing. Without this response the
// caller would remain pending until its own timeout even though Communication
// has already rejected the request definitively.
func (l *CommLoop) sendInvalidMessageTypeAck(req *circulation.Message) {
	if req == nil || req.Kind != circulation.ValueKindIntention || !req.Intention.AwaitResponse {
		return
	}
	ctxID := ""
	if l != nil && l.frame != nil {
		ctxID = l.frame.CtxId
	}
	ack := circulation.Message{
		Kind: circulation.ValueKindResponse,
		Response: circulation.Response{
			IntentionID: req.Intention.IntentionID,
			Status:      circulation.ValueStatusError,
			To:          req.Intention.From,
			From: circulation.Address{
				Context: circulation.ContextID(ctxID),
				Type:    circulation.ValueTypeCommunication,
				Cap:     req.Intention.To.Cap,
			},
			Identity: req.Intention.Identity,
			Error: &circulation.ResponseProblem{
				Origin:  circulation.ValueOriginCommunication,
				Code:    circulation.ValueCodeInvalid,
				Message: "missing intention target type",
				Details: map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidMsgType},
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
//   - awaited intentions receive a correlated Communication error when the
//     interface is unavailable.
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
//   - emits one reject trace for unavailable, invalid, or stopped delivery.
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
// - returns the queue-acceptance result.
//
//
// Contract:
// - A full egress queue applies blocking backpressure; it is not a delivery failure.
// - Unknown interfaces and shutdown return explicit non-accepted results.
// - Failure reporting is centralized through `reportDeliveryFailure`.

func (l *CommLoop) sendToIface(name string, msg circulation.Message) deliveryResult {
	target := string(msgToContext(&msg))
	if target == "" {
		target = "@interface_" + name
	}
	rt, ok := l.ifaces[name]
	if !ok || rt == nil || rt.Egress == nil {
		result := newDeliveryResult(deliveryUnavailable, target, fmt.Errorf("unknown iface: %s", name))
		l.reportDeliveryFailure(msg, result)
		return result
	}
	state := rt.State()
	if state.Status != InterfaceRunning {
		err := fmt.Errorf("interface %s unavailable (status=%s)", name, state.Status)
		if state.LastErr != nil {
			err = fmt.Errorf("interface %s unavailable: %w", name, state.LastErr)
		}
		result := newDeliveryResult(deliveryUnavailable, target, err)
		l.reportDeliveryFailure(msg, result)
		return result
	}
	select {
	case rt.Egress <- msg:
		return newDeliveryResult(deliveryAccepted, target, nil)
	case <-l.done:
		result := newDeliveryResult(deliveryStopped, target, fmt.Errorf("communication stopped before delivery to iface %s", name))
		l.reportDeliveryFailure(msg, result)
		return result
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
	if !junction.TraceGateEnabled(l.frame, tw) {
		return
	}
	if traceKind == circulation.ValueTraceCommIngress || traceKind == circulation.ValueTraceCommEgress {
		attachTraceMessageProjection(&tw, msg)
	}
	junction.TraceEmit(l.frame, tw)
}

func attachTraceMessageProjection(tw *circulation.TraceWire, msg circulation.Message) {
	if tw == nil {
		return
	}
	b, err := json.Marshal(msg)
	if err == nil && len(b) <= shared.MaxInlineTraceMessageBytes {
		if msg.Kind == circulation.ValueKindIntention {
			in := msg.Intention
			tw.Intention = &in
		} else if msg.Kind == circulation.ValueKindResponse {
			resp := msg.Response
			tw.Response = &resp
		}
		return
	}

	tw.MessageTruncated = true
	if err == nil {
		tw.MessageBytes = int64(len(b))
		sum := sha256.Sum256(b)
		tw.MessageSHA256 = hex.EncodeToString(sum[:])
	}
	if msg.Kind == circulation.ValueKindIntention {
		in := msg.Intention
		tw.PayloadKeys = sortedMapKeys(in.Params)
		in.Params = nil
		if len(in.Matters) > 32 {
			in.Matters = in.Matters[:32]
		}
		if len(in.ElementRefs) > 32 {
			in.ElementRefs = in.ElementRefs[:32]
		}
		tw.Intention = &in
		return
	}
	if msg.Kind == circulation.ValueKindResponse {
		resp := msg.Response
		tw.PayloadKeys = sortedMapKeys(resp.Payload)
		resp.Payload = nil
		if resp.Error != nil {
			problem := *resp.Error
			problem.Details = nil
			resp.Error = &problem
		}
		tw.Response = &resp
	}
}

func sortedMapKeys(values map[string]any) []string {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// enforceControlMessageLimit keeps bulk data out of the intention plane. An
// oversized intention is replaced by a correlated communication error; an
// oversized response is reduced in place to that same small error envelope.
func (l *CommLoop) enforceControlMessageLimit(msg circulation.Message, source ...junction.Delivery) (circulation.Message, bool) {
	b, err := json.Marshal(msg)
	if err != nil || shared.ValidateControlMessageBytes(int64(len(b)), shared.DefaultControlMessageBytes) == nil {
		return msg, true
	}
	details := map[string]any{
		circulation.KeyReason: circulation.ValueReasonPayloadTooLarge,
		circulation.KeySize:   int64(len(b)),
		circulation.KeyLimit:  shared.DefaultControlMessageBytes,
	}
	problem := &circulation.ResponseProblem{
		Origin:  circulation.ValueOriginCommunication,
		Code:    circulation.ValueCodeRefused,
		Message: "control message too large; store large payloads in Matter substance",
		Details: details,
	}
	if msg.Kind == circulation.ValueKindResponse {
		msg.Response.Status = circulation.ValueStatusError
		msg.Response.Payload = nil
		msg.Response.Error = problem
		return msg, true
	}
	if msg.Kind == circulation.ValueKindIntention && msg.Intention.AwaitResponse && !hasExternalAuthority(source...) {
		ctxID := ""
		if l != nil && l.frame != nil {
			ctxID = l.frame.CtxId
		}
		ack := circulation.Message{
			Kind: circulation.ValueKindResponse,
			Response: circulation.Response{
				IntentionID: msg.Intention.IntentionID,
				To:          msg.Intention.From,
				From: circulation.Address{
					Context: circulation.ContextID(ctxID),
					Type:    circulation.ValueTypeCommunication,
					Cap:     msg.Intention.To.Cap,
				},
				Status: circulation.ValueStatusError,
				Error:  problem,
			},
		}
		l.routeEgress(ack)
	}
	return msg, false
}
