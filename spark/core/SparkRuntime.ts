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

import type { SparkHostHandle, SparkHostInput, SparkHostRunner } from "../hosts/types.js";
import { launchBrique } from "../launchers/BriqueLauncher.js";
import { resolveUI } from "../ui/UIResolver.js";
import { briqueBinaryName } from "../util/briqueBinaryName.js";
import { waitForWebSocket } from "../util/waitForWebSocket.js";

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
  launchBrique?: (binaryPath: string, contextPath: string) => BriqueHandle;
  buildContextUI?: ContextUIBuilder;
  startHost?: (input: SparkHostInput) => SparkHostHandle | Promise<SparkHostHandle>;
  waitForWebSocket?: (url: string) => Promise<void>;
};

export async function startSpark(
  options: SparkStartOptions,
  deps: SparkDeps = {}
): Promise<SparkRuntimeHandle> {
  const {
    contextPath,
    briqueBinaryPath = path.join(".", "bin", briqueBinaryName()),
    sparkRoot = process.cwd(),
    uiConfig: uiConfigOverride,
    host: hostOverride,
    hostConfig,
  } = options;
  const absContextPath = path.resolve(contextPath);
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
  const waitForUI = deps.waitForWebSocket ?? waitForWebSocket;

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
  const uiRuntime = resolveUIRuntime(context);
  const uiTransportUrl = uiRuntime.transportUrl;

  const attachState = await probeExistingBriqueEngine(uiTransportUrl, path.dirname(absContextPath));
  if (attachState.kind === "mismatch") {
    throw new Error(
      `Brique engine already running at ${uiTransportUrl} for ${attachState.actualContextDir}; expected ${attachState.expectedContextDir}`
    );
  }
  const brique = attachState.kind === "matching"
    ? createAttachedBriqueHandle(uiTransportUrl)
    : launch(briqueBinaryPath, absContextPath);
  let host: SparkHostHandle | null = null;

  try {
    await waitForUI(uiTransportUrl);

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
  } catch (err) {
    brique.stop();
    throw err;
  }

  if (!host) {
    brique.stop();
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
      host.stop();
      brique.stop();
    },
  };
}

type BriqueEngineProbe =
  | { kind: "none" }
  | { kind: "matching"; contextDir: string }
  | { kind: "mismatch"; expectedContextDir: string; actualContextDir: string };

async function probeExistingBriqueEngine(
  uiTransportUrl: string,
  expectedContextDir: string
): Promise<BriqueEngineProbe> {
  let state: BriqueEngineState | null = null;
  try {
    state = await readBriqueEngineState(uiTransportUrl);
  } catch {
    return { kind: "none" };
  }

  const actualContextDir = state.contextDir ? path.resolve(state.contextDir) : "";
  const resolvedExpected = path.resolve(expectedContextDir);

  if (actualContextDir === resolvedExpected) {
    return { kind: "matching", contextDir: actualContextDir };
  }

  return {
    kind: "mismatch",
    expectedContextDir: resolvedExpected,
    actualContextDir: actualContextDir || "unknown",
  };
}

type BriqueEngineState = { contextDir: string | null };

function readBriqueEngineState(uiTransportUrl: string): Promise<BriqueEngineState> {
  return new Promise((resolve, reject) => {
    const intentionId = crypto.randomUUID();
    const ws = new WebSocket(uiTransportUrl);
    const timeout = setTimeout(() => {
      ws.close();
      reject(new Error(`Timed out probing Brique engine at ${uiTransportUrl}`));
    }, 1_500);

    const cleanup = () => {
      clearTimeout(timeout);
      ws.onopen = null;
      ws.onerror = null;
      ws.onclose = null;
      ws.onmessage = null;
    };

    ws.onopen = () => {
      ws.send(JSON.stringify({
        kind: "intention",
        ts: new Date().toISOString(),
        intention: {
          intention_id: intentionId,
          await_response: true,
          to: { context: "/root", cap: "read.state", type: "reflexive" },
          from: { context: "@ui_attach_probe:/", cap: "ui", type: "user" },
          identity: { id: "brique-attach-probe", kind: "system" },
          params: { include: ["context"] },
          correlation: { root_intention_id: intentionId, parent_intention_id: null },
        },
      }));
    };
    ws.onerror = () => {
      cleanup();
      reject(new Error(`Unable to probe Brique engine at ${uiTransportUrl}`));
    };
    ws.onclose = () => {
      cleanup();
      reject(new Error(`Brique engine probe closed before response at ${uiTransportUrl}`));
    };
    ws.onmessage = (event) => {
      if (typeof event.data !== "string") return;
      const incoming = JSON.parse(event.data) as { response?: { intention_id?: string; payload?: unknown } };
      const response = incoming.response;
      if (response?.intention_id !== intentionId) return;
      cleanup();
      ws.close();
      resolve({ contextDir: extractStateContextDir(response.payload) });
    };
  });
}

function extractStateContextDir(payload: unknown): string | null {
  if (!isRecord(payload)) return null;
  const contextPayload = payload.context;
  if (!isRecord(contextPayload)) return null;
  return typeof contextPayload.context_dir === "string" ? contextPayload.context_dir : null;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function createAttachedBriqueHandle(uiTransportUrl: string): BriqueHandle {
  return {
    stop: () => {
      console.log(`[brique] attached engine left running at ${uiTransportUrl}`);
    },
  };
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
  const addr = resolveSharedWebSocketListenerAddr(context) ?? uiConfig?.ws_addr ?? ":8080";
  const wsPath = uiConfig?.ws_path ?? "/ws";
  const host = addr.startsWith(":") ? `localhost${addr}` : addr;
  const uiName = normalizeUIName(uiConfig?.name ?? "main");

  return {
    uiName,
    uiAddressPrefix: `@ui_${uiName}`,
    rootContextId: "/root",
    transportUrl: `ws://${host}${wsPath}`,
  };
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
