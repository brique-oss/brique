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

import type { FlowDataObject } from "../../../../Brique_Substrate/projection/Flow/flowProjectionModel.js";

export type FlowSectionSummary = {
  nodeId: string;
  name: string;
  role?: string;
  morphing?: string;
  contract: FlowDataObject;
  resolutionNodeId?: string;
};

export type FlowExpansion = "collapsed" | "oneLevel";
