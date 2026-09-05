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

import type { ResolvedUI } from "../ui/types.js";
import type { UIConfig } from "../ui/types.js";
import type { SparkHostHandle } from "../hosts/types.js";

export type SparkContextConfig = {
  brique: {
    type?: string;
    ctx_name?: string;
    ctx_type?: string;
    children_list?: string[];
    engine_config?: Record<string, unknown>;
    ui_config?: {
      root_view: string;
      adapter: string;
      host: string;
      ws_addr?: string;
      ws_path?: string;
      name?: string;
    };
  };
};

export type SparkUIRuntimeConfig = {
  uiName: string;
  uiAddressPrefix: string;
  rootContextId: string;
  transportUrl: string;
};

export type SparkStartOptions = {
  contextPath: string;
  briqueBinaryPath?: string;
  sparkRoot?: string;
  uiConfig?: UIConfig;
  host?: string;
  hostConfig?: unknown;
};

export type SparkProjectionPlan = {
  contextPath: string;
  context: SparkContextConfig;
  ui: ResolvedUI;
  uiRuntime: SparkUIRuntimeConfig;
  uiTransportUrl: string;
};

export type SparkRuntimeHandle = SparkProjectionPlan & {
  brique: { stop: () => void };
  host: SparkHostHandle;
  stop: () => void;
};
