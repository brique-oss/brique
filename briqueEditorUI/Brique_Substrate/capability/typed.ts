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

import type { ExecuteResult } from "../types/index.js";
import type { BriqueSubstrate } from "../types/index.js";
import {
  formatBriqueCapabilityParams,
  parseBriqueCapabilityResponse,
} from "./contracts.js";

// ---------------------------------------------------------------------------
// Shared Brique semantic types
// ---------------------------------------------------------------------------

export type MeaningSection = Record<string, unknown>;

export type MeaningDescriptor = {
  brique?: MeaningSection;
  objective?: MeaningSection;
  functional?: MeaningSection;
  subjective?: MeaningSection;
};

export type FunctionalDescriptor = {
  function?: string;
  input_metamatter?: string;
  output_metamatter?: string;
  capacity_name?: string;
  capacity_type?: string;
  flow?: FlowStep[] | FunctionalFlowNode[];
  [key: string]: unknown;
};

export type FlowStep = {
  call?: string;
  sequence?: FlowStep[];
  pipeline?: FlowStep[];
  requires?: string[];
  produces?: string[];
  [key: string]: unknown;
};

export type FunctionalFlowNode = {
  id?: string;
  kind?: string;
  [key: string]: unknown;
};

export type DataDescriptor =
  | { kind: "inline"; bytes: string }
  | { kind: "http"; url?: string; method?: string; headers?: Record<string, string>; [key: string]: unknown }
  | Record<string, unknown>;

export type MatterDescriptor = {
  meaning?: MeaningDescriptor;
  functional?: FunctionalDescriptor;
  brique?: Record<string, unknown>;
  data?: DataDescriptor;
  [key: string]: unknown;
};

export type StructureNode = {
  kind?: string;
  name?: string;
  id?: string;
  context?: string;
  path?: string;
  file?: string;
  ref?: string;
  status?: string;
  meaning?: MeaningDescriptor;
  functional?: FunctionalDescriptor;
  brique?: Record<string, unknown>;
  children?: StructureNode[];
};

export type StructurePatch = {
  op: "add" | "remove" | "replace" | "move" | "copy" | string;
  path: string | string[];
  value?: unknown;
  from?: string;
};

export type SemanticPatchOperation = {
  path: string[];
  value: unknown;
};

export type SemanticValuePatch = {
  add?: SemanticPatchOperation[];
  remove?: SemanticPatchOperation[];
};

// edit items
export type EditCreateItem = {
  ctx_id: string;
  element_kind: string;
  name: string;
  content?: Record<string, unknown>;
};

export type EditDeleteItem = {
  ctx_id: string;
  element_kind: string;
  name: string;
};

export type EditDuplicateItem = {
  ctx_id: string;
  element_kind: string;
  source_name: string;
  target_name: string;
  /** Absolute context path (from /root) of the context that should receive the duplicate. */
  destination_ctx_id?: string;
};

export type EditPatchMeaningItem = {
  ctx_id: string;
  element_kind: string;
  element_name: string;
  /** Complete top-level meaning sections to replace. Unspecified sections are preserved. */
  patch?: Partial<Record<"objective" | "functional" | "subjective" | "brique", Record<string, unknown> | null>>;
  semantic_patch?: SemanticValuePatch;
};

// read.meaning input item
export type ReadMeaningInputItem = {
  element_kind: string;
  element_name: string;
  sections?: string[];
};

// read.meaning result item
export type ReadMeaningResultItem = {
  ok: boolean;
  element_kind: string;
  element_name: string;
  descriptor?: MeaningDescriptor;
  meaning?: MeaningDescriptor;
  [key: string]: unknown;
};

// meaning.query result item
export type MeaningQueryResultItem = {
  ok: boolean;
  element_kind: string;
  element_name: string;
  ctx_id: string;
  path?: string;
  meaning?: MeaningDescriptor;
  [key: string]: unknown;
};

export type CapabilityMeaningQueryFilter = {
  path: string;
  op: "EXISTS" | "EQ" | "LIKE";
  value?: unknown;
  match?: "ANY" | "ALL";
};

// read.state
export type StateContextDescriptor = {
  context_id: string;
  context_name?: string;
  proc_state?: string;
  pid?: number;
  ready?: boolean;
  building?: boolean;
  starting?: boolean;
  last_error?: string;
  ready_error?: string;
  build_at?: string;
  started_at?: string;
  ready_at?: string;
  last_exit_at?: string;
  stop_requested_at?: string;
  [key: string]: unknown;
};

// trace.inspect
export type TraceEvent = {
  ts_ns: number;
  ts_rfc3339?: string;
  event_id?: string;
  context_id?: string;
  intention_id?: string;
  root_intention_id?: string;
  parent_intention_id?: string;
  trace_kind: string;
  family?: string;
  msg_kind?: string;
  reason_code?: string;
  user_text?: string;
  intention_json?: string;
  response_json?: string;
  [key: string]: unknown;
};

export type TraceFacetNode = {
  value: string;
  count: number;
  children?: TraceFacetNode[];
};

export type TraceFacets = Record<string, TraceFacetNode>;

export type TraceTimeRange = {
  from_ts_ns: number;
  to_ts_ns: number;
};

export type TraceWindowApplied = {
  from_ts_ns: number;
  to_ts_ns: number;
};

export type TraceFilters = {
  intention_id?: string;
  root_intention_id?: string;
  parent_intention_id?: string;
  family?: string;
  trace_kind?: string;
  reason_code?: string;
  msg_kind?: string;
};

// vocabulary.query
export type VocabularyChild = {
  kind: "seg" | "value" | string;
  seg?: string;
  vtype?: string;
  v_text?: string;
  value?: string;
  count?: number;
  path?: string;
};

// vocabulary.patch
export type VocabularyPatch = Record<string, unknown> | {
  add?: Array<{ path: string[]; value: unknown }>;
  remove?: Array<{ path: string[]; value: unknown }>;
};

// ---------------------------------------------------------------------------
// Params and Payload per capability
// ---------------------------------------------------------------------------

// --- edit ---

export type EditCreateParams = { items: EditCreateItem[] };
export type EditCreatePayload = { result: Array<{ ok: boolean; element_kind: string; element_name: string; [key: string]: unknown }> };

export type EditDeleteParams = { items: EditDeleteItem[] };
export type EditDeletePayload = { result: Array<{ ok: boolean; element_kind: string; element_name: string; [key: string]: unknown }> };

export type EditDuplicateParams = { items: EditDuplicateItem[] };
export type EditDuplicatePayload = { result: Array<{ ok: boolean; element_kind: string; element_name: string; [key: string]: unknown }> };

export type EditGetElementTemplateParams = { item_type: string };
export type EditGetElementTemplatePayload = { element_kind: string; template: Record<string, unknown> };

export type EditPatchMeaningParams = { items: EditPatchMeaningItem[] };
export type EditPatchMeaningPayload = { result: Array<{ ok: boolean; element_kind: string; element_name: string; [key: string]: unknown }> };

// --- meaning ---

export type MeaningQueryParams = {
  element_kind?: string;
  ctx_id?: string;
  ctx_mode?: "strict" | "subtree";
  limit?: number;
  offset?: number;
  order_by?: "name" | "kind";
  filters?: CapabilityMeaningQueryFilter[];
};
export type MeaningQueryPayload = { result: MeaningQueryResultItem[] };

export type MeaningRebuildParams = { mode?: string; context?: string };
export type MeaningRebuildPayload = {
  total_elements_indexed: number;
  total_kv_rows: number;
  total_links: number;
  total_vocab_nodes: number;
  duration_ms: number;
  mode: string;
};

export type MeaningUpdateParams = {
  elements?: Array<{ ctx_id: string; element_kind: string; name: string; [key: string]: unknown }>;
  context?: string[];
};
export type MeaningUpdatePayload = {
  updated_elements_count: number;
  affected_vocab_paths: string[];
  duration_ms: number;
};

// --- read ---

export type ReadDocumentParams = {
  items: Array<{ name: string }>;
};
export type ReadDocumentPayload = {
  total: number;
  result: Array<{ name: string; ok: boolean; content?: unknown; [key: string]: unknown }>;
};

export type ReadMeaningParams = { input: ReadMeaningInputItem[] };
export type ReadMeaningPayload = { result: ReadMeaningResultItem[] };

export type ReadStateParams = {
  include?: Array<"context" | "trace" | "wrappers" | "families">;
};

export type WrapperSnapshot = {
  name: string;
  proc_state: string;
  pid: number;
  ready: boolean;
  building: boolean;
  starting: boolean;
  build_at: string;
  started_at: string;
  ready_at: string;
  last_exit_at: string;
  stop_requested_at?: string;
  last_error?: string;
  ready_error?: string;
};

export type ReadStatePayload = {
  context?: {
    context_id: string;
    context_name?: string;
    ctx_ext_name?: string;
    ctx_version?: string;
    engine_version?: string;
    context_dir: string;
  };
  trace?: {
    enabled: boolean;
    level: string;
  };
  wrappers?: WrapperSnapshot[];
  families?: Record<string, string>;
};

export type ReadStructureParams = {
  scope?: string;
  depth?: number;
  max_per_path?: number;
};
export type ReadStructurePayload = {
  root: StructureNode;
  params?: Record<string, unknown>;
};

// --- trace ---

export type TraceInspectParams = {
  limit?: number;
  offset?: number;
  order_by?: "ts_ns ASC" | "ts_ns DESC";
  mode?: "events" | "facets";
  include_payloads?: boolean;
  filters?: TraceFilters;
  time_range?: TraceTimeRange;
};
export type TraceInspectPayload = {
  events?: TraceEvent[];
  facets?: TraceFacets;
  duration_ms?: number;
  window_applied?: TraceWindowApplied;
  diagnostics?: unknown;
};

// --- vocabulary ---

export type VocabularyDeleteParams = { path: string };
export type VocabularyDeletePayload = { deleted_path: string; duration_ms: number };

export type VocabularyGetParams = Record<string, never>;
export type VocabularyGetPayload = { vocabulary: Record<string, unknown> };

export type VocabularyPatchParams = { patch: VocabularyPatch };
export type VocabularyPatchPayload = { updated_nodes: number; updated_values: number; duration_ms: number };

export type VocabularyQueryParams = {
  path?: string;
  include_values?: boolean;
  include_segments?: boolean;
};
export type VocabularyQueryPayload = { children: VocabularyChild[] };

// --- matter ---

export type MatterCloneParams = { source_matter_id: string; target_matter_id?: string; destination_ctx_id?: string };
export type MatterClonePayload = { ok: boolean; source_matter_id: string; target_matter_id: string };

export type MatterCreateParams = {
  matter_id: string;
  matter?: MatterDescriptor;
  // Accepts a raw string in addition to DataDescriptor: the engine writes a
  // plain string payload as the matter's raw bytes as-is (see
  // matter.create's inline payload encoding — string is not JSON-wrapped).
  payload?: DataDescriptor | string;
};
export type MatterCreatePayload = { ok: boolean; matter_id: string; revision: number | string };

export type MatterDeleteParams = { matter_id: string };
export type MatterDeletePayload = { ok: boolean; matter_id: string };

export type MatterDeriveParams = {
  target_matter_id: string;
  matter?: MatterDescriptor;
  derived_from?: string;
  payload?: DataDescriptor;
};
export type MatterDerivePayload = { ok: boolean; matter_id: string; revision: number | string };

export type MatterExistsParams = { matter_id: string };
export type MatterExistsPayload = { matter_id: string; exist: boolean };

export type MatterReadParams = {
  matter_id: string;
  read_mode?: string;
  want_meaning?: boolean;
  want_functional?: boolean;
  want_data?: boolean;
  want_brique?: boolean;
};
export type MatterReadPayload = {
  matter_id: string;
  meaning?: MeaningDescriptor;
  functional?: FunctionalDescriptor;
  brique?: Record<string, unknown>;
  data?: DataDescriptor;
};

export type MatterReadBatchParams = {
  matter_ids: string[];
  read_mode?: string;
  want_meaning?: boolean;
  want_functional?: boolean;
  want_data?: boolean;
  want_brique?: boolean;
};
export type MatterReadBatchPayload = {
  result: Array<MatterReadPayload & { ok?: boolean }>;
};

export type MatterSubscribeParams = { matter_id: string; sub_id?: string };
export type MatterSubscribePayload = { ok: boolean; matter_id: string; sub_id: string; substance_mode?: string };

export type MatterUnsubscribeParams = { matter_id: string; sub_id: string };
export type MatterUnsubscribePayload = { ok: boolean; matter_id: string; sub_id: string; removed: boolean; substance_mode?: string };

export type MatterWriteParams = {
  matter_id: string;
  meaning?: MeaningDescriptor;
  functional?: FunctionalDescriptor;
  brique?: Record<string, unknown>;
  semantic_patch?: SemanticValuePatch;
  // Accepts a raw string in addition to DataDescriptor: the engine writes a
  // plain string payload as the matter's raw bytes as-is (see matter.write's
  // inline payload encoding — string is not JSON-wrapped).
  data?: DataDescriptor | string;
  http_data?: unknown;
};
export type MatterWritePayload = { ok: boolean; matter_id: string; revision: number | string; http_data?: unknown };

// --- structure ---

export type StructureCloneParams = { source_structure_id: string; target_structure_id?: string; destination_ctx_id?: string };
export type StructureClonePayload = { ok: boolean; source_structure_id: string; target_structure_id: string; revision: number | string };

export type StructureCreateParams = {
  structure_id: string;
  structure?: StructureNode;
};
export type StructureCreatePayload = { ok: boolean; structure_id: string; revision: number | string };

export type StructureDeleteParams = { structure_id: string };
export type StructureDeletePayload = { ok: boolean; structure_id: string };

export type StructureDeriveParams = {
  target_structure_id: string;
  structure?: StructureNode;
  derived_from?: string;
};
export type StructureDerivePayload = { ok: boolean; target_structure_id: string; revision: number | string };

export type StructurePatchParams = {
  structure_id: string;
  exp_rev?: number | string;
  patches?: StructurePatch[];
  semantic_patch?: SemanticValuePatch;
};
export type StructurePatchPayload = { ok: boolean; structure_id: string; revision: number | string };

export type StructureReadParams = {
  structure_id: string;
  want_meaning?: boolean;
  want_functional?: boolean;
  want_brique?: boolean;
};
export type StructureReadPayload = {
  structure_id: string;
  meaning?: MeaningDescriptor;
  functional?: FunctionalDescriptor;
  brique?: Record<string, unknown>;
};

// ---------------------------------------------------------------------------
// Typed capability result envelope
// ---------------------------------------------------------------------------

export type CapabilityResult<TPayload> = {
  intentionId: string;
  status: string;
  ok: boolean;
  payload: TPayload | undefined;
  error: { origin?: string; code?: string; message?: string; details?: Record<string, unknown> } | undefined;
  raw: unknown;
};

// ---------------------------------------------------------------------------
// Capability client interface
// ---------------------------------------------------------------------------

export type CapabilityClient = {
  edit: {
    create(context: string, params: EditCreateParams): Promise<CapabilityResult<EditCreatePayload>>;
    delete(context: string, params: EditDeleteParams): Promise<CapabilityResult<EditDeletePayload>>;
    duplicate(context: string, params: EditDuplicateParams): Promise<CapabilityResult<EditDuplicatePayload>>;
    getElementTemplate(context: string, params: EditGetElementTemplateParams): Promise<CapabilityResult<EditGetElementTemplatePayload>>;
    patchMeaning(context: string, params: EditPatchMeaningParams): Promise<CapabilityResult<EditPatchMeaningPayload>>;
  };
  meaning: {
    query(context: string, params: MeaningQueryParams): Promise<CapabilityResult<MeaningQueryPayload>>;
    rebuild(context: string, params?: MeaningRebuildParams): Promise<CapabilityResult<MeaningRebuildPayload>>;
    update(context: string, params: MeaningUpdateParams): Promise<CapabilityResult<MeaningUpdatePayload>>;
  };
  read: {
    document(context: string, params: ReadDocumentParams): Promise<CapabilityResult<ReadDocumentPayload>>;
    meaning(context: string, params: ReadMeaningParams): Promise<CapabilityResult<ReadMeaningPayload>>;
    state(context: string, params?: ReadStateParams): Promise<CapabilityResult<ReadStatePayload>>;
    structure(context: string, params?: ReadStructureParams): Promise<CapabilityResult<ReadStructurePayload>>;
  };
  trace: {
    inspect(context: string, params?: TraceInspectParams): Promise<CapabilityResult<TraceInspectPayload>>;
  };
  vocabulary: {
    delete(context: string, params: VocabularyDeleteParams): Promise<CapabilityResult<VocabularyDeletePayload>>;
    get(context: string, params?: VocabularyGetParams): Promise<CapabilityResult<VocabularyGetPayload>>;
    patch(context: string, params: VocabularyPatchParams): Promise<CapabilityResult<VocabularyPatchPayload>>;
    query(context: string, params?: VocabularyQueryParams): Promise<CapabilityResult<VocabularyQueryPayload>>;
  };
  matter: {
    clone(context: string, params: MatterCloneParams): Promise<CapabilityResult<MatterClonePayload>>;
    create(context: string, params: MatterCreateParams): Promise<CapabilityResult<MatterCreatePayload>>;
    delete(context: string, params: MatterDeleteParams): Promise<CapabilityResult<MatterDeletePayload>>;
    derive(context: string, params: MatterDeriveParams): Promise<CapabilityResult<MatterDerivePayload>>;
    exists(context: string, params: MatterExistsParams): Promise<CapabilityResult<MatterExistsPayload>>;
    read(context: string, params: MatterReadParams): Promise<CapabilityResult<MatterReadPayload>>;
    readBatch(context: string, params: MatterReadBatchParams): Promise<CapabilityResult<MatterReadBatchPayload>>;
    subscribe(context: string, params: MatterSubscribeParams): Promise<CapabilityResult<MatterSubscribePayload>>;
    unsubscribe(context: string, params: MatterUnsubscribeParams): Promise<CapabilityResult<MatterUnsubscribePayload>>;
    write(context: string, params: MatterWriteParams): Promise<CapabilityResult<MatterWritePayload>>;
  };
  structure: {
    clone(context: string, params: StructureCloneParams): Promise<CapabilityResult<StructureClonePayload>>;
    create(context: string, params: StructureCreateParams): Promise<CapabilityResult<StructureCreatePayload>>;
    delete(context: string, params: StructureDeleteParams): Promise<CapabilityResult<StructureDeletePayload>>;
    derive(context: string, params: StructureDeriveParams): Promise<CapabilityResult<StructureDerivePayload>>;
    patch(context: string, params: StructurePatchParams): Promise<CapabilityResult<StructurePatchPayload>>;
    read(context: string, params: StructureReadParams): Promise<CapabilityResult<StructureReadPayload>>;
  };
};

// ---------------------------------------------------------------------------
// Factory
// ---------------------------------------------------------------------------

// The engine's edit.* item parser reads the JSON keys "type" and "name", while
// the capability spec and UI use "element_kind" / "element_name". Translate at
// the wire boundary so callers can keep using the documented fields.
function withItemType(params: Record<string, unknown>): Record<string, unknown> {
  const items = params.items;
  if (!Array.isArray(items)) return params;
  return {
    ...params,
    items: items.map((item) => {
      if (typeof item !== "object" || item === null || !("element_kind" in item)) return item;
      const { element_kind, element_name, ...rest } = item as {
        element_kind: unknown;
        element_name?: unknown;
      } & Record<string, unknown>;
      return {
        ...rest,
        ...(element_name !== undefined && rest.name === undefined ? { name: element_name } : {}),
        type: element_kind,
      };
    }),
  };
}

export function createCapabilityClient(substrate: BriqueSubstrate): CapabilityClient {
  async function call<TPayload>(
    context: string,
    capability: string,
    params?: Record<string, unknown>
  ): Promise<CapabilityResult<TPayload>> {
    const result: ExecuteResult = await substrate.execute({
      context,
      capability,
      params: formatBriqueCapabilityParams(capability, params),
    });
    const parsed = parseBriqueCapabilityResponse(capability, result.response, {
      strictPayload: true,
    });
    return {
      intentionId: parsed.intentionId,
      status: parsed.status,
      ok: parsed.ok,
      payload: parsed.payload as TPayload | undefined,
      error: parsed.error,
      raw: parsed.raw,
    };
  }

  return {
    edit: {
      create: (ctx, p) => call(ctx, "edit.create", withItemType(p)),
      delete: (ctx, p) => call(ctx, "edit.delete", withItemType(p)),
      duplicate: (ctx, p) => call(ctx, "edit.duplicate", withItemType(p)),
      getElementTemplate: (ctx, p) => call(ctx, "edit.get_element_template", p as Record<string, unknown>),
      patchMeaning: (ctx, p) => call(ctx, "edit.patch_meaning", withItemType(p)),
    },
    meaning: {
      query: (ctx, p) => call(ctx, "meaning.query", p as Record<string, unknown>),
      rebuild: (ctx, p) => call(ctx, "meaning.rebuild", p as Record<string, unknown>),
      update: (ctx, p) => call(ctx, "meaning.update", p as Record<string, unknown>),
    },
    read: {
      document: (ctx, p) => call(ctx, "read.document", p as Record<string, unknown>),
      meaning: (ctx, p) => call(ctx, "read.meaning", p as Record<string, unknown>),
      state: (ctx, p) => call(ctx, "read.state", p as Record<string, unknown>),
      structure: (ctx, p) => call(ctx, "read.structure", p as Record<string, unknown>),
    },
    trace: {
      inspect: (ctx, p) => call(ctx, "trace.inspect", p as Record<string, unknown>),
    },
    vocabulary: {
      delete: (ctx, p) => call(ctx, "vocabulary.delete", p as Record<string, unknown>),
      get: (ctx, p) => call(ctx, "vocabulary.get", p as Record<string, unknown>),
      patch: (ctx, p) => call(ctx, "vocabulary.patch", p as Record<string, unknown>),
      query: (ctx, p) => call(ctx, "vocabulary.query", p as Record<string, unknown>),
    },
    matter: {
      clone: (ctx, p) => call(ctx, "matter.clone", p as Record<string, unknown>),
      create: (ctx, p) => call(ctx, "matter.create", p as Record<string, unknown>),
      delete: (ctx, p) => call(ctx, "matter.delete", p as Record<string, unknown>),
      derive: (ctx, p) => call(ctx, "matter.derive", p as Record<string, unknown>),
      exists: (ctx, p) => call(ctx, "matter.exists", p as Record<string, unknown>),
      read: (ctx, p) => call(ctx, "matter.read", p as Record<string, unknown>),
      readBatch: (ctx, p) => call(ctx, "matter.read_batch", p as Record<string, unknown>),
      subscribe: (ctx, p) => call(ctx, "matter.subscribe", p as Record<string, unknown>),
      unsubscribe: (ctx, p) => call(ctx, "matter.unsubscribe", p as Record<string, unknown>),
      write: (ctx, p) => call(ctx, "matter.write", p as Record<string, unknown>),
    },
    structure: {
      clone: (ctx, p) => call(ctx, "structure.clone", p as Record<string, unknown>),
      create: (ctx, p) => call(ctx, "structure.create", p as Record<string, unknown>),
      delete: (ctx, p) => call(ctx, "structure.delete", p as Record<string, unknown>),
      derive: (ctx, p) => call(ctx, "structure.derive", p as Record<string, unknown>),
      patch: (ctx, p) => call(ctx, "structure.patch", p as Record<string, unknown>),
      read: (ctx, p) => call(ctx, "structure.read", p as Record<string, unknown>),
    },
  };
}
