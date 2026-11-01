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

package admission

import (
	"brique_engine/circulation"
	"brique_engine/security/access"
	"brique_engine/security/audit"
	"brique_engine/security/trust"
)

// baseRecord is what is known about an authenticated caller. Nothing here comes
// from the message body; identifiers are the ones the signature covers.
func (context *Context) baseRecord(channel string, intention circulation.Intention) audit.Record {
	return audit.Record{
		PolicyRevision: context.PolicyRevision(),
		PolicyHash:     context.PolicyHash(),
		Principal:      context.PrincipalID(),
		KeyID:          context.SigningKeyID(),
		Actor:          context.ActorID(),
		Channel:        channel,
		RootIntention:  context.RootIntentionID(),
		Intention:      intention.IntentionID,
		Context:        string(intention.To.Context),
		Capacity:       intention.To.Type + "/" + intention.To.Cap,
	}
}

// RecordDenial journals a refusal that is neither a capability nor a resource
// one (replay, unsupported, internal...).
func (context *Context) RecordDenial(recorder audit.Recorder, channel string, intention circulation.Intention, layer, reason string) {
	if recorder == nil || !context.Valid() {
		return
	}
	record := context.baseRecord(channel, intention)
	record.Layer, record.Reason = layer, reason
	recorder.Record(record)
}

// RecordCapabilityDenial journals the refusal of an engine or user capacity and
// names the explicit deny rule when there is one. It runs on the refusal path
// only.
func (context *Context) RecordCapabilityDenial(recorder audit.Recorder, runtime *trust.Runtime, channel string, intention circulation.Intention) {
	if recorder == nil || !context.Valid() {
		return
	}
	record := context.baseRecord(channel, intention)
	record.Layer, record.Reason = audit.LayerCapability, audit.ReasonCapabilityDenied
	if runtime != nil {
		to := intention.To
		// The refusal path may afford one more evaluation: if the principal alone
		// would have been allowed, the actor is what refused.
		var classVerbs []string
		if class, known := access.ClassOf(to.Type, to.Cap); known {
			classVerbs = class.Verbs()
		}
		if runtime.AuthorizeCapabilityVerbs(context.trustAdmission.StripActor(), string(to.Context), to.Type, to.Cap, classVerbs) {
			record.Reason = audit.ReasonActorDenied
		}
		path := string(to.Context)
		queries := []trust.DenialQuery{{Context: path, Family: to.Type, Name: to.Cap}}
		if to.Type == circulation.ValueTypeUser {
			queries = append(queries, trust.DenialQuery{Context: path, Kind: "capacity", ID: to.Cap, Action: "execute"})
		} else if class, known := access.ClassOf(to.Type, to.Cap); known {
			for _, verb := range class.Verbs() {
				queries = append(queries, trust.DenialQuery{Context: path, Verb: verb})
			}
		}
		for _, query := range queries {
			if id := runtime.ExplainDenial(context.trustAdmission, query); id != "" {
				record.PermissionID = id
				break
			}
		}
	}
	recorder.Record(record)
}

// RecordResourceDenial journals the first resource of the set that is refused.
func (context *Context) RecordResourceDenial(recorder audit.Recorder, runtime *trust.Runtime, channel string, intention circulation.Intention, accesses []access.ResourceAccess) {
	if recorder == nil || !context.Valid() || runtime == nil {
		return
	}
	record := context.baseRecord(channel, intention)
	record.Layer, record.Reason = audit.LayerResource, audit.ReasonResourceDenied
	for _, item := range accesses {
		normalized, err := access.Normalize(item)
		if err != nil {
			record.Reason = audit.ReasonInternal
			break
		}
		if runtime.AuthorizeResource(context.trustAdmission, string(normalized.Context), normalized.Kind, normalized.ID, normalized.Action) {
			continue
		}
		if runtime.AuthorizeResource(context.trustAdmission.StripActor(), string(normalized.Context), normalized.Kind, normalized.ID, normalized.Action) {
			record.Reason = audit.ReasonActorDenied
		}
		record.Context = string(normalized.Context)
		record.Resource = &audit.Resource{Kind: normalized.Kind, ID: normalized.ID, Action: normalized.Action}
		record.PermissionID = runtime.ExplainDenial(context.trustAdmission, trust.DenialQuery{
			Context: string(normalized.Context), Kind: normalized.Kind, ID: normalized.ID, Action: normalized.Action,
		})
		break
	}
	recorder.Record(record)
}
