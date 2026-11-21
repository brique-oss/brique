/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

import { spawn, spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import http from "node:http";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { performance } from "node:perf_hooks";

if (process.argv[2] === "--node-child") {
  await runNodeChild(process.argv[3]);
} else {
  await runBenchmark();
}

async function runBenchmark() {
  if (process.platform === "win32") {
    throw new Error("R3 process RSS benchmark currently requires ps on Unix");
  }
  const repository = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
  const binary = path.join(repository, "bin", "brique-web");
  const probe = spawnSync(binary, ["-h"], { stdio: "ignore" });
  if (probe.error) throw new Error("build bin/brique-web before running the R3 benchmark");

  const temporary = await mkdtemp(path.join(os.tmpdir(), "brique-r3-host-"));
  try {
    const index = "<!doctype html><script type=module src=/assets/app.12345678.js></script>\n";
    const app = `export const payload = ${JSON.stringify("x".repeat(1024 * 1024))};\n`;
    const bootstrap = `${JSON.stringify({
      format: "brique.spark-web-bootstrap/1",
      contextPath: "/root",
      childContextPaths: ["/root/app"],
      uiName: "main",
      uiAddressPrefix: "@ui_main",
      rootContextId: "/root",
      rootViewName: "Root",
      rootViewPath: "/assets/app.12345678.js",
      adapterName: "react",
    })}\n`;
    await mkdir(path.join(temporary, "assets"));
    await writeFile(path.join(temporary, "index.html"), index);
    await writeFile(path.join(temporary, "assets", "app.12345678.js"), app);
    await writeFile(path.join(temporary, "bootstrap.json"), bootstrap);
    const files = [
      publicationFile("assets/app.12345678.js", app),
      publicationFile("bootstrap.json", bootstrap),
      publicationFile("index.html", index),
    ];
    const manifest = {
      format: "brique.spark-publication/1",
      publication_id: publicationID(files),
      entrypoint: "index.html",
      bootstrap: "bootstrap.json",
      files,
    };
    await writeFile(path.join(temporary, "brique-publication.json"), `${JSON.stringify(manifest, null, 2)}\n`);
    const results = [];
    for (const instances of [1, 8]) {
      results.push(await measure("go-separate", instances, () => spawn(binary, ["-publication", temporary], { stdio: ["ignore", "pipe", "inherit"] })));
      results.push(await measure("node-separate", instances, () => spawn(process.execPath, [fileURLToPath(import.meta.url), "--node-child", temporary], { stdio: ["ignore", "pipe", "inherit"] })));
    }
    process.stdout.write(`${JSON.stringify({
      format: "brique.r3-web-host-benchmark/1",
      platform: `${process.platform}/${process.arch}`,
      node: process.version,
      asset_bytes: 1024 * 1024,
      requests_per_instance: 50,
      results,
    }, null, 2)}\n`);
  } finally {
    await rm(temporary, { recursive: true, force: true });
  }
}

async function measure(mode, instances, launch) {
  const children = [];
  const started = performance.now();
  try {
    const urls = await Promise.all(Array.from({ length: instances }, async () => {
      const child = launch();
      children.push(child);
      return readReadyURL(child);
    }));
    const readyMs = performance.now() - started;
    const rssKiB = children.reduce((total, child) => total + processRSSKiB(child.pid), 0);
    const loadStarted = performance.now();
    await Promise.all(urls.flatMap((url) => Array.from({ length: 50 }, () => fetchAsset(url))));
    const loadMs = performance.now() - loadStarted;
    const cpuMs = children.reduce((total, child) => total + processCPUms(child.pid), 0);
    return { mode, instances, ready_ms: round(readyMs), rss_kib: rssKiB, load_ms: round(loadMs), cpu_ms: cpuMs };
  } finally {
    for (const child of children) child.kill("SIGTERM");
    await Promise.all(children.map((child) => new Promise((resolve) => child.once("exit", resolve))));
  }
}

function readReadyURL(child) {
  return new Promise((resolve, reject) => {
    let buffered = "";
    const timeout = setTimeout(() => reject(new Error("web host readiness timed out")), 5000);
    child.once("error", reject);
    child.once("exit", (code) => reject(new Error(`web host exited before readiness: ${code}`)));
    child.stdout.setEncoding("utf8");
    child.stdout.on("data", (chunk) => {
      buffered += chunk;
      const match = buffered.match(/https?:\/\/[^\s]+\//u);
      if (!match) return;
      clearTimeout(timeout);
      resolve(match[0]);
    });
  });
}

async function fetchAsset(baseURL) {
  const response = await fetch(new URL("assets/app.12345678.js", baseURL));
  if (!response.ok) throw new Error(`asset response ${response.status}`);
  await response.arrayBuffer();
}

function processRSSKiB(pid) {
  const result = spawnSync("ps", ["-o", "rss=", "-p", String(pid)], { encoding: "utf8" });
  const value = Number.parseInt(result.stdout.trim(), 10);
  return Number.isFinite(value) ? value : 0;
}

function processCPUms(pid) {
  const result = spawnSync("ps", ["-o", "time=", "-p", String(pid)], { encoding: "utf8" });
  const raw = result.stdout.trim();
  if (!raw) return 0;
  const parts = raw.split(":");
  const seconds = Number.parseFloat(parts.pop());
  const minutes = Number.parseInt(parts.pop() ?? "0", 10);
  const hours = Number.parseInt(parts.pop() ?? "0", 10);
  if (![seconds, minutes, hours].every(Number.isFinite)) return 0;
  return Math.round(((hours * 60 + minutes) * 60 + seconds) * 1000);
}

function round(value) { return Math.round(value * 100) / 100; }

function publicationFile(filePath, content) {
  return { path: filePath, size: Buffer.byteLength(content), sha256: createHash("sha256").update(content).digest("hex") };
}

function publicationID(files) {
  const hash = createHash("sha256");
  const add = (value) => hash.update(`${Buffer.byteLength(value)}:${value}\n`);
  add("brique.spark-publication/1");
  add("index.html");
  add("bootstrap.json");
  add(String(files.length));
  for (const file of files) {
    add(file.path);
    add(String(file.size));
    add(file.sha256);
  }
  return `spub1_${hash.digest("base64url")}`;
}

async function runNodeChild(publication) {
  const { readFile } = await import("node:fs/promises");
  const index = await readFile(path.join(publication, "index.html"));
  const asset = await readFile(path.join(publication, "assets", "app.12345678.js"));
  const server = http.createServer({ maxHeaderSize: 32 << 10 }, (request, response) => {
    response.setHeader("X-Content-Type-Options", "nosniff");
    if (request.method !== "GET" && request.method !== "HEAD") {
      response.writeHead(405).end();
      return;
    }
    const body = request.url === "/" ? index : request.url === "/assets/app.12345678.js" ? asset : null;
    if (!body) {
      response.writeHead(404).end();
      return;
    }
    response.setHeader("Content-Length", body.length);
    response.writeHead(200);
    response.end(request.method === "HEAD" ? undefined : body);
  });
  server.listen(0, "127.0.0.1", () => {
    const address = server.address();
    process.stdout.write(`Node web host ready: http://127.0.0.1:${address.port}/\n`);
  });
  for (const signal of ["SIGINT", "SIGTERM"]) process.on(signal, () => server.close(() => process.exit(0)));
}
