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

import path from "path";

import {
  createRendererPayload,
  toBuiltWorkspacePath,
  type SparkRendererPayload,
} from "../rendererPayload.js";
import type { SparkHostInput } from "../types.js";
import { LocalWebSocketRelay, type SparkTransportRelay } from "../LocalWebSocketRelay.js";
import type { VsCodeHostConfig, VsCodeHostHandle } from "./types.js";

export type VsCodeWebviewPanel = {
  webview: {
    html: string;
    cspSource?: string;
    asWebviewUri: (uri: VsCodeUri) => { toString: () => string };
    postMessage?: (message: unknown) => Promise<boolean>;
    onDidReceiveMessage?: (
      callback: (message: unknown) => void
    ) => { dispose: () => void };
  };
  onDidDispose: (callback: () => void) => { dispose: () => void };
  dispose: () => void;
};

export type VsCodeUri = {
  fsPath: string;
  toString: () => string;
};

export type VsCodeHostRunnerDeps = {
  sparkRoot: string;
  createWebviewPanel: (
    viewType: string,
    title: string,
    showOptions: number | { viewColumn: number; preserveFocus?: boolean },
    options: {
      enableScripts: boolean;
      localResourceRoots: VsCodeUri[];
      retainContextWhenHidden: boolean;
    }
  ) => VsCodeWebviewPanel;
  createUri: (fsPath: string) => VsCodeUri;
  onWebviewMessage?: (message: unknown, respond: (message: unknown) => void) => void;
  createTransportRelay?: (url: string) => SparkTransportRelay;
};

export function startVsCodeHost(
  input: SparkHostInput,
  deps?: VsCodeHostRunnerDeps
): VsCodeHostHandle {
  if (!deps) {
    throw new Error("VSCode host must be started from the Spark VSCode extension");
  }

  const hostConfig = getVsCodeHostConfig(input.hostConfig);
  const title = hostConfig?.panel?.title ?? input.ui.rootView.name;
  const viewColumn = hostConfig?.panel?.viewColumn ?? 1;
  const showOptions = hostConfig?.panel?.preserveFocus
    ? { viewColumn, preserveFocus: true }
    : viewColumn;
  const distRoot = path.join(deps.sparkRoot, "dist");
  const nodeModulesRoot = path.join(deps.sparkRoot, "node_modules");
  const contextBuildRoot = path.join(
    path.dirname(input.contextPath),
    ".spark",
    "dist"
  );

  const distUri = deps.createUri(distRoot);
  const nodeModulesUri = deps.createUri(nodeModulesRoot);
  const contextBuildUri = deps.createUri(contextBuildRoot);

  const exitCallbacks: Array<() => void> = [];

  const panel = deps.createWebviewPanel("spark", title, showOptions, {
    enableScripts: true,
    localResourceRoots: [distUri, nodeModulesUri, contextBuildUri],
    retainContextWhenHidden: true,
  });

  const relay = deps.createTransportRelay?.(input.uiTransportUrl) ??
    new LocalWebSocketRelay(input.uiTransportUrl);
  const relayUnsubscribeMessage = relay.subscribe((data) => {
    void panel.webview.postMessage?.({ kind: "spark.transport.message", data });
  });
  const relayUnsubscribeState = relay.subscribeToConnectionState((state) => {
    void panel.webview.postMessage?.({ kind: "spark.transport.state", state });
  });

  panel.webview.html = buildWebviewHtml(input, panel, deps);

  const disposeListener = panel.onDidDispose(() => {
    for (const callback of exitCallbacks) callback();
  });
  const messageListener = panel.webview.onDidReceiveMessage
    ? panel.webview.onDidReceiveMessage((message) => {
        const respond = (response: unknown) => { void panel.webview.postMessage?.(response); };
        if (handleTransportMessage(message, relay, respond)) return;
        deps.onWebviewMessage?.(message, respond);
      })
    : undefined;

  return {
    stop: () => {
      disposeListener.dispose();
      messageListener?.dispose();
      relayUnsubscribeMessage();
      relayUnsubscribeState();
      relay.close();
      panel.dispose();
    },
    onExit: (callback) => {
      exitCallbacks.push(callback);
    },
  };
}

export function buildWebviewHtml(
  input: SparkHostInput,
  panel: VsCodeWebviewPanel,
  deps: VsCodeHostRunnerDeps
): string {
  const payload = createRendererPayload(input, (sourcePathOrUrl) =>
    toWebviewUri(sourcePathOrUrl, panel, deps)
  );
  const nonce = createNonce();
  const cspSource = panel.webview.cspSource ?? "'self'";
  const rendererUri = toWebviewUri(
    path.join(deps.sparkRoot, "spark", "hosts", "vscode", "renderer", "main.ts"),
    panel,
    deps
  );
  const styleLinks = payload.adapterStyleUrls
    .map((href) => `    <link rel="stylesheet" href="${escapeHtml(href)}" />`)
    .join("\n");

  return `<!doctype html>
<html>
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src ${cspSource} https: data:; style-src ${cspSource} 'unsafe-inline'; script-src 'nonce-${nonce}' ${cspSource}; connect-src 'none';" />
    <title>${escapeHtml(payload.rootViewName)}</title>
${styleLinks}
    <script nonce="${nonce}" type="importmap">
${escapeScriptJson({ imports: payload.adapterImportMap })}
    </script>
    <script nonce="${nonce}">
      const sparkVsCodeApi = typeof acquireVsCodeApi === "function"
        ? acquireVsCodeApi()
        : undefined;
      window.__SPARK_VSCODE_HOST__ = {
        clearTraceFiles(input) {
          return new Promise((resolve, reject) => {
            const requestId = String(Date.now()) + "-" + String(Math.random());
            const timeout = setTimeout(() => {
              window.removeEventListener("message", onMessage);
              reject(new Error("Trace deletion timed out"));
            }, 30000);
            function onMessage(event) {
              const message = event.data;
              if (message?.kind !== "spark.clearTraceFiles.result" || message?.requestId !== requestId) return;
              clearTimeout(timeout);
              window.removeEventListener("message", onMessage);
              if (message.ok) resolve(message.result);
              else reject(new Error(message.error || "Trace deletion failed"));
            }
            window.addEventListener("message", onMessage);
            sparkVsCodeApi?.postMessage({
              kind: "spark.clearTraceFiles",
              requestId,
              contextDirs: input?.contextDirs,
              recursive: input?.recursive
            });
          });
        },
        launchContextUI(contextPath) {
          sparkVsCodeApi?.postMessage({
            kind: "spark.launchContextUI",
            contextPath
          });
        },
        openLocalPath(input) {
          sparkVsCodeApi?.postMessage({
            kind: "spark.openLocalPath",
            path: input?.path,
            reveal: input?.reveal
          });
        },
        openWithOS(input) {
          sparkVsCodeApi?.postMessage({
            kind: "spark.openWithOS",
            path: input?.path
          });
        },
        startFileDrag(input) {
          sparkVsCodeApi?.postMessage({
            kind: "spark.startFileDrag",
            path: input?.path
          });
        }
      };
      const sparkTransportMessageSubscribers = new Set();
      const sparkTransportStateSubscribers = new Set();
      const sparkTransportPending = new Map();
      let sparkTransportState = "disconnected";
      window.addEventListener("message", (event) => {
        const message = event.data;
        if (message?.kind === "spark.transport.message" && typeof message.data === "string") {
          for (const subscriber of sparkTransportMessageSubscribers) subscriber(message.data);
          return;
        }
        if (message?.kind === "spark.transport.state" && (message.state === "connected" || message.state === "disconnected")) {
          sparkTransportState = message.state;
          for (const subscriber of sparkTransportStateSubscribers) subscriber(message.state);
          return;
        }
        if (message?.kind === "spark.transport.result" && typeof message.requestId === "string") {
          const pending = sparkTransportPending.get(message.requestId);
          if (!pending) return;
          sparkTransportPending.delete(message.requestId);
          clearTimeout(pending.timeout);
          if (message.ok) pending.resolve();
          else pending.reject(new Error(message.error || "Spark transport request failed"));
        }
      });
      function sparkTransportRequest(kind, data) {
        return new Promise((resolve, reject) => {
          const requestId = crypto.randomUUID();
          const timeout = setTimeout(() => {
            sparkTransportPending.delete(requestId);
            reject(new Error("Spark transport request timed out"));
          }, 15000);
          sparkTransportPending.set(requestId, { resolve, reject, timeout });
          sparkVsCodeApi?.postMessage({ kind, requestId, data });
        });
      }
      window.__SPARK_TRANSPORT_BRIDGE__ = {
        open() { sparkVsCodeApi?.postMessage({ kind: "spark.transport.open" }); },
        send(data) { return sparkTransportRequest("spark.transport.send", data); },
        subscribe(callback) {
          sparkTransportMessageSubscribers.add(callback);
          return () => sparkTransportMessageSubscribers.delete(callback);
        },
        subscribeToConnectionState(callback) {
          sparkTransportStateSubscribers.add(callback);
          callback(sparkTransportState);
          return () => sparkTransportStateSubscribers.delete(callback);
        },
        close() { sparkVsCodeApi?.postMessage({ kind: "spark.transport.close" }); }
      };
    </script>
    <script nonce="${nonce}">
      window.__SPARK_RENDERER_PAYLOAD__ = ${escapeScriptJson(payload)};
    </script>
  </head>
  <body>
    <div id="root"></div>
    <script nonce="${nonce}" type="module" src="${rendererUri}"></script>
  </body>
</html>`;
}

function getVsCodeHostConfig(hostConfig: unknown): VsCodeHostConfig | undefined {
  if (
    typeof hostConfig === "object" &&
    hostConfig !== null &&
    (hostConfig as { kind?: unknown }).kind === "vscode"
  ) {
    return hostConfig as VsCodeHostConfig;
  }

  return undefined;
}

function toWebviewUri(
  sourcePathOrUrl: string,
  panel: VsCodeWebviewPanel,
  deps: VsCodeHostRunnerDeps
): string {
  const builtPath = toBuiltWorkspacePath(sourcePathOrUrl, deps.sparkRoot);
  return panel.webview.asWebviewUri(deps.createUri(builtPath)).toString();
}

function escapeScriptJson(value: unknown): string {
  return JSON.stringify(value).replace(/</gu, "\\u003c");
}

function escapeHtml(value: string): string {
  return value
    .replace(/&/gu, "&amp;")
    .replace(/</gu, "&lt;")
    .replace(/>/gu, "&gt;")
    .replace(/"/gu, "&quot;");
}

function createNonce(): string {
  const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789";
  let nonce = "";
  for (let i = 0; i < 32; i += 1) {
    nonce += chars[Math.floor(Math.random() * chars.length)];
  }
  return nonce;
}

function handleTransportMessage(
  value: unknown,
  relay: SparkTransportRelay,
  respond: (message: unknown) => void,
): boolean {
  if (typeof value !== "object" || value === null) return false;
  const message = value as { kind?: unknown; requestId?: unknown; data?: unknown };
  if (message.kind === "spark.transport.open") {
    relay.open();
    return true;
  }
  if (message.kind === "spark.transport.close") {
    relay.close();
    return true;
  }
  if (message.kind !== "spark.transport.send") return false;
  if (typeof message.requestId !== "string" || typeof message.data !== "string") {
    return true;
  }
  void relay.send(message.data).then(
    () => respond({ kind: "spark.transport.result", requestId: message.requestId, ok: true }),
    (error) => respond({
      kind: "spark.transport.result",
      requestId: message.requestId,
      ok: false,
      error: error instanceof Error ? error.message : "Spark transport send failed",
    }),
  );
  return true;
}

export type { SparkRendererPayload };
