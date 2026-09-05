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

import type { CanonicalKey } from "../../types/index.js";

export type FlowNodeType =
  | "capacity"
  | "section"
  | "contract"
  | "resolution"
  | "flow"
  | "action"
  | "reference";

export type FlowNodeSubtype =
  | "sequence"
  | "if"
  | "switch"
  | "parallel"
  | "for_each"
  | "invoke"
  | "output"
  | "section_reference";

export type FlowChild = {
  id: string;
  role?: string;
  label?: string;
};

export type FlowDataScalar = string | number | boolean | null;

export type FlowDataEntry = {
  key: string;
  value: FlowDataValue;
};

export type FlowDataObject = {
  kind: "object";
  entries: FlowDataEntry[];
};

export type FlowDataArray = {
  kind: "array";
  items: FlowDataValue[];
};

export type FlowDataValue = FlowDataScalar | FlowDataObject | FlowDataArray;

export type FlowNode = {
  id: string;
  type: FlowNodeType;
  subtype?: FlowNodeSubtype;
  parentId?: string;
  children: FlowChild[];
  nextId?: string;
  data: FlowDataObject;
};

export type FlowProjectionWarning = {
  id: string;
  severity: "info" | "warning" | "error";
  message: string;
  targetKey?: CanonicalKey;
  createdAt?: number;
  metadata?: Record<string, unknown>;
};

export type FlowProjectionMetadata = {
  projection: "flow";
  root?: CanonicalKey;
  generatedAt?: number;
  warnings?: FlowProjectionWarning[];
  [key: string]: unknown;
};

export type FlowProjectionModel = {
  key: CanonicalKey;
  kind: "flow";
  rootId: string;
  nodesById: Record<string, FlowNode>;
  metadata?: FlowProjectionMetadata;
};
