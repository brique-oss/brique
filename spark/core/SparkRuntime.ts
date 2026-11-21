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
import { isIP } from "net";

import type { SparkHostHandle, SparkHostInput, SparkHostRunner } from "../hosts/types.js";
import { launchBrique } from "../launchers/BriqueLauncher.js";
import type { BriqueLaunchOptions } from "../launchers/BriqueLauncher.js";
import { resolveUI } from "../ui/UIResolver.js";
import { briqueBinaryName } from "../util/briqueBinaryName.js";
import {
  defaultRuntimeDirectory,
  tryVerifyEngineRuntime,
  waitForReadyEngineRuntime,
  type EngineRuntimeDescriptor,
} from "../util/localDiscovery.js";

import type {
  SparkContextConfig,
  SparkRuntimeHandle,
  SparkStartOptions,
  SparkUIRuntimeConfig,
} from "./types.js";

export type BriqueHandle = { stop: () => void };

export type ContextUIBuildInput = {
  contextPath: string;
  contextDir: string;
  rootViewName: string;
  adapterName: string;
  sparkRoot: string;
  rootViewSourcePath: string;
  instanceRootDir?: string;
};

export type ContextUIBuildOutput = {
  rootViewSourcePath: string;
  rootViewBuiltPath: string;
  rootViewBuiltUrl: string;
  outputDir: string;
};

export type ContextUIBuilder = (
  input: ContextUIBuildInput
) => Promise<ContextUIBuildOutput>;

export type SparkDeps = {
  launchBrique?: (binaryPath: string, contextPath: string, options?: BriqueLaunchOptions) => BriqueHandle;
  buildContextUI?: ContextUIBuilder;
  startHost?: (input: SparkHostInput) => SparkHostHandle | Promise<SparkHostHandle>;
  findEngineRuntime?: (rootDir: string, runtimeDirectory: string) => Promise<EngineRuntimeDescriptor | null>;
  waitForEngineRuntime?: (rootDir: string, runtimeDirectory: string) => Promise<EngineRuntimeDescriptor>;
};

export async function startSpark(
  options: SparkStartOptions,
  deps: SparkDeps = {}
): Promise<SparkRuntimeHandle> {
  const {
    contextPath,
    briqueBinaryPath = path.join(".", "bin", briqueBinaryName()),
    runtimeDirectory: runtimeDirectoryInput = defaultRuntimeDirectory(),
    sparkRoot = process.cwd(),
    uiConfig: uiConfigOverride,
    host: hostOverride,
    hostConfig,
    security,
  } = options;
  const absContextPath = path.resolve(contextPath);
  const runtimeDirectory = path.resolve(runtimeDirectoryInput);
  const context = await loadContext(absContextPath);

  if (!context.brique) {
    throw new Error("Invalid context: missing brique root");
  }

  const uiConfig = uiConfigOverride ?? context.brique.ui_config;

  if (!uiConfig) {
    throw new Error("Missing ui_config in context");
  }

  const ui = await resolveUI(
    uiConfig,
    path.dirname(absContextPath),
    sparkRoot
  );
  const launch = deps.launchBrique ?? launchBrique;
  const findRuntime = deps.findEngineRuntime ?? tryVerifyEngineRuntime;
  const waitForRuntime = deps.waitForEngineRuntime ?? waitForReadyEngineRuntime;

  const buildInput = {
    contextPath: absContextPath,
    contextDir: path.dirname(absContextPath),
    rootViewName: ui.rootView.name,
    adapterName: ui.adapter.name,
    sparkRoot,
    rootViewSourcePath: toLocalPath(ui.rootView.path),
  };
  const uiBuild = isSparkOwnedUI(buildInput.rootViewSourcePath, sparkRoot)
    ? createSparkOwnedUIBuild(buildInput.rootViewSourcePath)
    : await (deps.buildContextUI ?? await loadDefaultContextUIBuilder(sparkRoot))(buildInput);
  const resolvedUI = withBuiltRootView(ui, uiBuild);
  const selectedHost = hostOverride ?? ui.host;
  const selectedUI =
    selectedHost === resolvedUI.host
      ? resolvedUI
      : { ...resolvedUI, host: selectedHost };
  const rootDir = path.dirname(absContextPath);
  let descriptor = await findRuntime(rootDir, runtimeDirectory);
  let brique: BriqueHandle;
  if (descriptor) {
    if (descriptor.state === "failed" || descriptor.state === "stopping") {
      throw new Error(`Brique engine is ${descriptor.state} for ${rootDir}`);
    }
    brique = createAttachedBriqueHandle();
  } else {
    brique = launch(briqueBinaryPath, absContextPath, {
        runtimeDirectory,
        websocketAddress: "127.0.0.1:0",
        securityDirectory: security?.directory,
        securityGeneration: security?.initialGeneration,
        auditMaxFileMiB: security?.auditMaxFileMiB,
        auditRetentionDays: security?.auditRetentionDays,
      });
  }
  let host: SparkHostHandle | null = null;

  try {
    if (!descriptor || descriptor.state !== "ready") {
      descriptor = await waitForRuntime(rootDir, runtimeDirectory);
    }
    if (!descriptor.ui_websocket_address) {
      throw new Error("ready Brique engine did not publish its UI websocket address");
    }
    const uiRuntime = resolveUIRuntime(context, {
      ...uiConfig,
      ws_addr: descriptor.ui_websocket_address,
    });
    const uiTransportUrl = uiRuntime.transportUrl;

    const hostInput: SparkHostInput = {
      contextPath: absContextPath,
      context,
      ui: selectedUI,
      uiRuntime,
      uiTransportUrl,
      hostConfig,
    };

    host = deps.startHost
      ? await deps.startHost(hostInput)
      : await (await loadDefaultHost(selectedHost, sparkRoot)).start(hostInput);

    if (!host) {
      throw new Error(`Unknown host: ${ui.host}`);
    }

    let stopped = false;
    return {
      contextPath: absContextPath,
      context,
      ui: selectedUI,
      uiRuntime,
      uiTransportUrl,
      brique,
      host,
      stop: () => {
        if (stopped) return;
        stopped = true;
        host?.stop();
        brique.stop();
      },
    };
  } catch (err) {
    brique.stop();
    throw err;
  }
}

function createAttachedBriqueHandle(): BriqueHandle {
  return { stop: () => {} };
}

async function loadDefaultContextUIBuilder(sparkRoot: string): Promise<ContextUIBuilder> {
  const moduleUrl = pathToFileURL(
    path.join(sparkRoot, "dist", "spark", "ui", "build", "ContextUIBuilder.js")
  ).href;
  const dynamicImport = Function("specifier", "return import(specifier)") as (specifier: string) => Promise<unknown>;
  const mod = await dynamicImport(moduleUrl) as {
    buildContextUI?: ContextUIBuilder;
  };
  if (typeof mod.buildContextUI !== "function") {
    throw new Error(`Context UI builder does not export buildContextUI: ${moduleUrl}`);
  }
  return mod.buildContextUI;
}

async function loadDefaultHost(
  hostName: string,
  sparkRoot: string
): Promise<SparkHostRunner> {
  const moduleUrl = pathToFileURL(
    path.join(sparkRoot, "dist", "spark", "hosts", hostName, "index.js")
  ).href;
  const dynamicImport = Function("specifier", "return import(specifier)") as (specifier: string) => Promise<unknown>;
  const mod = await dynamicImport(moduleUrl) as {
    sparkHost?: SparkHostRunner;
  };
  if (!mod.sparkHost || typeof mod.sparkHost.start !== "function") {
    throw new Error(`Host does not export sparkHost.start: ${moduleUrl}`);
  }
  return mod.sparkHost;
}

function withBuiltRootView(
  ui: Awaited<ReturnType<typeof resolveUI>>,
  uiBuild: ContextUIBuildOutput
): Awaited<ReturnType<typeof resolveUI>> {
  return {
    ...ui,
    rootView: {
      ...ui.rootView,
      path: uiBuild.rootViewBuiltUrl,
    },
  };
}

function toLocalPath(pathOrUrl: string): string {
  return pathOrUrl.startsWith("file:")
    ? fileURLToPath(pathOrUrl)
    : path.resolve(pathOrUrl);
}

function createSparkOwnedUIBuild(rootViewSourcePath: string): ContextUIBuildOutput {
  return {
    rootViewSourcePath,
    rootViewBuiltPath: rootViewSourcePath,
    rootViewBuiltUrl: pathToFileURL(rootViewSourcePath).href,
    outputDir: path.dirname(rootViewSourcePath),
  };
}

function isSparkOwnedUI(rootViewSourcePath: string, sparkRoot: string): boolean {
  const relativePath = path.relative(
    path.resolve(sparkRoot, "briqueEditorUI"),
    rootViewSourcePath
  );

  return !relativePath.startsWith("..") && !path.isAbsolute(relativePath);
}

export function resolveUIWebSocketUrl(context: SparkContextConfig): string {
  return resolveUIRuntime(context).transportUrl;
}

export function resolveUIRuntime(context: SparkContextConfig, uiConfigOverride?: { ws_addr?: string; ws_path?: string; name?: string }): SparkUIRuntimeConfig {
  const uiConfig = uiConfigOverride ?? context.brique.ui_config;
  const addr = uiConfigOverride?.ws_addr ?? resolveSharedWebSocketListenerAddr(context) ?? uiConfig?.ws_addr ?? "127.0.0.1:8080";
  const wsPath = uiConfig?.ws_path ?? "/ws";
  const uiName = normalizeUIName(uiConfig?.name ?? "main");

  return {
    uiName,
    uiAddressPrefix: `@ui_${uiName}`,
    rootContextId: "/root",
    transportUrl: resolveLocalUIWebSocketUrl(addr, wsPath),
  };
}

export function resolveLocalUIWebSocketUrl(addr: string, wsPath: string): string {
  const host = addr.startsWith(":") ? `127.0.0.1${addr}` : addr;
  const url = new URL(`ws://${host}${wsPath.startsWith("/") ? wsPath : `/${wsPath}`}`);
  const hostname = url.hostname.replace(/^\[|\]$/gu, "");
  const ipVersion = isIP(hostname);
  const loopback = hostname.toLowerCase() === "localhost" ||
    (ipVersion === 4 && hostname.startsWith("127.")) ||
    (ipVersion === 6 && hostname === "::1");
  if (!loopback) {
    throw new Error(
      `Spark UI requires a loopback websocket listener; remote UI authentication is not implemented (${addr})`
    );
  }
  return url.toString();
}

function resolveSharedWebSocketListenerAddr(context: SparkContextConfig): string | undefined {
  const communication = context.brique.engine_config?.communication as
    | { websocket_listener?: { addr?: string } }
    | undefined;
  const addr = communication?.websocket_listener?.addr;
  return typeof addr === "string" && addr !== "" ? addr : undefined;
}

async function loadContext(contextPath: string): Promise<SparkContextConfig> {
  const raw = await fs.readFile(contextPath, "utf-8");
  return JSON.parse(raw) as SparkContextConfig;
}


function normalizeUIName(name: string): string {
  const normalized = name.trim();
  if (!normalized) {
    throw new Error("UI interface name is required");
  }
  return normalized;
}
