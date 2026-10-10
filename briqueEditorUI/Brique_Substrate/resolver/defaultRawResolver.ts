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

import type {
  CanonicalKey,
  ExecuteInput,
  RawResolution,
  RawResolver,
} from "../types/index.js";
import {
  isKnownBriqueCapability,
  parseBriqueCapabilityResponse,
  type KnownCapability,
} from "../capability/index.js";

// ---------------------------------------------------------------------------
// Options
// ---------------------------------------------------------------------------

export type DefaultRawResolverOptions = {
  /**
   * Per-key param overrides.
   * Key: keyOptionId(canonicalKey) — use keyOptionId() to build the key.
   */
  byKey?: Record<string, Record<string, unknown>>;
};

// ---------------------------------------------------------------------------
// Factory
// ---------------------------------------------------------------------------

/**
 * Default resolver: the canonical key kind is the Brique capability name.
 *
 * - kind must be one of the 32 known Brique capabilities.
 * - id is used as the primary identity param when the capability has one
 *   (matter_id, structure_id, etc.). For capabilities without a primary
 *   identity param (read.structure, read.state, trace.inspect, …) id acts
 *   as a caller-defined request identity and is not forwarded to Brique.
 * - byKey allows per-key param overrides on top of the capability defaults.
 */
export function createDefaultRawResolver(
  options: DefaultRawResolverOptions = {}
): RawResolver {
  return {
    resolve(key) {
      return resolveDefaultRawReadResolution(key, options);
    },
  };
}

// ---------------------------------------------------------------------------
// Resolution
// ---------------------------------------------------------------------------

export function resolveDefaultRawReadResolution(
  key: CanonicalKey,
  options: DefaultRawResolverOptions = {}
): RawResolution | undefined {
  const input = resolveDefaultRawRead(key, options);
  if (!input) return undefined;
  const capability = key.kind.trim() as KnownCapability;
  const meaningElement =
    capability === "read.meaning" ? parseMeaningElementId(key.id) : undefined;
  return {
    input,
    parse: (response) => {
      const parsed = parseBriqueCapabilityResponse(capability, response);
      if (!parsed.ok) return undefined;
      return meaningElement
        ? extractMeaningElementDescriptor(parsed.payload, meaningElement)
        : parsed.payload;
    },
  };
}

export function resolveDefaultRawRead(
  key: CanonicalKey,
  options: DefaultRawResolverOptions = {}
): ExecuteInput | undefined {
  const capability = key.kind.trim();
  if (!isKnownBriqueCapability(capability)) return undefined;

  const defaultParams = defaultParamsForCapability(capability, key.id);
  const keyOverrides = options.byKey?.[keyOptionId(key)] ?? {};

  return {
    context: key.context,
    capability,
    params: compact({ ...defaultParams, ...keyOverrides }),
  };
}

export function keyOptionId(key: CanonicalKey): string {
  return JSON.stringify([key.context, key.kind, key.id]);
}

// ---------------------------------------------------------------------------
// Capability defaults
// ---------------------------------------------------------------------------

const DEFAULT_STRUCTURE_DEPTH = 1;
const DEFAULT_STRUCTURE_MAX_PER_PATH = 50;
const DEFAULT_TRACE_LIMIT = 50;
const DEFAULT_TRACE_OFFSET = 0;
const DEFAULT_TRACE_ORDER_BY = "ts_ns DESC";
const DEFAULT_MEANING_QUERY_LIMIT = 50;
const DEFAULT_MEANING_QUERY_OFFSET = 0;
const DEFAULT_MEANING_QUERY_ORDER_BY = "name";

function defaultParamsForCapability(
  capability: string,
  id: string
): Record<string, unknown> {
  switch (capability) {
    // --- edit ---
    case "edit.create":
    case "edit.delete":
    case "edit.duplicate":
    case "edit.patch_meaning":
      return {};

    case "edit.get_element_template":
      return { item_type: id };

    // --- meaning ---
    case "meaning.query":
      return {
        limit: DEFAULT_MEANING_QUERY_LIMIT,
        offset: DEFAULT_MEANING_QUERY_OFFSET,
        order_by: DEFAULT_MEANING_QUERY_ORDER_BY,
        ...(id && !isWildcardId(id) ? { element_kind: id } : {}),
      };

    case "meaning.rebuild":
      return id && !isWildcardId(id) ? { mode: id } : {};

    case "meaning.update":
      return {};

    // --- read ---
    case "read.document":
      return { items: [{ name: id }] };

    case "read.meaning": {
      const element = parseMeaningElementId(id);
      return compact({
        input: element
          ? [{
              ...element,
              sections: ["brique", "objective", "subjective", "functional"],
              ...(element.element_kind === "capacity"
                ? { include_resolution: true, detail: "full" }
                : {}),
            }]
          : undefined,
      });
    }

    case "read.state":
      return {};

    case "read.structure":
      return {
        depth: DEFAULT_STRUCTURE_DEPTH,
        max_per_path: DEFAULT_STRUCTURE_MAX_PER_PATH,
      };

    // --- trace ---
    case "trace.inspect":
      return {
        mode: id === "facets" ? "facets" : "events",
        limit: DEFAULT_TRACE_LIMIT,
        offset: DEFAULT_TRACE_OFFSET,
        order_by: DEFAULT_TRACE_ORDER_BY,
      };

    // --- vocabulary ---
    case "vocabulary.delete":
      return compact({ path: id && !isWildcardId(id) ? id : undefined });

    case "vocabulary.query":
      return compact({ axis: id && !isWildcardId(id) ? id : undefined });

    case "vocabulary.get":
      return {};

    case "vocabulary.patch":
      return {};

    // --- matter ---
    case "matter.clone":
      return { source_matter_id: id };

    case "matter.create":
      return { matter_id: id };

    case "matter.delete":
    case "matter.exists":
    case "matter.read":
    case "matter.subscribe":
    case "matter.unsubscribe":
    case "matter.write":
      return { matter_id: id };

    case "matter.read_batch":
      return compact({ matter_ids: splitIds(id) });

    case "matter.derive":
      return { target_matter_id: id };

    // --- structure ---
    case "structure.clone":
      return { source_structure_id: id };

    case "structure.create":
    case "structure.delete":
    case "structure.patch":
    case "structure.read":
      return { structure_id: id };

    case "structure.derive":
      return { target_structure_id: id };

    default:
      return {};
  }
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------


function isWildcardId(id: string): boolean {
  const n = id.trim().toLowerCase();
  return n === "" || n === "." || n === "root" || n === "all" || n === "*";
}

function splitIds(id: string): string[] | undefined {
  const normalized = id.trim();
  if (!normalized || isWildcardId(normalized)) return undefined;
  const ids = normalized.split(/[|,]/).map((s) => s.trim()).filter(Boolean);
  return ids.length > 0 ? ids : undefined;
}

export function meaningElementKeyId(
  elementKind: string,
  elementName: string
): string {
  return `${elementKind}:${elementName}`;
}

function parseMeaningElementId(
  id: string
): { element_kind: string; element_name: string } | undefined {
  const separator = id.indexOf(":");
  if (separator <= 0 || separator === id.length - 1) return undefined;
  return {
    element_kind: id.slice(0, separator),
    element_name: id.slice(separator + 1),
  };
}

function extractMeaningElementDescriptor(
  payload: unknown,
  element: { element_kind: string; element_name: string }
): unknown {
  if (!isRecord(payload) || !Array.isArray(payload.result)) return undefined;

  const exact = payload.result.find((item) =>
    isRecord(item)
    && item.ok !== false
    && item.element_kind === element.element_kind
    && item.element_name === element.element_name
  );
  if (isRecord(exact)) return exact.descriptor ?? exact.meaning;

  const sameKind = payload.result.filter((item) =>
    isRecord(item)
    && item.ok !== false
    && item.element_kind === element.element_kind
  );
  if (sameKind.length !== 1 || !isRecord(sameKind[0])) return undefined;

  return sameKind[0].descriptor ?? sameKind[0].meaning;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function compact<T extends Record<string, unknown>>(params: T): T {
  return Object.fromEntries(
    Object.entries(params).filter(([, v]) => v !== undefined)
  ) as T;
}
