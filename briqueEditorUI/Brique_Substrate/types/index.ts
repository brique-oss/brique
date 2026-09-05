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
  FlowProjectionModel as FlowModel,
  SemanticProjectionModel as SemanticModel,
  TraceProjectionModel as TraceModel,
} from "../projection/index.js";

export type CanonicalKey = {
  context: string;
  kind: string;
  id: string;
};

export type SubstrateProjectionKind = "flow" | "trace" | "semantic";

export type ProjectionKind = SubstrateProjectionKind;

export type RawEntry = unknown;

export type RawError = {
  origin?: string;
  code?: string;
  message?: string;
  details?: Record<string, unknown>;
};

/**
 * The result of get() — always tells the UI why data is absent.
 *
 * - "ok"     : payload is the data resolved by the current request
 * - "error"  : Brique returned a non-ok status; payload is undefined, error is set
 * - "absent" : the resolver does not know this key (no capability mapped)
 */
export type RawResult =
  | { status: "ok";     payload: RawEntry }
  | { status: "error";  payload: undefined; error: RawError }
  | { status: "absent"; payload: undefined };

export type ProjectionResult<K extends ProjectionKind = ProjectionKind> =
  | { status: "ok";     model: ProjectionModelFor<K> }
  | { status: "error";  model: undefined; error: RawError }
  | { status: "absent"; model: undefined };

export type BriqueCapability = string;

export type ExecuteInput = {
  context: string;
  capability: BriqueCapability;
  params?: Record<string, unknown>;
  awaitResponse?: boolean;
};

export type ExecuteResult = {
  intentionId: string;
  response: unknown;
};

export type RawResolution = ExecuteInput | {
  input: ExecuteInput;
  parse?: (response: unknown) => unknown;
};

export type ProjectionModel =
  | FlowModel
  | SemanticModel
  | TraceModel;

export type ProjectionModelFor<K extends ProjectionKind> =
  K extends "flow"
    ? FlowModel
    : K extends "semantic"
      ? SemanticModel
      : TraceModel;

export type BriqueSubstrate = {
  execute(input: ExecuteInput): Promise<ExecuteResult>;
  get(key: CanonicalKey): Promise<RawResult>;
  project<K extends ProjectionKind>(
    key: CanonicalKey,
    kind: K
  ): Promise<ProjectionResult<K>>;
  mutate(input: ExecuteInput): Promise<ExecuteResult>;
  teardown(): void;
};

export type RawResolver = {
  resolve(key: CanonicalKey): RawResolution | undefined;
};

export type ProjectionTransformInput<K extends ProjectionKind = ProjectionKind> = {
  key: CanonicalKey;
  kind: K;
  raw: RawEntry;
};

export type ProjectionTransformer<K extends ProjectionKind = ProjectionKind> = {
  kind: K;
  transform(input: ProjectionTransformInput<K>): ProjectionModelFor<K>;
};

export type ProjectionTransforms = {
  [K in ProjectionKind]: ProjectionTransformer<K>;
};

export type {
  FlowChild,
  FlowNode,
  FlowNodeSubtype,
  FlowNodeType,
  FlowProjectionMetadata,
  FlowProjectionModel,
  FlowProjectionWarning,
  SemanticProjectionEdge,
  SemanticProjectionMetadata,
  SemanticProjectionModel,
  SemanticProjectionNode,
  SemanticProjectionWarning,
  TraceProjectionEdge,
  TraceProjectionMetadata,
  TraceProjectionModel,
  TraceProjectionNode,
  TraceProjectionWarning,
} from "../projection/index.js";
