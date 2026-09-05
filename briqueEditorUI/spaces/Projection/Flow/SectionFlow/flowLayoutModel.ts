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
  FlowNode,
  FlowProjectionModel,
} from "../../../../Brique_Substrate/projection/Flow/flowProjectionModel.js";
import type { FlowExpansion } from "./contracts.js";

export type FlowLayoutBoxKind =
  | "capacity"
  | "resolution"
  | "sequence"
  | "parallel"
  | "if"
  | "switch"
  | "for_each"
  | "branch"
  | "section"
  | "contract"
  | "action"
  | "reference"
  | "empty";

export type FlowLayoutBox = {
  children: FlowLayoutBox[];
  id: string;
  kind: FlowLayoutBoxKind;
  label?: string;
  node?: FlowNode;
  expansion?: FlowExpansion;
};

export type FlowLayoutContext = {
  model: FlowProjectionModel;
  sectionExpansions: Record<string, FlowExpansion>;
};

// ---------------------------------------------------------------------------
// Spacing constants — expressed in px, used by both renderer and architecture
// ---------------------------------------------------------------------------

export const FRAME_PADDING_X = 16;
export const FRAME_PADDING_TOP = 20;
export const FRAME_PADDING_BOTTOM = 14;
export const GAP_X = 16;
export const GAP_Y = 14;
export const LEAF_PADDING_X = 12;
export const LEAF_PADDING_Y = 10;
export const BRANCH_LABEL_HEIGHT = 20;

export const CONTRACT_MAX_WIDTH = 480;
