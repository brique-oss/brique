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

export type InspectionTarget = {
  context: string;
  elementKind: string;
  elementName: string;
  sourcePath?: string;
};

export type InspectionDisplayMode = "expanded" | "collapsed";

export type InspectionMode = "inspect" | "browse" | "search";

export type SemanticBrowseItem = {
  element_id: string;
  ctx_id: string;
  element_type: string;
  name: string;
};

export type SemanticBrowseState =
  | { kind: "idle" }
  | { kind: "loading" }
  | { kind: "ready"; items: SemanticBrowseItem[] }
  | { kind: "error"; message: string };

export type ReflexiveMeaningSections = {
  brique?: unknown;
  objective?: unknown;
  subjective?: unknown;
  functional?: unknown;
};

export type InspectionReadResult = ReflexiveMeaningSections;

export type InspectionReadState = {
  target: InspectionTarget | undefined;
  capability: "read.meaning" | "matter.read" | "structure.read" | undefined;
  result: InspectionReadResult | undefined;
  loading: boolean;
  error: string | undefined;
};
