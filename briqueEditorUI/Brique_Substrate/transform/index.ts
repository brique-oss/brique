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
  ProjectionKind,
  ProjectionModelFor,
  ProjectionTransformInput,
  ProjectionTransformer,
  ProjectionTransforms,
} from "../types/index.js";
import { semanticTransformer } from "./Semantic/index.js";
import { traceTransformer } from "./Trace/index.js";
import { flowTransformer } from "./Flow/index.js";

export const projectionTransforms: ProjectionTransforms = {
  flow: flowTransformer,
  semantic: semanticTransformer,
  trace: traceTransformer,
};

export function transformProjection<K extends ProjectionKind>(
  input: ProjectionTransformInput<K>
): ProjectionModelFor<K> {
  const transformer = projectionTransforms[input.kind] as ProjectionTransformer<K>;
  return transformer.transform(input);
}

export { flowTransformer } from "./Flow/index.js";
export { semanticTransformer } from "./Semantic/index.js";
export { traceTransformer } from "./Trace/index.js";
