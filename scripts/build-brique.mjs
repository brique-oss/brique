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
// The administration tool of an instance's security (identity, keys, access rights, HTTPS).
const securityBin = path.join(outputDir, process.platform === "win32" ? "brique-sec.exe" : "brique-sec");
const launcherBin = path.join(outputDir, process.platform === "win32" ? "brique-launcher.exe" : "brique-launcher");
const webHostBin = path.join(outputDir, process.platform === "win32" ? "brique-web.exe" : "brique-web");
const browserHostBin = path.join(outputDir, process.platform === "win32" ? "brique-browser-host.exe" : "brique-browser-host");

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

console.log("Building brique-sec...");
const buildSecurity = spawnSync("go", ["build", "-C", engineDir, "-o", securityBin, "./cmd/brique-sec"], {
  stdio: "inherit",
});

if (buildSecurity.status !== 0) {
  process.exit(buildSecurity.status ?? 1);
}

console.log("Building brique-launcher...");
const buildLauncher = spawnSync("go", ["build", "-C", engineDir, "-o", launcherBin, "./cmd/brique-launcher"], {
  stdio: "inherit",
});

if (buildLauncher.status !== 0) {
  process.exit(buildLauncher.status ?? 1);
}

console.log("Building brique-web...");
const buildWebHost = spawnSync("go", ["build", "-C", engineDir, "-o", webHostBin, "./cmd/brique-web"], {
  stdio: "inherit",
});

if (buildWebHost.status !== 0) {
  process.exit(buildWebHost.status ?? 1);
}

console.log("Building brique-browser-host...");
const buildBrowserHost = spawnSync("go", ["build", "-C", engineDir, "-o", browserHostBin, "./cmd/brique-browser-host"], {
  stdio: "inherit",
});

if (buildBrowserHost.status !== 0) {
  process.exit(buildBrowserHost.status ?? 1);
}

if (process.platform !== "win32") {
  chmodSync(outputBin, 0o755);
  chmodSync(securityBin, 0o755);
  chmodSync(launcherBin, 0o755);
  chmodSync(webHostBin, 0o755);
  chmodSync(browserHostBin, 0o755);
}

console.log(`Brique binary ready: ${outputBin}`);
console.log(`brique-sec ready: ${securityBin}`);
console.log(`brique-launcher ready: ${launcherBin}`);
console.log(`brique-web ready: ${webHostBin}`);
console.log(`brique-browser-host ready: ${browserHostBin}`);
