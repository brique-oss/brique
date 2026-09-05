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

import type { StructureNode } from "../../../../Brique_Substrate/capability/typed.ts";
import type { MeaningDescriptor } from "../../../../Brique_Substrate/capability/typed.ts";
import type { ReadStructurePayload } from "../../../../Brique_Substrate/capability/typed.ts";

export type { MeaningDescriptor };

export type ActiveContextStructure = ReadStructurePayload;

export type ElementKind =
  | "context"
  | "capacity"
  | "matter"
  | "structure"
  | "schema"
  | "document";

export type ElementItem = {
  kind: ElementKind;
  name: string;
  key: string;
};

export type ElementGroup = {
  kind: ElementKind;
  label: string;
  items: ElementItem[];
};

export type ElementSelectionRequest = {
  element: ElementItem;
};

export type TargetOpeningRequest = {
  element: ElementItem;
};

const ELEMENT_KIND_ORDER: ElementKind[] = [
  "document",
  "capacity",
  "matter",
  "structure",
  "schema",
];

const ELEMENT_KIND_LABELS: Record<ElementKind, string> = {
  context: "Contexts",
  capacity: "Capacities",
  matter: "Matters",
  structure: "Structures",
  schema: "Schemas",
  document: "Documents",
};

export function extractElementGroups(root: StructureNode): ElementGroup[] {
  const buckets: Record<ElementKind, ElementItem[]> = {
    context: [],
    capacity: [],
    matter: [],
    structure: [],
    schema: [],
    document: [],
  };

  collectElements(root, buckets);

  return ELEMENT_KIND_ORDER
    .filter((k) => buckets[k].length > 0)
    .map((k) => ({ kind: k, label: ELEMENT_KIND_LABELS[k], items: buckets[k] }));
}

function collectElements(
  node: StructureNode,
  buckets: Record<ElementKind, ElementItem[]>
): void {
  for (const child of node.children ?? []) {
    if (child.kind === "context") continue;
    const kind = normalizeKind(child.kind);
    if (kind && child.name) {
      const rawName = child.name.replace(/\.json$/, "");
      buckets[kind].push({
        kind,
        name: rawName,
        key: child.path ?? child.id ?? child.name,
      });
    } else if (child.kind !== "context") {
      collectElements(child, buckets);
    }
  }
}

function normalizeKind(kind: string | undefined): ElementKind | undefined {
  if (!kind) return undefined;
  const k = kind.toLowerCase();
  if (k === "capacity") return "capacity";
  if (k === "matter_item" || k === "matter") return "matter";
  if (k === "structure_item" || k === "structure") return "structure";
  if (k === "schema_item" || k === "schema") return "schema";
  if (k === "document_item" || k === "document") return "document";
  return undefined;
}
