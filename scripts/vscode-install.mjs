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
import { existsSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const rootDir = path.resolve(scriptDir, "..");
const vsixPath = path.join(rootDir, "brique-0.0.1.vsix");

const candidates = resolveCandidates();

for (const candidate of candidates) {
  const result = runInstall(candidate);
  if (result) process.exit(0);
}

console.error(
  "Could not find the VS Code 'code' CLI.\n" +
    "Run \"Shell Command: Install 'code' command in PATH\" from VS Code's command palette, then retry."
);
process.exit(1);

function resolveCandidates() {
  // "code" resolved via PATH is the standard cross-platform mechanism and is
  // tried first; the well-known per-OS install locations below are a
  // fallback for machines where the PATH command was never registered.
  const fromPath = ["code"];

  switch (process.platform) {
    case "darwin":
      return [
        ...fromPath,
        "/Applications/Visual Studio Code.app/Contents/Resources/app/bin/code",
        path.join(
          os.homedir(),
          "Applications/Visual Studio Code.app/Contents/Resources/app/bin/code"
        ),
      ];
    case "win32":
      return [
        ...fromPath,
        path.join(
          process.env.LOCALAPPDATA ?? "",
          "Programs",
          "Microsoft VS Code",
          "bin",
          "code.cmd"
        ),
        path.join(
          process.env.ProgramFiles ?? "",
          "Microsoft VS Code",
          "bin",
          "code.cmd"
        ),
      ];
    default:
      return [
        ...fromPath,
        "/usr/share/code/bin/code",
        "/usr/bin/code",
        "/snap/bin/code",
      ];
  }
}

function runInstall(candidate) {
  const isBareCommand = candidate === "code";
  if (!isBareCommand && !existsSync(candidate)) return false;

  console.log(`Installing extension via: ${candidate}`);
  const result = spawnSync(candidate, ["--install-extension", vsixPath, "--force"], {
    stdio: "inherit",
    shell: process.platform === "win32",
  });

  if (result.error || result.status !== 0) return false;
  return true;
}
