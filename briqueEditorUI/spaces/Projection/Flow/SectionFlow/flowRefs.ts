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


import type { FlowDataEntry, FlowDataValue } from "../../../../Brique_Substrate/projection/Flow/flowProjectionModel.js";
import { isFlowDataArray, isFlowDataObject } from "../../../../Brique_Substrate/projection/Flow/flowData.js";
import type { BriqueRef, BriqueRefKind } from "./FlowLayoutRenderer.js";

/**
 * The strings a named slot holds: the string itself, or those of a list. A slot such as `@matter: { prompt_matters: [...] }` binds
 * one Matter or several, in order; anything that is not a string (an object, a number) is not a reference.
 */
export function slotStrings(value: FlowDataValue | undefined): string[] {
  if (typeof value === "string") return [value];
  if (isFlowDataArray(value)) return value.items.filter((item): item is string => typeof item === "string");
  return [];
}

/**
 * Every string found anywhere in a value, in order. An output builds its messages from nested values; the runtime refs among
 * these strings (`$alias.payload.field`) are what it reads.
 */
export function allStrings(value: FlowDataValue | undefined): string[] {
  if (typeof value === "string") return [value];
  if (isFlowDataArray(value)) return value.items.flatMap((item) => allStrings(item));
  if (isFlowDataObject(value)) return value.entries.flatMap((entry) => allStrings(entry.value));
  return [];
}

/** What a parameter is bound to, under the name it takes in the invoked capacity. */
export type WithRow = {
  name: string;
  /** the references it holds: Matters, schemas, runtime refs. Empty for a literal. */
  refs: BriqueRef[];
  /** a short rendering of a value that holds no reference (text, number, list, object). */
  literal?: string;
  /** the whole value, pretty-printed, for a tooltip: what `literal` cut short. */
  full?: string;
};

const NAMED_SLOTS = ["@matter", "@schema", "@structure", "@document", "@capacity"];

const SUMMARY_MAX = 64;

/** The plain JavaScript value of a flow value. */
export function toPlain(value: FlowDataValue | undefined): unknown {
  if (value === undefined) return null;
  if (isFlowDataArray(value)) return value.items.map(toPlain);
  if (isFlowDataObject(value)) return Object.fromEntries(value.entries.map((entry) => [entry.key, toPlain(entry.value)]));
  return value;
}

/** A short rendering of a value: its compact JSON, cut after SUMMARY_MAX characters. The whole value is in `full`. */
export function summarize(value: FlowDataValue | undefined): string {
  const compact = JSON.stringify(toPlain(value)) ?? "null";
  return compact.length > SUMMARY_MAX ? `${compact.slice(0, SUMMARY_MAX - 1)}…` : compact;
}

/** The whole value, pretty-printed. */
export function wholeValue(value: FlowDataValue | undefined): string {
  return JSON.stringify(toPlain(value), null, 2) ?? "null";
}

/**
 * The rows of a `with` block, each under the name the invoked capacity gives the parameter. A named slot (`@matter`, `@schema`,
 * `@structure`, `@document`, `@capacity`) contributes one row per name it binds, holding one reference or an ordered list of them;
 * any other entry is one row: its references when it holds some, otherwise its value in short.
 */
export function withRows(
  entries: FlowDataEntry[],
  detect: (entryKey: string, value: unknown, ancestorKey?: string) => BriqueRef | null
): WithRow[] {
  const rows: WithRow[] = [];
  for (const { key, value } of entries) {
    const direct = detect(key, value, undefined);
    if (direct) {
      rows.push({ name: key, refs: [direct] });
      continue;
    }
    if (isFlowDataObject(value) && NAMED_SLOTS.includes(key)) {
      const kind = key.slice(1) as BriqueRefKind;
      for (const inner of value.entries) {
        const refs: BriqueRef[] = [];
        for (const text of slotStrings(inner.value)) {
          if (text.startsWith("/")) refs.push({ kind, value: text });
          else if (text.startsWith("$")) {
            const runtime = detect(key, text);
            if (runtime) refs.push(runtime);
          }
        }
        rows.push(refs.length > 0 ? { name: inner.key, refs } : { name: inner.key, refs: [], literal: summarize(inner.value), full: wholeValue(inner.value) });
      }
      continue;
    }
    const built: BriqueRef[] = [];
    for (const text of allStrings(value)) {
      if (!text.startsWith("$")) continue;
      const runtime = detect(key, text);
      if (runtime) built.push(runtime);
    }
    rows.push(built.length > 0 ? { name: key, refs: built } : { name: key, refs: [], literal: summarize(value), full: wholeValue(value) });
  }
  return rows;
}
