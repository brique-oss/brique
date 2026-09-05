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

import type { PulseRuntime } from "../runtime/index.js";

export type AdapterRootViewModule = {
  default?: unknown;
  [key: string]: unknown;
};

export type AdapterMountInput = {
  container: Element;
  pulse: PulseRuntime;
  contextPath: string;
  childContextPaths: string[];
  rootContextId: string;
  hostBridge?: {
    clearTraceFiles?: (input: { contextDirs: string[]; recursive?: boolean }) => Promise<{ contextDirs: string[]; deletedCount: number }>;
    launchContextUI?: (contextPath: string) => void | Promise<void>;
    openLocalPath?: (input: { path: string; reveal?: boolean }) => void | Promise<void>;
  };
  transportUrl: string;
  rootViewName: string;
  rootViewPath: string;
};

export type PulseAdapterRenderer = {
  mountPulseRoot: (input: AdapterMountInput) => void | Promise<void>;
};
