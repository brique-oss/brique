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

package reflexive

// reflexive_refusal.go
//
// What an EXTERNAL caller is told when it is refused (SECURITY.md §4.4,
// D06): a capacity it may not call and one that does not exist look
// alike; an element it may not touch and one that is absent look alike, also
// inside a multi-element request, where each element is decided on its own.

import (
	"encoding/json"
	"errors"
	"os"
	"strings"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/security/refusal"
)

// itemSpec describes a capacity whose request is a list of elements.
type itemSpec struct {
	// key is the parameter holding the list.
	key string
	// entry builds the answer for an element that is refused, in the shape the
	// handler itself uses for an element it did not find.
	entry func(item map[string]any) map[string]any
}

var reflexiveItemSpecs = map[string]itemSpec{
	"read.meaning": {key: circulation.KeyInput, entry: func(item map[string]any) map[string]any {
		kind, _ := item[circulation.KeyElementKind].(string)
		name, _ := item[circulation.KeyElementName].(string)
		return map[string]any{
			circulation.KeyOK: false, circulation.KeyElementKind: kind, circulation.KeyElementName: name,
			circulation.KeyError: refusal.ItemError(),
		}
	}},
	"edit.create":        {key: circulation.KeyItems, entry: editEntry},
	"edit.delete":        {key: circulation.KeyItems, entry: editEntry},
	"edit.patch_meaning": {key: circulation.KeyItems, entry: editEntry},
	"edit.duplicate":     {key: circulation.KeyItems, entry: editEntry},
}

// editEntry mirrors the entry the edit handlers give an element they did not
// find: type and name, or type, source and target for a duplication.
func editEntry(item map[string]any) map[string]any {
	kind, _ := item[circulation.KeyType].(string)
	entry := map[string]any{circulation.KeyType: normType(kind), circulation.KeyOK: false, circulation.KeyError: refusal.ItemError()}
	_, hasSource := item[circulation.KeySourceName]
	_, hasTarget := item[circulation.KeyTargetName]
	if hasSource || hasTarget {
		source, _ := item[circulation.KeySourceName].(string)
		target, _ := item[circulation.KeyTargetName].(string)
		entry[circulation.KeySourceName], entry[circulation.KeyTargetName] = strings.TrimSpace(source), strings.TrimSpace(target)
		return entry
	}
	name, _ := item[circulation.KeyName].(string)
	entry[circulation.KeyName] = strings.TrimSpace(name)
	return entry
}

// deniedItem remembers where a refused element stood in the request.
type deniedItem struct {
	index int
	entry map[string]any
}

// admitExternalReflexive is the door of an external request. It authorizes the
// capacity, then the elements: for a list-shaped request each element is decided
// alone, the refused ones are taken out of what the handler will see, and their
// answers are put back in place (finalizeExternalResponse).
func (l *ReflexiveLoop) admitExternalReflexive(delivery junction.Delivery) (circulation.Message, []deniedItem, error) {
	message, _ := delivery.Message()
	spec, listShaped := reflexiveItemSpecs[message.Intention.To.Cap]
	if !listShaped {
		return message, nil, l.authorizeExternalReflexive(delivery)
	}
	admitted, _ := delivery.ExternalAdmission()
	if err := l.authorizeExternalReflexiveCapability(delivery); err != nil {
		return message, nil, err
	}
	// The whole request must still be well formed, whatever the rights.
	if _, err := l.extractResourceAccesses(message.Intention); err != nil {
		return message, nil, err
	}
	provider, _ := l.frame.CtxCommReg.(junction.TrustRuntimeProvider)
	runtime := provider.TrustRuntime()
	raw, _ := message.Intention.Params[spec.key].([]any)
	kept := make([]any, 0, len(raw))
	var denied []deniedItem
	position := 0 // among the map entries: the handler answers those, in order
	for _, value := range raw {
		item, isMap := value.(map[string]any)
		if !isMap {
			kept = append(kept, value)
			continue
		}
		index := position
		position++
		single := message.Intention
		single.Params = shallowCopyParams(message.Intention.Params)
		single.Params[spec.key] = []any{value}
		accesses, err := l.extractResourceAccesses(single)
		if err != nil {
			return message, nil, err
		}
		if admitted.AuthorizeResources(runtime, accesses) {
			kept = append(kept, value)
			continue
		}
		admitted.RecordResourceDenial(junction.AuditOf(l.frame.CtxCommReg), runtime, "wire", single, accesses)
		denied = append(denied, deniedItem{index: index, entry: spec.entry(item)})
	}
	if len(denied) > 0 {
		filtered := message.Intention
		filtered.Params = shallowCopyParams(message.Intention.Params)
		filtered.Params[spec.key] = kept
		message.Intention = filtered
	}
	return message, denied, nil
}

func shallowCopyParams(params map[string]any) map[string]any {
	out := make(map[string]any, len(params))
	for key, value := range params {
		out[key] = value
	}
	return out
}

// refuseExternalReflexive answers a request the door refused, in canonical form.
func (l *ReflexiveLoop) refuseExternalReflexive(request junction.Delivery, cause error) {
	root, valid := request.Message()
	if !valid || root.Kind != circulation.ValueKindIntention {
		return
	}
	switch {
	case errors.Is(cause, errReflexiveCapabilityUnauthorized):
		l.emitExternalReflexiveProblem(request, root.Intention, refusal.CapabilityDenied(circulation.ValueOriginReflexive))
	case errors.Is(cause, errReflexiveResourceUnauthorized):
		l.emitExternalReflexiveProblem(request, root.Intention, refusal.NotFound(circulation.ValueOriginReflexive))
	case errors.Is(cause, errReflexiveTrustUnavailable), errors.Is(cause, errReflexiveExternalInvalidMessage):
		l.emitExternalReflexiveError(request, circulation.ValueCodeUnauthorized, cause.Error())
	default:
		// The request is malformed (extraction failed): that says nothing about
		// what exists, only about its shape.
		l.emitExternalReflexiveError(request, circulation.ValueCodeInvalid, cause.Error())
	}
}

func (l *ReflexiveLoop) emitExternalReflexiveProblem(request junction.Delivery, in circulation.Intention, problem *circulation.ResponseProblem) {
	response := errorResp(in, problem.Code, problem.Details, problem.Message)
	response.Response.Error.Origin = problem.Origin
	delivery, reservation, err := junction.BindExternalResponse(request, response)
	if err != nil {
		return
	}
	if !l.emitDeliveryToComm(delivery) && reservation.Valid() {
		_ = reservation.Abort()
	}
}

// finalizeExternalResponse is applied to every response owed to an external
// caller just before it is bound: absence has one error, and the answers of the
// refused elements are put back at their place.
func (l *ReflexiveLoop) finalizeExternalResponse(msg *circulation.Message) {
	refusal.Canonicalize(&msg.Response)
	if msg.Response.Status != circulation.ValueStatusOK || msg.Response.Payload == nil {
		return
	}
	if _, hasList := msg.Response.Payload[circulation.KeyResult]; !hasList {
		return
	}
	var items []any
	encoded, err := json.Marshal(msg.Response.Payload[circulation.KeyResult])
	if err != nil || json.Unmarshal(encoded, &items) != nil {
		return
	}
	denied := l.takeDenied(msg.Response.IntentionID)
	changed := len(denied) > 0
	merged := make([]any, 0, len(items)+len(denied))
	nextDenied, nextItem := 0, 0
	for position := 0; nextDenied < len(denied) || nextItem < len(items); position++ {
		if nextDenied < len(denied) && denied[nextDenied].index == position {
			merged = append(merged, denied[nextDenied].entry)
			nextDenied++
			continue
		}
		if nextItem >= len(items) {
			break
		}
		item := items[nextItem]
		nextItem++
		if m, isMap := item.(map[string]any); isMap && refusal.CanonicalizeItem(m) {
			changed = true
		}
		merged = append(merged, item)
	}
	if !changed {
		return // leave the handler's own payload untouched (null stays null)
	}
	payload := shallowCopyParams(msg.Response.Payload)
	payload[circulation.KeyResult] = merged
	msg.Response.Payload = payload
}

func (l *ReflexiveLoop) rememberDenied(intentionID string, denied []deniedItem) {
	if len(denied) == 0 {
		return
	}
	l.deniedMu.Lock()
	if l.denied == nil {
		l.denied = make(map[string][]deniedItem)
	}
	l.denied[intentionID] = denied
	l.deniedMu.Unlock()
}

func (l *ReflexiveLoop) takeDenied(intentionID string) []deniedItem {
	l.deniedMu.Lock()
	defer l.deniedMu.Unlock()
	return l.denied[intentionID]
}

func (l *ReflexiveLoop) forgetDenied(intentionID string) {
	l.deniedMu.Lock()
	delete(l.denied, intentionID)
	l.deniedMu.Unlock()
}

// externalRequestMissing is true when the caller being served is external and the
// path does not exist.
func (l *ReflexiveLoop) externalRequestMissing(in circulation.Intention, path string) bool {
	if _, bound := l.externalRequest(strings.TrimSpace(in.IntentionID)); !bound {
		return false
	}
	_, err := os.Stat(path)
	return os.IsNotExist(err)
}

// answerAllRefused replies to a list-shaped request in which every element was
// refused: there is nothing left for the handler to do, and the answer is the
// list of refusals, exactly as if the elements had all been absent.
func (l *ReflexiveLoop) answerAllRefused(delivery junction.Delivery, denied []deniedItem) {
	message, _ := delivery.Message()
	cleanup, err := l.bindExternalRequest(delivery)
	if err != nil {
		l.emitExternalReflexiveError(delivery, circulation.ValueCodeUnavailable, err.Error())
		return
	}
	defer cleanup()
	entries := make([]any, 0, len(denied))
	for _, item := range denied {
		entries = append(entries, item.entry)
	}
	l.emitResponseOK(message.Intention, map[string]any{circulation.KeyResult: entries})
}

// allRefused: the door took every element out of a list-shaped request.
func allRefused(message circulation.Message, denied []deniedItem) bool {
	if len(denied) == 0 {
		return false
	}
	spec := reflexiveItemSpecs[message.Intention.To.Cap]
	remaining, _ := message.Intention.Params[spec.key].([]any)
	for _, value := range remaining {
		if _, isMap := value.(map[string]any); isMap {
			return false
		}
	}
	return true
}
