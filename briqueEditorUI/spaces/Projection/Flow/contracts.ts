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

import type { FlowProjectionModel } from "../../../Brique_Substrate/projection/Flow/flowProjectionModel.js";
import type { FlowCapacityTarget } from "../contracts.js";

export type FlowCapacitySummary = {
  name: string;
  context: string;
  objective?: string;
};

export type FlowProjectionState = {
  target: FlowCapacityTarget | undefined;
  model: FlowProjectionModel | undefined;
  loading: boolean;
  error: string | undefined;
};

export type FlowProjectionFeedbackState =
  | { kind: "hidden" }
  | { kind: "loading"; message: string }
  | { kind: "empty"; message: string }
  | { kind: "error"; message: string };
