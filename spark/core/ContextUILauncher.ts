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

import fs from "fs/promises";
import path from "path";
import { fileURLToPath, pathToFileURL } from "url";

import type { SparkHostRunner } from "../hosts/types.js";
import { resolveUI } from "../ui/UIResolver.js";

import {
  resolveUIRuntime,
  type ContextUIBuildInput,
  type ContextUIBuildOutput,
  type ContextUIBuilder,
} from "./SparkRuntime.js";
import type {
  SparkContextConfig,
  SparkRuntimeHandle,
  SparkUIRuntimeConfig,
} from "./types.js";

export type LaunchSparkContextUIInput = {
  contextPath: string;
  editorContextPath: string;
  editorRootContextId: string;
  sparkRoot: string;
  onHostLog?: (line: string) => void;
};

export async function launchSparkContextUI(
  input: LaunchSparkContextUIInput
): Promise<SparkRuntimeHandle> {
  const contextPath = path.resolve(input.contextPath);
  const contextDir = path.dirname(contextPath);
  const instanceRootDir = await resolveBriqueInstanceRootDir(
    contextDir,
    path.dirname(path.resolve(input.editorContextPath))
  );
  const targetContext = JSON.parse(
    await fs.readFile(contextPath, "utf-8")
  ) as SparkContextConfig;

  const uiConfig = targetContext.brique?.ui_config;
  if (!uiConfig) {
    throw new Error("No ui_config found in context.");
  }

  const ui = await resolveUI(uiConfig, contextDir, input.sparkRoot);
  const buildContextUI = await loadContextUIBuilder(
    input.sparkRoot
  );
  const uiBuild = await buildContextUI({
    contextPath,
    contextDir,
    rootViewName: ui.rootView.name,
    adapterName: ui.adapter.name,
    sparkRoot: input.sparkRoot,
    rootViewSourcePath: toLocalPath(ui.rootView.path),
    instanceRootDir,
  });

  const resolvedUI = {
    ...ui,
    rootView: { ...ui.rootView, path: uiBuild.rootViewBuiltUrl },
  };

  const targetBaseUIRuntime = resolveUIRuntime(targetContext);
  const uiTransportUrl = await resolveContextTransportUrl(
    uiConfig,
    instanceRootDir,
    targetBaseUIRuntime.transportUrl
  );
  const targetUiRuntime: SparkUIRuntimeConfig = {
    ...targetBaseUIRuntime,
    rootContextId: resolveProjectedRootContextId(
      input.editorContextPath,
      contextPath,
      input.editorRootContextId
    ),
    transportUrl: uiTransportUrl,
  };

  const host = await startExternalHost({
    contextPath,
    context: targetContext,
    ui: resolvedUI,
    uiRuntime: targetUiRuntime,
    uiTransportUrl,
  }, instanceRootDir, input.sparkRoot, input.onHostLog);

  return {
    contextPath,
    context: targetContext,
    ui: resolvedUI,
    uiRuntime: targetUiRuntime,
    uiTransportUrl,
    brique: { stop: () => {} },
    host,
    stop: () => host.stop(),
  };
}

async function startExternalHost(
  hostInput: Omit<SparkRuntimeHandle, "brique" | "host" | "stop">,
  contextDir: string,
  sparkRoot: string,
  onHostLog?: (line: string) => void
): Promise<SparkRuntimeHandle["host"]> {
  if (hostInput.ui.host === "vscode") {
    throw new Error(
      "Context UI host 'vscode' cannot be launched as an external Spark UI."
    );
  }

  const hostConfig =
    hostInput.ui.host === "electron"
      ? {
          kind: "electron",
          sparkRoot,
          packageRoot: contextDir,
          onLog: onHostLog,
      }
      : undefined;

  return (await loadSparkHost(hostInput.ui.host, sparkRoot)).start({
    ...hostInput,
    hostConfig,
  });
}

async function loadContextUIBuilder(
  sparkRoot: string
): Promise<ContextUIBuilder> {
  const moduleUrl = pathToFileURL(
    path.join(sparkRoot, "dist", "spark", "ui", "build", "ContextUIBuilder.js")
  ).href;
  const mod = await dynamicImportModule<{
    buildContextUI?: (input: ContextUIBuildInput) => Promise<ContextUIBuildOutput>;
  }>(moduleUrl);
  if (typeof mod.buildContextUI !== "function") {
    throw new Error(`Context UI builder does not export buildContextUI: ${moduleUrl}`);
  }
  return mod.buildContextUI;
}

async function loadSparkHost(
  hostName: string,
  sparkRoot: string
): Promise<SparkHostRunner> {
  const moduleUrl = pathToFileURL(
    path.join(sparkRoot, "dist", "spark", "hosts", hostName, "index.js")
  ).href;
  const mod = await dynamicImportModule<{
    sparkHost?: SparkHostRunner;
  }>(moduleUrl);
  if (!mod.sparkHost || typeof mod.sparkHost.start !== "function") {
    throw new Error(`Host does not export sparkHost.start: ${moduleUrl}`);
  }
  return mod.sparkHost;
}

async function resolveBriqueInstanceRootDir(
  contextDir: string,
  activeRootDir: string
): Promise<string> {
  const resolvedContextDir = path.resolve(contextDir);
  const resolvedActiveRootDir = path.resolve(activeRootDir);
  const relativeToActiveRoot = path.relative(
    resolvedActiveRootDir,
    resolvedContextDir
  );

  if (
    relativeToActiveRoot === "" ||
    (
      !relativeToActiveRoot.startsWith("..") &&
      !path.isAbsolute(relativeToActiveRoot)
    )
  ) {
    return resolvedActiveRootDir;
  }

  let current = resolvedContextDir;
  while (true) {
    const candidateContextPath = path.join(current, "context.json");
    try {
      const context = JSON.parse(
        await fs.readFile(candidateContextPath, "utf-8")
      ) as SparkContextConfig;
      if (context.brique?.ctx_type === "root") {
        return current;
      }
    } catch {
      // Continue walking upward until a Brique root context is found.
    }

    const parent = path.dirname(current);
    if (parent === current) {
      throw new Error(
        `Unable to locate Brique instance root for context: ${resolvedContextDir}`
      );
    }
    current = parent;
  }
}

async function dynamicImportModule<T>(moduleUrl: string): Promise<T> {
  const dynamicImport = Function("specifier", "return import(specifier)") as (
    specifier: string
  ) => Promise<unknown>;
  return await dynamicImport(moduleUrl) as T;
}

function formatResolveError(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

function toLocalPath(pathOrUrl: string): string {
  return pathOrUrl.startsWith("file:")
    ? fileURLToPath(pathOrUrl)
    : path.resolve(pathOrUrl);
}

async function resolveContextTransportUrl(
  uiConfig: { ws_addr?: string; ws_path?: string },
  instanceRootDir: string,
  fallback: string
): Promise<string> {
  const wsPath = uiConfig.ws_path ?? "/ws";
  const addr = (await resolveInstanceSharedWebSocketListenerAddr(instanceRootDir)) ?? uiConfig.ws_addr;
  if (!addr) return fallback;
  const host = addr.startsWith(":") ? `localhost${addr}` : addr;
  return `ws://${host}${wsPath}`;
}

async function resolveInstanceSharedWebSocketListenerAddr(
  instanceRootDir: string
): Promise<string | undefined> {
  try {
    const rootContext = JSON.parse(
      await fs.readFile(path.join(instanceRootDir, "context.json"), "utf-8")
    ) as SparkContextConfig;
    const communication = rootContext.brique?.engine_config?.communication as
      | { websocket_listener?: { addr?: string } }
      | undefined;
    const addr = communication?.websocket_listener?.addr;
    return typeof addr === "string" && addr !== "" ? addr : undefined;
  } catch {
    return undefined;
  }
}

export function resolveProjectedRootContextId(
  editorContextPath: string,
  targetContextPath: string,
  editorRootContextId: string
): string {
  const editorContextDir = path.dirname(editorContextPath);
  const targetContextDir = path.dirname(targetContextPath);
  const relativeDir = path.relative(editorContextDir, targetContextDir);

  if (!relativeDir || relativeDir === ".") {
    return normalizeContextId(editorRootContextId);
  }

  if (relativeDir.startsWith("..") || path.isAbsolute(relativeDir)) {
    return normalizeContextId(editorRootContextId);
  }

  const suffix = relativeDir
    .split(path.sep)
    .filter(Boolean)
    .join("/");

  return `${normalizeContextId(editorRootContextId)}/${suffix}`;
}

function normalizeContextId(contextId: string): string {
  const trimmed = contextId.trim();
  if (!trimmed || trimmed === "/") return "/root";
  return `/${trimmed.replace(/^\/+/u, "").replace(/\/+$/u, "")}`;
}
