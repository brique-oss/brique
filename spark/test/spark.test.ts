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

import assert from "node:assert/strict";
import test from "node:test";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";

import {
  startSpark,
  resolveUIRuntime,
  resolveUIWebSocketUrl,
} from "../core/SparkRuntime.js";
import { resolveUI } from "../ui/UIResolver.js";
import { buildContextUI } from "../ui/build/ContextUIBuilder.js";
import {
  buildElectronEnv,
  resolveElectronBinary,
} from "../hosts/electron/ElectronHostRunner.js";
import {
  createRendererPayload,
  toBuiltWorkspacePath,
} from "../hosts/rendererPayload.js";
import {
  buildWebviewHtml,
  startVsCodeHost,
} from "../hosts/vscode/VsCodeHostRunner.js";
import { resolveProjectedRootContextId } from "../core/ContextUILauncher.js";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

type StopTracked = { stop: () => void; stopCalled: boolean };

function makeBriqueMock(): StopTracked & { process: null } {
  const m = { process: null, stopCalled: false, stop() { m.stopCalled = true; } };
  return m;
}

function makeHostMock(): StopTracked & { onExit: (cb: () => void) => void } {
  const m = { stopCalled: false, stop() { m.stopCalled = true; }, onExit(_cb: () => void) {} };
  return m;
}

const noopWait = async (_url: string) => {};

async function writeTempContext(content: unknown): Promise<string> {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), "spark-test-"));
  const file = path.join(dir, "context.json");
  await fs.writeFile(file, JSON.stringify(content), "utf-8");
  return file;
}

const validContext = {
  brique: {
    type: "local",
    ctx_name: "test",
    ctx_type: "root",
    ui_config: {
      root_view: "EditorRoot",
      adapter: "react",
      host: "electron",
      name: "ui",
      ws_addr: ":9000",
      ws_path: "/ws",
    },
    engine_config: {
      communication: {
        interfaces: [
          {
            name: "ui",
            type: "ui",
            driver: "ws",
            config: { addr: ":9000", path: "/ws" },
          },
        ],
      },
    },
  },
};

function makeDefaultDeps(overrides: Record<string, unknown> = {}) {
  return {
    launchBrique: () => makeBriqueMock(),
    buildContextUI: (input: { rootViewSourcePath: string }) => ({
      rootViewSourcePath: input.rootViewSourcePath,
      rootViewBuiltPath: input.rootViewSourcePath,
      rootViewBuiltUrl: pathToFileURL(input.rootViewSourcePath).href,
      outputDir: path.dirname(input.rootViewSourcePath),
    }),
    startHost: () => makeHostMock(),
    waitForWebSocket: noopWait,
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// A. Context Loading
// ---------------------------------------------------------------------------

test("A: loads valid context.json", async () => {
  const contextPath = await writeTempContext(validContext);
  const handle = await startSpark({ contextPath }, makeDefaultDeps());
  assert.ok(handle);
  assert.equal(handle.contextPath, path.resolve(contextPath));
  handle.stop();
});

test("A: throws if context file does not exist", async () => {
  await assert.rejects(
    () => startSpark({ contextPath: "/nonexistent/context.json" }, makeDefaultDeps()),
    (err: Error) => {
      assert.ok(err instanceof Error);
      return true;
    }
  );
});

test("A: throws if JSON is invalid", async () => {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), "spark-test-"));
  const file = path.join(dir, "context.json");
  await fs.writeFile(file, "{ not valid json", "utf-8");
  await assert.rejects(() => startSpark({ contextPath: file }, makeDefaultDeps()));
});

test("A: throws if brique root is missing", async () => {
  const contextPath = await writeTempContext({ other: true });
  await assert.rejects(
    () => startSpark({ contextPath }, makeDefaultDeps()),
    /missing brique root/i
  );
});

// ---------------------------------------------------------------------------
// B. UI Config Validation
// ---------------------------------------------------------------------------

test("B: throws if ui_config is missing", async () => {
  const contextPath = await writeTempContext({ brique: { type: "local" } });
  await assert.rejects(
    () => startSpark({ contextPath }, makeDefaultDeps()),
    /ui_config/i
  );
});

test("B: throws if ui_config.root_view is missing", async () => {
  await assert.rejects(
    () => resolveUI({ root_view: "", adapter: "react", host: "electron" }),
    /root_view/i
  );
});

test("B: throws if ui_config.adapter is missing", async () => {
  await assert.rejects(
    () => resolveUI({ root_view: "EditorRoot", adapter: "", host: "electron" }),
    /adapter/i
  );
});

test("B: throws if ui_config.host is missing", async () => {
  await assert.rejects(
    () => resolveUI({ root_view: "EditorRoot", adapter: "react", host: "" }),
    /host/i
  );
});

test("B: throws if ui_config.host is not a string", async () => {
  await assert.rejects(
    () =>
      resolveUI({
        root_view: "EditorRoot",
        adapter: "react",
        host: { kind: "electron" },
      } as never),
    /ui_config\.host must be a non-empty string/i
  );
});

test("B: accepts valid ui_config", async () => {
  const contextPath = await writeTempContext(validContext);
  const handle = await startSpark({ contextPath }, makeDefaultDeps());
  assert.ok(handle.ui);
  handle.stop();
});

// ---------------------------------------------------------------------------
// C. UI Resolution
// ---------------------------------------------------------------------------

test("C: resolves React adapter", async () => {
  const contextPath = await writeTempContext(validContext);
  const handle = await startSpark({ contextPath }, makeDefaultDeps());
  assert.equal(handle.ui.adapter.name, "react");
  handle.stop();
});

test("C: resolves root view name", async () => {
  const contextPath = await writeTempContext(validContext);
  const handle = await startSpark({ contextPath }, makeDefaultDeps());
  assert.equal(handle.ui.rootView.name, "EditorRoot");
  handle.stop();
});

test("C: resolves root view path", async () => {
  const contextPath = await writeTempContext(validContext);
  const handle = await startSpark({ contextPath }, makeDefaultDeps());
  assert.ok(handle.ui.rootView.path.includes("EditorRoot"));
  handle.stop();
});

test("C: throws on unknown adapter", async () => {
  await assert.rejects(
    () => resolveUI({ root_view: "EditorRoot", adapter: "unknown-adapter", host: "electron" }),
    /unknown adapter/i
  );
});

test("C2: buildContextUI bundles a React context root and externalizes Spark/React imports", async () => {
  const contextDir = await fs.mkdtemp(path.join(os.tmpdir(), "spark-context-ui-"));
  const uiDir = path.join(contextDir, "ui");
  const blocksDir = path.join(uiDir, "blocks");
  await fs.mkdir(blocksDir, { recursive: true });
  const contextPath = path.join(contextDir, "context.json");
  const rootViewSourcePath = path.join(uiDir, "DemoRoot.tsx");

  await fs.writeFile(contextPath, JSON.stringify(validContext), "utf-8");
  await fs.writeFile(
    path.join(blocksDir, "Label.tsx"),
    [
      'import React from "react";',
      'export function Label() { return <span>ok</span>; }',
    ].join("\n"),
    "utf-8"
  );
  await fs.writeFile(
    rootViewSourcePath,
    [
      'import React from "react";',
      'import { usePulse } from "@spark/pulse/react";',
      'import { Label } from "./blocks/Label.js";',
      "export function DemoRoot() {",
      "  usePulse();",
      "  return <Label />;",
      "}",
      "export default DemoRoot;",
    ].join("\n"),
    "utf-8"
  );

  const output = await buildContextUI({
    contextPath,
    contextDir,
    rootViewName: "DemoRoot",
    adapterName: "react",
    sparkRoot: path.resolve("."),
    rootViewSourcePath,
  });
  const builtSource = await fs.readFile(output.rootViewBuiltPath, "utf-8");

  assert.equal(output.rootViewBuiltPath, path.join(contextDir, ".spark", "dist", "ui", "DemoRoot.js"));
  assert.match(builtSource, /from "react\/jsx-runtime"/);
  assert.match(builtSource, /from "@spark\/pulse\/react"/);
  assert.match(builtSource, /function Label/);
});

// ---------------------------------------------------------------------------
// D. Brique Launcher
// ---------------------------------------------------------------------------

test("D: calls Brique launcher with correct binary path", async () => {
  const captured = { binary: "" };
  const briqueMock = makeBriqueMock();
  const contextPath = await writeTempContext(validContext);

  const handle = await startSpark(
    { contextPath, briqueBinaryPath: "./custom/brique" },
    makeDefaultDeps({
      launchBrique: (binaryPath: string) => {
        captured.binary = binaryPath;
        return briqueMock;
      },
    })
  );

  assert.ok(captured.binary.includes("custom/brique"));
  handle.stop();
});

test("D: calls Brique launcher with correct context path", async () => {
  let capturedContextPath: string | null = null;
  const contextPath = await writeTempContext(validContext);

  const handle = await startSpark(
    { contextPath },
    makeDefaultDeps({
      launchBrique: (_: string, ctxPath: string) => {
        capturedContextPath = ctxPath;
        return makeBriqueMock();
      },
    })
  );

  assert.equal(capturedContextPath, path.resolve(contextPath));
  handle.stop();
});

test("D: exposes Brique stop handle", async () => {
  const briqueMock = makeBriqueMock();
  const contextPath = await writeTempContext(validContext);

  const handle = await startSpark(
    { contextPath },
    makeDefaultDeps({ launchBrique: () => briqueMock })
  );

  assert.equal(handle.brique, briqueMock);
  assert.equal(typeof handle.brique.stop, "function");
  handle.stop();
});

// ---------------------------------------------------------------------------
// E. UI WebSocket Resolution
// ---------------------------------------------------------------------------

test("E: extracts UI WebSocket URL from ui_config", async () => {
  const contextPath = await writeTempContext(validContext);
  const handle = await startSpark({ contextPath }, makeDefaultDeps());
  assert.equal(handle.uiTransportUrl, "ws://localhost:9000/ws");
  handle.stop();
});

test("E: resolves UI runtime from ui_config ws fields", () => {
  const ctx = {
    brique: {
      ...validContext.brique,
      ctx_name: "workspace",
      ui_config: {
        root_view: "EditorRoot",
        adapter: "react",
        host: "electron",
        name: "inspector",
        ws_addr: ":7777",
        ws_path: "/ui",
      },
    },
  };

  assert.deepEqual(resolveUIRuntime(ctx), {
    uiName: "inspector",
    uiAddressPrefix: "@ui_inspector",
    rootContextId: "/root",
    transportUrl: "ws://localhost:7777/ui",
  });
});

test("E: defaults address to :8080", () => {
  const ctx = {
    brique: {
      ...validContext.brique,
      ui_config: { root_view: "EditorRoot", adapter: "react", host: "electron" },
    },
  };
  const url = resolveUIWebSocketUrl(ctx);
  assert.ok(url.includes("8080"));
});

test("E: defaults path to /ws", () => {
  const ctx = {
    brique: {
      ...validContext.brique,
      ui_config: { root_view: "EditorRoot", adapter: "react", host: "electron", ws_addr: ":7777" },
    },
  };
  const url = resolveUIWebSocketUrl(ctx);
  assert.ok(url.endsWith("/ws"));
});

test("E: converts :8080 to ws://localhost:8080/ws", () => {
  const ctx = {
    brique: {
      ...validContext.brique,
      ui_config: { root_view: "EditorRoot", adapter: "react", host: "electron", ws_addr: ":8080", ws_path: "/ws" },
    },
  };
  assert.equal(resolveUIWebSocketUrl(ctx), "ws://localhost:8080/ws");
});

test("E: preserves explicit host address", () => {
  const ctx = {
    brique: {
      ...validContext.brique,
      ui_config: { root_view: "EditorRoot", adapter: "react", host: "electron", ws_addr: "192.168.1.5:4000", ws_path: "/ws" },
    },
  };
  assert.equal(resolveUIWebSocketUrl(ctx), "ws://192.168.1.5:4000/ws");
});

test("E: defaults uiName to main when name not set", () => {
  const ctx = {
    brique: {
      ...validContext.brique,
      ui_config: { root_view: "EditorRoot", adapter: "react", host: "electron" },
    },
  };
  assert.equal(resolveUIRuntime(ctx).uiName, "main");
});

// ---------------------------------------------------------------------------
// F. Host Startup
// ---------------------------------------------------------------------------

test("F: starts Electron host when host = electron", async () => {
  let hostStarted = false;
  const contextPath = await writeTempContext(validContext);

  const handle = await startSpark(
    { contextPath },
    makeDefaultDeps({
      startHost: () => {
        hostStarted = true;
        return makeHostMock();
      },
    })
  );

  assert.ok(hostStarted);
  handle.stop();
});

test("F: passes context path to host", async () => {
  let capturedContextPath: string | null = null;
  const contextPath = await writeTempContext(validContext);

  const handle = await startSpark(
    { contextPath },
    makeDefaultDeps({
      startHost: (input: { contextPath: string }) => {
        capturedContextPath = input.contextPath;
        return makeHostMock();
      },
    })
  );

  assert.equal(capturedContextPath, path.resolve(contextPath));
  handle.stop();
});

test("F: passes resolved UI to host", async () => {
  let capturedUI: unknown = null;
  const contextPath = await writeTempContext(validContext);

  const handle = await startSpark(
    { contextPath },
    makeDefaultDeps({
      startHost: (input: { ui: unknown }) => {
        capturedUI = input.ui;
        return makeHostMock();
      },
    })
  );

  assert.ok(capturedUI);
  handle.stop();
});

test("F: passes UI transport URL to host", async () => {
  let capturedUrl: string | null = null;
  const contextPath = await writeTempContext(validContext);

  const handle = await startSpark(
    { contextPath },
    makeDefaultDeps({
      startHost: (input: { uiTransportUrl: string }) => {
        capturedUrl = input.uiTransportUrl;
        return makeHostMock();
      },
    })
  );

  assert.equal(capturedUrl, "ws://localhost:9000/ws");
  handle.stop();
});

test("F: passes resolved UI runtime to host", async () => {
  let capturedRuntime: unknown = null;
  const ctx = {
    ...validContext,
    brique: {
      ...validContext.brique,
      ui_config: {
        root_view: "EditorRoot",
        adapter: "react",
        host: "electron",
        name: "panel",
        ws_addr: ":9091",
        ws_path: "/panel",
      },
    },
  };
  const contextPath = await writeTempContext(ctx);

  const handle = await startSpark(
    { contextPath },
    makeDefaultDeps({
      startHost: (input: { uiRuntime: unknown }) => {
        capturedRuntime = input.uiRuntime;
        return makeHostMock();
      },
    })
  );

  assert.deepEqual(capturedRuntime, {
    uiName: "panel",
    uiAddressPrefix: "@ui_panel",
    rootContextId: "/root",
    transportUrl: "ws://localhost:9091/panel",
  });
  assert.deepEqual(handle.uiRuntime, capturedRuntime);
  handle.stop();
});

test("F: throws on unknown host", async () => {
  const ctx = {
    ...validContext,
    brique: {
      ...validContext.brique,
      ui_config: { root_view: "EditorRoot", adapter: "react", host: "unknown-host" },
    },
  };
  const contextPath = await writeTempContext(ctx);

  await assert.rejects(
    () => startSpark({ contextPath }, makeDefaultDeps({
      startHost: (input: { ui: { host: string } }) => {
        throw new Error(`Unknown host: ${input.ui.host}`);
      },
    })),
    /unknown host/i
  );
});

test("F: host override selects a different host for the projection plan", async () => {
  let capturedHost: string | null = null;
  let capturedConfig: unknown = null;
  const contextPath = await writeTempContext(validContext);

  const handle = await startSpark(
    {
      contextPath,
      host: "vscode",
      hostConfig: { kind: "vscode", panel: { title: "Spark" } },
    },
    makeDefaultDeps({
      startHost: (input: { ui: { host: string }; hostConfig?: unknown }) => {
        capturedHost = input.ui.host;
        capturedConfig = input.hostConfig;
        return makeHostMock();
      },
    })
  );

  assert.equal(capturedHost, "vscode");
  assert.deepEqual(capturedConfig, { kind: "vscode", panel: { title: "Spark" } });
  assert.equal(handle.ui.host, "vscode");
  handle.stop();
});

test("F: uiConfig override selects extension UI without changing Brique context path", async () => {
  let capturedRootView: string | null = null;
  let capturedContextPath: string | null = null;
  const contextPath = await writeTempContext(validContext);

  const handle = await startSpark(
    {
      contextPath,
      uiConfig: {
        root_view: "BriqueEditorRoot",
        adapter: "react",
        host: "vscode",
      },
      host: "vscode",
    },
    makeDefaultDeps({
      launchBrique: (_binaryPath: string, ctxPath: string) => {
        capturedContextPath = ctxPath;
        return makeBriqueMock();
      },
      startHost: (input: { ui: { rootView: { name: string }; host: string } }) => {
        capturedRootView = input.ui.rootView.name;
        assert.equal(input.ui.host, "vscode");
        return makeHostMock();
      },
    })
  );

  assert.equal(capturedContextPath, path.resolve(contextPath));
  assert.equal(capturedRootView, "BriqueEditorRoot");
  assert.ok(handle.ui.rootView.path.includes("briqueEditorUI"));
  handle.stop();
});

// ---------------------------------------------------------------------------
// F2. Renderer Payload
// ---------------------------------------------------------------------------

function makeHostInputFixture() {
  return {
    contextPath: "/abs/path/context.json",
    context: validContext,
    ui: {
      adapter: {
        name: "react",
        renderer: {
          entrypoint: "file:///abs/path/pulse/adapters/react/runner.ts",
          importMap: {
            react: "file:///abs/path/dist/pulse/adapters/react/deps/react-vendor.js",
          },
        },
      },
      host: "electron",
      rootView: { name: "EditorRoot", path: "/abs/path/EditorRoot.tsx" },
    },
    uiTransportUrl: "ws://localhost:9000/ws",
    uiRuntime: {
      uiName: "panel",
      uiAddressPrefix: "@ui_panel",
      rootContextId: "/test",
      transportUrl: "ws://localhost:9000/ws",
    },
  };
}

test("F2: createRendererPayload maps projection fields through asset mapper", () => {
  const input = makeHostInputFixture();
  const payload = createRendererPayload(input as never, (value) => `mapped:${value}`);

  assert.equal(payload.contextPath, "/abs/path/context.json");
  assert.equal(payload.uiTransportUrl, "ws://localhost:9000/ws");
  assert.equal(payload.uiName, "panel");
  assert.equal(payload.uiAddressPrefix, "@ui_panel");
  assert.equal(payload.rootContextId, "/test");
  assert.equal(payload.rootViewName, "EditorRoot");
  assert.equal(payload.rootViewPath, "mapped:/abs/path/EditorRoot.tsx");
  assert.equal(payload.adapterName, "react");
  assert.equal(
    payload.adapterRendererPath,
    "mapped:file:///abs/path/pulse/adapters/react/runner.ts"
  );
  assert.equal(
    payload.adapterImportMap.react,
    "mapped:file:///abs/path/dist/pulse/adapters/react/deps/react-vendor.js"
  );
});

test("F2: toBuiltWorkspacePath keeps context-owned .spark build outputs in place", () => {
  const workspaceRoot = path.resolve("/workspace/Spark");
  const builtContextUI = path.join(
    workspaceRoot,
    "test",
    "sandbox",
    "root",
    ".spark",
    "dist",
    "ui",
    "SandboxRoot.js"
  );

  assert.equal(toBuiltWorkspacePath(builtContextUI, workspaceRoot), builtContextUI);
});

test("F2: buildElectronEnv serializes the common renderer payload", () => {
  const input = makeHostInputFixture();
  const env = buildElectronEnv(input as never, "/abs/renderer/index.html");
  const payload = JSON.parse(env.SPARK_RENDERER_PAYLOAD);

  assert.equal(payload.uiName, "panel");
  assert.equal(payload.uiAddressPrefix, "@ui_panel");
  assert.equal(payload.rootContextId, "/test");
  assert.equal(payload.uiTransportUrl, "ws://localhost:9000/ws");
  assert.equal(payload.contextPath, "/abs/path/context.json");
  assert.equal(payload.rootViewName, "EditorRoot");
  assert.equal(payload.adapterName, "react");
  assert.equal(env.SPARK_RENDERER_ENTRYPOINT, "/abs/renderer/index.html");
  assert.equal(env.SPARK_UI_NAME, payload.uiName);
});

test("F2: buildElectronEnv keeps compatibility SPARK_* vars", () => {
  const input = {
    contextPath: "/abs/path/context.json",
    context: validContext,
    ui: {
      adapter: {
        name: "react",
        renderer: {
          entrypoint: "file:///abs/path/pulse/adapters/react/runner.ts",
          importMap: {
            react: "file:///abs/path/dist/pulse/adapters/react/deps/react-vendor.js",
          },
        },
      },
      host: "electron",
      rootView: { name: "EditorRoot", path: "/abs/path/EditorRoot.tsx" },
    },
    uiTransportUrl: "ws://localhost:9000/ws",
    uiRuntime: {
      uiName: "panel",
      uiAddressPrefix: "@ui_panel",
      rootContextId: "/test",
      transportUrl: "ws://localhost:9000/ws",
    },
  };

  const env = buildElectronEnv(input as never, "/abs/renderer/index.html");

  assert.equal(env.SPARK_UI_NAME, "panel");
  assert.equal(env.SPARK_UI_ADDRESS_PREFIX, "@ui_panel");
  assert.equal(env.SPARK_ROOT_CONTEXT_ID, "/test");
  assert.equal(env.SPARK_UI_TRANSPORT_URL, "ws://localhost:9000/ws");
  assert.equal(env.SPARK_CONTEXT_PATH, "/abs/path/context.json");
  assert.equal(env.SPARK_RENDERER_ENTRYPOINT, "/abs/renderer/index.html");
  assert.equal(env.SPARK_UI_ROOT_VIEW, "EditorRoot");
  assert.equal(env.SPARK_ADAPTER, "react");
  assert.equal(env.SPARK_ADAPTER_RENDERER_PATH, "file:///abs/path/pulse/adapters/react/runner.ts");
  assert.equal(
    JSON.parse(env.SPARK_ADAPTER_IMPORT_MAP).react,
    "file:///abs/path/dist/pulse/adapters/react/deps/react-vendor.js"
  );
});

test("F2: resolveElectronBinary reads the electron package path from sparkRoot", async () => {
  const sparkRoot = await fs.mkdtemp(path.join(os.tmpdir(), "spark-electron-"));
  const electronPackageRoot = path.join(sparkRoot, "node_modules", "electron");
  await fs.mkdir(path.join(electronPackageRoot, "dist", "Electron"), {
    recursive: true,
  });
  await fs.writeFile(
    path.join(electronPackageRoot, "package.json"),
    JSON.stringify({ name: "electron", version: "0.0.0" }),
    "utf-8"
  );
  await fs.writeFile(
    path.join(electronPackageRoot, "path.txt"),
    path.join("Electron", "bin"),
    "utf-8"
  );
  await fs.writeFile(
    path.join(electronPackageRoot, "dist", "Electron", "bin"),
    "",
    "utf-8"
  );

  assert.equal(
    await fs.realpath(resolveElectronBinary(sparkRoot)),
    await fs.realpath(path.join(electronPackageRoot, "dist", "Electron", "bin"))
  );
});

test("F2: buildWebviewHtml injects common renderer payload and import map", () => {
  const input = makeHostInputFixture();
  const deps = {
    sparkRoot: "/abs/path",
    createUri: (fsPath: string) => ({
      fsPath,
      toString: () => fsPath,
    }),
    createWebviewPanel: (() => {
      throw new Error("not used");
    }) as never,
  };
  const panel = {
    webview: {
      html: "",
      asWebviewUri: (uri: { fsPath: string }) => ({
        toString: () => `webview:${uri.fsPath}`,
      }),
    },
    onDidDispose: () => ({ dispose() {} }),
    dispose() {},
  };

  const html = buildWebviewHtml(input as never, panel, deps);

  assert.match(html, /window\.__SPARK_RENDERER_PAYLOAD__/);
  assert.match(html, /webview:\/abs\/path\/dist\/EditorRoot\.js/);
  assert.match(html, /webview:\/abs\/path\/dist\/pulse\/adapters\/react\/runner\.js/);
  assert.match(html, /webview:\/abs\/path\/dist\/pulse\/adapters\/react\/deps\/react-vendor\.js/);
  assert.match(html, /"react"/);
});

test("F2: startVsCodeHost allows context-owned .spark build outputs", () => {
  const input = makeHostInputFixture();
  input.contextPath = "/abs/path/test/sandbox/root/context.json";
  let capturedResourceRoots: Array<{ fsPath: string }> = [];

  const handle = startVsCodeHost(input as never, {
    sparkRoot: "/abs/path",
    createUri: (fsPath: string) => ({
      fsPath,
      toString: () => fsPath,
    }),
    createWebviewPanel: (_viewType, _title, _showOptions, options) => {
      capturedResourceRoots = options.localResourceRoots;
      return {
        webview: {
          html: "",
          asWebviewUri: (uri: { fsPath: string }) => ({
            toString: () => `webview:${uri.fsPath}`,
          }),
          onDidReceiveMessage: () => ({ dispose() {} }),
        },
        onDidDispose: () => ({ dispose() {} }),
        dispose() {},
      };
    },
  });

  assert.deepEqual(
    capturedResourceRoots.map((uri) => uri.fsPath),
    [
      "/abs/path/dist",
      "/abs/path/node_modules",
      "/abs/path/test/sandbox/root/.spark/dist",
    ]
  );
  handle.stop();
});

test("F2: resolveProjectedRootContextId maps child context paths under the editor root", () => {
  assert.equal(
    resolveProjectedRootContextId(
      "/workspace/root/context.json",
      "/workspace/root/app_test/context.json",
      "/root"
    ),
    "/root/app_test"
  );
  assert.equal(
    resolveProjectedRootContextId(
      "/workspace/root/context.json",
      "/workspace/root/app_test/text_context/context.json",
      "/root"
    ),
    "/root/app_test/text_context"
  );
  assert.equal(
    resolveProjectedRootContextId(
      "/workspace/root/context.json",
      "/workspace/root/context.json",
      "/root"
    ),
    "/root"
  );
});

// ---------------------------------------------------------------------------
// G. Runtime Handle
// ---------------------------------------------------------------------------

test("G: startSpark returns runtime handle with all required fields", async () => {
  const briqueMock = makeBriqueMock();
  const hostMock = makeHostMock();
  const contextPath = await writeTempContext(validContext);

  const handle = await startSpark(
    { contextPath },
    makeDefaultDeps({
      launchBrique: () => briqueMock,
      startHost: () => hostMock,
    })
  );

  assert.ok(handle.contextPath);
  assert.ok(handle.context);
  assert.ok(handle.ui);
  assert.equal(handle.brique, briqueMock);
  assert.equal(handle.host, hostMock);
  assert.ok(handle.uiTransportUrl.startsWith("ws://"));
  assert.equal(typeof handle.stop, "function");

  handle.stop();
});

// ---------------------------------------------------------------------------
// H. Shutdown Lifecycle
// ---------------------------------------------------------------------------

test("H: stop() calls host stop", async () => {
  const hostMock = makeHostMock();
  const contextPath = await writeTempContext(validContext);

  const handle = await startSpark(
    { contextPath },
    makeDefaultDeps({ startHost: () => hostMock })
  );

  handle.stop();
  assert.ok(hostMock.stopCalled);
});

test("H: stop() calls Brique stop", async () => {
  const briqueMock = makeBriqueMock();
  const contextPath = await writeTempContext(validContext);

  const handle = await startSpark(
    { contextPath },
    makeDefaultDeps({ launchBrique: () => briqueMock })
  );

  handle.stop();
  assert.ok(briqueMock.stopCalled);
});

test("H: stop() is idempotent", async () => {
  let hostStopCount = 0;
  let briqueStopCount = 0;
  const contextPath = await writeTempContext(validContext);

  const handle = await startSpark(
    { contextPath },
    makeDefaultDeps({
      launchBrique: () => ({ process: null, stopCalled: false, stop: () => { briqueStopCount += 1; } }),
      startHost: () => ({ stopCalled: false, stop: () => { hostStopCount += 1; } }),
    })
  );

  handle.stop();
  handle.stop();

  assert.ok(hostStopCount >= 1);
  assert.ok(briqueStopCount >= 1);
});

// ---------------------------------------------------------------------------
// I. Error Handling
// ---------------------------------------------------------------------------

test("I: UI resolution failure prevents Brique launch", async () => {
  let briqueLaunched = false;
  const contextPath = await writeTempContext({
    brique: {
      ui_config: { root_view: "EditorRoot", adapter: "bad-adapter", host: "electron" },
    },
  });

  await assert.rejects(
    () =>
      startSpark(
        { contextPath },
        makeDefaultDeps({
          launchBrique: () => {
            briqueLaunched = true;
            return makeBriqueMock();
          },
        })
      )
  );

  assert.ok(!briqueLaunched);
});

test("I: host startup failure stops Brique", async () => {
  const briqueMock = makeBriqueMock();
  const ctx = {
    ...validContext,
    brique: {
      ...validContext.brique,
      ui_config: { root_view: "EditorRoot", adapter: "react", host: "unknown-host" },
    },
  };
  const contextPath = await writeTempContext(ctx);

  await assert.rejects(
    () =>
      startSpark(
        { contextPath },
        makeDefaultDeps({
          launchBrique: () => briqueMock,
          startHost: () => { throw new Error("host failed"); },
        })
      )
  );

  assert.ok(briqueMock.stopCalled);
});

test("I: websocket readiness failure stops Brique", async () => {
  const briqueMock = makeBriqueMock();
  const contextPath = await writeTempContext(validContext);

  await assert.rejects(
    () =>
      startSpark(
        { contextPath },
        makeDefaultDeps({
          launchBrique: () => briqueMock,
          waitForWebSocket: async () => {
            throw new Error("websocket unavailable");
          },
        })
      ),
    /websocket unavailable/
  );

  assert.ok(briqueMock.stopCalled);
});

test("I: startup errors are surfaced", async () => {
  const contextPath = await writeTempContext({ brique: {} });
  const err = await startSpark({ contextPath }, makeDefaultDeps()).catch((e) => e);
  assert.ok(err instanceof Error);
  assert.ok(err.message.length > 0);
});
