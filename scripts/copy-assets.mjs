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

import { copyFile, mkdir, readdir, stat } from "node:fs/promises";
import path from "node:path";

async function copyNonTs(sourceDir, outputDir) {
  if (!(await exists(sourceDir))) return;
  await mkdir(outputDir, { recursive: true });
  const entries = await readdir(sourceDir, { withFileTypes: true });
  for (const entry of entries) {
    if (!entry.isFile()) continue;
    if (entry.name.endsWith(".ts") || entry.name.endsWith(".tsx")) continue;
    await copyFile(
      path.join(sourceDir, entry.name),
      path.join(outputDir, entry.name)
    );
  }
}

async function copyIfExists(source, destination) {
  if (!(await exists(source))) return;
  await mkdir(path.dirname(destination), { recursive: true });
  await copyFile(source, destination);
}

async function exists(target) {
  try {
    await stat(target);
    return true;
  } catch {
    return false;
  }
}

// Electron renderer static assets (shims + index.html) → electron dist
await copyNonTs(
  "spark/hosts/electron/renderer",
  "dist/spark/hosts/electron/renderer"
);

// Electron main.cjs → electron dist
await copyIfExists("spark/hosts/electron/main.cjs", "dist/spark/hosts/electron/main.cjs");

// VSCode renderer static assets (shims + index.html) → vscode dist
await copyNonTs(
  "spark/hosts/vscode/renderer",
  "dist/spark/hosts/vscode/renderer"
);

// Adapter static assets → main dist
await copyNonTs(
  "pulse/adapters/react",
  "dist/pulse/adapters/react"
);
