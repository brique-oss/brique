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
	"brique_engine/shared"
)

// routeIngress
//
// Functional role (Brique DSL):
// - always:
//   - send trace event "CommIngress"
// - >if: current context is root
//   - call: routeIngressRoot
// - >else:
//   - call: routeIngressLocal
//
//
// Expected Message Fields:
// - Direct in this function:
//   - none.
// - Via delegated routing branches (`routeIngressLocal` / `routeIngressRoot`):
//   - `kind`
//   - `to.context`
//   - `to.type`
//   - `from.context`
//   - `identity.pubkey` (root outer ingress crypto branch)
//   - `intention.params` (root scattered branch)
//
// Expected Params Keys/values:
// - Direct in this function:
//   - none.
// - Via delegated root scattered branch:
//   - `circulation.KeyScatteredParam` (checked by `isScatteredIntention` / `handleScatteredIngress`)
//
// Produced Response Fields:
// - Valid:
//   - response fields may be emitted indirectly by delegated ingress branches when the inbound or delegated message kind is `circulation.ValueKindResponse`.
// - On error:
//   - error response fields may be emitted indirectly by delegated ingress branches.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none directly from this function.
// - On error:
//   - none directly from this function.
//
// Produced Trace:
// - Valid:
//   - one comm-ingress trace is emitted before routing.
//   - delegated branches may emit additional reject traces during ingress validation.
// - On error:
//   - delegated branches may emit reject traces.
//
// Produced Outbound Message:
// - Valid:
//   - delegated branches may relay admitted ingress traffic to local families, control channel, contexts, interfaces, or root egress.
// - On error:
//   - delegated branches may emit trace messages describing rejected ingress traffic.
//
// State/Storage Effects:
// - emits one ingress trace.
// - delegates to root or local ingress routing.
//
// Inputs:
// - receiver `l *CommLoop`.
// - msg: inbound structured message
// - endpoint: ingress origin class (inner/ui/wrapper/outer)
// - ifacename: physical interface name (when relevant)
// - frame.CtxId: runtime context identity for root/local split
//
//
// Outputs:
// - no direct return value; observable outputs are side effects of selected ingress branch and trace emission.
//
//
// Contract:
// - Single ingress entry point for CommLoop routing.
// - No business dispatch here; only variant selection.

func (l *CommLoop) routeIngress(msg circulation.Message, endpoint string, ifacename string) {
	l.sendToTrace(msg, circulation.ValueTraceCommIngress, "", "")
	if l.frame != nil && shared.IsRootContext(l.frame.CtxId) {
		l.routeIngressRoot(msg, endpoint, ifacename)
		return
	}
	l.routeIngressLocal(msg, endpoint, ifacename)
}

// routeIngressLocal
//
// Functional role (Brique DSL):
// - >sequence:
//   - infer message family type from envelope (intention/response target type)
//   - validate type presence
//   - normalize destination address (UI default + canonical internal ctx id)
//   - >switch by endpoint:
//     - EndpointInnerCtx:
//       - validate privileged wrapper transport (@wrapper_*) against boundary registry
//       - process @ destinations (@ui_ / @ext_) via ownership and boundary rules
//       - otherwise continue to local dispatch
//     - EndpointUI:
//       - stamp from.context as "@ui_<iface>"
//       - process @ destinations or forward to internal target context
//       - otherwise continue to local dispatch
//     - EndpointWrapper:
//       - project wrapper-local from.context into instance namespace
//       - process @ destinations or forward to internal target context
//       - otherwise continue to local dispatch
//   - >if type == control:
//     - allow only from EndpointUI
//     - send to CtrlIn
//   - >if type == user:
//     - forbid on root
//     - send to execution family channel
//   - >else:
//     - send to family channel matching type
//
//
// Expected Message Fields:
// - message fields consumed directly or indirectly:
//   - `kind`
//   - `to.context`
//   - `to.type`
//   - `from.context`
//
// Expected Params Keys/values:
// - none (no `msg.Intention.Params[...]` key consumed directly in this function).
//
// Produced Response Fields:
// - Valid:
//   - response fields may be enqueued to Ctrl, Execution, or family channels when `msg.Kind == circulation.ValueKindResponse`.
// - On error:
//   - none directly; invalid ingress is rejected via trace only.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none beyond delegated reject traces on invalid local ingress branches.
// - On error:
//   - emits reject traces for invalid endpoint use, invalid `@` routing, missing family type, or forbidden root-local user/control branches.
//
// Produced Outbound Message:
// - Valid:
//   - dispatches admitted ingress traffic to control, execution, family, context, wrapper, or UI routes depending on endpoint and destination.
// - On error:
//   - emits trace messages describing rejected local ingress traffic.
//
// State/Storage Effects:
// - mutates message source/destination context fields in place for normalization and endpoint stamping.
// - may block until a control/family/context/interface channel accepts the message or `l.done` closes.
// - may enqueue the message on control/family/context/interface channels.
//
// Inputs:
// - receiver `l *CommLoop`.
// - msg circulation.Message.
// - endpoint: `EndpointInnerCtx | EndpointUI | EndpointWrapper`.
// - ifacename: ingress interface label (for `@ui_` / `@wrapper_` projection).
// - frame: local registry, control channel, family channels.
//
//
// Outputs:
// - no direct return value; observable outputs are forwarding, relay, enqueue, canonicalization/stamping, or reject/trace side effects.
//
//
// Contract:
// - Non-root ingress membrane and shared local dispatcher used by root for local cases.
// - Enforces ownership/boundary invariants before dispatching into Ctrl/Family channels.
// - Nil `frame` is a silent no-op.
// - Missing control/family channels and unresolved local dispatch targets fail closed by dropping the message without emitting a response.

func (l *CommLoop) routeIngressLocal(msg circulation.Message, endpoint string, ifacename string) {
	if l.frame == nil {
		return
	}

	t := msgType(&msg)
	if t == "" {
		l.sendToTrace(msg, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidMsgType, "missing message type")
		return
	}

	toCtx := string(msgToContext(&msg))

	// UI defaulting: if UI does not provide an explicit destination,
	// treat it as targeting the owning context.
	if endpoint == EndpointUI && strings.TrimSpace(toCtx) == "" {
		switch msg.Kind {
		case circulation.ValueKindIntention:
			msg.Intention.To.Context = circulation.ContextID(l.frame.CtxId)
		case circulation.ValueKindResponse:
			msg.Response.To.Context = circulation.ContextID(l.frame.CtxId)
		}
		toCtx = string(msgToContext(&msg))
	}

	// Canonicalize internal absolute ids to always start with '/'.
	// (Do not touch @... addresses.)
	if strings.TrimSpace(toCtx) != "" && toCtx[0] != '@' {
		canon := canonicalInternalCtxID(toCtx)
		if canon != toCtx {
			switch msg.Kind {
			case circulation.ValueKindIntention:
				msg.Intention.To.Context = circulation.ContextID(canon)
			case circulation.ValueKindResponse:
				msg.Response.To.Context = circulation.ContextID(canon)
			}
			toCtx = canon
		}
	}

	// -------- helpers (factorized) --------

	resolveUIOwner := func(uiName string) (shared.ContextAddr, bool) {
		if uiName == "" || l.frame.CtxCommReg == nil {
			return "", false
		}
		scope := shared.ContextAddr(msgFromContext(&msg))
		if scope != "" {
			ownerID, ok := l.frame.CtxCommReg.ResolveUIInScope(uiName, scope)
			if ok && ownerID != "" {
				return ownerID, true
			}
		}
		ownerID, ok := l.frame.CtxCommReg.ResolveUI(uiName)
		return ownerID, ok && ownerID != ""
	}

	// Helper: handle any @... destination (interface address).
	// - only @ui_<name> and @ext_<pubkey> are valid ingress interface targets here.
	// - @wrapper_* is PRIVILEGED and handled above (boundary-only, innerCtx-only).
	handleAtAddress := func(fromEndpoint string) {
		tag, _, ok := parseAtAddress(toCtx)
		if !ok {
			l.sendToTrace(msg, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidToContextName, "invalid @ address")
			return
		}

		// wrapper transport is privileged; do not accept here.
		if strings.HasPrefix(tag, "wrapper_") {
			l.sendToTrace(msg, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidToContextName,
				fmt.Sprintf("wrapper transport forbidden at ingress local: %s", toCtx))
			return
		}

		// external destination -> root boundary
		if strings.HasPrefix(tag, "ext_") {
			if fromEndpoint == EndpointInnerCtx {
				l.sendToTrace(msg, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidToContextName, fmt.Sprintf("invalid ext_ ingress on local context: %s", toCtx))
				return
			}
			l.forwardToRoot(msg)
			return
		}

		// internal UI destination -> enforce ownership
		if strings.HasPrefix(tag, "ui_") {
			uiName := strings.TrimPrefix(tag, "ui_")
			if uiName == "" {
				l.sendToTrace(msg, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidToContextName, "ui name empty")
				return
			}
			ownerID, ok := resolveUIOwner(uiName)
			if !ok {
				if fromEndpoint == EndpointWrapper && !shared.IsRootContext(l.frame.CtxId) {
					l.forwardToRoot(msg)
					return
				}
				// no registry info => drop (do not assume ownership)
				l.sendToTrace(msg, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidToContextName, fmt.Sprintf("unknown ui name: %s", uiName))
				return
			}
			if shared.ContextAddr(l.frame.CtxId) != ownerID {
				if fromEndpoint == EndpointInnerCtx {
					l.sendToTrace(msg, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidToContextName, fmt.Sprintf("invalid ui ingress: expected owner %s, got %s (to=%s)",
						ownerID, shared.ContextAddr(l.frame.CtxId), toCtx))
					return
				}
				if fromEndpoint == EndpointWrapper && !shared.IsRootContext(l.frame.CtxId) {
					l.forwardToRoot(msg)
					return
				}
				l.forwardToContext(ownerID, msg)
				return
			}
			// we own it: relay directly to iface (no stamping)
			l.sendToIface(uiName, msg)
			return
		}

		// any other @tag is invalid at ingress local
		l.sendToTrace(msg, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidToContextName, fmt.Sprintf("invalid @ destination in ingress local: %s", toCtx))
	}

	// --------------------------------------
	// 2) Messages coming from another context (EndpointInnerCtx)
	// --------------------------------------
	if endpoint == EndpointInnerCtx {
		// Only families may EMIT "@wrapper_*" on egress. Therefore:
		// - UI / outerCtx MUST NOT inject "@wrapper_*" at ingress.
		// - InnerCtx may carry "@wrapper_*" ONLY as a boundary forwarding step,
		//   and ONLY the correct boundary context may deliver it to the wrapper iface.
		if len(toCtx) > 0 && toCtx[0] == '@' {
			tag, _, ok := parseAtAddress(toCtx)
			if ok && strings.HasPrefix(tag, "wrapper_") {
				// Only accept delivery to wrapper iface when message arrived from innerCtx,
				// and ONLY if this context is the registered boundary for that wrapper.
				if endpoint != EndpointInnerCtx {
					l.sendToTrace(msg, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidEndPoint,
						fmt.Sprintf("wrapper transport forbidden from endpoint=%s (to=%s)", endpoint, toCtx))
					return
				}
				wrapperName := strings.TrimPrefix(tag, "wrapper_")
				if wrapperName == "" || l.frame.CtxCommReg == nil {
					l.sendToTrace(msg, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidToContextName,
						fmt.Sprintf("invalid wrapper transport destination: %s", toCtx))
					return
				}
				boundaryID, ok := l.frame.CtxCommReg.ResolveWrapperBoundary(wrapperName)
				if !ok || boundaryID == "" {
					l.sendToTrace(msg, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidToContextName,
						fmt.Sprintf("wrapper boundary not found: %s", wrapperName))
					return
				}
				if shared.ContextAddr(l.frame.CtxId) != boundaryID {
					l.sendToTrace(msg, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidToContextName,
						fmt.Sprintf("invalid wrapper transport ingress: expected boundary %s, got %s (wrapper=%s, to=%s)",
							boundaryID, shared.ContextAddr(l.frame.CtxId), wrapperName, toCtx))
					return
				}
				// Only a `user`-type intention (a capacity call whose owning
				// capacity may live in a different context than this wrapper's
				// owner) can need readiness enforcement here. Responses, and
				// intentions of any other family type (matter, etc., which
				// never go through Execution's capacity registry), are
				// delivered straight to the iface as before: the destination
				// wrapper is either already alive (a response always targets
				// the wrapper that emitted the original call) or readiness is
				// not this ingress path's concern for that family.
				if msg.Kind != circulation.ValueKindIntention || msg.Intention.To.Type != circulation.ValueTypeUser {
					_ = l.sendToIface(wrapperName, msg)
					return
				}

				// This context is the wrapper's owning boundary. Whether the
				// wrapper process is already running (readiness enforced on a
				// prior local call) or not (first call comes from a remote
				// context whose own resolveIn skips local readiness for a
				// wrapper it does not own) cannot be assumed here. The
				// to.context is left untouched (still "@wrapper_<name>:/<rel>"):
				// Execution's runUserJob recognizes this wrapper-transport form
				// on a `user` intention and treats it purely as a
				// readiness-then-relay case — it does NOT attempt to resolve a
				// local capacity for it, since the capacity backing this call
				// may be declared in a different context than the one owning
				// this wrapper (that resolution already happened, correctly,
				// in the caller's own Execution loop).
				execCh, ok := l.frame.LookupFamily(shared.FamilyExecution)
				if !ok || execCh == nil {
					l.sendToTrace(msg, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidToContextName,
						fmt.Sprintf("execution family unavailable for wrapper delivery: %s", wrapperName))
					return
				}
				select {
				case execCh <- msg:
				case <-l.done:
				}
				return
			}
		}
		// InnerCtx may carry @ui_<name> ONLY if it is already routed to the owning context.

		if len(toCtx) > 0 && toCtx[0] == '@' {
			handleAtAddress(endpoint)
			return
		}

		// Otherwise: message is for this context -> continue below (control/user/family).
	}

	// --------------------------------------
	// 3) Messages coming from a UI interface (EndpointUI)
	// --------------------------------------
	if endpoint == EndpointUI {
		// UI may target @ui_<name> or @ext_<pubkey>
		stampFromUIContext(&msg, ifacename)

		if len(toCtx) > 0 && toCtx[0] == '@' {
			handleAtAddress(endpoint)
			return
		}

		// UI may target another internal context: forward.
		if len(toCtx) > 0 && toCtx[0] == '@' {
			// continue; it will go to exec/family below
		} else if toCtx != "" && shared.ContextAddr(toCtx) != shared.ContextAddr(l.frame.CtxId) {
			l.forwardToContext(shared.ContextAddr(toCtx), msg)
			return
		}

		// Otherwise: for this context -> continue below.
	}

	// --------------------------------------
	// 4) Messages coming from a wrapper interface (EndpointWrapper)
	// --------------------------------------
	if endpoint == EndpointWrapper {
		// Project wrapper-local origin into instance space by prepending boundary ctx_id to from.context.
		switch msg.Kind {
		case circulation.ValueKindIntention:
			// Wrapper Ready Message
			if msg.Intention.To.Type == circulation.ValueTypeExecution && string(msg.Intention.To.Context) == l.frame.CtxId {
				msg.Intention.From.Context = circulation.ContextID("@wrapper_" + ifacename)
			} else {
				if msg.Intention.From.Context == "" {
					msg.Intention.From.Context = circulation.ContextID(l.frame.CtxId)
				} else {
					msg.Intention.From.Context = circulation.ContextID(l.frame.CtxId + "/" + string(msg.Intention.From.Context))
				}
			}
		case circulation.ValueKindResponse:
			if msg.Response.From.Context == "" {
				msg.Response.From.Context = circulation.ContextID(l.frame.CtxId)
			} else {
				msg.Response.From.Context = circulation.ContextID(l.frame.CtxId + "/" + string(msg.Response.From.Context))
			}
		}

		// Wrapper may target @ui_<name> or @ext_<pubkey>
		if len(toCtx) > 0 && toCtx[0] == '@' {
			tag, _, ok := parseAtAddress(toCtx)
			if ok && strings.HasPrefix(tag, "ui_") && !shared.IsRootContext(l.frame.CtxId) {
				uiName := strings.TrimPrefix(tag, "ui_")
				if ownerID, ok := resolveUIOwner(uiName); ok && shared.ContextAddr(l.frame.CtxId) != ownerID {
					l.forwardToContext(ownerID, msg)
					return
				} else if !ok {
					l.forwardToRoot(msg)
					return
				}
			}
			l.routeEgressLocal(msg)
			return
		}

		// Wrapper may target an internal context: forward.
		// If we are now targeting an interface address (@...), do NOT forward as ctx_id.
		if len(toCtx) > 0 && toCtx[0] == '@' {
			// continue; it will go to exec/family below
		} else if toCtx != "" && shared.ContextAddr(toCtx) != shared.ContextAddr(l.frame.CtxId) {
			l.forwardToContext(shared.ContextAddr(toCtx), msg)
			return
		}

		// Otherwise: for this boundary context -> continue below.
	}

	// Interface-addressed messages must not fall through to user execution.
	// Some transports can enter with a non-specialized endpoint string while
	// still carrying a valid @ui_ or @ext_ destination.
	if len(toCtx) > 0 && toCtx[0] == '@' {
		if msg.Kind == circulation.ValueKindResponse && msg.Response.Identity.Kind == "wrapper" && msg.Response.From.Context == "" {
			msg.Response.From.Context = circulation.ContextID(l.frame.CtxId)
		}
		handleAtAddress(endpoint)
		return
	}

	// --------------------------------------
	// Control messages
	// --------------------------------------
	if t == circulation.ValueTypeControl {
		// Accept control from UI or wrapper endpoints directly, or from inner-context
		// forwarding when the original source is a UI (from.context starts with "@ui_")
		// or a wrapper (identity.kind == "wrapper", e.g. a wrapper boundary context
		// forwarding a control intention on behalf of its own wrapper to a different
		// target context, as MCP does for context.start/context.restart).
		// outerCtx (cross-instance) remains refused: another Brique instance must not
		// be able to start/restart/stop contexts on this one.
		fromCtx := ""
		fromWrapper := false
		if msg.Kind == circulation.ValueKindIntention {
			fromCtx = string(msg.Intention.From.Context)
			fromWrapper = msg.Intention.Identity.Kind == "wrapper"
		}
		fromUI := strings.HasPrefix(fromCtx, "@ui_")
		allowedDirect := endpoint == EndpointUI || endpoint == EndpointWrapper
		if !allowedDirect && !(endpoint == EndpointInnerCtx && (fromUI || fromWrapper)) {
			l.sendToTrace(msg, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidEndPoint, fmt.Sprintf("invalid endpoint for control message: %s", endpoint))
			return
		}
		if l.frame.CtrlIn == nil {
			return
		}
		select {
		case l.frame.CtrlIn <- msg:
			return
		case <-l.done:
			return
		}
	}

	// --------------------------------------
	// User messages -> execution family
	// --------------------------------------
	if t == circulation.ValueTypeUser {
		if l.frame != nil && shared.IsRootContext(l.frame.CtxId) {
			l.sendToTrace(msg, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidCapacityType, "no user capacity available in root")
			return
		}
		execCh, ok := l.frame.LookupFamily(shared.FamilyExecution)
		if !ok || execCh == nil {
			return
		}
		select {
		case execCh <- msg:
			return
		case <-l.done:
			return
		}
	}

	// --------------------------------------
	// Family messages
	// --------------------------------------
	famCh, ok := l.frame.LookupFamily(shared.FamilyName(t))
	if !ok || famCh == nil {
		return
	}
	select {
	case famCh <- msg:
		return
	case <-l.done:
		return
	}
}

// routeIngressRoot
//
// Functional role (Brique DSL):
// - >switch by endpoint:
//   - EndpointOuterCtx:
//     - verify signature and extract public identity
//     - stamp from.context as "@ext_<pubkey>"
//     - >if to.context is @ui_*: delegate to local ingress
//     - >if to.context is other @*: reject
//     - resolve external ctx name to internal ctx id
//     - >if destination is root:
//       - optional scattered fan-out
//       - delegate to local ingress
//     - >else: forward to target internal context
//   - EndpointWrapper:
//     - reject (wrapper ingress forbidden at root)
//   - EndpointUI:
//     - canonicalize internal destination id
//     - >if to.context is @ext_*: route through root egress boundary
//     - >if to.context is @ui_*: delegate to local ingress
//     - >if destination is root:
//       - optional scattered fan-out
//       - delegate to local ingress
//     - >else: forward to target internal context
//   - EndpointInnerCtx:
//     - >if destination is root:
//       - optional scattered fan-out
//       - delegate to local ingress
//     - >if to.context is @ext_*: route through root egress boundary
//     - >if to.context is @ui_*: delegate to local ingress
//     - >else internal ctx != root: no relay (drop silently by contract)
//
//
// Expected Message Fields:
// - message fields consumed directly or indirectly:
//   - `kind`
//   - `to.context`
//   - `to.cap`
//   - `to.type`
//   - `from.context`
//   - `identity.pubkey`
//   - `intention.params`
//
// Expected Params Keys/values:
// - params keys consumed indirectly in root scattered branch:
//   - `circulation.KeyScatteredParam`
//
// Produced Response Fields:
// - Valid:
//   - scatter ack responses may be emitted indirectly.
//   - response fields may be enqueued locally or forwarded to contexts/interfaces when the admitted message kind is `circulation.ValueKindResponse`.
// - On error:
//   - scatter error acks may be emitted indirectly.
//   - invalid root ingress is otherwise rejected via trace only.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - scatter ack payload keys may be emitted indirectly.
// - On error:
//   - scatter error details may be emitted indirectly.
//
// Produced Trace:
// - Valid:
//   - local ingress delegation, root egress handoff, and scattered fan-out may emit ingress, egress, and reject traces indirectly.
// - On error:
//   - emits reject traces for invalid root ingress endpoint or addressing branches.
//
// Produced Outbound Message:
// - Valid:
//   - forwards admitted root ingress traffic to local families, internal contexts, UI interfaces, root egress, or scatter fan-out paths.
// - On error:
//   - emits trace messages describing rejected root ingress traffic.
//
// State/Storage Effects:
// - mutates message source/destination context fields in place for root boundary handling.
// - may verify external signatures and resolve external names.
// - may trigger root scatter fan-out.
// - may enqueue the message on local family/context/interface channels.
//
// Inputs:
// - receiver `l *CommLoop`.
// - msg circulation.Message.
// - endpoint: `EndpointOuterCtx | EndpointUI | EndpointInnerCtx | EndpointWrapper`.
// - ifacename: interface label when endpoint is interface-based.
// - frame: root registry + boundary resolvers.
//
//
// Outputs:
// - no direct return value; observable outputs are reject/trace, local delegation, context forwarding, root egress handoff, or scattered fan-out side effects.
//
//
// Contract:
// - Root ingress boundary for external trust crossing and instance entry routing.
// - Only root handles external identity verification and ext-name resolution.
// - Nil `frame` is a silent no-op.
// - Inner-context traffic targeting a non-root internal context is intentionally not relayed by root and is dropped silently.

func (l *CommLoop) routeIngressRoot(
	msg circulation.Message,
	endpoint string,
	ifacename string,
) {
	if l.frame == nil {
		return
	}

	toCtx := string(msgToContext(&msg))

	// -------------------------------------------------
	// 1) Messages coming from outside the instance
	// -------------------------------------------------
	if endpoint == EndpointOuterCtx {
		origFrom := ""
		switch msg.Kind {
		case circulation.ValueKindIntention:
			origFrom = strings.TrimSpace(string(msg.Intention.From.Context))
		case circulation.ValueKindResponse:
			origFrom = strings.TrimSpace(string(msg.Response.From.Context))
		}

		// --- crypto / identity boundary ---
		if err := l.verifyMessageSignature(msg); err != nil {
			l.sendToTrace(msg, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidCrypto, "invalid signature")
			return
		}

		pub := extractPublicKey(&msg)
		if pub == "" {
			l.sendToTrace(msg, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidCrypto, "public key empty")
			return
		}
		switch msg.Kind {
		case circulation.ValueKindIntention:
			if origFrom != "" && !strings.HasPrefix(origFrom, "@ext_") {
				l.rememberExternalReplyTarget(msg, origFrom, pub)
			}
			msg.Intention.From.Context = circulation.ContextID("@ext_" + pub)
		case circulation.ValueKindResponse:
			msg.Response.From.Context = circulation.ContextID("@ext_" + pub)
		}

		// --- destination handling ---
		if strings.HasPrefix(toCtx, "@") {
			tag, _, ok := parseAtAddress(toCtx)
			if !ok {
				l.sendToTrace(msg, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidToContextName, fmt.Sprintf("invalid to context address: %s", toCtx))
				return
			}

			// @ui_ allowed
			if strings.HasPrefix(tag, "ui_") {
				uiName := strings.TrimPrefix(tag, "ui_")
				if uiName == "" || l.frame.CtxCommReg == nil {
					l.sendToTrace(msg, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidToContextName, fmt.Sprintf("invalid ui name at root outer ingress: %s", toCtx))
					return
				}
				ownerID, ok := l.frame.CtxCommReg.ResolveUI(uiName)
				if !ok || ownerID == "" {
					l.sendToTrace(msg, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidToContextName, fmt.Sprintf("unknown ui owner id: %s", toCtx))
					return
				}
				if shared.ContextAddr(l.frame.CtxId) != ownerID {
					l.forwardToContext(ownerID, msg)
					return
				}
				l.routeIngressLocal(msg, EndpointInnerCtx, ifacename)
				return
			}

			// @ext_ chaining not allowed
			l.sendToTrace(msg, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidToContextName, fmt.Sprintf("invalid external chaining at root: %s", toCtx))
			return
		}

		// external ctx_ext_name → internal ctx_id
		if !l.resolveExtContext(&msg) {
			l.sendToTrace(msg, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidToContextName, fmt.Sprintf("no internal context corresponding to: %s", toCtx))
			return
		}

		toCtx = string(msgToContext(&msg))

		// Outer → root (resolved): enter local root handling (control/family only)
		if toCtx == shared.RootContextID {
			// scattered fan-out (root-only)
			if l.isScatteredIntention(&msg) {
				l.handleScatteredIngress(msg, endpoint, ifacename)
				return
			}
			l.routeIngressLocal(msg, EndpointInnerCtx, ifacename)
			return
		}

		l.forwardToContext(shared.ContextAddr(toCtx), msg)
		return
	}

	// -------------------------------------------------
	// 2) Wrapper ingress is forbidden at root
	// -------------------------------------------------
	if endpoint == EndpointWrapper {
		l.sendToTrace(msg, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidToContextName, "wrapper ingress forbidden at root")
		return
	}

	// -------------------------------------------------
	// 3) Messages coming from UI
	// -------------------------------------------------
	if endpoint == EndpointUI {
		// Canonicalize internal context ids early (UI may send "root/..." without leading '/').
		if strings.TrimSpace(toCtx) != "" && !strings.HasPrefix(toCtx, "@") {
			canon := canonicalInternalCtxID(toCtx)
			if canon != toCtx {
				switch msg.Kind {
				case circulation.ValueKindIntention:
					msg.Intention.To.Context = circulation.ContextID(canon)
				case circulation.ValueKindResponse:
					msg.Response.To.Context = circulation.ContextID(canon)
				}
				toCtx = canon
			}
		}

		// UI → @ext_ or @ui_
		if strings.HasPrefix(toCtx, "@") {
			tag, _, ok := parseAtAddress(toCtx)
			if !ok {
				l.sendToTrace(msg, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidToContextName, fmt.Sprintf("invalid to context address: %s", toCtx))
				return
			}

			// @ext_ : external boundary
			if strings.HasPrefix(tag, "ext_") {
				l.routeEgressRoot(msg)
				return
			}

			// @ui_ : handled locally (root may own UIs)
			if strings.HasPrefix(tag, "ui_") {
				l.routeIngressLocal(msg, endpoint, ifacename)
				return
			}

			l.sendToTrace(msg, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidToContextName, fmt.Sprintf("invalid @ destination at root ingress: %s", toCtx))
			return
		}

		// UI → root (explicit or implicit): enter local root handling (control/family only)
		if toCtx == shared.RootContextID {
			// scattered fan-out (root-only)
			if l.isScatteredIntention(&msg) {
				l.handleScatteredIngress(msg, endpoint, ifacename)
				return
			}
			l.routeIngressLocal(msg, endpoint, ifacename)
			return
		}

		l.forwardToContext(shared.ContextAddr(toCtx), msg)
		return
	}

	// -------------------------------------------------
	// 4) Messages coming from inside the instance
	// -------------------------------------------------
	if endpoint == EndpointInnerCtx {

		// Inner → root: enter local root handling (control/family only)
		if toCtx == shared.RootContextID {
			// scattered fan-out (root-only)
			if l.isScatteredIntention(&msg) {
				l.handleScatteredIngress(msg, endpoint, ifacename)
				return
			}
			l.routeIngressLocal(msg, endpoint, ifacename)
			return
		}

		// Inner → @ui_ or @ext_
		if strings.HasPrefix(toCtx, "@") {
			tag, _, ok := parseAtAddress(toCtx)
			if !ok {
				l.sendToTrace(msg, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidToContextName, fmt.Sprintf("invalid to context address: %s", toCtx))
				return
			}

			if strings.HasPrefix(tag, "ext_") {
				l.routeEgressRoot(msg)
				return
			}

			// @ui_ : handled locally (root may own UIs)
			if strings.HasPrefix(tag, "ui_") {
				l.routeIngressLocal(msg, endpoint, ifacename)
				return
			}

			l.sendToTrace(msg, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidToContextName, fmt.Sprintf("invalid @ destination from innerCtx at root: %s", toCtx))
			return
		}

		// Inner → internal ctx : root must NOT relay
		return
	}
}
