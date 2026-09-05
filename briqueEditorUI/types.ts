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

export type ConnectionState =
  | "connected"
  | "reconnecting"
  | "disconnected"
  | "error";

export type OpenLocalPathInput = {
  path: string;
  reveal?: boolean;
};

export type ClearTraceFilesResult = {
  contextDirs: string[];
  deletedCount: number;
};

export type EditorHostBridge = {
  clearTraceFiles?: (input: { contextDirs: string[]; recursive?: boolean }) => Promise<ClearTraceFilesResult>;
  launchContextUI?: (contextPath: string) => void | Promise<void>;
  openLocalPath?: (input: OpenLocalPathInput) => void | Promise<void>;
  openWithOS?: (input: { path: string }) => void | Promise<void>;
  startFileDrag?: (input: { path: string }) => void | Promise<void>;
};
