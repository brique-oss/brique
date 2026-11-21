/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

import { readFile } from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";

import type { SparkProjectionPlan } from "../core/types.js";
import { publishSparkWeb } from "../hosts/web/WebPublisher.js";

const [sourceValue, contextValue, outputValue, repositoryValue] = process.argv.slice(2);
if (!sourceValue || !contextValue || !outputValue || !repositoryValue) {
  throw new Error("usage: publish-browser-e2e <source.tsx> <context.json> <output> <repository>");
}

const source = path.resolve(sourceValue);
const contextPath = path.resolve(contextValue);
const context = JSON.parse(await readFile(contextPath, "utf8"));
const plan: SparkProjectionPlan = {
  contextPath,
  context,
  ui: {
    rootView: { name: "RootView", path: pathToFileURL(source).href },
    adapter: {
      name: "react",
      module: {},
      renderer: { entrypoint: pathToFileURL(source).href, styleUrls: [] },
    },
    host: "web",
  },
  uiRuntime: {
    uiName: "main",
    uiAddressPrefix: "@ui_main",
    rootContextId: "/root",
    transportUrl: "",
  },
  uiTransportUrl: "",
};

const publication = await publishSparkWeb({
  plan,
  sparkRoot: path.resolve(repositoryValue),
  outputDirectory: path.resolve(outputValue),
});
process.stdout.write(`${publication.manifest.publication_id}\n`);
