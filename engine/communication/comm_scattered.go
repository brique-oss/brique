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
	"time"
)

// isScatteredIntention
//
// Functional role (Brique DSL):
// - validate whether inbound message requests root scattered fan-out
// - condition: message kind is intention AND params contains `scattered`
//
//
// Expected Message Fields:
// - message fields consumed directly:
//   - `kind`
//   - `intention`
//   - `intention.params`
//
// Expected Params Keys/values:
// - Valid:
//   - {
//       "scattered": "any present value; existence alone marks the intention as scatter-candidate for this predicate helper"
//     }
// - On error:
//   - none.
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
// - returns bool.
//
//
// Contract:
// - Pure predicate helper; no side effects.

func (l *CommLoop) isScatteredIntention(msg *circulation.Message) bool {
	if msg == nil || msg.Kind != circulation.ValueKindIntention {
		return false
	}
	if msg.Intention.Params == nil {
		return false
	}
	_, ok := msg.Intention.Params[circulation.KeyScatteredParam]
	return ok
}

// scatterAllowed
//
// Functional role (Brique DSL):
// - authorize scatter request by `(family type, cap)` against communication config whitelist.
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
// - receiver `l *CommLoop`.
// - familyType string, capName string.
//
//
// Outputs:
// - returns bool.
//
//
// Contract:
// - Pure authorization helper; no side effects.

func (l *CommLoop) scatterAllowed(familyType string, capName string) bool {
	familyType = strings.TrimSpace(familyType)
	capName = strings.TrimSpace(capName)
	if familyType == "" || capName == "" {
		return false
	}
	allowedFam, ok := l.cfg.AllowedScatter[familyType]
	if !ok || allowedFam == nil {
		return false
	}
	return allowedFam[capName]
}

// handleScatteredIngress
//
// Functional role (Brique DSL):
// - root-only orchestration of a scattered intention:
//   - validate request eligibility (root, intention kind, to.context=/root, allowed type/cap)
//   - validate and bound `params.scattered` items
//   - fan-out one sub-intention per valid item to target contexts
//   - emit one ack response to requester (ok/error)
// - enforce anti-pattern guards:
//   - scatter-of-scatter forbidden
//   - interface destinations forbidden in item.to.context
//
//
// Expected Message Fields:
// - Direct in this function:
//   - `kind`
//   - `intention.to.context`
//   - `intention.to.type`
//   - `intention.to.cap`
//   - `intention.from`
//   - `intention.identity`
//   - `intention.correlation`
//   - `intention.params`
// - Via delegated fallback branch (`routeIngressLocal(msg, endpoint, ifacename)` when `params.scattered` is absent):
//   - `kind`
//   - `to.context`
//   - `to.type`
//   - `from.context`
//   - `identity.pubkey`
//   - `intention.params`
//
// Expected Params Keys/values:
// - Valid:
//   - {
//       "scattered": [
//         {
//           "to": {
//             "context": "required absolute internal context id string `/...`; interface addresses `@...` are forbidden",
//             "version": "optional string; propagated into the spawned destination address"
//           },
//           "params": {
//             "...": "optional sub-intention params forwarded verbatim to the spawned intention; nested `scattered` is forbidden"
//           }
//         }
//       ]
//     }
// - On error:
//   - none.
//
// Produced Response Fields:
// - Valid:
//   - scatter ack response via `sendScatterAckOk`.
// - On error:
//   - scatter error ack via `sendScatterAckError` on top-level invalid scatter requests.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - {
//       "stream": {
//         "total": "number; original scattered item count before per-item filtering",
//         "spawned_intention_ids": ["list of spawned sub-intention ids actually forwarded"]
//       }
//     }
// - On error:
//   - none; top-level invalid scatter requests emit `response.error.details`, not `response.payload`.
//
// Produced Trace:
// - Valid:
//   - scattered item forwarding, local fallback routing, and final ack egress may emit traces indirectly.
// - On error:
//   - emits reject traces for invalid top-level scatter shape and for invalid individual scattered items.
//
// Produced Outbound Message:
// - Valid:
//   - forwards one spawned sub-intention per accepted scattered item to its target context.
//   - emits one final success ack response through normal egress routing.
// - On error:
//   - emits one error ack response for top-level invalid scatter requests.
//   - emits reject traces for skipped or invalid scatter items.
//
// State/Storage Effects:
// - may forward multiple spawned sub-intentions to internal context channels.
// - may emit one ack response through Comm egress.
//
// Inputs:
// - receiver `l *CommLoop`.
// - msg circulation.Message.
// - endpoint string.
// - ifacename string.
//
//
// Outputs:
// - no direct return value; observable outputs are per-item forward, ack response emission, local fallback delegation, or reject/trace side effects.
//
//
// Contract:
// - Must be called only by root ingress flow for intentions targeting `/root`.
// - Performs fan-out orchestration but not response aggregation of spawned sub-intentions.
// - Top-level validation failures emit at most one error ack and stop processing.
// - Invalid individual scatter items are skipped with reject traces while remaining items continue.
// - Success ack reports the requested total item count and the subset of actually spawned intention ids.

func (l *CommLoop) handleScatteredIngress(msg circulation.Message, endpoint string, ifacename string) {
	// Root-only guard (defense-in-depth)
	if l.frame == nil || !shared.IsRootContext(l.frame.CtxId) {
		l.sendToTrace(msg, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidEndPoint, "scatter invoked outside root")
		return
	}

	// Only intentions are eligible.
	if msg.Kind != circulation.ValueKindIntention {
		l.sendToTrace(msg, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidMsgType, "scatter requires intention")
		return
	}

	// Must target root.
	if string(msg.Intention.To.Context) != shared.RootContextID {
		l.sendToTrace(msg, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidToContextName, "scatter requires to.context=/root")
		return
	}

	// Family (type) + cap whitelist
	famType := strings.TrimSpace(msg.Intention.To.Type)
	capName := strings.TrimSpace(msg.Intention.To.Cap)
	if famType == "" || capName == "" {
		l.sendToTrace(msg, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidMsgType, "scatter requires intention.type and intention.cap")
		l.sendScatterAckError(&msg, "invalid", "scatter requires intention.type and intention.cap", nil)
		return
	}
	if !l.scatterAllowed(famType, capName) {
		l.sendToTrace(msg, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidCapacityType, fmt.Sprintf("scatter cap not allowed: type=%s cap=%s", famType, capName))
		l.sendScatterAckError(&msg, "refused", "scatter capability not allowed by configuration", map[string]any{
			"type": famType, "cap": capName,
		})
		return
	}

	// Extract scattered list
	params := msg.Intention.Params
	raw, ok := params[circulation.KeyScatteredParam]
	if !ok {
		// Nothing to do
		l.routeIngressLocal(msg, endpoint, ifacename)
		return
	}

	list, ok := raw.([]any)
	if !ok {
		// also accept []map[string]any via interface{} cast patterns
		l.sendToTrace(msg, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidMsgType, "scatter params.scattered must be an array")
		l.sendScatterAckError(&msg, "invalid", "params.scattered must be an array", nil)
		return
	}

	// Bounds (anti-burst)
	if l.cfg.MaxScatterItems > 0 && len(list) > l.cfg.MaxScatterItems {
		l.sendToTrace(msg, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidMsgType, fmt.Sprintf("scatter too many items: %d > %d", len(list), l.cfg.MaxScatterItems))
		l.sendScatterAckError(&msg, "invalid", "too many scattered items", map[string]any{
			"count": len(list), "max": l.cfg.MaxScatterItems,
		})
		return
	}

	// Fan-out: create sub-intentions and forward
	spawned := make([]string, 0, len(list))
	fromAddr := msg.Intention.From // MUST be preserved (requester, includes version if any)

	for i, it := range list {
		m, ok := it.(map[string]any)
		if !ok || m == nil {
			l.sendToTrace(msg, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidMsgType, fmt.Sprintf("scatter item %d invalid (expected object)", i))
			continue
		}

		// to (required): object {context, version?}
		// Accept legacy "to_context" as string for compatibility.
		var toAddr circulation.Address
		if toObj, ok := m[circulation.KeyTo].(map[string]any); ok && toObj != nil {
			if s, _ := toObj[circulation.KeyContext].(string); strings.TrimSpace(s) != "" {
				toAddr.Context = circulation.ContextID(strings.TrimSpace(s))
			}
			if v, _ := toObj[circulation.KeyVersion].(string); strings.TrimSpace(v) != "" {
				toAddr.Version = strings.TrimSpace(v)
			}
		}
		if strings.TrimSpace(string(toAddr.Context)) == "" {
			l.sendToTrace(msg, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidToContextName, fmt.Sprintf("scatter item %d missing to.context", i))
			continue
		}
		// canonicalize internal absolute ids (no change for @...)
		toStr := string(toAddr.Context)
		if toStr != "" && toStr[0] != '@' {
			canon := canonicalInternalCtxID(toStr)
			if len(canon) > 1 {
				canon = strings.TrimSuffix(canon, "/")
			}
			toAddr.Context = circulation.ContextID(canon)
		}
		// Propagate family routing info (required for correct family dispatch on receivers).
		toAddr.Type = msg.Intention.To.Type
		toAddr.Cap = msg.Intention.To.Cap

		// Scattered is intended for fan-out to contexts (internal or remote instance routing done elsewhere),
		// but each item target MUST be a context address, not an interface address.
		// In particular, refuse "@..." here (defense-in-depth).
		// - External instance scatter should be routed as a normal message to that instance (to="@ext_..."),
		//   and the remote root would execute its own scatter.
		if strings.HasPrefix(string(toAddr.Context), "@") {
			l.sendToTrace(msg, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidToContextName,
				fmt.Sprintf("scatter item %d invalid to.context (interface address forbidden): %s", i, string(toAddr.Context)))
			continue
		}
		if !strings.HasPrefix(string(toAddr.Context), "/") {
			l.sendToTrace(msg, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidToContextName,
				fmt.Sprintf("scatter item %d invalid to.context (must be absolute '/...'): %s", i, string(toAddr.Context)))
			continue
		}

		// params
		pAny, _ := m[circulation.KeyParams]
		subParams, _ := pAny.(map[string]any)
		if subParams == nil {
			subParams = map[string]any{}
		}

		// Prevent scatter-of-scatter (fail-closed)
		if _, ok := subParams[circulation.KeyScatteredParam]; ok {
			l.sendToTrace(msg, circulation.ValueTraceCommReject, circulation.ValueReasonInvalidMsgType, fmt.Sprintf("scatter-of-scatter forbidden (item %d)", i))
			continue
		}

		// Build sub-intention:
		// - same Type + Cap
		// - to = item.to (context+version)
		// - from.context = original requester (already stamped at ingress boundary by Comm)
		// - await_response forced true
		// - correlation preserved as-is (root+parent)
		sub := circulation.Message{
			Kind: circulation.ValueKindIntention,
			Intention: circulation.Intention{
				IntentionID:   fmt.Sprintf("s%x%02x", uint64(time.Now().UnixNano()), uint8(i)), // stable per-item id; can be replaced by generator if available
				AwaitResponse: true,
				To:            toAddr,
				From:          fromAddr,
				Identity:      msg.Intention.Identity,
				Correlation:   msg.Intention.Correlation,
				Params:        subParams,
			},
		}

		// Forward to target context
		l.forwardToContext(shared.ContextAddr(string(toAddr.Context)), sub)
		spawned = append(spawned, sub.Intention.IntentionID)
	}

	// Ack response (one response)
	l.sendScatterAckOk(&msg, len(list), spawned)
}

// sendScatterAckOk
//
// Functional role (Brique DSL):
// - emit communication-level success acknowledgement for one scattered request.
//
//
// Expected Message Fields:
// - intention/message fields consumed directly:
//   - `kind`
//   - `intention`
//   - `intention.from`
//   - `intention.identity`
//   - `intention.intentionid`
// - response fields consumed indirectly via `routeEgress(ack)`:
//   - `response`
//   - `response.to`
//   - `response.from`
//   - `response.intentionid`
//
// Expected Params Keys/values:
// - none (no `msg.Intention.Params[...]` key consumed directly in this function).
//
// Produced Response Fields:
// - Valid:
//   - response.intentionid in the constructed ack.
//   - response.to in the constructed ack.
//   - response.from in the constructed ack.
//   - response.status=`ok` in the constructed ack.
//   - response.payload in the constructed ack.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - {
//       "stream": {
//         "total": "number; original scatter item count received by the root handler",
//         "spawned_intention_ids": ["list of spawned sub-intention ids actually forwarded"]
//       }
//     }
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - delegated egress may emit the normal comm-egress trace for the ack response.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - emits one success ack response through normal communication egress routing.
// - On error:
//   - none.
//
// State/Storage Effects:
// - emits one scatter ack response through Comm egress.
//
// Inputs:
// - receiver `l *CommLoop`.
// - req *circulation.Message, total int, spawned []string.
//
//
// Outputs:
// - no direct return value; observable outputs are ack construction and delegated egress side effects.
//
//
// Contract:
// - No-op if `req` is nil or not an intention.

func (l *CommLoop) sendScatterAckOk(req *circulation.Message, total int, spawned []string) {
	if req == nil || req.Kind != circulation.ValueKindIntention {
		return
	}
	ack := circulation.Message{
		Kind: circulation.ValueKindResponse,
		Response: circulation.Response{
			IntentionID: req.Intention.IntentionID,
			Status:      "ok",
			// Reply to requester (preserve requester version if any).
			To: req.Intention.From,
			// Root emits the ack as a communication-level response.
			From: circulation.Address{
				Context: circulation.ContextID(shared.RootContextID),
				Type:    circulation.ValueTypeCommunication,
				Cap:     "scatter_ack",
			},
			Identity: req.Intention.Identity,
			Payload: map[string]any{
				"stream": map[string]any{
					"total":                 total,
					"spawned_intention_ids": spawned,
				},
			},
		},
	}
	l.routeEgress(ack)
}

// sendScatterAckError
//
// Functional role (Brique DSL):
// - emit communication-level error acknowledgement for one scattered request.
//
//
// Expected Message Fields:
// - intention/message fields consumed directly:
//   - `kind`
//   - `intention`
//   - `intention.from`
//   - `intention.identity`
//   - `intention.intentionid`
// - response fields consumed indirectly via `routeEgress(ack)`:
//   - `response`
//   - `response.to`
//   - `response.from`
//   - `response.intentionid`
//
// Expected Params Keys/values:
// - none (no `msg.Intention.Params[...]` key consumed directly in this function).
//
// Produced Response Fields:
// - Valid:
//   - response.intentionid in the constructed ack.
//   - response.to in the constructed ack.
//   - response.from in the constructed ack.
//   - response.status=`error` in the constructed ack.
//   - response.error in the constructed ack.
// - On error:
//   - none beyond the emitted error ack itself.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none; this helper emits `response.error.details`, not `response.payload`.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - delegated egress may emit the normal comm-egress trace for the error ack.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - emits one error ack response through normal communication egress routing.
// - On error:
//   - none.
//
// State/Storage Effects:
// - emits one scatter error ack response through Comm egress.
//
// Inputs:
// - receiver `l *CommLoop`.
// - req *circulation.Message, code string, message string, details map[string]any.
//
//
// Outputs:
// - no direct return value; observable outputs are ack construction and delegated egress side effects.
//
//
// Contract:
// - No-op if `req` is nil or not an intention.

func (l *CommLoop) sendScatterAckError(req *circulation.Message, code string, message string, details map[string]any) {
	if req == nil || req.Kind != circulation.ValueKindIntention {
		return
	}
	errObj := &circulation.ResponseProblem{
		Origin:  circulation.ValueOriginCommunication,
		Code:    code,
		Message: message,
	}
	if details != nil {
		errObj.Details = details
	}
	ack := circulation.Message{
		Kind: circulation.ValueKindResponse,
		Response: circulation.Response{
			IntentionID: req.Intention.IntentionID,
			Status:      "error",
			// Reply to requester (preserve requester version if any).
			To: req.Intention.From,
			// Root emits the ack as a communication-level response.
			From: circulation.Address{
				Context: circulation.ContextID(shared.RootContextID),
				Type:    circulation.ValueTypeCommunication,
				Cap:     "scatter_ack",
			},
			Identity: req.Intention.Identity,
			Error:    errObj,
		},
	}
	l.routeEgress(ack)
}
