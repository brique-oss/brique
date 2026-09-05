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
  ActiveContext,
  ActiveContextActivationRequest,
  HierarchyDisplayedResponseState,
  HierarchyStructure,
} from "../HierarchyOverview/contracts.js";

export type {
  ActiveContext,
  ActiveContextActivationRequest,
  HierarchyDisplayedResponseState,
  HierarchyStructure,
};

export {
  findContextLineage,
  getStructureNodeContext,
  getStructureNodeLabel,
} from "../HierarchyOverview/contracts.js";

import type { StructureNode } from "../../../../Brique_Substrate/capability/typed.js";
import { getStructureNodeContext } from "../HierarchyOverview/contracts.js";

export type ContextNodeView = {
  key: ActiveContext;
  label: string;
  children: ContextNodeView[];
};

export function buildContextView(
  node: StructureNode,
  depth: number
): ContextNodeView {
  return {
    key: getStructureNodeContext(node),
    label: node.name ?? node.context ?? node.path ?? node.id ?? "Unnamed",
    children:
      depth > 0
        ? (node.children ?? []).map((c) => buildContextView(c, depth - 1))
        : [],
  };
}

export function findNodeByContext(
  node: StructureNode,
  target: ActiveContext
): StructureNode | undefined {
  if (getStructureNodeContext(node) === target) return node;
  for (const child of node.children ?? []) {
    const found = findNodeByContext(child, target);
    if (found) return found;
  }
  return undefined;
}

export function getParentContext(
  root: StructureNode,
  target: ActiveContext
): ActiveContext | undefined {
  function search(
    node: StructureNode,
    parent: ActiveContext | undefined
  ): ActiveContext | undefined {
    if (getStructureNodeContext(node) === target) return parent;
    for (const child of node.children ?? []) {
      const found = search(child, getStructureNodeContext(node));
      if (found !== undefined) return found;
    }
    return undefined;
  }
  return search(root, undefined);
}

export function parseBreadcrumb(
  root: StructureNode,
  target: ActiveContext
): ContextNodeView[] {
  function search(
    node: StructureNode,
    path: ContextNodeView[]
  ): ContextNodeView[] | undefined {
    const current: ContextNodeView = {
      key: getStructureNodeContext(node),
      label: node.name ?? node.context ?? node.path ?? node.id ?? "Unnamed",
      children: [],
    };
    const next = [...path, current];
    if (current.key === target) return next;
    for (const child of node.children ?? []) {
      const found = search(child, next);
      if (found) return found;
    }
    return undefined;
  }
  return search(root, []) ?? [];
}
