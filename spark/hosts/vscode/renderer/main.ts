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

import { PulseRuntime } from "../../../../pulse/runtime/index.js";
import type { PulseAdapterRenderer } from "../../../../pulse/adapters/renderer.js";
import type { SparkRendererPayload } from "../../rendererPayload.js";

async function bootstrap() {
  const payload = readRendererPayload();
  console.info(
    [
      "[spark-vscode-renderer] payload",
      `context=${payload.contextPath}`,
      `root=${payload.rootContextId}`,
      `ui=${payload.uiName}`,
      `transport=${payload.uiTransportUrl}`,
      `rootView=${payload.rootViewName}`,
    ].join(" ")
  );

  const pulse = new PulseRuntime({
    webSocketUrl: payload.uiTransportUrl,
    transportName: payload.uiName,
    uiName: payload.uiName,
    uiAddressPrefix: payload.uiAddressPrefix,
  });

  const adapterRenderer = await import(payload.adapterRendererPath) as PulseAdapterRenderer;
  if (typeof adapterRenderer.mountPulseRoot !== "function") {
    throw new Error(
      `Adapter renderer does not export mountPulseRoot: ${payload.adapterRendererPath}`
    );
  }

  const container = document.getElementById("root");
  if (!container) {
    throw new Error("Missing renderer root element");
  }

  await adapterRenderer.mountPulseRoot({
    container,
    pulse,
    contextPath: payload.contextPath,
    childContextPaths: payload.childContextPaths,
    rootContextId: payload.rootContextId,
    hostBridge: readHostBridge(),
    transportUrl: payload.uiTransportUrl,
    rootViewName: payload.rootViewName,
    rootViewPath: payload.rootViewPath,
  });
}

function readHostBridge(): {
  clearTraceFiles?: (input: { contextDirs: string[]; recursive?: boolean }) => Promise<{ contextDirs: string[]; deletedCount: number }>;
  launchContextUI?: (contextPath: string) => void | Promise<void>;
  openLocalPath?: (input: { path: string; reveal?: boolean }) => void | Promise<void>;
  openWithOS?: (input: { path: string }) => void | Promise<void>;
  startFileDrag?: (input: { path: string }) => void | Promise<void>;
} | undefined {
  return (globalThis as {
    __SPARK_VSCODE_HOST__?: {
      clearTraceFiles?: (input: { contextDirs: string[]; recursive?: boolean }) => Promise<{ contextDirs: string[]; deletedCount: number }>;
      launchContextUI?: (contextPath: string) => void | Promise<void>;
      openLocalPath?: (input: { path: string; reveal?: boolean }) => void | Promise<void>;
      openWithOS?: (input: { path: string }) => void | Promise<void>;
      startFileDrag?: (input: { path: string }) => void | Promise<void>;
    };
  }).__SPARK_VSCODE_HOST__;
}

bootstrap().catch((err) => {
  console.error("[spark-vscode-renderer:error]", err);
  document.body.innerHTML = `<pre style="color:red;white-space:pre-wrap;">${String(err?.stack ?? err)}</pre>`;
});

function readRendererPayload(): SparkRendererPayload {
  const payload = (globalThis as {
    __SPARK_RENDERER_PAYLOAD__?: SparkRendererPayload;
  }).__SPARK_RENDERER_PAYLOAD__;

  if (!payload) {
    throw new Error("Missing VSCode renderer payload");
  }

  return payload;
}
