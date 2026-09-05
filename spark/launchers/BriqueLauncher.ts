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

import { spawn, type ChildProcess } from "child_process";
import path from "path";

export type BriqueProcess = {
  process: ChildProcess;
  stop: () => void;
};

export function launchBrique(
  binaryPath: string,
  contextPath: string
): BriqueProcess {
  const absBinary = path.resolve(binaryPath);
  const absContext = path.resolve(contextPath);
  const rootDir = path.dirname(absContext);

  const proc = spawn(absBinary, ["-root_path", rootDir], {
    stdio: ["ignore", "pipe", "pipe"],
  });

  proc.stdout.on("data", (data) => {
    console.log(`[brique] ${data.toString()}`);
  });

  proc.stderr.on("data", (data) => {
    console.error(`[brique:error] ${data.toString()}`);
  });

  proc.on("error", (err) => {
    console.error("[brique:error]", err);
  });

  proc.on("exit", (code) => {
    console.log(`[brique] exited with code ${code}`);
  });

  return {
    process: proc,
    stop: () => {
      if (!proc.killed) {
        proc.kill();
      }
    },
  };
}

