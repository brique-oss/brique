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

export type PreviewTarget = {
  elementKind: string;
  elementName: string;
  context: string;
  pos: { x: number; y: number };
};

export type OpenTarget = {
  elementKind: string;
  elementName: string;
  context: string;
};

export type ResolvedDocument =
  | { kind: "local"; name: string; rel: string; abs: string }
  | { kind: "ref"; name: string; ref: string }
  | { kind: "error"; message: string };

export type ResolvedMatter =
  | { kind: "file_share"; name: string; abs: string; locator: string }
  | { kind: "brique_file"; name: string; abs: string }
  | { kind: "url"; name: string; locator: string }
  | { kind: "raw"; name: string; label: string; json: unknown }
  | { kind: "unsupported"; name: string; mode: string; wrapperName?: string }
  | { kind: "error"; message: string };
