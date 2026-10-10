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
	"brique_engine/shared"
	"fmt"
	"strings"
)

// routeEgress
//
// Functional role (Brique DSL):
// - >if: current context is root
//   - call: routeEgressRoot
// - >else:
//   - call: routeEgressLocal
// - always:
//   - send trace event "CommEgress" before routing decision
//
//
// Expected Message Fields:
// - message fields consumed indirectly via `sendToTrace` / `routeEgressRoot` / `routeEgressLocal`:
//   - `kind`
//   - `to.context`
//   - `from.context`
//   - destination type envelope (via `msgType(&msg)` on wrapper guard branch)
//
// Expected Params Keys/values:
// - none (no `msg.Intention.Params[...]` key consumed directly in this function).
//
// Produced Response Fields:
// - Valid:
//   - response fields may be forwarded indirectly by the selected egress branch when `msg.Kind == circulation.ValueKindResponse`.
// - On error:
//   - none directly from this function.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none directly from this function.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - one comm-egress trace is emitted before routing.
//   - delegated branches may emit additional drop traces when validation fails downstream.
// - On error:
//   - delegated branches may emit drop traces.
//
// Produced Outbound Message:
// - Valid:
//   - delegated branches may forward the outbound message to contexts, root boundary, or interfaces.
// - On error:
//   - delegated branches may emit trace messages describing dropped outbound traffic.
//
// State/Storage Effects:
// - emits one egress trace.
// - delegates to root or local egress routing.
//
// Inputs:
// - receiver `l *CommLoop`.
// - msg circulation.Message.
// - frame.CtxId: runtime context identity used for root/local branch.
//
//
// Outputs:
// - no direct return value; observable outputs are side effects of selected egress branch and trace emission.
//
//
// Contract:
// - Entry point for outbound routing.
// - Does not mutate payload semantics; only selects routing path.

func (l *CommLoop) routeEgress(msg circulation.Message) {
	var allowed bool
	msg, allowed = l.enforceControlMessageLimit(msg)
	if !allowed {
		l.sendToTrace(msg, circulation.ValueTraceCommReject, circulation.ValueReasonPayloadTooLarge, "control message too large; use Matter substance")
		return
	}
	l.sendToTrace(msg, circulation.ValueTraceCommEgress, "", "")
	if l.frame != nil && shared.IsRootContext(l.frame.CtxId) {
		l.routeEgressRoot(msg)
		return
	}
	l.routeEgressLocal(msg)
}

// routeEgressLocal
//
// Functional role (Brique DSL):
// - >sequence:
//   - read: to.context from message envelope
//   - validate: non-empty destination
//   - stamp: from.context with current local context id
//   - >if destination starts with "@":
//     - parse: tag + path
//     - >if "@ext_*": forward to root boundary
//     - >if "@wrapper_*":
//       - validate message type (forbid user/control)
//       - resolve wrapper boundary owner
//       - >if current context is owner: send to wrapper iface
//       - >else: forward to boundary context
//     - >if "@ui_*":
//       - resolve UI owner
//       - >if current context is owner: send to UI iface
//       - >else: forward to owner context
//     - >else: drop + trace
//   - >else (internal address):
//     - validate absolute ctx id "/..."
//     - forward to internal context channel
//
//
// Expected Message Fields:
// - message fields consumed directly or indirectly:
//   - `to.context` (via `msgToContext(&msg)`)
//   - `kind` and destination type envelope (via `msgType(&msg)` on wrapper guard branch)
//   - `from.context` (mutated via `stampFromContext(&msg, ...)`)
//
// Expected Params Keys/values:
// - none (no `msg.Intention.Params[...]` key consumed directly in this function).
//
// Produced Response Fields:
// - Valid:
//   - response fields may be forwarded to contexts or interfaces when `msg.Kind == circulation.ValueKindResponse`.
// - On error:
//   - none directly; invalid egress is dropped and traced.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none beyond delegated drop traces on invalid local egress branches.
// - On error:
//   - emits drop traces for invalid local destinations, forbidden wrapper transport, missing ownership, or malformed internal targets.
//
// Produced Outbound Message:
// - Valid:
//   - forwards the outbound message to another context, to root boundary, or to a local wrapper/UI interface depending on destination class.
// - On error:
//   - emits trace messages describing dropped local egress traffic.
//
// State/Storage Effects:
// - mutates `from.context` in place for local egress stamping.
// - may block until a context or interface channel accepts the message or `l.done` closes.
// - may enqueue the message on a context or interface channel.
//
// Inputs:
// - receiver `l *CommLoop`.
// - msg circulation.Message.
// - frame: runtime registry + current context id.
// - destination: `msg.to.context` (internal `/...` or interface `@tag:/...`).
//
//
// Outputs:
// - no direct return value; observable outputs are forwarding, relay, or drop/trace side effects.
//
//
// Contract:
// - Local (non-root) outbound membrane.
// - No crypto operation here; only addressing validation + forwarding/relay.
// - Nil `frame` is a silent no-op.
// - Missing ownership/registry data on `@ui_*` and `@wrapper_*` paths fails closed by drop or silent abort according to the concrete branch.

func (l *CommLoop) routeEgressLocal(msg circulation.Message) {
	if l.frame == nil {
		return
	}

	toCtxID := msgToContext(&msg)
	toCtx := string(toCtxID)
	if toCtx == "" {
		l.sendToTrace(msg, circulation.ValueTraceCommDrop, circulation.ValueReasonInvalidToContextName, "empty to context address")
		return
	}

	// Preserve the logical caller when forwarding to wrapper transport.
	// Wrapper links are boundary-local and the wrapper needs the original
	// caller address to build the terminal response route.
	if !strings.HasPrefix(toCtx, "@wrapper_") {
		stampFromContext(&msg, circulation.ContextID(l.frame.CtxId))
	}

	// Parse to.context routing.
	if len(toCtx) > 0 && toCtx[0] == '@' {
		// Expect format: @tag:/path
		tag, _, ok := parseAtAddress(toCtx)
		if !ok {
			l.sendToTrace(msg, circulation.ValueTraceCommDrop, circulation.ValueReasonInvalidToContextName, fmt.Sprintf("invalid to context address: %s", toCtx))
			return
		}

		// Invariant: wrapper transport is emitted by engine/runtime paths.
		// User capacities legitimately target wrappers through Execution, while
		// control messages remain forbidden on this transport.
		if strings.HasPrefix(tag, "wrapper_") {
			mt := msgType(&msg)
			if mt == circulation.ValueTypeControl {
				l.sendToTrace(msg, circulation.ValueTraceCommDrop, circulation.ValueReasonInvalidCapacityType,
					fmt.Sprintf("wrapper transport forbidden for message type=%s (to=%s)", mt, toCtx))
				return
			}
		}

		// 1) External destination: always handled by root.
		// External addresses are of the form: @ext_<pubkey>:/...
		if strings.HasPrefix(tag, "ext_") {
			l.forwardToRoot(msg)
			return
		}

		// 2) Wrapper destination:
		// - current:  @wrapper_<name>:/...
		if strings.HasPrefix(tag, "wrapper_") {
			wrapperName := strings.TrimPrefix(tag, "wrapper_")
			if wrapperName == "" {
				l.sendToTrace(msg, circulation.ValueTraceCommDrop, circulation.ValueReasonInvalidToContextName, "wrapper name empty")
				return
			}
			if l.frame.CtxCommReg == nil {
				return
			}
			boundaryID, ok := l.frame.CtxCommReg.ResolveWrapperBoundary(wrapperName)
			if !ok || boundaryID == "" {
				l.sendToTrace(msg, circulation.ValueTraceCommDrop, circulation.ValueReasonInvalidToContextName, fmt.Sprintf("wrapper boundary not found: %s", wrapperName))
				return
			}
			// If we are not the boundary, forward physically to boundary (keep "@wrapper_*" as destination).
			if shared.ContextAddr(l.frame.CtxId) != boundaryID {
				l.forwardToContext(boundaryID, msg)
				return
			}
			l.sendToIface(wrapperName, msg)
			return
		}

		// 3) UI destination:
		// - current:  @ui_<name>:/...
		if strings.HasPrefix(tag, "ui_") {
			ifaceName := strings.TrimPrefix(tag, "ui_")
			if ifaceName == "" {
				l.sendToTrace(msg, circulation.ValueTraceCommDrop, circulation.ValueReasonInvalidToContextName, "empty ifaceName for ui")
				return
			}
			// Ownership must be enforced on egress too (normal path for context->UI).
			if l.frame.CtxCommReg == nil {
				return
			}
			ownerID, ok := l.frame.CtxCommReg.ResolveUI(ifaceName)
			if !ok || ownerID == "" {
				l.sendToTrace(msg, circulation.ValueTraceCommDrop, circulation.ValueReasonInvalidToContextName, fmt.Sprintf("ui owner id unknown for ifaceName: %s", ifaceName))
				return
			}
			if shared.ContextAddr(l.frame.CtxId) != ownerID {
				l.forwardToContext(ownerID, msg)
				return
			}
			// We own it: RELAY directly to iface (do not stamp).
			l.sendToIface(ifaceName, msg)
			return
		}

		// fallback
		l.sendToTrace(msg, circulation.ValueTraceCommDrop, circulation.ValueReasonInvalidToContextName, fmt.Sprintf("unknown @ destination in egress local: %s", toCtx))
		return
	}

	// Internal ctx ids are absolute and must start with '/'.
	// (Compatibility canonicalization happens at ingress boundaries.)
	if toCtx[0] != '/' {
		l.sendToTrace(msg, circulation.ValueTraceCommDrop, circulation.ValueReasonInvalidToContextName, fmt.Sprintf("invalid internal to.context (must be absolute '/...'): %s", toCtx))
		return
	}

	// Intra-instance context routing (ctx_id)
	l.forwardToContext(shared.ContextAddr(toCtx), msg)
}

// routeEgressRoot
//
// Functional role (Brique DSL):
// - >sequence:
//   - read: to.context
//   - validate: non-empty destination
//   - >if destination starts with "@":
//     - parse: tag + path
//     - >if "@ui_*":
//       - resolve owner
//       - >if root owns it: send to UI iface
//       - >else: forward to UI owner context
//     - >if "@wrapper_*": drop (forbidden from root egress)
//     - >if "@ext_*":
//       - compute public from.context
//       - stamp public from.context
//       - update identity + sign
//       - send via external interface ("outerCtx")
//     - >else: drop (unknown @tag)
//   - >else:
//     - forward to internal context channel
//
//
// Expected Message Fields:
// - message fields consumed directly or indirectly:
//   - `to.context` (via `msgToContext(&msg)`)
//   - `from.context` (via `publicFromContext(&msg)` on external `@ext_*` branch)
//
// Expected Params Keys/values:
// - none (no `msg.Intention.Params[...]` key consumed directly in this function).
//
// Produced Response Fields:
// - Valid:
//   - response fields may be forwarded to contexts or interfaces when `msg.Kind == circulation.ValueKindResponse`.
// - On error:
//   - none directly; invalid root egress is dropped and traced.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none beyond delegated drop traces on invalid root egress branches.
// - On error:
//   - emits drop traces for invalid root destinations, forbidden wrapper egress, or external signing/public-identity failures.
//
// Produced Outbound Message:
// - Valid:
//   - forwards the outbound message to an internal context, a root-owned UI interface, or the external interface after public stamping/signing.
// - On error:
//   - emits trace messages describing dropped root egress traffic.
//
// State/Storage Effects:
// - may mutate source context to public form and sign the message for external egress.
// - may block until a context or interface channel accepts the message or `l.done` closes.
// - may enqueue the message on a context or interface channel.
//
// Inputs:
// - receiver `l *CommLoop`.
// - msg circulation.Message.
// - frame: root registry context + ownership resolvers.
// - destination: `msg.to.context`.
//
//
// Outputs:
// - no direct return value; observable outputs are forwarding, relay, signing/stamping, or drop/trace side effects.
//
//
// Contract:
// - Root-only outbound membrane.
// - Sole branch that prepares external egress identity/signature in Comm egress routing.
// - Nil `frame` is a silent no-op.
// - External egress without a resolvable public sender identity fails closed with a drop trace.

func (l *CommLoop) routeEgressRoot(msg circulation.Message) {
	if l.frame == nil {
		return
	}

	toCtxID := msgToContext(&msg)
	toCtx := string(toCtxID)
	if toCtx == "" {
		l.sendToTrace(msg, circulation.ValueTraceCommDrop, circulation.ValueReasonInvalidToContextName, "empty to context address")
		return
	}

	// Addressed to an interface: @tag:/path
	if len(toCtx) > 0 && toCtx[0] == '@' {
		tag, _, ok := parseAtAddress(toCtx)
		if !ok {
			l.sendToTrace(msg, circulation.ValueTraceCommDrop, circulation.ValueReasonInvalidToContextName, fmt.Sprintf("invalid to context address: %s", toCtx))
			return
		}

		// 1) UI egress (internal UI interfaces) with ownership enforcement
		// @ui_<name>:/...
		if strings.HasPrefix(tag, "ui_") {
			uiName := strings.TrimPrefix(tag, "ui_")
			if uiName == "" || l.frame.CtxCommReg == nil {
				l.sendToTrace(msg, circulation.ValueTraceCommDrop, circulation.ValueReasonInvalidToContextName, "invalid to context address ui name empty")
				return
			}

			ownerID, ok := l.frame.CtxCommReg.ResolveUI(uiName)
			if !ok || ownerID == "" {
				l.sendToTrace(msg, circulation.ValueTraceCommDrop, circulation.ValueReasonInvalidToContextName, fmt.Sprintf("unknown ui owner id: %s", toCtx))
				return
			}
			if shared.ContextAddr(l.frame.CtxId) != ownerID {
				l.forwardToContext(ownerID, msg)
				return
			}

			// Root owns it: relay directly to iface (no stamping)
			l.sendToIface(uiName, msg)
			return
		}

		// 2) Wrapper transport is NEVER a root egress target.
		if strings.HasPrefix(tag, "wrapper_") {
			l.sendToTrace(msg, circulation.ValueTraceCommDrop, circulation.ValueReasonInvalidToContextName, fmt.Sprintf("forbidden root egress tag: %s", tag))
			return
		}

		// 3) External egress: must be @ext_<pubkey>:/...
		if strings.HasPrefix(tag, "ext_") {
			if msg.Kind == circulation.ValueKindResponse {
				l.rewriteExternalResponseToPublicTarget(&msg)
			}
			// Determine and stamp public from.context.
			pubFrom := l.publicFromContext(&msg)
			if pubFrom == "" {
				l.sendToTrace(msg, circulation.ValueTraceCommDrop, circulation.ValueReasonInvalidCrypto, "public key empty")
				return
			}
			l.stampFromPublicContext(&msg, pubFrom)

			// Convention: external interface name is "outerCtx"
			l.sendToIface("outerCtx", msg)
			return
		}

		// 4) Any other @tag is forbidden (fail-closed).
		l.sendToTrace(msg, circulation.ValueTraceCommDrop, circulation.ValueReasonInvalidCrypto, fmt.Sprintf("forbidden root egress tag: %s", tag))
		return
	}

	// -------------------------------------------------
	// Non-@ destination: internal ctx routing (ctx_id)
	// Root must forward directly, with wrapper-boundary check.
	// -------------------------------------------------
	l.forwardToContext(shared.ContextAddr(toCtx), msg)
}
