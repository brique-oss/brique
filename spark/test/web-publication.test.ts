/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

import assert from "node:assert/strict";
import { mkdtemp, readFile, readdir, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { pathToFileURL } from "node:url";

import type { SparkProjectionPlan } from "../core/types.js";
import {
  publishSparkWeb,
  SparkBootstrapFormat,
  SparkPublicationFormat,
  SparkUICatalogFormat,
} from "../hosts/web/WebPublisher.js";

const repository = path.resolve(import.meta.dirname, "..", "..");

test("Spark publishes a deterministic atomic web snapshot without machine paths", async () => {
  const temporary = await mkdtemp(path.join(os.tmpdir(), "spark-publication-test-"));
  try {
    const source = path.join(temporary, "RootView.tsx");
    await writeFile(source, `
import React from "react";
export function RootView() { return React.createElement("main", null, "Published Brique UI"); }
`);
    const contextPath = path.join(temporary, "private-instance", "root", "context.json");
    const plan = projectionPlan(source, contextPath, ["zeta", "child"]);
    const first = await publishSparkWeb({ plan, sparkRoot: repository, outputDirectory: path.join(temporary, "publication-a") });
    const second = await publishSparkWeb({ plan, sparkRoot: repository, outputDirectory: path.join(temporary, "publication-b") });

    assert.equal(first.manifest.format, SparkPublicationFormat);
    assert.match(first.manifest.publication_id, /^spub1_[A-Za-z0-9_-]{43}$/u);
    assert.deepEqual(first.manifest, second.manifest);
    assert.deepEqual(first.bootstrap, second.bootstrap);
    assert.equal(first.bootstrap.format, SparkBootstrapFormat);
    assert.equal(first.catalog.format, SparkUICatalogFormat);
    assert.deepEqual(first.catalog.entries, [{
      contextPath: "/root", uiName: "main", url: "/", bootstrap: "/bootstrap.json",
    }]);
    assert.equal(first.bootstrap.contextPath, "/root");
    assert.deepEqual(first.bootstrap.childContextPaths, ["/root/child", "/root/zeta"]);
    assert.equal("transportUrl" in first.bootstrap, false);
    assert.match(first.bootstrap.rootViewPath, /^\/assets\/root-[A-Z0-9]+\.js$/u);

    const allFiles = await publicationFiles(first.directory);
    assert.equal(allFiles.some((name) => name.endsWith(".map")), false);
    assert.equal(allFiles.includes("index.html"), true);
    assert.equal(allFiles.includes("bootstrap.json"), true);
    assert.equal(allFiles.includes("ui-catalog.json"), true);
    assert.equal(allFiles.includes("brique-publication.json"), true);
    assert.equal(first.manifest.files.some((file) => file.path === "brique-publication.json"), false);
    const serialized = await Promise.all(allFiles.map((name) => readFile(path.join(first.directory, name), "utf8").catch(() => "")));
    assert.equal(serialized.join("\n").includes(temporary), false);

    await assert.rejects(
      publishSparkWeb({ plan, sparkRoot: repository, outputDirectory: first.directory }),
      /already exists/u,
    );
  } finally {
    await rm(temporary, { recursive: true, force: true });
  }
});

test("Spark publishes deterministic secondary UIs under opaque stable routes", async () => {
  const temporary = await mkdtemp(path.join(os.tmpdir(), "spark-publication-multi-ui-"));
  try {
    const rootSource = path.join(temporary, "RootView.tsx");
    const childSource = path.join(temporary, "ChildView.tsx");
    await writeFile(rootSource, `export function RootView() { return null; }\n`);
    await writeFile(childSource, `export function ChildView() { return null; }\n`);
    const root = projectionPlan(rootSource, path.join(temporary, "root", "context.json"), ["child"]);
    const child = projectionPlan(childSource, path.join(temporary, "root", "child", "context.json"), []);
    child.uiRuntime = { ...child.uiRuntime, rootContextId: "/root/child", uiName: "child", uiAddressPrefix: "@ui_child" };
    child.ui.rootView.name = "ChildView";

    const publication = await publishSparkWeb({
      plan: root,
      additionalPlans: [child],
      sparkRoot: repository,
      outputDirectory: path.join(temporary, "publication"),
    });
    assert.equal(publication.catalog.entries.length, 2);
    const secondary = publication.catalog.entries[1];
    assert.equal(secondary.contextPath, "/root/child");
    assert.match(secondary.url, /^\/ui\/ui1_[A-Za-z0-9_-]{43}\/$/u);
    assert.equal(secondary.bootstrap, `${secondary.url}bootstrap.json`);
    assert.equal(await readFile(path.join(publication.directory, secondary.url, "index.html"), "utf8").then(Boolean), true);
    const bootstrap = JSON.parse(await readFile(path.join(publication.directory, secondary.bootstrap), "utf8"));
    assert.equal(bootstrap.contextPath, "/root/child");
    const entry = await readFile(path.join(publication.directory, "assets", (await readdir(path.join(publication.directory, "assets"))).find((name) => name.startsWith("app-"))!), "utf8");
    assert.match(entry, /new URL\("bootstrap.json",document\.baseURI\)/u);
	assert.match(entry, /brique\.public-web\.v1/u);
	assert.match(entry, /__brique\/public\//u);
	assert.doesNotMatch(entry, /No authorized Brique browser transport is active/u);
  } finally {
    await rm(temporary, { recursive: true, force: true });
  }
});

test("Spark refuses an invalid logical child before publishing", async () => {
  const temporary = await mkdtemp(path.join(os.tmpdir(), "spark-publication-invalid-"));
  try {
    const source = path.join(temporary, "RootView.tsx");
    await writeFile(source, `export function RootView() { return null; }\n`);
    const output = path.join(temporary, "publication");
    await assert.rejects(
      publishSparkWeb({
        plan: projectionPlan(source, path.join(temporary, "root", "context.json"), ["../escape"]),
        sparkRoot: repository,
        outputDirectory: output,
      }),
      /Invalid child context/u,
    );
    await assert.rejects(readFile(path.join(output, "index.html")));
    await assert.rejects(
      publishSparkWeb({
        plan: projectionPlan(source, path.join(temporary, "root", "context.json"), ["same", "same"]),
        sparkRoot: repository,
        outputDirectory: output,
      }),
      /Duplicate child context/u,
    );
  } finally {
    await rm(temporary, { recursive: true, force: true });
  }
});

function projectionPlan(source: string, contextPath: string, children: string[]): SparkProjectionPlan {
  return {
    contextPath,
    context: { brique: { ctx_type: "root", children_list: children } },
    ui: {
      rootView: { name: "RootView", path: pathToFileURL(source).href },
      adapter: { name: "react", module: {}, renderer: { entrypoint: pathToFileURL(source).href, styleUrls: [] } },
      host: "web",
    },
    uiRuntime: { uiName: "main", uiAddressPrefix: "@ui_main", rootContextId: "/root", transportUrl: "ws://127.0.0.1:1/ws" },
    uiTransportUrl: "ws://127.0.0.1:1/ws",
  };
}

async function publicationFiles(directory: string): Promise<string[]> {
  const result: string[] = [];
  async function walk(current: string): Promise<void> {
    for (const entry of await readdir(current, { withFileTypes: true })) {
      const full = path.join(current, entry.name);
      if (entry.isDirectory()) await walk(full);
      else result.push(path.relative(directory, full).split(path.sep).join("/"));
    }
  }
  await walk(directory);
  return result.sort();
}
