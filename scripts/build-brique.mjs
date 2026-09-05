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

import { spawnSync } from "node:child_process";
import { chmodSync, existsSync, mkdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const rootDir = path.resolve(scriptDir, "..");
const engineDir = path.join(rootDir, "engine");
const outputDir = path.join(rootDir, "bin");
// Keep in sync with util/briqueBinaryName.ts (this script runs standalone,
// outside the tsc/dist pipeline, so it cannot import compiled TS output).
const outputBin = path.join(outputDir, process.platform === "win32" ? "brique.exe" : "brique");

if (spawnSync("go", ["version"], { stdio: "ignore" }).error) {
  console.error("go is required to build Brique");
  process.exit(1);
}

if (!existsSync(path.join(engineDir, "go.mod"))) {
  console.error(`Brique Engine not found at ${engineDir}`);
  process.exit(1);
}

mkdirSync(outputDir, { recursive: true });

console.log("Building Brique Engine...");
console.log(`  source: ${engineDir}`);
console.log(`  output: ${outputBin}`);

const build = spawnSync("go", ["build", "-C", engineDir, "-o", outputBin, "."], {
  stdio: "inherit",
});

if (build.status !== 0) {
  process.exit(build.status ?? 1);
}

if (process.platform !== "win32") {
  chmodSync(outputBin, 0o755);
}

console.log(`Brique binary ready: ${outputBin}`);
