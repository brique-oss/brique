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

import { copyFile, mkdir } from "node:fs/promises";
import path from "node:path";

import { build } from "esbuild";

// Bundle l'extension VS Code en CJS autonome — VS Code n'accepte pas les ESM extensions
await build({
  entryPoints: ["spark/hosts/vscode/extension.ts"],
  outfile: "dist/spark/hosts/vscode/extension.cjs",
  bundle: true,
  format: "cjs",
  platform: "node",
  target: "node20",
  external: ["vscode", "electron"],
  logLevel: "silent",
});

const reactAdapterOutDir = path.resolve("dist", "pulse", "adapters", "react");
const reactDepsOutDir = path.join(reactAdapterOutDir, "deps");

await mkdir(reactDepsOutDir, { recursive: true });

await copyFile(
  path.resolve("node_modules", "@xyflow", "react", "dist", "style.css"),
  path.join(reactDepsOutDir, "xyflow-react.css")
);

await buildBrowserModule({
  entryPoints: ["elkjs/lib/elk.bundled.js"],
  outfile: path.join(reactDepsOutDir, "elkjs.js"),
  define: { "process.env.NODE_ENV": '"production"' },
});

await buildBrowserModule({
  source: `
    import React from "react";
    import * as jsxRuntime from "react/jsx-runtime";
    import * as jsxDevRuntime from "react/jsx-dev-runtime";
    import * as reactDomClient from "react-dom/client";
    import * as reactDom from "react-dom";

    export const {
      Children, Component, Fragment, Profiler, PureComponent, StrictMode,
      Suspense, cloneElement, createContext, createElement, createRef,
      forwardRef, isValidElement, lazy, memo, startTransition, use,
      useActionState, useCallback, useContext, useDebugValue, useDeferredValue,
      useEffect, useId, useImperativeHandle, useInsertionEffect, useLayoutEffect,
      useMemo, useOptimistic, useReducer, useRef, useState, useSyncExternalStore,
      useTransition, version,
    } = React;

    export const { jsx, jsxs } = jsxRuntime;
    export const { jsxDEV } = jsxDevRuntime;
    export const { createRoot, hydrateRoot } = reactDomClient;
    export const { createPortal, flushSync } = reactDom;
    export default React;
  `,
  outfile: path.join(reactDepsOutDir, "react-vendor.js"),
  define: { "process.env.NODE_ENV": '"production"' },
});

await buildBrowserModule({
  entryPoints: ["@xyflow/react"],
  outfile: path.join(reactDepsOutDir, "xyflow-react.js"),
  external: [
    "react",
    "react-dom",
    "react-dom/client",
    "react/jsx-runtime",
    "react/jsx-dev-runtime",
  ],
  banner: {
    js: `
      import * as __sparkReactRequireShim from "react";
      const require = (id) => {
        if (id === "react") return __sparkReactRequireShim;
        throw new Error('Dynamic require of "' + id + '" is not supported');
      };
    `,
  },
  define: { "process.env.NODE_ENV": '"production"' },
});

async function buildBrowserModule({ source, entryPoints, outfile, external = [], alias = {}, banner }) {
  const input = source
    ? {
        stdin: {
          contents: source,
          resolveDir: process.cwd(),
          sourcefile: "adapter-browser-dependency.js",
          loader: "js",
        },
      }
    : { entryPoints };

  await build({
    ...input,
    outfile,
    bundle: true,
    format: "esm",
    platform: "browser",
    target: "es2020",
    external,
    alias,
    banner,
    logLevel: "silent",
  });
}
