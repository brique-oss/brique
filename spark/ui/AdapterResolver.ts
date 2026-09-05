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
import { pathToFileURL } from "url";

import type { ResolvedAdapter } from "./types.js";

export async function resolveAdapter(
  name: string,
  sparkRoot = process.cwd()
): Promise<ResolvedAdapter> {
  switch (name) {
    case "react":
      return resolveReactAdapter(sparkRoot);
    default:
      throw new Error(`Unknown adapter: ${name}`);
  }
}

async function resolveReactAdapter(sparkRoot: string): Promise<ResolvedAdapter> {
  const ReactAdapter = await import("../../pulse/adapters/react/index.js");
  const adapterDir = path.resolve(sparkRoot, "pulse", "adapters", "react");
  const adapterDistDir = path.resolve(
    sparkRoot,
    "dist",
    "pulse",
    "adapters",
    "react"
  );
  const reactVendorUrl = pathToFileURL(
    path.join(adapterDistDir, "deps", "react-vendor.js")
  ).href;
  const elkjsUrl = pathToFileURL(
    path.join(adapterDistDir, "deps", "elkjs.js")
  ).href;
  const xyflowReactUrl = pathToFileURL(
    path.join(adapterDistDir, "deps", "xyflow-react.js")
  ).href;
  const xyflowReactStyleUrl = pathToFileURL(
    path.join(adapterDistDir, "deps", "xyflow-react.css")
  ).href;

  return {
    name: "react",
    module: ReactAdapter,
    renderer: {
      entrypoint: pathToFileURL(path.join(adapterDir, "runner.ts")).href,
      importMap: {
        react: reactVendorUrl,
        "react/jsx-runtime": reactVendorUrl,
        "react/jsx-dev-runtime": reactVendorUrl,
        "react-dom": reactVendorUrl,
        "react-dom/client": reactVendorUrl,
        "@xyflow/react": xyflowReactUrl,
        "elkjs/lib/elk.bundled.js": elkjsUrl,
        "@spark/pulse/react": pathToFileURL(
          path.join(adapterDistDir, "index.js")
        ).href,
        "@spark/pulse/runtime": pathToFileURL(
          path.resolve(sparkRoot, "dist", "pulse", "runtime", "index.js")
        ).href,
      },
      styleUrls: [xyflowReactStyleUrl],
    },
  };
}
