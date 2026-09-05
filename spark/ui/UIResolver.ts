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

import { resolveAdapter } from "./AdapterResolver.js";
import type { ResolvedUI, UIConfig } from "./types.js";

const BUILT_IN_ROOT_VIEWS = new Set(["BriqueEditorRoot"]);

export async function resolveUI(
  uiConfig: UIConfig,
  contextDir?: string,
  sparkRoot = process.cwd()
): Promise<ResolvedUI> {
  if (!isRecord(uiConfig)) {
    throw new Error("Missing ui_config");
  }

  const { root_view, adapter, host } = uiConfig;
  if (typeof root_view !== "string" || root_view.trim() === "") {
    throw new Error("ui_config.root_view must be a non-empty string");
  }
  if (typeof adapter !== "string" || adapter.trim() === "") {
    throw new Error("ui_config.adapter must be a non-empty string");
  }
  if (typeof host !== "string" || host.trim() === "") {
    throw new Error("ui_config.host must be a non-empty string");
  }


  return {
    rootView: {
      name: root_view,
      path: resolveRootViewPath(root_view, contextDir, sparkRoot),
    },
    adapter: await resolveAdapter(adapter, sparkRoot),
    host,
  };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function resolveRootViewPath(
  rootViewName: string,
  contextDir?: string,
  sparkRoot = process.cwd()
): string {
  if (BUILT_IN_ROOT_VIEWS.has(rootViewName)) {
    return pathToFileURL(
      path.resolve(sparkRoot, "briqueEditorUI", `${rootViewName}.tsx`)
    ).href;
  }

  if (!contextDir) {
    return `./ui/${rootViewName}.tsx`;
  }

  return pathToFileURL(path.resolve(contextDir, "ui", `${rootViewName}.tsx`)).href;
}
