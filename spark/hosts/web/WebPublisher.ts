/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

import { createHash } from "node:crypto";
import { existsSync, realpathSync } from "node:fs";
import {
  mkdir,
  mkdtemp,
  readFile,
  readdir,
  rename,
  rm,
  writeFile,
} from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

import type { BuildOptions, Metafile } from "esbuild";

import type { SparkProjectionPlan } from "../../core/types.js";

export const SparkPublicationFormat = "brique.spark-publication/2";
export const SparkBootstrapFormat = "brique.spark-web-bootstrap/1";
export const SparkUICatalogFormat = "brique.spark-ui-catalog/1";
export const SparkPublicationManifestName = "brique-publication.json";
export const SparkUICatalogName = "ui-catalog.json";

export type SparkPublicationFile = {
  path: string;
  size: number;
  sha256: string;
};

export type SparkPublicationManifest = {
  format: typeof SparkPublicationFormat;
  publication_id: string;
  entrypoint: "index.html";
  bootstrap: "bootstrap.json";
  catalog: typeof SparkUICatalogName;
  files: SparkPublicationFile[];
};

export type SparkUICatalogEntry = {
  contextPath: string;
  uiName: string;
  url: string;
  bootstrap: string;
};

export type SparkUICatalog = {
  format: typeof SparkUICatalogFormat;
  entries: SparkUICatalogEntry[];
};

export type SparkWebBootstrap = {
  format: typeof SparkBootstrapFormat;
  contextPath: string;
  childContextPaths: string[];
  uiName: string;
  uiAddressPrefix: string;
  rootContextId: string;
  rootViewName: string;
  rootViewPath: string;
  adapterName: string;
};

export type PublishSparkWebInput = {
  plan: SparkProjectionPlan;
  additionalPlans?: SparkProjectionPlan[];
  sparkRoot: string;
  outputDirectory: string;
};

export type SparkWebPublication = {
  directory: string;
  manifest: SparkPublicationManifest;
  bootstrap: SparkWebBootstrap;
  catalog: SparkUICatalog;
};

export async function publishSparkWeb(
  input: PublishSparkWebInput
): Promise<SparkWebPublication> {
  const plans = [input.plan, ...(input.additionalPlans ?? []).slice().sort((left, right) =>
    uiCoordinate(left).localeCompare(uiCoordinate(right)))];
  const seen = new Set<string>();
  for (const plan of plans) {
    if (plan.ui.adapter.name !== "react") {
      throw new Error(`Spark web publication does not support adapter: ${plan.ui.adapter.name}`);
    }
    const coordinate = uiCoordinate(plan);
    if (seen.has(coordinate)) throw new Error(`Duplicate Spark web UI: ${coordinate}`);
    seen.add(coordinate);
  }
  const output = path.resolve(input.outputDirectory);
  if (existsSync(output)) {
    throw new Error(`Spark publication already exists: ${output}`);
  }
  const outputParent = path.dirname(output);
  await mkdir(outputParent, { recursive: true });
  const stagingParent = await mkdtemp(path.join(outputParent, ".spark-publication-stage-"));
  const publication = path.join(stagingParent, "publication");

  try {
    await mkdir(publication, { recursive: true });
    const roots = plans.map((plan, index) => ({
      entryName: index === 0 ? "root" : `root-${uiToken(plan)}`,
      source: localFile(plan.ui.rootView.path),
    }));
    const build = await buildWebBundle({
      sparkRoot: path.resolve(input.sparkRoot),
      publication,
      roots,
    });
    const appPath = outputForEntry(build.metafile, "spark-web-entry:web-entry", publication);
    const catalogEntries: SparkUICatalogEntry[] = [];
    let bootstrap: SparkWebBootstrap | undefined;
    for (let index = 0; index < plans.length; index++) {
      const plan = plans[index];
      const rootPath = outputForEntry(build.metafile, roots[index].source, publication);
      const stylePath = await publishStyles(plan, publication);
      const relativeDirectory = index === 0 ? "" : `ui/${uiToken(plan)}`;
      const bootstrapRelative = relativeDirectory === "" ? "bootstrap.json" : `${relativeDirectory}/bootstrap.json`;
      const indexRelative = relativeDirectory === "" ? "index.html" : `${relativeDirectory}/index.html`;
      const current: SparkWebBootstrap = {
        format: SparkBootstrapFormat,
        contextPath: plan.uiRuntime.rootContextId,
        childContextPaths: logicalChildren(plan),
        uiName: plan.uiRuntime.uiName,
        uiAddressPrefix: plan.uiRuntime.uiAddressPrefix,
        rootContextId: plan.uiRuntime.rootContextId,
        rootViewName: plan.ui.rootView.name,
        rootViewPath: "/" + rootPath,
        adapterName: plan.ui.adapter.name,
      };
      await mkdir(path.dirname(path.join(publication, bootstrapRelative)), { recursive: true });
      await writeJSON(path.join(publication, bootstrapRelative), current);
      await writeFile(path.join(publication, indexRelative), htmlDocument(appPath, stylePath), { mode: 0o600 });
      const url = relativeDirectory === "" ? "/" : `/${relativeDirectory}/`;
      catalogEntries.push({
        contextPath: current.contextPath,
        uiName: current.uiName,
        url,
        bootstrap: `/${bootstrapRelative}`,
      });
      if (index === 0) bootstrap = current;
    }
    const catalog: SparkUICatalog = { format: SparkUICatalogFormat, entries: catalogEntries };
    await writeJSON(path.join(publication, SparkUICatalogName), catalog);
    const files = await inventory(publication);
    const manifest: SparkPublicationManifest = {
      format: SparkPublicationFormat,
      publication_id: publicationID(files),
      entrypoint: "index.html",
      bootstrap: "bootstrap.json",
      catalog: SparkUICatalogName,
      files,
    };
    await writeJSON(path.join(publication, SparkPublicationManifestName), manifest);
    if (existsSync(output)) {
      throw new Error(`Spark publication already exists: ${output}`);
    }
    await rename(publication, output);
    await rm(stagingParent, { recursive: true, force: true });
    if (!bootstrap) throw new Error("Spark web root bootstrap is missing");
    return { directory: output, manifest, bootstrap, catalog };
  } catch (error) {
    await rm(stagingParent, { recursive: true, force: true });
    throw error;
  }
}

async function buildWebBundle(input: {
  sparkRoot: string;
  publication: string;
  roots: Array<{ entryName: string; source: string }>;
}): Promise<{ metafile: Metafile }> {
  const esbuild = await import("esbuild");
  const options: BuildOptions = {
    entryPoints: Object.fromEntries([
      ["app", "spark-web-entry:web-entry"],
      ...input.roots.map((root) => [root.entryName, root.source]),
    ]),
    outdir: path.join(input.publication, "assets"),
    bundle: true,
    splitting: true,
    format: "esm",
    platform: "browser",
    target: "es2020",
    jsx: "automatic",
    sourcemap: false,
    minify: true,
    legalComments: "none",
    metafile: true,
    write: true,
    entryNames: "[name]-[hash]",
    chunkNames: "chunk-[hash]",
    assetNames: "asset-[hash]",
    absWorkingDir: input.sparkRoot,
    nodePaths: [path.join(input.sparkRoot, "node_modules")],
    alias: {
      "@spark/pulse/react": path.join(input.sparkRoot, "pulse", "adapters", "react", "index.ts"),
      "@spark/pulse/runtime": path.join(input.sparkRoot, "pulse", "runtime", "index.ts"),
      // One React for the whole page. A UI is resolved from its own folder, upward: an instance that
      // installed its own react (its package.json asks for one) would otherwise bring a second copy next
      // to the one the Pulse adapter and the renderer use, and every hook of the page would fail with
      // "Cannot read properties of null". Subpaths (react/jsx-runtime, react-dom/client) follow.
      "react": path.join(input.sparkRoot, "node_modules", "react"),
      "react-dom": path.join(input.sparkRoot, "node_modules", "react-dom"),
    },
    plugins: [{
      name: "spark-web-entry",
      setup(build) {
        build.onResolve({ filter: /^spark-web-entry:web-entry$/ }, () => ({ path: "web-entry", namespace: "spark-web-entry" }));
        build.onLoad({ filter: /^web-entry$/, namespace: "spark-web-entry" }, () => ({
          contents: webEntrypoint(input.sparkRoot),
          loader: "ts",
          resolveDir: input.sparkRoot,
        }));
      },
    }],
    logLevel: "silent",
  };
  const result = await esbuild.build(options);
  if (!result.metafile) throw new Error("Spark web build did not produce a metafile");
  return { metafile: result.metafile };
}

function webEntrypoint(sparkRoot: string): string {
  const rendererTransport = path.join(sparkRoot, "spark", "hosts", "rendererTransport.ts");
  const adapterRunner = path.join(sparkRoot, "pulse", "adapters", "react", "runner.ts");
  return `
import { createBrowserWebPulse } from ${JSON.stringify(rendererTransport)};
import { mountPulseRootModule } from ${JSON.stringify(adapterRunner)};

async function bootstrapWebUI() {
  const bootstrapURL = new URL("bootstrap.json", document.baseURI);
  const bootstrap = await fetch(bootstrapURL, { cache: "no-store", credentials: "same-origin" }).then((response) => {
    if (!response.ok) throw new Error("Spark bootstrap unavailable");
    return response.json();
  });
  if (bootstrap.format !== ${JSON.stringify(SparkBootstrapFormat)}) throw new Error("Unsupported Spark bootstrap");
  document.documentElement.dataset.briqueUiName = bootstrap.uiName;
  const globalScope = globalThis;
	const protocol = location.protocol === "https:" ? "wss:" : "ws:";
	const publicURL = new URL("/__brique/public/" + encodeURIComponent(bootstrap.uiName), location.origin);
	publicURL.protocol = protocol;
	const pulse = createBrowserWebPulse(
		{ ...bootstrap, uiTransportUrl: publicURL.href },
		publicURL.href,
		(authority) => { document.documentElement.dataset.briqueTransport = authority; },
	);
  const rootViewModule = await import(bootstrap.rootViewPath);
  const container = document.getElementById("root");
  if (!container) throw new Error("Missing Spark root element");
  await mountPulseRootModule({
    container,
    pulse,
    contextPath: bootstrap.contextPath,
    childContextPaths: bootstrap.childContextPaths,
    rootContextId: bootstrap.rootContextId,
    transportUrl: "",
    rootViewName: bootstrap.rootViewName,
    rootViewPath: bootstrap.rootViewPath,
  }, rootViewModule);
}

bootstrapWebUI().catch((error) => {
  console.error("Spark web bootstrap failed", error);
  document.body.innerHTML = '<pre id="brique-web-error"></pre>';
  const target = document.getElementById("brique-web-error");
  if (target) target.textContent = String(error?.stack ?? error);
});
`.trimStart();
}

async function publishStyles(plan: SparkProjectionPlan, publication: string): Promise<string | undefined> {
  const chunks: Buffer[] = [];
  for (const source of plan.ui.adapter.renderer.styleUrls ?? []) {
    chunks.push(await readFile(localFile(source)));
  }
  if (chunks.length === 0) return undefined;
  const content = Buffer.concat(chunks.map((chunk, index) => index === 0 ? chunk : Buffer.concat([Buffer.from("\n"), chunk])));
  const digest = createHash("sha256").update(content).digest("hex").slice(0, 16);
  const relative = `assets/style-${digest}.css`;
  await writeFile(path.join(publication, relative), content, { mode: 0o600 });
  return relative;
}

function logicalChildren(plan: SparkProjectionPlan): string[] {
  const root = plan.uiRuntime.rootContextId.replace(/\/$/u, "");
  const result = (plan.context.brique.children_list ?? []).map((child) => {
    if (!validChild(child)) throw new Error(`Invalid child context name for web publication: ${child}`);
    return `${root}/${child}`;
  }).sort();
  for (let index = 1; index < result.length; index++) {
    if (result[index] === result[index - 1]) {
      throw new Error(`Duplicate child context for web publication: ${result[index]}`);
    }
  }
  return result;
}

function validChild(value: string): boolean {
  return value !== "" && value !== "." && value !== ".." && !value.includes("/") && !value.includes("\\");
}

function uiCoordinate(plan: SparkProjectionPlan): string {
  const contextPath = plan.uiRuntime.rootContextId;
  const uiName = plan.uiRuntime.uiName;
  if (!logicalContextPath(contextPath)) throw new Error(`Invalid logical context path for web UI: ${contextPath}`);
  if (uiName.trim() === "" || uiName !== uiName.trim() || /[\x00\r\n]/u.test(uiName)) {
    throw new Error(`Invalid Spark web UI name: ${uiName}`);
  }
  return `${contextPath}\u0000${uiName}`;
}

function logicalContextPath(value: string): boolean {
  return value.startsWith("/") && value !== "/" && !value.includes("\\") &&
    !/[\x00\r\n]/u.test(value) && path.posix.normalize(value) === value;
}

function uiToken(plan: SparkProjectionPlan): string {
  return `ui1_${createHash("sha256").update(uiCoordinate(plan)).digest("base64url")}`;
}

function outputForEntry(metafile: Metafile, entry: string, publication: string): string {
  const virtual = entry.startsWith("spark-web-entry:");
  const canonicalEntry = virtual ? entry : realpathSync(entry);
  for (const [output, description] of Object.entries(metafile.outputs)) {
    const matches = virtual
      ? description.entryPoint === entry
      : description.entryPoint !== undefined &&
        !description.entryPoint.startsWith("spark-web-entry:") &&
        realpathSync(path.resolve(description.entryPoint)) === canonicalEntry;
    if (matches) {
      const relative = path.relative(realpathSync(publication), realpathSync(path.resolve(output)));
      if (relative.startsWith("..") || path.isAbsolute(relative)) break;
      return relative.split(path.sep).join("/");
    }
  }
  throw new Error(`Spark web output is missing for entry: ${entry}`);
}

function htmlDocument(appPath: string, stylePath?: string): string {
  const style = stylePath ? `  <link rel="stylesheet" href="/${stylePath}">\n` : "";
  return `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <link rel="brique-instance-descriptor" href="/.well-known/brique/instance-descriptor">
  <title>Brique</title>
${style}</head>
<body>
  <div id="root"></div>
  <script type="module" src="/${appPath}"></script>
</body>
</html>
`;
}

async function inventory(directory: string): Promise<SparkPublicationFile[]> {
  const result: SparkPublicationFile[] = [];
  async function walk(current: string): Promise<void> {
    const entries = await readdir(current, { withFileTypes: true });
    entries.sort((left, right) => left.name.localeCompare(right.name));
    for (const entry of entries) {
      const full = path.join(current, entry.name);
      if (entry.isSymbolicLink()) throw new Error("Spark publication staging contains a symbolic link");
      if (entry.isDirectory()) {
        await walk(full);
        continue;
      }
      if (!entry.isFile()) throw new Error("Spark publication staging contains an unsupported file");
      const relative = path.relative(directory, full).split(path.sep).join("/");
      if (relative.endsWith(".map")) throw new Error("Spark web publication contains a source map");
      const content = await readFile(full);
      result.push({ path: relative, size: content.length, sha256: createHash("sha256").update(content).digest("hex") });
    }
  }
  await walk(directory);
  // Byte order, not locale order: the manifest is verified file by file in byte order (Go compares
  // strings that way), and localeCompare puts "root-ui1_…" before "root-YH…" while bytes put it after.
  result.sort((left, right) => (left.path < right.path ? -1 : left.path > right.path ? 1 : 0));
  return result;
}

function publicationID(files: SparkPublicationFile[]): string {
  const hash = createHash("sha256");
  const add = (value: string) => hash.update(`${Buffer.byteLength(value)}:${value}\n`);
  add(SparkPublicationFormat);
  add("index.html");
  add("bootstrap.json");
  add(SparkUICatalogName);
  add(String(files.length));
  for (const file of files) {
    add(file.path);
    add(String(file.size));
    add(file.sha256);
  }
  return `spub1_${hash.digest("base64url")}`;
}

async function writeJSON(file: string, value: unknown): Promise<void> {
  await writeFile(file, `${JSON.stringify(value, null, 2)}\n`, { mode: 0o600 });
}

function localFile(value: string): string {
  if (!value.startsWith("file:")) throw new Error(`Spark web asset is not a local file URL: ${value}`);
  const result = fileURLToPath(value);
  if (!path.isAbsolute(result)) throw new Error("Spark web asset path is not absolute");
  return result;
}
