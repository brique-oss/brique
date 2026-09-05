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

export type SemanticProjectionNode = {
  id: string;
  key?: CanonicalKey;
  kind: string;
  label?: string;
  data?: unknown;
  metadata?: Record<string, unknown>;
};

export type SemanticProjectionEdge = {
  id: string;
  source: string;
  target: string;
  kind?: string;
  label?: string;
  data?: unknown;
  metadata?: Record<string, unknown>;
};

export type SemanticProjectionWarning = {
  id: string;
  severity: "info" | "warning" | "error";
  message: string;
  targetKey?: CanonicalKey;
  createdAt?: number;
  metadata?: Record<string, unknown>;
};

export type SemanticProjectionMetadata = {
  projection: "semantic";
  root?: CanonicalKey;
  generatedAt?: number;
  warnings?: SemanticProjectionWarning[];
  [key: string]: unknown;
};

export type SemanticProjectionModel = {
  key: CanonicalKey;
  kind: "semantic";
  nodes: SemanticProjectionNode[];
  edges: SemanticProjectionEdge[];
  metadata?: SemanticProjectionMetadata;
};
