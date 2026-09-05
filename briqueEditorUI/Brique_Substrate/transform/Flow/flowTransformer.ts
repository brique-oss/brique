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
  ProjectionTransformer,
} from "../../types/index.js";
import type {
  FlowChild,
  FlowNode,
  FlowNodeSubtype,
  FlowNodeType,
  FlowProjectionModel,
} from "../../projection/Flow/flowProjectionModel.js";
import { flowDataObject } from "../../projection/Flow/flowData.js";

type JsonRecord = Record<string, unknown>;

type FlowBuilder = {
  nodesById: Record<string, FlowNode>;
};

type ProjectedEntry = {
  entryId: string;
  continuationId: string;
};

const SUPPORTED_OPERATORS = [
  "sequence",
  "parallel",
  "if",
  "switch",
  "for_each",
] as const;

type SupportedOperator = typeof SUPPORTED_OPERATORS[number];

export const flowTransformer: ProjectionTransformer<"flow"> = {
  kind: "flow",

  transform(input): FlowProjectionModel {
    const descriptor = requireRecord(input.raw, "Flow descriptor");
    const functional = requireRecord(
      descriptor.functional,
      "Flow descriptor functional"
    );
    const sections = extractSections(functional);

    if (sections.length === 0) {
      throw new Error("Flow descriptor functional contains no #section");
    }

    const builder: FlowBuilder = { nodesById: {} };
    const rootId = addNode(builder, {
      path: "Capacity",
      type: "capacity",
      data: descriptor,
    });

    sections.forEach(([sectionName, sectionValue], index) => {
      projectSection(
        builder,
        rootId,
        sectionName,
        sectionValue,
        index === 0 ? "Root" : sectionName
      );
    });

    assertModelReferences(builder.nodesById, rootId);

    return {
      key: input.key,
      kind: "flow",
      rootId,
      nodesById: builder.nodesById,
      metadata: {
        projection: "flow",
        root: input.key,
        generatedAt: Date.now(),
      },
    };
  },
};

function projectSection(
  builder: FlowBuilder,
  capacityId: string,
  sectionName: string,
  sectionValue: JsonRecord,
  sectionPathSegment: string
): void {
  const sectionPath = `Capacity/${sectionPathSegment}`;
  const sectionId = projectSectionNode(
    builder,
    capacityId,
    sectionName,
    sectionValue,
    sectionPath
  );
  addChild(builder, capacityId, sectionId, "section", sectionName);
}

function projectSectionNode(
  builder: FlowBuilder,
  parentId: string,
  sectionName: string,
  sectionValue: JsonRecord,
  sectionPath: string
): string {
  const sectionId = addNode(builder, {
    path: sectionPath,
    type: "section",
    parentId,
    data: { [sectionName]: sectionValue },
  });

  const contractValue = omitKey(sectionValue, "resolution");
  const contractId = addNode(builder, {
    path: `${sectionPath}/Contract`,
    type: "contract",
    parentId: sectionId,
    data: { [sectionName]: contractValue },
  });
  addChild(builder, sectionId, contractId, "contract");

  const resolutionId = addNode(builder, {
    path: `${sectionPath}/Resolution`,
    type: "resolution",
    parentId: sectionId,
    data: isRecord(sectionValue.resolution)
      ? { resolution: sectionValue.resolution }
      : {},
  });
  addChild(builder, sectionId, resolutionId, "resolution");

  if (!isRecord(sectionValue.resolution)) return sectionId;

  const entry = projectFlowEntry(
    builder,
    sectionValue.resolution,
    resolutionId,
    `${sectionPath}/Resolution`
  );
  if (entry) addChild(builder, resolutionId, entry.entryId, "entry");

  return sectionId;
}

function projectFlowEntry(
  builder: FlowBuilder,
  fragment: JsonRecord,
  parentId: string,
  path: string
): ProjectedEntry | undefined {
  if ("invoke" in fragment) {
    requireRecord(fragment.invoke, `${path}/invoke`);
    const id = addNode(builder, {
      path: `${path}/invoke`,
      type: "action",
      subtype: "invoke",
      parentId,
      data: { invoke: fragment.invoke },
    });
    return { entryId: id, continuationId: id };
  }

  if ("output" in fragment) {
    requireRecord(fragment.output, `${path}/output`);
    const id = addNode(builder, {
      path: `${path}/output`,
      type: "action",
      subtype: "output",
      parentId,
      data: { output: fragment.output },
    });
    return { entryId: id, continuationId: id };
  }

  const sectionReference = findSectionReference(fragment, path);
  if (sectionReference) {
    const [name, value] = sectionReference;
    if (isSectionDefinition(value)) {
      const sectionId = projectSectionNode(
        builder,
        parentId,
        name,
        value,
        `${path}/${name}`
      );
      return { entryId: sectionId, continuationId: sectionId };
    }

    const id = addNode(builder, {
      path: `${path}/${name}`,
      type: "reference",
      subtype: "section_reference",
      parentId,
      data: { [name]: value },
    });
    return { entryId: id, continuationId: id };
  }

  for (const subtype of SUPPORTED_OPERATORS) {
    const key = `>${subtype}`;
    if (!(key in fragment)) continue;
    const value = requireRecord(fragment[key], `${path}/${key}`);
    return projectOperator(
      builder,
      subtype,
      value,
      parentId,
      `${path}/${key}`
    );
  }

  return undefined;
}

function projectOperator(
  builder: FlowBuilder,
  subtype: SupportedOperator,
  value: JsonRecord,
  parentId: string,
  path: string
): ProjectedEntry {
  const id = addNode(builder, {
    path,
    type: "flow",
    subtype,
    parentId,
    data: { [`>${subtype}`]: value },
  });

  switch (subtype) {
    case "sequence":
      projectSequence(builder, id, value, path);
      break;
    case "parallel":
      projectParallel(builder, id, value, path);
      break;
    case "if":
      projectIf(builder, id, value, path);
      break;
    case "switch":
      projectSwitch(builder, id, value, path);
      break;
    case "for_each":
      projectForEach(builder, id, value, path);
      break;
  }

  return { entryId: id, continuationId: id };
}

function projectSequence(
  builder: FlowBuilder,
  sequenceId: string,
  value: JsonRecord,
  path: string
): void {
  const items = requireArray(value.items, `${path}/items`);
  let previous: ProjectedEntry | undefined;

  items.forEach((item, index) => {
    const itemRecord = requireRecord(item, `${path}/items/${index}`);
    const current = projectFlowEntry(
      builder,
      itemRecord,
      sequenceId,
      `${path}/items/${index}`
    );
    if (!current) return;

    if (!previous) {
      addChild(builder, sequenceId, current.entryId, "entry");
    } else {
      builder.nodesById[previous.continuationId].nextId = current.entryId;
    }
    previous = current;
  });
}

function projectParallel(
  builder: FlowBuilder,
  parallelId: string,
  value: JsonRecord,
  path: string
): void {
  const items = requireArray(value.items, `${path}/items`);
  items.forEach((item, index) => {
    const entry = projectFlowEntry(
      builder,
      requireRecord(item, `${path}/items/${index}`),
      parallelId,
      `${path}/items/${index}`
    );
    if (entry) addChild(builder, parallelId, entry.entryId, "branch");
  });
}

function projectIf(
  builder: FlowBuilder,
  ifId: string,
  value: JsonRecord,
  path: string
): void {
  for (const [key, role] of [[">then", "then"], [">else", "else"]] as const) {
    if (!(key in value)) continue;
    const entry = projectFlowEntry(
      builder,
      requireRecord(value[key], `${path}/${key}`),
      ifId,
      `${path}/${key}`
    );
    if (entry) addChild(builder, ifId, entry.entryId, role);
  }
}

function projectSwitch(
  builder: FlowBuilder,
  switchId: string,
  value: JsonRecord,
  path: string
): void {
  const cases = requireArray(value.cases, `${path}/cases`);
  cases.forEach((item, index) => {
    const caseValue = requireRecord(item, `${path}/cases/${index}`);
    if (!("then" in caseValue)) return;
    const entry = projectFlowEntry(
      builder,
      requireRecord(caseValue.then, `${path}/cases/${index}/then`),
      switchId,
      `${path}/cases/${index}/then`
    );
    if (entry) {
      addChild(builder, switchId, entry.entryId, "case", scalarLabel(caseValue.value));
    }
  });

  if (!(">default" in value)) return;
  const defaultEntry = projectFlowEntry(
    builder,
    requireRecord(value[">default"], `${path}/>default`),
    switchId,
    `${path}/>default`
  );
  if (defaultEntry) {
    addChild(builder, switchId, defaultEntry.entryId, "default", "default");
  }
}

function projectForEach(
  builder: FlowBuilder,
  forEachId: string,
  value: JsonRecord,
  path: string
): void {
  if (!(">do" in value)) return;
  const body = projectFlowEntry(
    builder,
    requireRecord(value[">do"], `${path}/>do`),
    forEachId,
    `${path}/>do`
  );
  if (body) addChild(builder, forEachId, body.entryId, "body");
}

function extractSections(functional: JsonRecord): Array<[string, JsonRecord]> {
  const sections: Array<[string, JsonRecord]> = [];
  for (const [name, value] of Object.entries(functional)) {
    if (!name.startsWith("#")) continue;
    sections.push([name, requireRecord(value, `functional/${name}`)]);
  }
  return sections;
}

function findSectionReference(
  fragment: JsonRecord,
  path: string
): [string, JsonRecord] | undefined {
  for (const [name, value] of Object.entries(fragment)) {
    if (!name.startsWith("#")) continue;
    return [name, requireRecord(value, `${path}/${name}`)];
  }
  return undefined;
}

function addNode(
  builder: FlowBuilder,
  input: {
    path: string;
    type: FlowNodeType;
    subtype?: FlowNodeSubtype;
    parentId?: string;
    data: JsonRecord;
  }
): string {
  const id = stableId(input.path);
  if (builder.nodesById[id]) {
    throw new Error(`Flow projection produced duplicate node id: ${id}`);
  }
  builder.nodesById[id] = {
    id,
    type: input.type,
    subtype: input.subtype,
    parentId: input.parentId,
    children: [],
    data: flowDataObject(input.data),
  };
  return id;
}

function addChild(
  builder: FlowBuilder,
  parentId: string,
  id: string,
  role?: string,
  label?: string
): void {
  const child: FlowChild = { id };
  if (role !== undefined) child.role = role;
  if (label !== undefined) child.label = label;
  builder.nodesById[parentId].children.push(child);
}

function assertModelReferences(
  nodesById: Record<string, FlowNode>,
  rootId: string
): void {
  if (!nodesById[rootId]) throw new Error("Flow projection rootId is unresolved");

  for (const node of Object.values(nodesById)) {
    if (node.parentId && !nodesById[node.parentId]) {
      throw new Error(`Flow node parentId is unresolved: ${node.id}`);
    }
    for (const child of node.children) {
      if (!nodesById[child.id]) {
        throw new Error(`Flow child id is unresolved: ${child.id}`);
      }
    }
    if (node.nextId && !nodesById[node.nextId]) {
      throw new Error(`Flow node nextId is unresolved: ${node.id}`);
    }
  }
}

function stableId(path: string): string {
  return `flow:${path.replace(/[^A-Za-z0-9_.:-]+/g, "_")}`;
}

function omitKey(record: JsonRecord, omitted: string): JsonRecord {
  return Object.fromEntries(
    Object.entries(record).filter(([key]) => key !== omitted)
  );
}

function scalarLabel(value: unknown): string | undefined {
  return typeof value === "string" ||
    typeof value === "number" ||
    typeof value === "boolean"
    ? String(value)
    : undefined;
}

function requireArray(value: unknown, label: string): unknown[] {
  if (!Array.isArray(value)) throw new Error(`${label} must be an array`);
  return value;
}

function requireRecord(value: unknown, label: string): JsonRecord {
  if (!isRecord(value)) throw new Error(`${label} must be an object`);
  return value;
}

function isRecord(value: unknown): value is JsonRecord {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isSectionDefinition(value: JsonRecord): boolean {
  return "resolution" in value ||
    "role" in value ||
    "inputs" in value ||
    "outputs" in value ||
    "effects" in value ||
    "transformation_contract" in value;
}
