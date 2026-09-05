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

import type { ExecuteInput, BriqueCapability } from "../types/index.js";
import type {
  EditCreatePayload,
  EditDeletePayload,
  EditDuplicatePayload,
  EditGetElementTemplatePayload,
  EditPatchMeaningPayload,
  MeaningQueryPayload,
  MeaningRebuildPayload,
  MeaningUpdatePayload,
  ReadDocumentPayload,
  ReadMeaningPayload,
  ReadStatePayload,
  ReadStructurePayload,
  TraceInspectPayload,
  VocabularyDeletePayload,
  VocabularyGetPayload,
  VocabularyPatchPayload,
  VocabularyQueryPayload,
  MatterClonePayload,
  MatterCreatePayload,
  MatterDeletePayload,
  MatterDerivePayload,
  MatterExistsPayload,
  MatterReadPayload,
  MatterReadBatchPayload,
  MatterSubscribePayload,
  MatterUnsubscribePayload,
  MatterWritePayload,
  StructureClonePayload,
  StructureCreatePayload,
  StructureDeletePayload,
  StructureDerivePayload,
  StructurePatchPayload,
  StructureReadPayload,
} from "./typed.js";

// ---------------------------------------------------------------------------
// Capability → Payload type map
// ---------------------------------------------------------------------------

export type CapabilityPayloadMap = {
  "edit.create": EditCreatePayload;
  "edit.delete": EditDeletePayload;
  "edit.duplicate": EditDuplicatePayload;
  "edit.get_element_template": EditGetElementTemplatePayload;
  "edit.patch_meaning": EditPatchMeaningPayload;
  "meaning.query": MeaningQueryPayload;
  "meaning.rebuild": MeaningRebuildPayload;
  "meaning.update": MeaningUpdatePayload;
  "read.document": ReadDocumentPayload;
  "read.meaning": ReadMeaningPayload;
  "read.state": ReadStatePayload;
  "read.structure": ReadStructurePayload;
  "trace.inspect": TraceInspectPayload;
  "vocabulary.delete": VocabularyDeletePayload;
  "vocabulary.get": VocabularyGetPayload;
  "vocabulary.patch": VocabularyPatchPayload;
  "vocabulary.query": VocabularyQueryPayload;
  "matter.clone": MatterClonePayload;
  "matter.create": MatterCreatePayload;
  "matter.delete": MatterDeletePayload;
  "matter.derive": MatterDerivePayload;
  "matter.exists": MatterExistsPayload;
  "matter.read": MatterReadPayload;
  "matter.read_batch": MatterReadBatchPayload;
  "matter.subscribe": MatterSubscribePayload;
  "matter.unsubscribe": MatterUnsubscribePayload;
  "matter.write": MatterWritePayload;
  "structure.clone": StructureClonePayload;
  "structure.create": StructureCreatePayload;
  "structure.delete": StructureDeletePayload;
  "structure.derive": StructureDerivePayload;
  "structure.patch": StructurePatchPayload;
  "structure.read": StructureReadPayload;
};

export type KnownCapability = keyof CapabilityPayloadMap;

// ---------------------------------------------------------------------------
// Typed parse result
// ---------------------------------------------------------------------------

export type CapabilityContract = {
  capability: BriqueCapability;
  params: string[];
  payload: string[];
};

export type FormatCapabilityInput = {
  context: string;
  capability: BriqueCapability;
  params?: Record<string, unknown>;
};

export type ParsedBriqueResponse<TPayload = Record<string, unknown>> = {
  capability: BriqueCapability;
  intentionId: string;
  status: string;
  ok: boolean;
  payload: TPayload | undefined;
  error?: { origin?: string; code?: string; message?: string; details?: Record<string, unknown> };
  raw: unknown;
};

export type ParseBriqueResponseOptions = {
  strictPayload?: boolean;
};

const CONTRACTS: CapabilityContract[] = [
  contract("edit.create", ["items"], ["result"]),
  contract("edit.delete", ["items"], ["result"]),
  contract("edit.duplicate", ["items"], ["result"]),
  contract("edit.get_element_template", ["item_type"], ["element_kind", "template"]),
  contract("edit.patch_meaning", ["items"], ["result"]),
  contract(
    "meaning.query",
    ["element_kind", "ctx_id", "limit", "offset", "order_by", "filters"],
    ["result"]
  ),
  contract(
    "meaning.rebuild",
    ["mode", "context"],
    [
      "total_elements_indexed",
      "total_kv_rows",
      "total_links",
      "total_vocab_nodes",
      "duration_ms",
      "mode",
    ]
  ),
  contract(
    "meaning.update",
    ["elements", "context"],
    ["updated_elements_count", "affected_vocab_paths", "duration_ms"]
  ),
  contract("read.document", ["items"], ["total", "result"]),
  contract("read.meaning", ["input"], ["result"]),
  contract("read.state", ["include"], ["context", "trace", "wrappers", "families"]),
  contract("read.structure", ["scope", "depth", "max_per_path"], ["root", "params"]),
  contract(
    "trace.inspect",
    ["limit", "offset", "order_by", "mode", "include_payloads", "filters", "time_range"],
    ["facets", "events", "duration_ms", "window_applied", "diagnostics"]
  ),
  contract("vocabulary.delete", ["path"], ["deleted_path", "duration_ms"]),
  contract("vocabulary.get", [], ["vocabulary"]),
  contract("vocabulary.patch", ["patch"], ["updated_nodes", "updated_values", "duration_ms"]),
  contract("vocabulary.query", ["path", "include_values", "include_segments"], ["children"]),
  contract("matter.clone", ["source_matter_id", "target_matter_id", "destination_ctx_id"], ["ok", "source_matter_id", "target_matter_id"]),
  contract("matter.create", ["matter_id", "matter", "payload"], ["ok", "matter_id", "revision"]),
  contract("matter.delete", ["matter_id"], ["ok", "matter_id"]),
  contract("matter.derive", ["target_matter_id", "matter", "derived_from", "payload"], ["ok", "matter_id", "revision"]),
  contract("matter.exists", ["matter_id"], ["matter_id", "exist"]),
  contract(
    "matter.read",
    ["matter_id", "read_mode", "want_meaning", "want_functional", "want_data", "want_brique"],
    ["matter_id", "meaning", "functional", "brique", "data"]
  ),
  contract(
    "matter.read_batch",
    ["matter_ids", "read_mode", "want_meaning", "want_functional", "want_data", "want_brique"],
    ["result"]
  ),
  contract("matter.subscribe", ["matter_id", "sub_id"], ["ok", "matter_id", "sub_id", "substance_mode"]),
  contract("matter.unsubscribe", ["matter_id", "sub_id"], ["ok", "matter_id", "sub_id", "removed", "substance_mode"]),
  contract("matter.write", ["matter_id", "meaning", "functional", "brique", "semantic_patch", "data", "http_data"], ["ok", "matter_id", "revision", "http_data"]),
  contract("structure.clone", ["source_structure_id", "target_structure_id", "destination_ctx_id"], ["ok", "source_structure_id", "target_structure_id", "revision"]),
  contract("structure.create", ["structure_id", "structure"], ["ok", "structure_id", "revision"]),
  contract("structure.delete", ["structure_id"], ["ok", "structure_id"]),
  contract("structure.derive", ["target_structure_id", "structure", "derived_from"], ["ok", "target_structure_id", "revision"]),
  contract("structure.patch", ["structure_id", "exp_rev", "patches", "semantic_patch"], ["ok", "structure_id", "revision"]),
  contract("structure.read", ["structure_id", "want_meaning", "want_functional", "want_brique"], ["structure_id", "meaning", "functional", "brique"]),
];

const CONTRACT_BY_CAPABILITY = new Map(
  CONTRACTS.map((item) => [item.capability, item])
);

export function listBriqueCapabilityContracts(): CapabilityContract[] {
  return CONTRACTS.map((item) => ({
    capability: item.capability,
    params: [...item.params],
    payload: [...item.payload],
  }));
}

export function getBriqueCapabilityContract(
  capability: BriqueCapability
): CapabilityContract | undefined {
  const contract = CONTRACT_BY_CAPABILITY.get(capability);
  if (!contract) return undefined;
  return {
    capability: contract.capability,
    params: [...contract.params],
    payload: [...contract.payload],
  };
}

export function isKnownBriqueCapability(
  capability: BriqueCapability
): boolean {
  return CONTRACT_BY_CAPABILITY.has(capability);
}

export function formatBriqueCapabilityIntention(
  input: FormatCapabilityInput
): ExecuteInput {
  return {
    context: input.context,
    capability: input.capability,
    params: formatBriqueCapabilityParams(input.capability, input.params),
  };
}

export function formatBriqueCapabilityParams(
  capability: BriqueCapability,
  params: Record<string, unknown> = {}
): Record<string, unknown> {
  const contract = requireContract(capability);
  const allowed = new Set(contract.params);
  const formatted: Record<string, unknown> = {};

  for (const [key, value] of Object.entries(params)) {
    if (value === undefined) continue;
    if (!allowed.has(key)) {
      throw new Error(`Unsupported ${capability} param: ${key}`);
    }
    formatted[key] = value;
  }

  return formatted;
}

export function parseBriqueCapabilityResponse<C extends KnownCapability>(
  capability: C,
  response: unknown,
  options?: ParseBriqueResponseOptions
): ParsedBriqueResponse<CapabilityPayloadMap[C]>;
export function parseBriqueCapabilityResponse(
  capability: BriqueCapability,
  response: unknown,
  options?: ParseBriqueResponseOptions
): ParsedBriqueResponse;
export function parseBriqueCapabilityResponse(
  capability: BriqueCapability,
  response: unknown,
  options: ParseBriqueResponseOptions = {}
): ParsedBriqueResponse {
  const contract = requireContract(capability);
  const envelope = requireRecord(response, `${capability} response`);
  const intentionId = requireString(envelope.intention_id, `${capability} response.intention_id`);
  const status = requireString(envelope.status, `${capability} response.status`);
  const payload = envelope.payload === undefined
    ? undefined
    : requireRecord(envelope.payload, `${capability} response.payload`);

  if (payload && options.strictPayload) {
    const allowed = new Set(contract.payload);
    for (const key of Object.keys(payload)) {
      if (!allowed.has(key)) {
        throw new Error(`Unsupported ${capability} payload key: ${key}`);
      }
    }
  }

  const error = isRecord(envelope.error)
    ? {
        origin: typeof envelope.error.origin === "string" ? envelope.error.origin : undefined,
        code: typeof envelope.error.code === "string" ? envelope.error.code : undefined,
        message: typeof envelope.error.message === "string" ? envelope.error.message : undefined,
        details: isRecord(envelope.error.details) ? envelope.error.details as Record<string, unknown> : undefined,
      }
    : undefined;

  return {
    capability,
    intentionId,
    status,
    ok: status === "ok",
    payload: payload as Record<string, unknown> | undefined,
    error,
    raw: response,
  };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function contract(
  capability: BriqueCapability,
  params: string[],
  payload: string[]
): CapabilityContract {
  return { capability, params, payload };
}

function requireContract(capability: BriqueCapability): CapabilityContract {
  const contract = CONTRACT_BY_CAPABILITY.get(capability);
  if (!contract) {
    throw new Error(`Unsupported Brique capability: ${capability}`);
  }
  return contract;
}

function requireRecord(value: unknown, label: string): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    throw new Error(`${label} must be an object`);
  }
  return value as Record<string, unknown>;
}

function requireString(value: unknown, label: string): string {
  if (typeof value !== "string" || value.trim() === "") {
    throw new Error(`${label} must be a non-empty string`);
  }
  return value;
}
