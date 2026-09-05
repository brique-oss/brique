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
  FlowChild,
  FlowDataValue,
  FlowNode,
  FlowProjectionModel,
} from "../../../../Brique_Substrate/projection/Flow/flowProjectionModel.js";
import {
  flowDataGet,
  isFlowDataArray,
  isFlowDataObject,
} from "../../../../Brique_Substrate/projection/Flow/flowData.js";
import type {
  FlowLayoutBox,
  FlowLayoutContext,
} from "./flowLayoutModel.js";

export function buildCapacityLayout(
  model: FlowProjectionModel,
  sectionExpansions: Record<string, import("./contracts.js").FlowExpansion>
): FlowLayoutBox {
  const context: FlowLayoutContext = { model, sectionExpansions };
  const capacity = model.nodesById[model.rootId];
  if (!capacity) {
    return {
      id: "layout:capacity:empty",
      kind: "capacity",
      label: "capacity",
      children: [],
    };
  }

  const sectionChildren = capacity.children.filter((child) => child.role === "section");
  const sections = sectionChildren
    .map((child) => model.nodesById[child.id])
    .filter((node): node is FlowNode => node?.type === "section")
    .map((node, index) => buildSectionLayout(context, node, index === 0 ? "oneLevel" : "collapsed"));

  return {
    id: `layout:capacity:${model.rootId}`,
    kind: "capacity",
    label: capacity.data.entries[0]?.key ?? "capacity",
    node: capacity,
    children: sections,
  };
}

function buildSectionLayout(context: FlowLayoutContext, node: FlowNode, defaultExpansion: import("./contracts.js").FlowExpansion = "collapsed"): FlowLayoutBox {
  const children: FlowLayoutBox[] = [];
  const expansion = context.sectionExpansions[node.id] ?? defaultExpansion;

  if (expansion !== "collapsed") {
    const contract = childByRole(context, node, "contract");
    const resolution = childByRole(context, node, "resolution");
    if (contract) {
      children.push({
        id: `layout:${contract.id}`,
        kind: "contract",
        label: "contract",
        node: contract,
        children: [],
      });
    }
    if (resolution) {
      children.push({
        id: `layout:${resolution.id}`,
        kind: "resolution",
        label: "resolution",
        node: resolution,
        children: [buildResolutionContent(context, resolution)],
      });
    }
  }

  return {
    id: `layout:${node.id}`,
    kind: "section",
    label: node.data.entries[0]?.key ?? "section",
    node,
    expansion,
    children,
  };
}

function buildResolutionContent(
  context: FlowLayoutContext,
  resolutionNode: FlowNode
): FlowLayoutBox {
  const entryId = resolutionNode.children.find((child) => child.role === "entry")?.id;
  return entryId
    ? buildSequenceFromEntry(context, entryId)
    : {
        id: `layout:${resolutionNode.id}:empty`,
        kind: "empty",
        label: "empty resolution",
        node: resolutionNode,
        children: [],
      };
}

function buildSequenceFromEntry(
  context: FlowLayoutContext,
  entryId: string
): FlowLayoutBox {
  const children: FlowLayoutBox[] = [];
  const visited = new Set<string>();
  let node: FlowNode | undefined = context.model.nodesById[entryId];

  while (node && !visited.has(node.id)) {
    visited.add(node.id);

    if (node.subtype === "sequence") {
      const sequenceEntry = node.children.find((child) => child.role === "entry")?.id;
      if (sequenceEntry) children.push(buildSequenceFromEntry(context, sequenceEntry));
    } else {
      children.push(buildNodeLayout(context, node));
    }

    node = node.nextId ? context.model.nodesById[node.nextId] : undefined;
  }

  if (children.length === 1) return children[0];
  return {
    id: `layout:sequence:${entryId}`,
    kind: "sequence",
    label: "sequence",
    children,
  };
}

function buildNodeLayout(context: FlowLayoutContext, node: FlowNode): FlowLayoutBox {
  if (node.type === "section") return buildSectionLayout(context, node);

  switch (node.subtype) {
    case "parallel":
      return buildBranchingLayout(context, node, "parallel", "branch");
    case "if":
      return buildBranchingLayout(context, node, "if", "then");
    case "switch":
      return buildBranchingLayout(context, node, "switch", "case");
    case "for_each":
      return buildBranchingLayout(context, node, "for_each", "body");
    case "invoke":
      return leafBox(node, "action", "invoke");
    case "output":
      return leafBox(node, "action", "output");
    case "section_reference":
      return leafBox(node, "reference", "section reference");
    default:
      return leafBox(node, node.type === "reference" ? "reference" : "action", node.type);
  }
}

function buildBranchingLayout(
  context: FlowLayoutContext,
  node: FlowNode,
  kind: "parallel" | "if" | "switch" | "for_each",
  defaultRole: string
): FlowLayoutBox {
  const branchChildren = node.children.filter((child) => child.role !== "entry");

  // for_each body has no label — render content directly
  const children = kind === "for_each"
    ? branchChildren.map((child) => buildSequenceFromEntry(context, child.id))
    : branchChildren.map((child, index) => buildBranchBox(context, child, defaultRole, index));

  return {
    id: `layout:${node.id}`,
    kind,
    label: operatorLabel(node),
    node,
    children,
  };
}

function operatorLabel(node: FlowNode): string {
  const data = node.data;
  const opKey = data.entries[0]?.key ?? "";
  const opValue = data.entries[0]?.value;
  const obj = isFlowDataObject(opValue) ? opValue : undefined;

  if (node.subtype === "if") {
    const when = obj ? flowDataGet(obj, "when") : undefined;
    return `if ${formatCondition(when)}`;
  }
  if (node.subtype === "switch") {
    const on = obj ? flowDataGet(obj, "on") : undefined;
    return on ? `switch ${formatDataValue(on)}` : "switch";
  }
  if (node.subtype === "for_each") {
    const collection = obj ? flowDataGet(obj, "collection") : undefined;
    const as_ = obj ? flowDataGet(obj, "as") : undefined;
    const base = collection ? `for each ${formatDataValue(collection)}` : "for each";
    return as_ ? `${base} as ${formatDataValue(as_)}` : base;
  }
  return node.subtype ?? opKey;
}

function formatCondition(value: FlowDataValue | undefined): string {
  if (!value) return "";
  const obj = isFlowDataObject(value) ? value : undefined;
  if (!obj) return formatDataValue(value);

  const left = flowDataGet(obj, "left");
  const op = flowDataGet(obj, "op");
  const right = flowDataGet(obj, "right");

  if (left !== undefined && op !== undefined && right !== undefined) {
    return `${formatDataValue(left)} ${opSymbol(String(op))} ${formatDataValue(right)}`;
  }
  return formatDataValue(value);
}

function opSymbol(op: string): string {
  switch (op) {
    case "eq":  return "==";
    case "neq": return "!=";
    case "lt":  return "<";
    case "lte": return "<=";
    case "gt":  return ">";
    case "gte": return ">=";
    default:    return op;
  }
}

function formatDataValue(value: FlowDataValue): string {
  if (typeof value === "string") return value;
  if (typeof value === "number" || typeof value === "boolean") return String(value);
  if (value === null) return "null";
  if (isFlowDataArray(value)) return value.items.map(formatDataValue).join(", ");
  if (isFlowDataObject(value)) return value.entries.map(({ key, value: v }) => `${key} ${formatDataValue(v)}`).join(" ");
  return "";
}

function buildBranchBox(
  context: FlowLayoutContext,
  child: FlowChild,
  defaultRole: string,
  index: number
): FlowLayoutBox {
  return {
    id: `layout:branch:${child.id}:${child.role ?? defaultRole}:${index}`,
    kind: "branch",
    label: child.label ?? `${child.role ?? defaultRole} ${index + 1}`,
    children: [buildSequenceFromEntry(context, child.id)],
  };
}

function childByRole(
  context: FlowLayoutContext,
  node: FlowNode,
  role: string
): FlowNode | undefined {
  const id = node.children.find((child) => child.role === role)?.id;
  return id ? context.model.nodesById[id] : undefined;
}

function leafBox(
  node: FlowNode,
  kind: "action" | "reference",
  label: string
): FlowLayoutBox {
  return {
    id: `layout:${node.id}`,
    kind,
    label,
    node,
    children: [],
  };
}
