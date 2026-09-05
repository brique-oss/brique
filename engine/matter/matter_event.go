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

package matter

// matter/matter_event.go
//
// Subscriptions + notifications for MatterLoop.
//
// Contract (as agreed):
// - Subscribe/unsubscribe exist as capabilities: "matter_subscribe" / "matter_unsubscribe".
// - Subscriptions are stored in MatterLoop ONLY for brique-mode matters.
// - For wrapper-mode matters, subscription storage/notification lives in the wrapper (so we delegate).
// - Notifications are emitted as Intention messages routed to the subscriber address (often @ui_*).
// - We notify on:
//   - matter.write (brique-mode)   -> event: "matter_written"
//   - matter.delete (brique-mode)  -> event: "matter_deleted"
// - No notification on create (doesn't make sense in this model).

import (
	"fmt"
	"strings"
	"time"

	"brique_engine/circulation"
	"brique_engine/configuration"
	"brique_engine/junction"
	"brique_engine/shared"
)

// rewriteToWrapperTransport
//
// Functional role (Brique DSL):
// - rewrite target context from matter-local address to wrapper boundary transport address for wrapper-mode delegation.
//
//
// Expected Message Fields:
// - message fields consumed directly:
//   - `intention`
//   - `intention.to`
//   - `intention.to.context`
//   - `kind`
//
// Expected Params Keys/values:
// - none.
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
//   - {
//       "reason": "string carried in the returned details map when wrapper routing rewrite fails"
//     }
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none directly from this function.
// - On error:
//   - none directly from this function.
//
// State/Storage Effects:
// - mutates `msg.Intention.To.Context` in place on successful wrapper transport rewrite.
//
// Inputs:
//
// - receiver `l *MatterLoop`.
// - msg *circulation.Message, entry CatalogEntry.
//
//
// Outputs:
//
// - returns (wrapperName string, ok bool, code string, details map[string]any, text string).
//
//
// Contract:
// - Rewrites `msg.Intention.To.Context` in place only when wrapper boundary resolution succeeds.
// - Leaves `msg` unchanged on failure.
//

func (l *MatterLoop) rewriteToWrapperTransport(msg *circulation.Message, entry CatalogEntry) (wrapperName string, ok bool, code string, details map[string]any, text string) {
	if l == nil || msg == nil || msg.Kind != circulation.ValueKindIntention {
		return "", false, circulation.ValueCodeInternal, map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidMsgType}, "invalid message kind"
	}

	in := msg.Intention
	origToCtx := strings.TrimSpace(string(in.To.Context))

	// wrapper name from catalog projection (strict)
	if entry.Brique != nil {
		if s, ok2 := entry.Brique[configuration.KeyWrpName].(string); ok2 {
			wrapperName = strings.TrimSpace(s)
		}
	}
	if wrapperName == "" {
		return "", false, circulation.ValueCodeRefused, map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidMode}, "wrapper mode requires brique wrapper name"
	}

	// boundary resolution
	if l.frame == nil || l.frame.CtxCommReg == nil {
		return wrapperName, false, circulation.ValueCodeInternal, map[string]any{circulation.KeyReason: circulation.ValueReasonMissingContextFrame}, "missing context comm registry"
	}
	boundaryID, ok2 := l.frame.CtxCommReg.ResolveWrapperBoundary(wrapperName)
	if !ok2 || strings.TrimSpace(string(boundaryID)) == "" {
		return wrapperName, false, circulation.ValueCodeRefused, map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidToContextName}, "wrapper boundary not found"
	}

	// rel path (best-effort; empty if origToCtx is boundary or not under boundary)
	rel := ""
	if origToCtx != "" {
		prefix := strings.TrimSuffix(string(boundaryID), "/")
		if origToCtx == prefix {
			rel = ""
		} else if strings.HasPrefix(origToCtx, prefix+"/") {
			rel = strings.TrimPrefix(origToCtx, prefix+"/")
		}
	}

	// rewrite to.context
	msg.Intention.To.Context = circulation.ContextID("@wrapper_" + wrapperName + ":/" + rel)
	return wrapperName, true, "", nil, ""
}

// -----------------------------
// Subscription table (brique-mode only)
// -----------------------------

type MatterSubscription struct {
	ID        string
	MatterID  string
	Target    circulation.Address // where to send notifications (typically in.From)
	CreatedAt int64
}

// ensureSubsInit
//
// Functional role (Brique DSL):
// - initialize local subscription table lazily under lock.
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
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
// - initializes the in-memory subscription table when absent.
//
// Inputs:
//
// - none.
//
//
// Outputs:
//
// - no direct return value; effects are produced via state updates, emitted messages, or filesystem I/O.
//
//
// Contract:
// - Creates `l.subs` only when absent.
// - Repeated calls after initialization are no-ops apart from lock acquisition.
//

func (l *MatterLoop) ensureSubsInit() {
	// Defensive: in case older constructors didn't init.
	l.subsMu.Lock()
	if l.subs == nil {
		l.subs = make(map[string]map[string]MatterSubscription)
	}
	l.subsMu.Unlock()
}

// addSub
//
// Functional role (Brique DSL):
// - insert or replace one brique-mode matter subscription in local subscription table.
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
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
// - inserts or replaces one subscription in the in-memory subscription table.
//
// Inputs:
//
// - matterID string, sub MatterSubscription.
//
//
// Outputs:
//
// - no direct return value; effects are produced via state updates, emitted messages, or filesystem I/O.
//
//
// Contract:
// - Empty `matterID` or subscription ID is ignored.
// - Reusing the same subscription ID for a matter overwrites the previous subscription entry.
//

func (l *MatterLoop) addSub(matterID string, sub MatterSubscription) {
	if matterID == "" || sub.ID == "" {
		return
	}
	l.ensureSubsInit()
	l.subsMu.Lock()
	defer l.subsMu.Unlock()
	m := l.subs[matterID]
	if m == nil {
		m = make(map[string]MatterSubscription)
		l.subs[matterID] = m
	}
	m[sub.ID] = sub
}

// removeSub
//
// Functional role (Brique DSL):
// - remove one brique-mode matter subscription from local subscription table.
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
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
// - removes one subscription and may delete the per-matter bucket.
//
// Inputs:
//
// - matterID, subID string.
//
//
// Outputs:
//
// - returns bool.
//
//
// Contract:
// - Deletes the per-matter bucket when its last subscription is removed.
// - Returns `false` for missing matter bucket or unknown subscription id without mutation.
//

func (l *MatterLoop) removeSub(matterID, subID string) bool {
	if matterID == "" || subID == "" {
		return false
	}
	l.ensureSubsInit()
	l.subsMu.Lock()
	defer l.subsMu.Unlock()
	m := l.subs[matterID]
	if m == nil {
		return false
	}
	if _, ok := m[subID]; !ok {
		return false
	}
	delete(m, subID)
	if len(m) == 0 {
		delete(l.subs, matterID)
	}
	return true
}

// listSubs
//
// Functional role (Brique DSL):
// - list current local subscriptions for one brique-mode matter.
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
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
// - allocates a snapshot slice from the in-memory subscription table.
//
// Inputs:
//
// - matterID string.
//
//
// Outputs:
//
// - returns []MatterSubscription.
//
//
// Contract:
// - Returns a snapshot slice; callers do not hold the subscription lock.
// - Subscription ordering in the returned slice is unspecified.
//

func (l *MatterLoop) listSubs(matterID string) []MatterSubscription {
	if matterID == "" {
		return nil
	}
	l.ensureSubsInit()
	l.subsMu.RLock()
	defer l.subsMu.RUnlock()
	m := l.subs[matterID]
	if len(m) == 0 {
		return nil
	}
	out := make([]MatterSubscription, 0, len(m))
	for _, s := range m {
		out = append(out, s)
	}
	return out
}

// capMatterSubscribe
//
// External interaction contract source of truth:
// - engine/matter/capacity/matter_subscribe.json
//
// Keep this header minimal.
// Update the capacity descriptor when external behavior changes.
func (l *MatterLoop) capMatterSubscribe(msg circulation.Message) {
	if msg.Kind != circulation.ValueKindIntention {
		return
	}
	in := msg.Intention

	matterID := shared.GetParamString(in.Params, circulation.KeyMatterID)
	matterID = strings.TrimSpace(matterID)
	if matterID == "" {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonMissingMatterID},
			"matter_id is required"))
		return
	}

	// Strict catalog check
	entry, ok := l.catalogGetMatter(matterID)
	if !ok {
		l.emitResponseError(errorResp(in, circulation.ValueCodeRefused,
			map[string]any{circulation.KeyReason: circulation.ValueReasonMatterNotInCatalog, circulation.KeyMatterID: matterID},
			"matter not in runtime catalog"))
		return
	}

	mode := catalogSubstanceMode(entry)
	if mode == "" {
		mode = circulation.ValueModeBrique
	}

	// Only brique or wrapper are allowed.
	if mode != circulation.ValueModeBrique && mode != circulation.ValueModeWrapper {
		l.emitResponseError(errorResp(in, circulation.ValueCodeRefused,
			map[string]any{
				circulation.KeyReason:        circulation.ValueReasonInvalidMode,
				circulation.KeyMatterID:      matterID,
				circulation.KeySubstanceMode: mode,
			},
			"subscribe is only supported for brique or wrapper mode"))
		return
	}

	// Wrapper mode -> delegate to wrapper (wrapper must be running).
	if mode == circulation.ValueModeWrapper {
		wrapperName, ok, code, details, text := l.rewriteToWrapperTransport(&msg, entry)
		if !ok {
			if details == nil {
				details = map[string]any{}
			}
			details[circulation.KeyMatterID] = matterID
			details[circulation.KeySubstanceMode] = mode
			l.emitResponseError(errorResp(in, code, details, text))
			return
		}

		st, ok := l.frame.Wrappers[wrapperName]
		if !ok || st.ProcState != junction.ProcRunning {
			l.emitResponseError(errorResp(in, circulation.ValueCodeRefused,
				map[string]any{circulation.KeyReason: circulation.ValueReasonWrapperNotRunning, circulation.KeyWrapper: wrapperName},
				"wrapper is not running"))
			return
		}

		// Pass-through to wrapper.
		l.emitToComm(msg)
		return
	}

	// Brique-mode -> store locally.
	subID := ""
	if in.Params != nil {
		if s, ok := in.Params[circulation.KeySubID].(string); ok {
			subID = strings.TrimSpace(s)
		}
	}
	if subID == "" {
		subID = fmt.Sprintf("sub_%d", time.Now().UnixNano())
	}

	target := in.From
	// Optional override: params[KeyTarget] could be something else later (minimal: ignore unless needed).

	l.addSub(matterID, MatterSubscription{
		ID:        subID,
		MatterID:  matterID,
		Target:    target,
		CreatedAt: time.Now().UnixNano(),
	})

	l.emitResponseOK(in, map[string]any{
		circulation.KeyOK:            true,
		circulation.KeyMatterID:      matterID,
		circulation.KeySubID:         subID,
		circulation.KeySubstanceMode: mode,
	})
}

// capMatterUnsubscribe
//
// External interaction contract source of truth:
// - engine/matter/capacity/matter_unsubscribe.json
//
// Keep this header minimal.
// Update the capacity descriptor when external behavior changes.
func (l *MatterLoop) capMatterUnsubscribe(msg circulation.Message) {
	if msg.Kind != circulation.ValueKindIntention {
		return
	}
	in := msg.Intention

	matterID := shared.GetParamString(in.Params, circulation.KeyMatterID)
	matterID = strings.TrimSpace(matterID)
	if matterID == "" {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonMissingMatterID},
			"matter_id is required"))
		return
	}
	subID := ""
	if in.Params != nil {
		if s, ok := in.Params[circulation.KeySubID].(string); ok {
			subID = strings.TrimSpace(s)
		}
	}
	if subID == "" {
		l.emitResponseError(errorResp(in, circulation.ValueCodeInvalid,
			map[string]any{circulation.KeyReason: circulation.ValueReasonInvalidRequest, circulation.KeyMatterID: matterID},
			"sub_id is required"))
		return
	}

	// Strict catalog check to decide delegation.
	entry, ok := l.catalogGetMatter(matterID)
	if !ok {
		l.emitResponseError(errorResp(in, circulation.ValueCodeRefused,
			map[string]any{circulation.KeyReason: circulation.ValueReasonMatterNotInCatalog, circulation.KeyMatterID: matterID},
			"matter not in runtime catalog"))
		return
	}
	mode := catalogSubstanceMode(entry)
	if mode == "" {
		mode = circulation.ValueModeBrique
	}

	// Wrapper mode -> delegate to wrapper.
	if mode == circulation.ValueModeWrapper {
		wrapperName, ok, code, details, text := l.rewriteToWrapperTransport(&msg, entry)
		if !ok {
			if details == nil {
				details = map[string]any{}
			}
			details[circulation.KeyMatterID] = matterID
			details[circulation.KeySubstanceMode] = mode
			l.emitResponseError(errorResp(in, code, details, text))
			return
		}
		st, ok := l.frame.Wrappers[wrapperName]
		if !ok || st.ProcState != junction.ProcRunning {
			l.emitResponseError(errorResp(in, circulation.ValueCodeRefused,
				map[string]any{circulation.KeyReason: circulation.ValueReasonWrapperNotRunning, circulation.KeyWrapper: wrapperName},
				"wrapper is not running"))
			return
		}
		l.emitToComm(msg)
		return
	}

	// Brique-mode -> remove locally.
	removed := l.removeSub(matterID, subID)

	l.emitResponseOK(in, map[string]any{
		circulation.KeyOK:            true,
		circulation.KeyMatterID:      matterID,
		circulation.KeySubID:         subID,
		circulation.KeyRemoved:       removed,
		circulation.KeySubstanceMode: mode,
	})
}

// notifyBriqueMatterWritten
//
// Functional role (Brique DSL):
// - emit `matter_written` event intentions to all local subscribers of one brique-mode matter.
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
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
//   - none directly from this function.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - kind and intention when forwarding `matter_written` event messages to Comm.
// - On error:
//   - none.
//
// State/Storage Effects:
// - reads the in-memory subscription table.
// - may emit one event intention per local subscription.
//
// Inputs:
//
// - receiver `l *MatterLoop`.
// - matterID string, rev int64, srcIntentionID string.
//
//
// Outputs:
//
// - no direct return value; effects are produced via state updates, emitted messages, or filesystem I/O.
//
//
// Contract:
// - No message is emitted when there are no local subscribers.
// - Per-subscriber notify failures do not abort remaining notifications.
//

func (l *MatterLoop) notifyBriqueMatterWritten(matterID string, rev int64, srcIntentionID string) {
	subs := l.listSubs(matterID)
	if len(subs) == 0 {
		return
	}
	for _, s := range subs {
		params := map[string]any{
			circulation.KeyEvent:         circulation.ValueEventMatterWritten,
			circulation.KeyOp:            circulation.ValueOpWrite,
			circulation.KeyMatterID:      matterID,
			circulation.KeyRevision:      rev,
			circulation.KeySubstanceMode: circulation.ValueModeBrique,
		}
		if srcIntentionID != "" {
			params[circulation.KeySourceIntentionID] = srcIntentionID
		}
		l.emitToComm(circulation.Message{
			Kind: circulation.ValueKindIntention,
			Intention: circulation.Intention{
				IntentionID: circulation.NewIntentionID(),
				To:          s.Target,
				From:        circulation.Address{Context: circulation.ContextID(l.frame.CtxId), Type: circulation.ValueTypeMatter, Cap: circulation.ValueCapMatterEvent},
				Params:      params,
			},
		})
	}
}

// notifyBriqueMatterDeleted
//
// Functional role (Brique DSL):
// - emit `matter_deleted` event intentions to all local subscribers of one brique-mode matter.
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
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
//   - none directly from this function.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - kind and intention when forwarding `matter_deleted` event messages to Comm.
// - On error:
//   - none.
//
// State/Storage Effects:
// - reads the in-memory subscription table.
// - may emit one event intention per local subscription.
//
// Inputs:
//
// - receiver `l *MatterLoop`.
// - matterID string, srcIntentionID string.
//
//
// Outputs:
//
// - no direct return value; effects are produced via state updates, emitted messages, or filesystem I/O.
//
//
// Contract:
// - No message is emitted when there are no local subscribers.
//

func (l *MatterLoop) notifyBriqueMatterDeleted(matterID string, srcIntentionID string) {
	subs := l.listSubs(matterID)
	if len(subs) == 0 {
		return
	}
	for _, s := range subs {
		params := map[string]any{
			circulation.KeyEvent:         circulation.ValueEventMatterDeleted,
			circulation.KeyOp:            circulation.ValueOpDelete,
			circulation.KeyMatterID:      matterID,
			circulation.KeySubstanceMode: circulation.ValueModeBrique,
		}
		if srcIntentionID != "" {
			params[circulation.KeySourceIntentionID] = srcIntentionID
		}
		l.emitToComm(circulation.Message{
			Kind: circulation.ValueKindIntention,
			Intention: circulation.Intention{
				IntentionID: circulation.NewIntentionID(),
				To:          s.Target,
				From:        circulation.Address{Context: circulation.ContextID(l.frame.CtxId), Type: circulation.ValueTypeMatter, Cap: circulation.ValueCapMatterEvent},
				Params:      params,
			},
		})
	}
}
