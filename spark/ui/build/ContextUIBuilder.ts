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

import { existsSync } from "fs";
import { mkdir } from "fs/promises";
import { createRequire } from "module";
import path from "path";
import { pathToFileURL } from "url";

type Plugin = {
  name: string;
  setup(buildApi: {
    onResolve(
      options: { filter: RegExp },
      callback: (args: {
        importer?: string;
        path: string;
      }) => { path: string; external?: boolean } | undefined
    ): void;
  }): void;
};

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

export async function buildContextUI(
  input: ContextUIBuildInput
): Promise<ContextUIBuildOutput> {
  if (isSparkOwnedUI(input.rootViewSourcePath, input.sparkRoot)) {
    return {
      rootViewSourcePath: input.rootViewSourcePath,
      rootViewBuiltPath: input.rootViewSourcePath,
      rootViewBuiltUrl: pathToFileURL(input.rootViewSourcePath).href,
      outputDir: path.dirname(input.rootViewSourcePath),
    };
  }

  switch (input.adapterName) {
    case "react":
      return buildReactContextUI(input);
    default:
      throw new Error(`No UI builder registered for adapter: ${input.adapterName}`);
  }
}

async function buildReactContextUI(
  input: ContextUIBuildInput
): Promise<ContextUIBuildOutput> {
  const outputDir = path.resolve(input.contextDir, ".spark", "dist", "ui");
  const rootViewBuiltPath = path.join(outputDir, `${input.rootViewName}.js`);

  await mkdir(outputDir, { recursive: true });

  try {
    const esbuild = await loadEsbuild(input.sparkRoot);
    await esbuild.build({
      entryPoints: [input.rootViewSourcePath],
      outfile: rootViewBuiltPath,
      bundle: true,
      format: "esm",
      platform: "browser",
      target: "es2020",
      jsx: "automatic",
      sourcemap: true,
      absWorkingDir: input.contextDir,
      external: [
        "react",
        "react/jsx-runtime",
        "react/jsx-dev-runtime",
        "react-dom/client",
        "@spark/pulse/react",
        "@spark/pulse/runtime",
      ],
      plugins: [
        sparkPulseReactAliasPlugin(input.sparkRoot),
        typescriptSourceResolvePlugin(),
      ],
      logLevel: "silent",
    });
  } catch (err) {
    throw new Error(
      [
        "Failed to build context UI",
        `context: ${input.contextPath}`,
        `root_view: ${input.rootViewName}`,
        `source: ${input.rootViewSourcePath}`,
        `adapter: ${input.adapterName}`,
        `error: ${formatBuildError(err)}`,
      ].join("\n")
    );
  }

  return {
    rootViewSourcePath: input.rootViewSourcePath,
    rootViewBuiltPath,
    rootViewBuiltUrl: pathToFileURL(rootViewBuiltPath).href,
    outputDir,
  };
}

async function loadEsbuild(packageRoot: string): Promise<{
  build(options: unknown): Promise<unknown>;
}> {
  const esbuildUrl = resolveEsbuildUrl(packageRoot);
  const dynamicImport = Function("specifier", "return import(specifier)") as (
    specifier: string
  ) => Promise<unknown>;
  const mod = await dynamicImport(esbuildUrl) as {
    build?: (options: unknown) => Promise<unknown>;
  };
  if (typeof mod.build !== "function") {
    throw new Error(`esbuild module does not export build: ${esbuildUrl}`);
  }
  return { build: mod.build };
}

function resolveEsbuildUrl(packageRoot: string): string {
  try {
    const requireFromPackageRoot = createRequire(path.join(packageRoot, "package.json"));
    return pathToFileURL(requireFromPackageRoot.resolve("esbuild-wasm")).href;
  } catch (err) {
    throw new Error(
      `Cannot resolve esbuild-wasm under ${packageRoot}: ${formatBuildError(err)}`
    );
  }
}

function sparkPulseReactAliasPlugin(sparkRoot: string): Plugin {
  const reactAdapterIndex = path.resolve(
    sparkRoot,
    "pulse",
    "adapters",
    "react",
    "index.ts"
  );

  const pulseRuntimeIndex = path.resolve(
    sparkRoot,
    "pulse",
    "runtime",
    "index.ts"
  );

  const pulseRuntimeDir = path.resolve(sparkRoot, "pulse", "runtime");

  return {
    name: "spark-pulse-alias",
    setup(buildApi) {
      buildApi.onResolve({ filter: /^@spark\/pulse\/react$/ }, () => ({
        path: "@spark/pulse/react",
        external: true,
      }));

      buildApi.onResolve({ filter: /^@spark\/pulse\/runtime$/ }, () => ({
        path: "@spark/pulse/runtime",
        external: true,
      }));

      buildApi.onResolve({ filter: /.*/ }, (args) => {
        if (!args.importer || !args.path.startsWith(".")) return undefined;

        const candidate = path.resolve(path.dirname(args.importer), args.path);
        const normalizedCandidate = normalizeTsPath(candidate);

        if (normalizedCandidate === reactAdapterIndex) {
          return { path: "@spark/pulse/react", external: true };
        }

        if (
          normalizedCandidate === pulseRuntimeIndex ||
          normalizedCandidate.startsWith(pulseRuntimeDir + path.sep)
        ) {
          return { path: "@spark/pulse/runtime", external: true };
        }

        return undefined;
      });
    },
  };
}

function typescriptSourceResolvePlugin(): Plugin {
  return {
    name: "spark-typescript-source-resolve",
    setup(buildApi) {
      buildApi.onResolve({ filter: /^\./ }, (args) => {
        if (!args.importer) return undefined;

        const basePath = path.resolve(path.dirname(args.importer), args.path);
        for (const candidate of sourceCandidates(basePath, args.path)) {
          if (existsSync(candidate)) return { path: candidate };
        }

        return undefined;
      });
    },
  };
}

function sourceCandidates(basePath: string, importPath: string): string[] {
  if (importPath.endsWith(".js")) {
    const withoutJs = basePath.slice(0, -3);
    return [`${withoutJs}.ts`, `${withoutJs}.tsx`, basePath];
  }

  return [
    basePath,
    `${basePath}.ts`,
    `${basePath}.tsx`,
    path.join(basePath, "index.ts"),
    path.join(basePath, "index.tsx"),
  ];
}

function normalizeTsPath(candidate: string): string {
  if (candidate.endsWith(".js")) {
    return `${candidate.slice(0, -3)}.ts`;
  }
  if (candidate.endsWith(".ts")) {
    return candidate;
  }
  return path.join(candidate, "index.ts");
}

function isSparkOwnedUI(rootViewSourcePath: string, sparkRoot: string): boolean {
  const relativePath = path.relative(
    path.resolve(sparkRoot, "briqueEditorUI"),
    rootViewSourcePath
  );

  return !relativePath.startsWith("..") && !path.isAbsolute(relativePath);
}

function formatBuildError(err: unknown): string {
  if (err instanceof Error) return err.message;
  return String(err);
}
