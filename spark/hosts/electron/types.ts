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

import type { SparkHostInput, SparkHostHandle } from "../types.js";

export type ElectronWindowConfig = {
  width?: number;
  height?: number;
  minWidth?: number;
  minHeight?: number;
  resizable?: boolean;
  maximized?: boolean;
  fullscreen?: boolean;
  title?: string;
};

export type ElectronRendererConfig = {
  entrypoint?: string;
  preload?: string;
  devUrl?: string;
};

export type ElectronHostConfig = {
  kind: "electron";
  sparkRoot?: string;
  packageRoot?: string;
  onLog?: (line: string) => void;
  window?: ElectronWindowConfig;
  renderer?: ElectronRendererConfig;
};

export type ElectronHostInput = SparkHostInput & {
  hostConfig?: ElectronHostConfig;
};

export type ElectronHostHandle = SparkHostHandle;
