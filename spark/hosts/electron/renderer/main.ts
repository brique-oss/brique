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
      "[spark-renderer] payload",
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
    hostBridge: createElectronHostBridge(payload.contextPath),
    transportUrl: payload.uiTransportUrl,
    rootViewName: payload.rootViewName,
    rootViewPath: payload.rootViewPath,
  });
}

function createElectronHostBridge(rootContextPath: string) {
  return {
    async clearTraceFiles(input: { contextDirs: string[]; recursive?: boolean }) {
      const nodeRequire = (globalThis as { require?: NodeRequire }).require;
      if (!nodeRequire) throw new Error("Electron Node bridge is unavailable");
      const fs = nodeRequire("node:fs/promises") as typeof import("node:fs/promises");
      const path = nodeRequire("node:path") as typeof import("node:path");
      const rootDir = path.dirname(path.resolve(rootContextPath));
      const contextDirs = await expandContextDirs(
        input.contextDirs,
        input.recursive === true,
        path,
        fs.readFile,
      );
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
    },
  };
}

async function expandContextDirs(
  roots: string[],
  recursive: boolean,
  path: typeof import("node:path"),
  readFile: typeof import("node:fs/promises").readFile,
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
    const descriptor = JSON.parse(await readFile(path.join(contextDir, "context.json"), "utf-8")) as {
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

bootstrap().catch((err) => {
  console.error("[spark-renderer:error]", err);
  document.body.innerHTML = `<pre style="color:red;white-space:pre-wrap;">${String(err?.stack ?? err)}</pre>`;
});

function readRendererPayload(): SparkRendererPayload {
  const raw = process.env.SPARK_RENDERER_PAYLOAD;
  if (!raw) {
    throw new Error("Missing renderer env: SPARK_RENDERER_PAYLOAD");
  }
  return JSON.parse(raw) as SparkRendererPayload;
}
