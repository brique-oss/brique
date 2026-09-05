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
  ReadStructurePayload,
  StructureNode,
} from "../../../../Brique_Substrate/capability/typed.js";

export type ActiveContext = string;

export type ActiveContextActivationRequest = {
  target: ActiveContext;
};

export type HierarchyDisplayMode = "expanded" | "collapsed";

export type HierarchyStructure = ReadStructurePayload;

export type HierarchyDisplayedResponseState = {
  hierarchy: HierarchyStructure | undefined;
  loading: boolean;
  error: string | undefined;
};

export type RefreshRequest = void;

export function deriveContextPath(parentCtx: string, childName: string): ActiveContext {
  const base = parentCtx.startsWith("/") ? parentCtx : `/${parentCtx}`;
  const normalized = base === "/" ? "" : base;
  return `${normalized}/${childName}`;
}

export function getStructureNodeContext(node: StructureNode): ActiveContext {
  const raw = node.context ?? node.path ?? node.id ?? node.name ?? "";
  if (raw === "" || raw === "root") return "/root";
  if (raw.startsWith("/")) return raw;
  return `/root/${raw}`;
}

export function getStructureNodeLabel(node: StructureNode): string {
  return node.name ?? node.context ?? node.path ?? node.id ?? "Unnamed context";
}

export function hierarchyContainsContext(
  hierarchy: HierarchyStructure,
  context: ActiveContext
): boolean {
  return findContextLineage(hierarchy.root, context) !== undefined;
}

export function findContextLineage(
  node: StructureNode,
  context: ActiveContext
): StructureNode[] | undefined {
  if (getStructureNodeContext(node) === context) return [node];

  for (const child of node.children ?? []) {
    const childLineage = findContextLineage(child, context);
    if (childLineage) return [node, ...childLineage];
  }

  return undefined;
}
