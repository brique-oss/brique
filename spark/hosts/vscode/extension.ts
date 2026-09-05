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

import * as vscode from "vscode";

import { launchSparkContextUI } from "../../core/ContextUILauncher.js";
import { startSpark } from "../../core/SparkRuntime.js";
import type { SparkRuntimeHandle } from "../../core/types.js";
import { launchBrique } from "../../launchers/BriqueLauncher.js";
import { briqueBinaryName } from "../../util/briqueBinaryName.js";
import { waitForWebSocket } from "../../util/waitForWebSocket.js";
import { startVsCodeHost } from "./VsCodeHostRunner.js";

let editorRuntime: SparkRuntimeHandle | null = null;
const projectionRuntimes = new Set<SparkRuntimeHandle>();
let outputChannel: vscode.OutputChannel | null = null;

export function activate(extensionContext: vscode.ExtensionContext): void {
  outputChannel = vscode.window.createOutputChannel("Spark");
  extensionContext.subscriptions.push(outputChannel);
  logSpark("Extension activated");

  extensionContext.subscriptions.push(
    vscode.commands.registerCommand("brique.openContext", async () => {
      const contextPath = await pickContextFile();
      if (!contextPath) return;
      await openSparkContext(extensionContext, contextPath);
    }),
    vscode.commands.registerCommand("brique.openWorkspaceContext", async () => {
      const contextPath = await pickWorkspaceContextFile();
      if (!contextPath) return;
      await openSparkContext(extensionContext, contextPath);
    }),
    vscode.commands.registerCommand("brique.stop", () => {
      stopEditorRuntime();
    }),
    vscode.commands.registerCommand("brique.launchContextUI", async (contextPath?: unknown) => {
      const selectedContextPath =
        typeof contextPath === "string" ? contextPath : await pickContextFile();
      if (!selectedContextPath) return;
      await launchContextUI(extensionContext, selectedContextPath);
    }),
    vscode.commands.registerCommand("brique.newInstance", async () => {
      await createNewInstance(extensionContext);
    })
  );
}

export function deactivate(): void {
  stopAllRuntimes();
  outputChannel = null;
}

export function formatBriqueEditorPanelTitle(contextPath: string): string {
  const contextDir = path.dirname(contextPath);
  const contextName = path.basename(contextDir);
  const instanceName = contextName === "root" ? path.basename(path.dirname(contextDir)) : contextName;
  return `Brique: ${instanceName || contextName}`;
}

async function openSparkContext(
  extensionContext: vscode.ExtensionContext,
  contextPath: string
): Promise<void> {
  try {
    stopEditorRuntime();
    const sparkRoot = extensionContext.extensionUri.fsPath;
    const title = formatBriqueEditorPanelTitle(contextPath);
    logSpark(`Opening editor context ${contextPath}`);

    const runtime = await startSpark(
      {
        contextPath,
        sparkRoot,
        briqueBinaryPath: path.join(sparkRoot, "bin", briqueBinaryName()),
        uiConfig: {
          root_view: "BriqueEditorRoot",
          adapter: "react",
          host: "vscode",
        },
        host: "vscode",
        hostConfig: {
          kind: "vscode",
          panel: {
            title,
            viewColumn: vscode.ViewColumn.One,
          },
        },
      },
      {
        launchBrique,
        waitForWebSocket,
        startHost: (input) =>
          startVsCodeHost(input, {
            sparkRoot,
            createUri: (fsPath) => vscode.Uri.file(fsPath),
            onWebviewMessage: (message, respond) => {
              void handleEditorMessage(extensionContext, message, respond);
            },
            createWebviewPanel: (viewType, panelTitle, showOptions, options) =>
              vscode.window.createWebviewPanel(
                viewType,
                panelTitle,
                showOptions,
                options
              ),
          }),
      }
    );

    editorRuntime = runtime;
    runtime.host.onExit(() => {
      if (editorRuntime === runtime) {
        editorRuntime = null;
      }
      runtime.stop();
    });
  } catch (err) {
    stopEditorRuntime();
    logSpark(formatError(err));
    vscode.window.showErrorMessage(formatError(err));
  }
}

async function launchContextUI(
  extensionContext: vscode.ExtensionContext,
  contextPath: string
): Promise<void> {
  try {
    if (!editorRuntime) {
      vscode.window.showErrorMessage("No active Spark editor runtime.");
      return;
    }

    const sparkRoot = extensionContext.extensionUri.fsPath;
    logSpark(`Launching context UI ${contextPath}`);

    const handle = await launchSparkContextUI({
      contextPath,
      editorContextPath: editorRuntime.contextPath,
      editorRootContextId: editorRuntime.uiRuntime.rootContextId,
      sparkRoot,
      onHostLog: (line) => logSpark(`[${path.basename(path.dirname(contextPath))}] ${line}`),
    });

    logSpark(
      `Resolved context UI host=${handle.ui.host} rootView=${handle.ui.rootView.name} transport=${handle.uiTransportUrl}`
    );

    projectionRuntimes.add(handle);
    handle.host.onExit(() => {
      projectionRuntimes.delete(handle);
    });
  } catch (err) {
    logSpark(formatError(err));
    vscode.window.showErrorMessage(formatError(err));
  }
}

async function createNewInstance(extensionContext: vscode.ExtensionContext): Promise<void> {
  const uris = await vscode.window.showOpenDialog({
    canSelectFiles: false,
    canSelectFolders: true,
    canSelectMany: false,
    title: "Select a destination folder for the new Brique instance",
  });

  const destDir = uris?.[0]?.fsPath;
  if (!destDir) return;

  const fs = await import("node:fs/promises");
  const templateDir = path.join(extensionContext.extensionUri.fsPath, "templates", "instance");

  try {
    await copyDirRecursive(fs, templateDir, destDir);
    logSpark(`New instance created at ${destDir}`);
    vscode.window.showInformationMessage(`New Brique instance created at ${destDir}`);
  } catch (err) {
    logSpark(formatError(err));
    vscode.window.showErrorMessage(formatError(err));
  }
}

async function copyDirRecursive(
  fs: typeof import("node:fs/promises"),
  src: string,
  dest: string
): Promise<void> {
  await fs.mkdir(dest, { recursive: true });
  const entries = await fs.readdir(src, { withFileTypes: true });
  for (const entry of entries) {
    const srcPath = path.join(src, entry.name);
    const destPath = path.join(dest, entry.name);
    if (entry.isDirectory()) {
      await copyDirRecursive(fs, srcPath, destPath);
    } else {
      await fs.copyFile(srcPath, destPath);
    }
  }
}

function logSpark(message: string): void {
  const line = `[${new Date().toISOString()}] ${message}`;
  outputChannel?.appendLine(line);
  console.log(`[spark-extension] ${message}`);
}

async function handleEditorMessage(
  extensionContext: vscode.ExtensionContext,
  message: unknown,
  respond?: (message: unknown) => void,
): Promise<void> {
  try {
    if (isLaunchContextUIMessage(message)) {
      await launchContextUI(extensionContext, message.contextPath);
      return;
    }

    if (isOpenLocalPathMessage(message)) {
      await openLocalPath(message);
      return;
    }

    if (isOpenWithOSMessage(message)) {
      await openWithOS(message);
      return;
    }

    if (isStartFileDragMessage(message)) {
      await startFileDrag(message);
      return;
    }

    if (isClearTraceFilesMessage(message)) {
      try {
        const result = await clearTraceFiles(message);
        respond?.({ kind: "spark.clearTraceFiles.result", requestId: message.requestId, ok: true, result });
      } catch (error) {
        respond?.({ kind: "spark.clearTraceFiles.result", requestId: message.requestId, ok: false, error: formatError(error) });
        throw error;
      }
    }
  } catch (err) {
    logSpark(formatError(err));
    console.error("[spark-extension] webview message failed", err);
  }
}

async function openLocalPath(input: OpenLocalPathMessage): Promise<void> {
  if (input.path.trim() === "") return;

  const uri = vscode.Uri.file(input.path);
  if (input.reveal) {
    await vscode.commands.executeCommand("revealInExplorer", uri);
    return;
  }

  await vscode.commands.executeCommand("vscode.open", uri);
}

async function pickContextFile(): Promise<string | undefined> {
  const uris = await vscode.window.showOpenDialog({
    canSelectFiles: true,
    canSelectFolders: false,
    canSelectMany: false,
    filters: {
      "Spark context": ["json"],
    },
    title: "Open Spark context.json",
  });

  return uris?.[0]?.fsPath;
}

async function pickWorkspaceContextFile(): Promise<string | undefined> {
  const matches = await vscode.workspace.findFiles(
    "**/context.json",
    "**/{node_modules,dist}/**",
    200
  );

  const rootMatches = await filterRootContextFiles(matches);

  if (rootMatches.length === 0) {
    vscode.window.showWarningMessage("No root context.json found in this workspace.");
    return undefined;
  }

  if (rootMatches.length === 1) {
    return rootMatches[0].fsPath;
  }

  const picked = await vscode.window.showQuickPick(
    rootMatches.map((uri: vscode.Uri) => ({
      label: vscode.workspace.asRelativePath(uri),
      uri,
    })),
    { title: "Open Spark root context" }
  );

  return picked?.uri.fsPath;
}

async function filterRootContextFiles(
  uris: readonly vscode.Uri[]
): Promise<vscode.Uri[]> {
  const fs = await import("node:fs/promises");
  const roots: vscode.Uri[] = [];

  for (const uri of uris) {
    try {
      const raw = await fs.readFile(uri.fsPath, "utf-8");
      const context = JSON.parse(raw) as {
        brique?: { ctx_type?: unknown };
      };

      if (context.brique?.ctx_type === "root") {
        roots.push(uri);
      }
    } catch {
      // Ignore invalid context candidates in the workspace picker.
    }
  }

  return roots;
}

function stopEditorRuntime(): void {
  if (!editorRuntime) return;
  const runtime = editorRuntime;
  editorRuntime = null;
  runtime.stop();
}

function stopAllRuntimes(): void {
  stopEditorRuntime();
  for (const runtime of [...projectionRuntimes]) {
    projectionRuntimes.delete(runtime);
    runtime.stop();
  }
}

function isLaunchContextUIMessage(message: unknown): message is {
  kind: "spark.launchContextUI";
  contextPath: string;
} {
  return (
    typeof message === "object" &&
    message !== null &&
    (message as { kind?: unknown }).kind === "spark.launchContextUI" &&
    typeof (message as { contextPath?: unknown }).contextPath === "string"
  );
}

type OpenLocalPathMessage = {
  kind: "spark.openLocalPath";
  path: string;
  reveal?: boolean;
};

function isOpenLocalPathMessage(message: unknown): message is OpenLocalPathMessage {
  return (
    typeof message === "object" &&
    message !== null &&
    (message as { kind?: unknown }).kind === "spark.openLocalPath" &&
    typeof (message as { path?: unknown }).path === "string" &&
    (
      (message as { reveal?: unknown }).reveal === undefined ||
      typeof (message as { reveal?: unknown }).reveal === "boolean"
    )
  );
}

type OpenWithOSMessage = {
  kind: "spark.openWithOS";
  path: string;
};

function isOpenWithOSMessage(message: unknown): message is OpenWithOSMessage {
  return (
    typeof message === "object" &&
    message !== null &&
    (message as { kind?: unknown }).kind === "spark.openWithOS" &&
    typeof (message as { path?: unknown }).path === "string"
  );
}

async function openWithOS(input: OpenWithOSMessage): Promise<void> {
  if (input.path.trim() === "") return;
  await vscode.env.openExternal(vscode.Uri.file(input.path));
}

type StartFileDragMessage = {
  kind: "spark.startFileDrag";
  path: string;
};

function isStartFileDragMessage(message: unknown): message is StartFileDragMessage {
  return (
    typeof message === "object" &&
    message !== null &&
    (message as { kind?: unknown }).kind === "spark.startFileDrag" &&
    typeof (message as { path?: unknown }).path === "string"
  );
}

async function startFileDrag(input: StartFileDragMessage): Promise<void> {
  if (input.path.trim() === "") return;
  const { webContents } = await import("electron");
  webContents.getFocusedWebContents()?.startDrag({
    file: input.path,
    icon: input.path,
  });
}

type ClearTraceFilesMessage = {
  kind: "spark.clearTraceFiles";
  requestId: string;
  contextDirs: string[];
  recursive?: boolean;
};

function isClearTraceFilesMessage(message: unknown): message is ClearTraceFilesMessage {
  return (
    typeof message === "object" &&
    message !== null &&
    (message as { kind?: unknown }).kind === "spark.clearTraceFiles" &&
    typeof (message as { requestId?: unknown }).requestId === "string" &&
    Array.isArray((message as { contextDirs?: unknown }).contextDirs) &&
    (message as { contextDirs: unknown[] }).contextDirs.every((value) => typeof value === "string") &&
    ((message as { recursive?: unknown }).recursive === undefined || typeof (message as { recursive?: unknown }).recursive === "boolean")
  );
}

async function clearTraceFiles(input: ClearTraceFilesMessage): Promise<{ contextDirs: string[]; deletedCount: number }> {
  if (!editorRuntime) throw new Error("No active Spark editor runtime.");
  const fs = await import("node:fs/promises");
  const rootDir = path.dirname(path.resolve(editorRuntime.contextPath));
  const contextDirs = await expandContextDirs(fs, input.contextDirs, input.recursive === true);

  let deletedCount = 0;
  for (const rawDir of contextDirs) {
    const contextDir = path.resolve(rawDir);
    const relative = path.relative(rootDir, contextDir);
    if (relative.startsWith("..") || path.isAbsolute(relative)) {
      throw new Error(`Trace directory is outside the opened context: ${contextDir}`);
    }
    const traceDir = path.join(contextDir, "trace");
    let entries;
    try {
      entries = await fs.readdir(traceDir, { withFileTypes: true });
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code === "ENOENT") continue;
      throw error;
    }
    const targets = entries
      .filter((entry) => entry.isFile() && entry.name.endsWith(".jsonl"))
      .map((entry) => path.join(traceDir, entry.name));
    await Promise.all(targets.map((target) => fs.unlink(target)));
    deletedCount += targets.length;
  }
  return { contextDirs, deletedCount };
}

async function expandContextDirs(
  fs: typeof import("node:fs/promises"),
  roots: string[],
  recursive: boolean,
): Promise<string[]> {
  if (!recursive) return roots;
  const result: string[] = [];
  const visited = new Set<string>();
  const pending = [...roots];
  while (pending.length > 0) {
    const contextDir = path.resolve(pending.shift()!);
    if (visited.has(contextDir)) continue;
    visited.add(contextDir);
    result.push(contextDir);
    const descriptor = JSON.parse(await fs.readFile(path.join(contextDir, "context.json"), "utf-8")) as {
      brique?: { children_list?: unknown };
    };
    if (!Array.isArray(descriptor.brique?.children_list)) continue;
    for (const child of descriptor.brique.children_list) {
      if (typeof child === "string" && child !== "" && child !== "." && child !== ".." && !child.includes("/") && !child.includes("\\")) {
        pending.push(path.join(contextDir, child));
      }
    }
  }
  return result;
}

function formatError(err: unknown): string {
  if (err instanceof Error) {
    return `Spark failed to start: ${err.message}`;
  }
  return `Spark failed to start: ${String(err)}`;
}
