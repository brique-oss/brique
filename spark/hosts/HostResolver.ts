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

import type { SparkHostRunner } from "./types.js";

export async function resolveHost(name: string): Promise<SparkHostRunner> {
  switch (name) {
    case "electron":
      return (await import("./electron/index.js")).sparkHost;
    case "vscode":
      return (await import("./vscode/index.js")).sparkHost;
    default:
      throw new Error(`Unknown host: ${name}`);
  }
}

