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

// Value* constants define the vocabulary of common values used across Brique JSON exchanges.
// They are plain strings on purpose, so the same vocabulary can be reused in any target language.

const (
	// Message kinds (envelope.kind)
	ValueKindIntention = "intention"
	ValueKindResponse  = "response"
	ValueKindTrace     = "trace"

	// Intention / Response families (intention.type / response.type)
	ValueTypeCommunication = "communication"
	ValueTypeExecution     = "execution"
	ValueTypeMatter        = "matter"
	ValueTypeTrace         = "trace"
	ValueTypeReflexive     = "reflexive"
	ValueTypeControl       = "control"
	ValueTypeUser          = "user"

	// matters[].mode
	ValueModeRead  = "read"
	ValueModeWrite = "write"

	// params.mode (Matter mode)
	ValueModeBrique  = "brique"
	ValueModeWrapper  = "wrapper"
	ValueModeExtRef   = "ext_ref"
	ValueModePhysical = "physical"

	// param section
	ValueBrique    = "brique"
	ValueObjective  = "objective"
	ValueSubjective = "subjective"
	ValueFunctional = "functional"

	// Catalog entry kinds (brique.kind)
	ValueEntryKindMatter    = "matter"
	ValueEntryKindStructure = "structure"

	// Common payload discriminator values
	ValueDataKindInline = "inline"
	ValueDataKindHTTP   = "http"

	// Capability names / event values
	ValueCapMatterEvent     = "matter_event"
	ValueEventMatterWritten = "matter_written"
	ValueEventMatterDeleted = "matter_deleted"
	ValueOpDelete           = "delete"
	ValueOpWrite            = "write"

	// response.status
	ValueStatusOK      = "ok"
	ValueStatusError   = "error"
	ValueStatusRunning = "running"

	// error.origin (ResponseProblem.origin)
	ValueOriginCommunication = "communication"
	ValueOriginExecution     = "execution"
	ValueOriginMatter        = "matter"
	ValueOriginTrace         = "trace"
	ValueOriginReflexive     = "reflexive"
	ValueOriginControl       = "control"
	ValueOriginUser          = "user"

	// error.code (ResponseProblem.code)
	ValueCodeRefused       = "refused"
	ValueCodeInvalid       = "invalid"
	ValueCodeNotFound      = "not_found"
	ValueCodeUnauthorized  = "unauthorized"
	ValueCodeConflict      = "conflict"
	ValueCodeTimeout       = "timeout"
	ValueCodeUnavailable   = "unavailable"
	ValueCodeCanceled      = "canceled"
	ValueCodeInternal      = "internal"
	ValueCodeConfiguration = "configuration"
	ValueCodeReadFail      = "read_fail"

	// trace kind
	// generic family
	ValueTraceFamilyError     = "FamilyError"
	ValueTraceFamilyEnter     = "FamilyEnter"
	ValueTraceFamilyExit      = "FamilyExit"
	ValueTraceFamilySaturated = "FamilySaturated"
	ValueTracePendingResponse = "ResponseEnter"
	ValueTracePendingOrphanId = "OrphanId"

	// context loop
	ValueTraceContextLoop = "ContextLoop"

	// comm loop
	ValueTraceCommIngress = "CommIngress"
	ValueTraceCommEgress  = "CommEgress"
	ValueTraceCommDrop    = "CommDrop"
	ValueTraceCommReject  = "CommReject"

	// execution loop
	ValueTraceWrapperReady      = "WrapperReady"
	ValueTraceAwaitIntention    = "AwaitIntention"
	ValueTraceWrpStartBuilding  = "WrpStartBuilding"
	ValueTraceWrpEndBuilding    = "WrpEndBuilding"
	ValueTraceWrpStartRequested = "WrpStartRequested"
	ValueTraceWrpStartDone      = "WrpStartDone"
	ValueTraceWrpWaitStart      = "WrpWaitStart"

	// matter loop
	ValueTraceLeaseOpen    = "substance_lease_open"
	ValueTraceLeaseClose   = "substance_lease_close"
	ValueTraceLeaseExpired = "substance_lease_expired"
	ValueTraceLeaseFail    = "substance_lease_fail"
	ValueTraceLeaseUpload  = "substance_lease_upload"
	ValueTraceLeaseDone    = "substance_lease_done"

	// reflexive loop
	ValueDir            = "dir"
	ValueContext        = "context"
	ValueContextFile    = "context_file"
	ValueCapacity       = "capacity"
	ValueSchema         = "schema"
	ValueDocument       = "document"
	ValueStructure      = "structure"
	ValueMatter         = "matter"
	ValueIntention      = "intention"
	ValueResponse       = "response"
	ValueMessage        = "message"
	ValueLocal          = "local"
	ValueRef            = "ref"
	ValueTrace          = "trace"
	ValueMatterItem     = "matter_item"
	ValueStructureItem  = "structure_item"
	ValueDocumentItem   = "document_item"
	ValueSchemaItem     = "schema_item"
	ValueUnknown        = "unknown"
	ValueFull           = "full"
	ValueDetailInvoke   = "invoke"
	ValueMeaningOnly    = "meaning-only"
	ValueVocabOnly      = "vocabulary-only"
	ValueOpEQ           = "EQ"
	ValueOpEXISTS       = "EXISTS"
	ValueOpLIKE         = "LIKE"
	ValueMatchAll       = "ALL"
	ValueMatchAny       = "ANY"
	ValueName           = "name"
	ValueKind           = "kind"
	ValueModeEvents     = "events"
	ValueModeFacets     = "facets"
	ValueCtxModeStrict  = "strict"
	ValueCtxModeSubtree = "subtree"

	// -----------------------------
	// Detail level values
	// -----------------------------

	ValueDetailCommOnly = "comm-only"
	ValueDetailFull     = "full"

	// -----------------------------
	// Follow done reasons
	// -----------------------------

	ValueDoneBudgetExhausted = "budget_exhausted"
	ValueDoneNoMoreEdges     = "no_more_edges"
	ValueDoneLimitsReached   = "limits_reached"
)

// Detailed reasons used in error.details[KeyReason].
const (
	ValueReasonMissingIntentionID   = "missing_intention_id"
	ValueReasonWrapperNotConfigured = "wrapper_not_configured"
	ValueReasonMissingToContextName = "missing_to_context_name"
	ValueReasonInvalidToContextName = "invalid_to_context_name"
	ValueReasonMissingCapName       = "missing_cap_name"
	ValueReasonUnknownCap           = "unknown_cap"
	ValueReasonTimeoutWaitingAnswer = "timeout_waiting_answer"
	ValueReasonInvalidEndPoint      = "invalid_endpoint"
	ValueReasonInvalidCapacityType  = "invalid_capacity_type"

	ValueReasonWrapperFailedReady = "wrapper_already_failed_to_be_ready"
	ValueReasonWrapperBuildFailed = "wrapper_failed_to_be_built"
	ValueReasonWrapperRunFailed   = "wrapper_failed_to_be_run"
	ValueReasonWrapperTimeout     = "wrapper_failed_to_be_run"
	ValueReasonWrapperNotRunning  = "wrapper is not running"

	ValueReasonContextStopped     = "context_stopped"
	ValueReasonContextUnreachable = "context_unreachable"

	ValueReasonPendingRegisterError   = "pending_register_error"
	ValueReasonMissingContextFrame    = "missing_context_frame"
	ValueReasonCommFamilyNotAvailable = "communication_family_missing"

	ValueReasonAlgoFailure = "algorithm_robustness_failed"

	ValueReasonOrphanResponse      = "no intention retrieve for response"
	ValueReasonCatalogInvalidEntry = "invalid entries found during matter catalogue creation"
	ValueReasonAlreadyExists       = "element already exist"
	ValueReasonIOFailure           = "failure during IO operation"

	ValueReasonInvalidPayload = "payload is invalid"
	ValueReasonMissingPayload = "payload is missing"

	ValueReasonMissingMatterID    = "matter id is missing"
	ValueReasonMissingMatterIDs   = "matter ids are missing"
	ValueReasonUnknownMatter      = "matter id is unknown"
	ValueReasonMatterNotInCatalog = "matter id is not in the catalogue"

	ValueReasonMissingStructureID = "structure id is missing"
	ValueReasonUnknownStructure   = "structure id is unknown"

	ValueReasonReadFailed  = "reading element failed"
	ValueReasonJSONInvalid = "invalid json element"

	ValueReasonNoOpWrite = "no destination write defined operation canceled"

	ValueReasonDataNotWritableInMode = "matter mode incompatible with write"

	ValueReasonFilesystemError = "error with file system"

	ValueReasonInvalidPatch = "invalid patch for meaning update"

	ValueReasonInvalidMode = "mode invalid"

	ValueReasonDataUnavailable = "wrapper not started data unavailable"

	ValueReasonInvalidMsgType = "invalid message type"

	ValueReasonBusy = "loop busy"

	ValueReasonModeForbidsPayloadIO = "invalid matter mode no payload allowed"

	ValueReasonWrongKind = "wrong kind element"

	ValueReasonInvalidPath = "invalid path"

	ValueReasonRevMismatch = "revision not coherent"

	ValueReasonInvalidRequest = "request invalid"

	ValueReasonChildRestartError = "child restart error"
	ValueReasonChildStartError   = "child start error"

	ValueReasonInvalidConfiguration = "invalid configuration"

	ValueReasonInvalidCrypto = "invalid crypto"

	ValueReasonMissingParams = "missing params"

	ValueReasonInvalidParams = "invalid params"

	ValueReasonMissingItemType = "missing_item_type"

	ValueReasonUnsupportedItemType = "unsupported_item_type"

	ValueReasonTemplateLoadFailed = "template_load_failed"

	ValueReasonTraceRootMissing = "trace_root_missing"
	ValueReasonTraceUnreadable  = "trace_unreadable"
	ValueReasonInvalidLimit     = "invalid_limit"
	ValueReasonInvalidBudget    = "invalid_budget"
	ValueReasonProjectionFailed = "projection_failed"
	ValueReasonQueryFailed      = "query_failed"
	ValueReasonDBOpenFailed     = "db_open_failed"
	ValueReasonSchemaFailed     = "schema_failed"
	ValueReasonInsertFailed     = "insert_failed"
	ValueReasonParseFailed      = "parse_failed"
	ValueReasonOrderByInvalid   = "order_by_invalid"
	ValueReasonModeInvalid      = "mode_invalid"
	ValueReasonNoOrchestration  = "no_orchestration"
)
