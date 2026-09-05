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
import { fileURLToPath, pathToFileURL } from "url";

import type { SparkHostInput } from "./types.js";

export type SparkRendererPayload = {
  contextPath: string;
  childContextPaths: string[];
  uiTransportUrl: string;
  uiName: string;
  uiAddressPrefix: string;
  rootContextId: string;
  rootViewName: string;
  rootViewPath: string;
  adapterName: string;
  adapterRendererPath: string;
  adapterImportMap: Record<string, string>;
  adapterStyleUrls: string[];
};

export type AssetUrlMapper = (sourcePathOrUrl: string) => string;

export function createRendererPayload(
  input: SparkHostInput,
  mapAssetUrl: AssetUrlMapper
): SparkRendererPayload {
  return {
    contextPath: input.contextPath,
    childContextPaths: resolveChildContextPaths(input),
    uiTransportUrl: input.uiTransportUrl,
    uiName: input.uiRuntime.uiName,
    uiAddressPrefix: input.uiRuntime.uiAddressPrefix,
    rootContextId: input.uiRuntime.rootContextId,
    rootViewName: input.ui.rootView.name,
    rootViewPath: mapAssetUrl(input.ui.rootView.path),
    adapterName: input.ui.adapter.name,
    adapterRendererPath: mapAssetUrl(input.ui.adapter.renderer.entrypoint),
    adapterImportMap: mapImportValues(
      input.ui.adapter.renderer.importMap ?? {},
      mapAssetUrl
    ),
    adapterStyleUrls: (input.ui.adapter.renderer.styleUrls ?? []).map(mapAssetUrl),
  };
}

function resolveChildContextPaths(input: SparkHostInput): string[] {
  const children = input.context.brique.children_list ?? [];
  const contextDir = path.dirname(input.contextPath);

  return children.map((child) =>
    path.resolve(contextDir, child, "context.json")
  );
}

export function rendererPayloadToEnv(
  payload: SparkRendererPayload,
  rendererEntrypoint: string
): Record<string, string> {
  return {
    SPARK_RENDERER_PAYLOAD: JSON.stringify(payload),
    SPARK_CONTEXT_PATH: payload.contextPath,
    SPARK_UI_TRANSPORT_URL: payload.uiTransportUrl,
    SPARK_UI_NAME: payload.uiName,
    SPARK_UI_ADDRESS_PREFIX: payload.uiAddressPrefix,
    SPARK_ROOT_CONTEXT_ID: payload.rootContextId,
    SPARK_RENDERER_ENTRYPOINT: rendererEntrypoint,
    SPARK_UI_ROOT_VIEW: payload.rootViewName,
    SPARK_UI_ROOT_VIEW_PATH: payload.rootViewPath,
    SPARK_ADAPTER: payload.adapterName,
    SPARK_ADAPTER_RENDERER_PATH: payload.adapterRendererPath,
    SPARK_ADAPTER_IMPORT_MAP: JSON.stringify(payload.adapterImportMap),
    SPARK_ADAPTER_STYLE_URLS: JSON.stringify(payload.adapterStyleUrls),
  };
}

export function toBuiltWorkspacePath(
  sourcePathOrUrl: string,
  workspaceRoot = process.cwd()
): string {
  const sourcePath = sourcePathOrUrl.startsWith("file:")
    ? fileURLToPath(sourcePathOrUrl)
    : path.resolve(sourcePathOrUrl);

  const relativePath = path.relative(workspaceRoot, sourcePath);
  if (relativePath.startsWith("..") || path.isAbsolute(relativePath)) {
    return sourcePath;
  }

  if (relativePath === "dist" || relativePath.startsWith(`dist${path.sep}`)) {
    return sourcePath;
  }

  if (isContextBuildOutput(relativePath)) {
    return sourcePath;
  }

  return path.resolve(
    workspaceRoot,
    "dist",
    relativePath.replace(/\.(tsx|ts)$/u, ".js")
  );
}

function isContextBuildOutput(relativePath: string): boolean {
  const parts = relativePath.split(path.sep);
  return parts.some(
    (part, index) =>
      part === ".spark" &&
      parts[index + 1] === "dist"
  );
}

export function toBuiltFileUrl(
  sourcePathOrUrl: string,
  workspaceRoot = process.cwd()
): string {
  const builtPath = toBuiltWorkspacePath(sourcePathOrUrl, workspaceRoot);
  if (builtPath === sourcePathOrUrl && sourcePathOrUrl.startsWith("file:")) {
    return sourcePathOrUrl;
  }
  return pathToFileURL(builtPath).href;
}

function mapImportValues(
  imports: Record<string, string>,
  mapValue: AssetUrlMapper
): Record<string, string> {
  return Object.fromEntries(
    Object.entries(imports).map(([key, value]) => [key, mapValue(value)])
  );
}
