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

package circulation

// Key* constants define the vocabulary of keys used across Brique JSON exchanges:
// - envelope fields (message/intention/response)
// - generic maps: params, payload, error.details, etc.
//
// They are plain strings on purpose, so the same vocabulary can be reused in any target language.

const (
	// Envelope
	KeyKind      = "kind"
	KeyTS        = "ts"
	KeyIntention = "intention"
	KeyResponse  = "response"
	KeyTrace     = "trace"

	// Intention / Response common
	KeyType        = "type"
	KeyIntentionID = "intention_id"

	KeyTo      = "to"
	KeyFrom    = "from"
	KeyContext = "context"
	KeyVersion = "version"

	KeyIdentity  = "identity"
	KeyID        = "id"
	KeyPubKey    = "pubkey"
	KeySignature = "signature"

	KeyCap               = "cap"
	KeyAwaitResponse     = "await_response"
	KeyMatters           = "matters"
	KeyParams            = "params"
	KeyScatteredParam    = "scattered"
	KeyCorrelation       = "correlation"
	KeyRootIntentionID   = "root_intention_id"
	KeyParentIntentionID = "parent_intention_id"
	KeySourceIntentionID = "source_intention_id"

	// Response
	KeyStatus  = "status"
	KeyPayload = "payload"
	KeyError   = "error"
	KeyOrigin  = "origin"
	KeyCode    = "code"
	KeyMessage = "message"
	KeyDetails = "details"

	// Common outcome/meta keys (payload/details)
	KeyOK      = "ok"
	KeyReason  = "reason"
	KeyWarning = "warning"
	KeyRemoved = "removed"

	// Common content keys (payload/details)
	KeyBytes    = "bytes"
	KeyEncoding = "encoding"
	KeySize     = "size"

	ValueEncodingBase64 = "base64"
	KeyHTTP             = "http"
	KeyEvent            = "event"
	KeyOp               = "op"

	// Problem payload keys (HTTP substance getter)
	KeyDetail         = "detail"
	KeyExpectedMethod = "expected_method"
	KeyRID            = "rid"
	KeyFile           = "file"
	KeyCatalogKind    = "catalog_kind"
	KeyExpectedKind   = "expected_kind"
	KeyWrapper        = "wrapper"

	// Common param keys (Intention.Params)
	KeyMatterID      = "matter_id"
	KeyMatterIDs     = "matter_ids"
	KeySchemaRef     = "schema_ref"
	KeyExist         = "exist"
	KeyResult        = "result"
	KeySubstanceMode = "substance_mode"
	KeySubstanceType = "substance_type"
	KeyStructureID   = "structure_id"

	KeyMatter         = "matter"
	KeySourceMatterID = "source_matter_id"
	KeyTargetMatterID = "target_matter_id"
	KeyDerivedFrom    = "derived_from"

	KeyReadMode          = "read_mode"
	KeyMeaning           = "meaning"
	KeyObjective         = "objective"
	KeySubjective        = "subjective"
	KeyFunctional        = "functional"
	KeyFormat            = "format"
	KeyData              = "data"
	KeyHTTPData          = "http_data"
	KeyBrique            = "brique"
	KeyVocabulary        = "vocabulary"
	KeyAxis              = "axis"
	KeySections          = "sections"
	KeyIncludeResolution = "include_resolution"
	KeyDSLRoot           = "#root"
	KeyResolution        = "resolution"
	KeyDetailParam       = "detail"
	KeyRole              = "role"
	KeyInputs            = "inputs"
	KeyOutputs           = "outputs"

	KeyWantMeaning    = "want_meaning"
	KeyWantFunctional = "want_functional"
	KeyWantBrique     = "want_brique"

	KeyStructure         = "structure"
	KeySourceStructureID = "source_structure_id"
	KeyTargetStructureID = "target_structure_id"

	KeyItems   = "items"
	KeySlot    = "slot"
	KeyResolve = "resolve"

	KeyStreamID = "stream_id"
	KeyChunk    = "chunk"

	KeyMode             = "mode"
	KeyRevision         = "revision"
	KeyExpectedRevision = "expected_revision"
	KeyCurrentRevision  = "current_revision"

	KeyChild        = "child"
	KeyName         = "name"
	KeySubID        = "sub_id"
	KeyTarget       = "target"
	KeyExpRev       = "expected_rev" // legacy alias of KeyExpectedRevision
	KeyLegacyExpRev = "exp_rev"      // legacy descriptor spelling
	KeyCurRev       = "current_rev"
	KeyPatches      = "patches"
	KeyErrorText    = "error"
	KeyPayloadType  = "payload_type"

	// reflexive loop
	KeyInput            = "input"
	KeyElementKind      = "element_kind"
	KeyElementName      = "element_name"
	KeyPath             = "path"
	KeyDesc             = "descriptor"
	KeyDescriptorJSON   = "descriptor_json"
	KeyRef              = "ref"
	KeyTotal            = "total"
	KeyRuntime          = "runtime"
	KeyWrappers         = "wrappers"
	KeyFamilies         = "families"
	KeyChildren         = "children"
	KeyInclude          = "include"
	KeyContextId        = "context_id"
	KeyContextDir       = "context_dir"
	KeyRoot             = "root"
	KeyScope            = "scope"
	KeyDepth            = "depth"
	KeyMaxPerPath       = "max_per_path"
	KeyTree             = "tree"
	KeyMissing          = "missing"
	KeyState            = "state"
	KeyProcState        = "proc_state"
	KeyPID              = "pid"
	KeyReady            = "ready"
	KeyBuilding         = "building"
	KeyStarting         = "starting"
	KeyBuildAt          = "build_at"
	KeyStartedAt        = "started_at"
	KeyReadyAt          = "ready_at"
	KeyLastExitAt       = "last_exit_at"
	KeyStopRequestedAt  = "stop_requested_at"
	KeyLastError        = "last_error"
	KeyReadyError       = "ready_error"
	KeyPatch            = "patch"
	KeyPatchJSON        = "patch_json"
	KeySemanticPatch    = "semantic_patch"
	KeyContent          = "content"
	KeyContentJSON      = "content_json"
	KeySourceName       = "source_name"
	KeyTargetName       = "target_name"
	KeyDestinationCtxId = "destination_ctx_id"
	KeyItemType         = "item_type"
	KeyTemplate         = "template"
	KeyIncludeRaw       = "include_raw"
	KeyElements         = "elements"
	KeyCtxId            = "ctx_id"
	KeyCtxMode          = "ctx_mode"
	KeyLimit            = "limit"
	KeyOffset           = "offset"
	KeyOrderBy          = "order_by"
	KeyFilters          = "filters"
	KeyValue            = "value"
	KeyMatch            = "match"
	KeyIncludeValues    = "include_values"
	KeyIncludeSegments  = "include_segments"
	KeyUserText         = "user_text"
	KeyDiagnostics      = "diagnostics"
	KeyEventId          = "event_id"
	KeyTsNs             = "ts_ns"
	KeyTsRFC3339        = "ts_rfc3339"
	KeyIntentionJSON    = "intention_json"
	KeyResponseJSON     = "response_json"
	// -----------------------------
	// Generic keys
	// -----------------------------

	KeyTimeRange       = "time_range"
	KeyIncludePayloads = "include_payloads"

	KeyEvents        = "events"
	KeyFacets        = "facets"
	KeyDurationMs    = "duration_ms"
	KeyWindowApplied = "window_applied"

	KeySeed            = "seed"
	KeyBudgetEvents    = "budget_events"
	KeyPerContextLimit = "per_context_limit"
	KeyMaxHops         = "max_hops"
	KeyMaxContexts     = "max_contexts"
	KeyDetailLevel     = "detail_level"

	KeyTimeline        = "timeline"
	KeyContextsVisited = "contexts_visited"
	KeyHopsUsed        = "hops_used"
	KeySeedsUsed       = "seeds_used"
	KeyBudgetRequested = "budget_events_requested"
	KeyBudgetConsumed  = "budget_events_consumed"
	KeyDoneReason      = "done_reason"

	// -----------------------------
	// Filter keys
	// -----------------------------

	KeyIntentionId       = "intention_id"
	KeyRootIntentionId   = "root_intention_id"
	KeyParentIntentionId = "parent_intention_id"
	KeyFamily            = "family"
	KeyTraceKind         = "trace_kind"
	KeyReasonCode        = "reason_code"
	KeyMsgKind           = "msg_kind"

	KeyFromTsNs = "from_ts_ns"
	KeyToTsNs   = "to_ts_ns"
)
