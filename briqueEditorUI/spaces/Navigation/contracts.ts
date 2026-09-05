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

import type { InspectionTarget, InspectionDisplayMode, InspectionMode } from "../Inspection/contracts.js";
import type { ProjectionKind, FlowCapacityTarget } from "../Projection/contracts.js";
import type {
  SemanticContextFilterMode,
  SemanticWindowConfig,
  SemanticWindowSelections,
  SemanticWorkspaceMode,
} from "../Projection/Semantic/contracts.js";

export type TabId = string;

export type TabState = {
  id: TabId;
  activeProjection: ProjectionKind;
  selectedElement: InspectionTarget | undefined;
  flowCapacityTarget: FlowCapacityTarget | undefined;
  inspectionDisplayMode: InspectionDisplayMode;
  windowSelections: SemanticWindowSelections;
  inspectSelections: SemanticWindowSelections;
  inspectSelectedKinds: Set<string>;
  inspectionMode: InspectionMode;
  selectedKinds: Set<string>;
  windowList: SemanticWindowConfig[];
  semanticWorkspaceMode: SemanticWorkspaceMode;
  contextFilterMode: "off" | SemanticContextFilterMode;
  initialStructureContext?: string;
  history: InspectionTarget[];
  showHistory: boolean;
};

const HISTORY_MAX = 500;

export function appendHistory(history: InspectionTarget[], entry: InspectionTarget): InspectionTarget[] {
  const last = history[history.length - 1];
  if (last && last.context === entry.context && last.elementKind === entry.elementKind && last.elementName === entry.elementName) {
    return history;
  }
  const next = [...history, entry];
  return next.length > HISTORY_MAX ? next.slice(next.length - HISTORY_MAX) : next;
}

export function makeEmptyTab(id: TabId): TabState {
  return {
    id,
    activeProjection: "structure",
    selectedElement: undefined,
    flowCapacityTarget: undefined,
    inspectionDisplayMode: "expanded",
    windowSelections: {},
    inspectSelections: {},
    inspectSelectedKinds: new Set(),
    inspectionMode: "inspect",
    selectedKinds: new Set(),
    windowList: [],
    semanticWorkspaceMode: "edit",
    contextFilterMode: "off",
    history: [],
    showHistory: false,
  };
}

export function makeSeededTab(id: TabId, element: InspectionTarget): TabState {
  const isCapacity = element.elementKind === "capacity";
  return {
    ...makeEmptyTab(id),
    activeProjection: isCapacity ? "flow" : "structure",
    selectedElement: element,
    initialStructureContext: isCapacity ? undefined : element.context,
    flowCapacityTarget: isCapacity
      ? { context: element.context, capacityName: element.elementName }
      : undefined,
  };
}

let _tabCounter = 0;
export function nextTabId(): TabId {
  return `tab-${++_tabCounter}`;
}
