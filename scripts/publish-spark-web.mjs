/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

import { readFile, realpath } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const repository = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const options = parseArgs(process.argv.slice(2));
const contextPath = path.resolve(options.context);
const instanceRoot = await realpath(path.dirname(contextPath));

const [{ resolveUI }, { buildContextUI }, { publishSparkWeb }] = await Promise.all([
  import(pathToModule("dist/spark/ui/UIResolver.js")),
  import(pathToModule("dist/spark/ui/build/ContextUIBuilder.js")),
  import(pathToModule("dist/spark/hosts/web/WebPublisher.js")),
]);
const plans = await discoverWebPlans(contextPath, "/root");
if (plans.length === 0 || plans[0].ui.rootView.name === "BriqueEditorRoot") {
  throw new Error("Root context has no publishable web UI");
}
const publication = await publishSparkWeb({
  sparkRoot: repository,
  outputDirectory: path.resolve(options.output),
  plan: plans[0],
  additionalPlans: plans.slice(1),
});
process.stdout.write(`${JSON.stringify({
  directory: publication.directory,
  publication_id: publication.manifest.publication_id,
  files: publication.manifest.files.length,
  uis: publication.catalog.entries.length,
}, null, 2)}\n`);

async function discoverWebPlans(rootContextPath, rootContextId) {
  const result = [];
  await visit(rootContextPath, rootContextId, true);
  return result;

  async function visit(candidatePath, logicalPath, isRoot) {
    const canonicalPath = await realpath(candidatePath);
    const relative = path.relative(instanceRoot, canonicalPath);
    if (relative.startsWith("..") || path.isAbsolute(relative)) {
      throw new Error(`Context escapes instance root: ${candidatePath}`);
    }
    const context = JSON.parse(await readFile(canonicalPath, "utf8"));
    if (!context?.brique || typeof context.brique !== "object") {
      throw new Error(`Context has no brique object: ${canonicalPath}`);
    }
    const uiConfig = context.brique.ui_config;
    const publishable = uiConfig && uiConfig.root_view !== "BriqueEditorRoot" && uiConfig.adapter === "react";
    if (isRoot && uiConfig && !publishable) {
      throw new Error("Root context UI is internal or not compatible with the web publisher");
    }
    if (publishable) {
      const plan = await buildPlan(canonicalPath, logicalPath, context, uiConfig);
      if (isRoot) result.unshift(plan);
      else result.push(plan);
    } else if (isRoot) {
      throw new Error("Root context has no brique.ui_config");
    }
    const children = context.brique.children_list ?? [];
    if (!Array.isArray(children)) throw new Error(`children_list is not an array: ${logicalPath}`);
    const seen = new Set();
    for (const child of [...children].sort()) {
      if (!validChild(child) || seen.has(child)) throw new Error(`Invalid or duplicate child context: ${String(child)}`);
      seen.add(child);
      await visit(path.join(path.dirname(canonicalPath), child, "context.json"), `${logicalPath}/${child}`, false);
    }
  }
}

async function buildPlan(candidatePath, logicalPath, context, uiConfig) {
  const contextDir = path.dirname(candidatePath);
  const ui = await resolveUI(uiConfig, contextDir, repository);
  const built = await buildContextUI({
    contextPath: candidatePath,
    contextDir,
    rootViewName: ui.rootView.name,
    adapterName: ui.adapter.name,
    sparkRoot: repository,
    rootViewSourcePath: fileURLToPath(ui.rootView.path),
    instanceRootDir: instanceRoot,
  });
  const uiName = typeof uiConfig.name === "string" && uiConfig.name.trim() !== "" ? uiConfig.name.trim() : "main";
  return {
    contextPath: candidatePath,
    context,
    ui: { ...ui, rootView: { ...ui.rootView, path: built.rootViewBuiltUrl } },
    uiRuntime: { uiName, uiAddressPrefix: `@ui_${uiName}`, rootContextId: logicalPath, transportUrl: "" },
    uiTransportUrl: "",
  };
}

function validChild(value) {
  return typeof value === "string" && value !== "" && value !== "." && value !== ".." &&
    !value.includes("/") && !value.includes("\\") && !/[\x00\r\n]/u.test(value);
}

function pathToModule(relative) {
  return new URL(`../${relative}`, import.meta.url).href;
}

function parseArgs(args) {
  let context;
  let output;
  for (let index = 0; index < args.length; index++) {
    const value = args[index];
    if (value === "--context") context = args[++index];
    else if (value === "--output") output = args[++index];
    else throw new Error(`Unknown argument: ${value}`);
  }
  if (!context || !output) throw new Error("Usage: publish-spark-web --context <context.json> --output <new-directory>");
  return { context, output };
}
