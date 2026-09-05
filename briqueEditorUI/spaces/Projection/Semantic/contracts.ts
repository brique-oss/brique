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

export type SemanticWorkspaceMode = "edit" | "use";

export type SemanticVocabularySnapshot = {
  vocabulary: Record<string, unknown>;
};

export type SemanticBootstrapState =
  | { kind: "loading"; message: string }
  | { kind: "rebuilding"; message: string }
  | { kind: "ready"; message: string; snapshot: SemanticVocabularySnapshot }
  | { kind: "error"; message: string };

export type SemanticWindowId = string;

export type VocabularyPath = string[];

export type SemanticWindowLayoutPatch = {
  id: SemanticWindowId;
  position?: { x: number; y: number };
  size?: { width: number; height: number };
};

export type SemanticWindowPathPatch = {
  id: SemanticWindowId;
  path: string[];
};

export type SemanticWindowSelectionPatch = {
  id: SemanticWindowId;
  selectedCategories?: string[];
  selectedValues?: string[];
};

export type SemanticWindowMatchModePatch = {
  id: SemanticWindowId;
  matchMode: "ANY" | "ALL";
};

export type SemanticFilterPath = string[];

export type SemanticWindowConfig = {
  id: SemanticWindowId;
  path: VocabularyPath;
  pos: { x: number; y: number };
  size: { width: number; height: number };
  valueLevel: boolean;
  defaultKeys: string[];
  defaultCombinator: "AND" | "OR";
};

export type SemanticWindowSelectionEntry = {
  selectedKeys: string[];
  combinator: "AND" | "OR";
};

export type SemanticWindowSelections = Record<SemanticWindowId, SemanticWindowSelectionEntry>;

export type SemanticWindowSelection = {
  windowId: SemanticWindowId;
  activePath: VocabularyPath;
  selectedKeys: string[];
  combinator: "AND" | "OR";
  valueLevel: boolean;
};

export type SemanticContextFilterMode = "strict" | "subtree";

export type CurrentSemanticFilter = {
  windows: SemanticWindowSelection[];
  kinds: string[];
  context?: { ctxId: string; mode: SemanticContextFilterMode };
};

export type SavedSemanticWindow = {
  id: string;
  path: VocabularyPath;
  pos: { x: number; y: number };
  size: { width: number; height: number };
  defaultKeys?: string[];
  defaultCombinator?: "AND" | "OR";
};

export type SavedSemanticWorkspace = {
  version: 1;
  mode: SemanticWorkspaceMode;
  windows: SavedSemanticWindow[];
};

export type SavedWorkspaceEntry = {
  name: string;
  matterId: string;
};

export type SavedWorkspaceIndex = {
  version: 1;
  entries: SavedWorkspaceEntry[];
};

export const WORKSPACE_REGISTRY_STRUCTURE_ID = "semantic-workspaces";

export function workspaceMatterId(name: string): string {
  return `semantic-workspace-${name}`;
}
