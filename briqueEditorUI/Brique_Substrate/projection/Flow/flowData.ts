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
  FlowDataArray,
  FlowDataObject,
  FlowDataValue,
} from "./flowProjectionModel.js";

export function flowDataObject(value: unknown): FlowDataObject {
  if (!isRecord(value)) {
    throw new Error("Flow node data must be an object");
  }
  return {
    kind: "object",
    entries: Object.entries(value).map(([key, entryValue]) => ({
      key,
      value: flowDataValue(entryValue),
    })),
  };
}

export function flowDataValue(value: unknown): FlowDataValue {
  if (value === null || typeof value === "string" ||
      typeof value === "number" || typeof value === "boolean") {
    return value;
  }
  if (Array.isArray(value)) {
    const array: FlowDataArray = {
      kind: "array",
      items: value.map(flowDataValue),
    };
    return array;
  }
  return flowDataObject(value);
}

export function flowDataGet(
  object: FlowDataObject | undefined,
  key: string
): FlowDataValue | undefined {
  return object?.entries.find((entry) => entry.key === key)?.value;
}

export function isFlowDataObject(
  value: FlowDataValue | undefined
): value is FlowDataObject {
  return typeof value === "object" && value !== null && value.kind === "object";
}

export function isFlowDataArray(
  value: FlowDataValue | undefined
): value is FlowDataArray {
  return typeof value === "object" && value !== null && value.kind === "array";
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
