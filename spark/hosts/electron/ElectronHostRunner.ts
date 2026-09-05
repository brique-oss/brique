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
import { existsSync, readFileSync } from "fs";
import { createRequire } from "module";
import path from "path";

import {
  createRendererPayload,
  rendererPayloadToEnv,
  toBuiltFileUrl,
} from "../rendererPayload.js";
import type { SparkHostInput } from "../types.js";
import type { ElectronHostConfig, ElectronHostHandle } from "./types.js";

export function startElectronHost(input: SparkHostInput): ElectronHostHandle {
  const hostConfig = getElectronHostConfig(input.hostConfig);
  const sparkRoot = hostConfig?.sparkRoot ?? process.cwd();
  const electronBinary = resolveElectronBinary(hostConfig?.packageRoot ?? sparkRoot);
  const electronMain = path.resolve(
    sparkRoot,
    "dist",
    "spark",
    "hosts",
    "electron",
    "main.cjs"
  );
  const rendererEntrypoint =
    hostConfig?.renderer?.entrypoint ??
    path.join(sparkRoot, "dist", "spark", "hosts", "electron", "renderer", "index.html");
  const absRendererEntrypoint = path.resolve(rendererEntrypoint);
  const { ELECTRON_RUN_AS_NODE: _removed, ...parentEnv } = process.env;
  const exitCallbacks: Array<() => void> = [];

  const proc: ChildProcess = spawn(electronBinary, [electronMain], {
    stdio: hostConfig?.onLog ? ["ignore", "pipe", "pipe"] : "inherit",
    env: {
      ...parentEnv,
      ...buildElectronEnv(input, absRendererEntrypoint),
    },
  });

  if (hostConfig?.onLog) {
    attachLogPipe(proc.stdout, hostConfig.onLog);
    attachLogPipe(proc.stderr, hostConfig.onLog);
  }

  proc.on("exit", () => {
    for (const callback of exitCallbacks) callback();
  });

  return {
    stop: () => {
      if (!proc.killed) proc.kill();
    },
    onExit: (callback) => {
      exitCallbacks.push(callback);
    },
  };
}

function attachLogPipe(
  stream: NodeJS.ReadableStream | null,
  onLog: (line: string) => void
): void {
  if (!stream) return;
  stream.on("data", (chunk: Buffer | string) => {
    for (const line of String(chunk).split(/\r?\n/u)) {
      if (line.trim()) onLog(line);
    }
  });
}

export function buildElectronEnv(
  input: SparkHostInput,
  rendererEntrypoint: string
): Record<string, string> {
  const hostConfig = getElectronHostConfig(input.hostConfig);
  return rendererPayloadToEnv(
    createRendererPayload(input, (sourcePathOrUrl) =>
      toBuiltFileUrl(sourcePathOrUrl, hostConfig?.sparkRoot)
    ),
    rendererEntrypoint
  );
}

function getElectronHostConfig(hostConfig: unknown): ElectronHostConfig | undefined {
  if (
    typeof hostConfig === "object" &&
    hostConfig !== null &&
    (hostConfig as { kind?: unknown }).kind === "electron"
  ) {
    return hostConfig as ElectronHostConfig;
  }

  return undefined;
}

export function resolveElectronBinary(sparkRoot = process.cwd()): string {
  const packageBinary = resolveElectronPackageBinary(sparkRoot);
  if (packageBinary) return packageBinary;

  try {
    const require = createRequire(path.join(sparkRoot, "package.json"));
    const electronModule = require("electron") as unknown;
    if (typeof electronModule === "string") return electronModule;
    if (
      typeof electronModule === "object" &&
      electronModule !== null &&
      typeof (electronModule as { default?: unknown }).default === "string"
    ) {
      return (electronModule as { default: string }).default;
    }

    throw new Error(
      `Electron module resolved to ${typeof electronModule}, not an executable path`
    );
  } catch {
    throw new Error("Electron binary not found. Install 'electron' dependency.");
  }
}

function resolveElectronPackageBinary(sparkRoot: string): string | null {
  try {
    const requireFromSparkRoot = createRequire(path.join(sparkRoot, "package.json"));
    const packageJsonPath = requireFromSparkRoot.resolve("electron/package.json");
    const packageRoot = path.dirname(packageJsonPath);
    const executablePathFile = path.join(packageRoot, "path.txt");

    if (!existsSync(executablePathFile)) return null;

    const executablePath = readFileSync(executablePathFile, "utf-8").trim();
    if (!executablePath) return null;

    const overrideDistPath = process.env.ELECTRON_OVERRIDE_DIST_PATH;
    if (overrideDistPath) {
      return path.join(overrideDistPath, executablePath);
    }

    return path.join(packageRoot, "dist", executablePath);
  } catch {
    return null;
  }
}
