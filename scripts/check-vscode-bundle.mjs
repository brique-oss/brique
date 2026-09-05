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

import { readFile } from "node:fs/promises";

const extensionBundle = "dist/spark/hosts/vscode/extension.cjs";
const source = await readFile(extensionBundle, "utf-8");

const forbiddenPatterns = [
  ["Host resolver", /hosts\/HostResolver/u],
  ["esbuild package", /node_modules\/esbuild/u],
  ["esbuild bundled API error", /The esbuild JavaScript API cannot be bundled/u],
  ["bundled Electron createRequire(import.meta.url)", /createRequire\(import_meta\.url\)/u],
];

for (const [label, pattern] of forbiddenPatterns) {
  if (pattern.test(source)) {
    throw new Error(`Invalid VSCode extension bundle: found ${label}.`);
  }
}

const requiredPatterns = [
  ["context UI launcher", /launchSparkContextUI/u],
  ["external host loader", /loadSparkHost/u],
];

for (const [label, pattern] of requiredPatterns) {
  if (!pattern.test(source)) {
    throw new Error(`Invalid VSCode extension bundle: missing ${label}.`);
  }
}

console.log("VSCode extension bundle check passed.");
