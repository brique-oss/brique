/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

import { spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { cp, mkdir, mkdtemp, readdir, readFile, rename, rm, stat, writeFile } from "node:fs/promises";
import path from "node:path";
import { verifyLockfile } from "./lockfile-check.mjs";
import { fileURLToPath } from "node:url";

const scriptDirectory = path.dirname(fileURLToPath(import.meta.url));
const repository = path.resolve(scriptDirectory, "..");
const output = parseOutput(process.argv.slice(2));
const outputParent = path.dirname(output);
const binarySuffix = process.platform === "win32" ? ".exe" : "";

await mkdir(outputParent, { recursive: true });
try {
  await stat(output);
  throw new Error(`runtime output already exists: ${output}`);
} catch (err) {
  if (err?.code !== "ENOENT") throw err;
}

verifyLockfile(repository);
const stagingParent = await mkdtemp(path.join(outputParent, ".brique-runtime-stage-"));
const staging = path.join(stagingParent, "package");

try {
  run("npm", ["run", "build:brique"]);
  run("npm", ["run", "build"]);

  await mkdir(path.join(staging, "bin"), { recursive: true });
  for (const name of ["brique", "brique-sec", "brique-launcher", "brique-web", "brique-browser-host"]) {
    await cp(
      path.join(repository, "bin", `${name}${binarySuffix}`),
      path.join(staging, "bin", `${name}${binarySuffix}`),
    );
  }
  await mkdir(path.join(staging, "lib"), { recursive: true });
  await cp(path.join(repository, "dist", "spark"), path.join(staging, "lib", "spark"), { recursive: true });
  await cp(path.join(repository, "dist", "pulse"), path.join(staging, "lib", "pulse"), { recursive: true });
  await cp(path.join(repository, "dist", "briqueEditorUI"), path.join(staging, "lib", "briqueEditorUI"), { recursive: true });
  const chromiumExtension = path.join(staging, "lib", "browser-extension");
  const firefoxExtension = path.join(staging, "lib", "browser-extension-firefox");
  await cp(path.join(repository, "browser-extension"), chromiumExtension, { recursive: true });
  await rm(path.join(chromiumExtension, "manifest.firefox.json"));
  await cp(path.join(repository, "browser-extension"), firefoxExtension, { recursive: true });
  await rm(path.join(firefoxExtension, "manifest.json"));
  await rename(path.join(firefoxExtension, "manifest.firefox.json"), path.join(firefoxExtension, "manifest.json"));
  for (const name of ["LICENSE", "NOTICE", "README.md"]) {
    await cp(path.join(repository, name), path.join(staging, name));
  }

  const enginePath = path.join(staging, "bin", `brique${binarySuffix}`);
  const engineBuild = JSON.parse(run(enginePath, ["-build_info_json"], true));
  const provenance = readProvenance();
  const files = await inventory(staging);
  const contracts = [
    "brique.build-info/1",
    "brique.browser-engine/1",
    "brique.browser-extension/1",
    "brique.browser.bridge/1",
    "brique.engine-runtime/1",
    "brique.engine-runtime/2",
    "brique.instance-descriptor/1",
    "brique.launcher-control/1",
    "brique.launcher-registry/1",
    "brique.launcher-registry/2",
    "brique.launcher-registry/3",
    "brique.local-control/1",
    "brique.local-ui/1",
    "brique.matter-event-marker/2",
    "brique.matter-outbox/2",
    "brique.matter-subscriptions/2",
    "brique.runtime/1",
    "brique.spark-publication/1",
    "brique.spark-publication/2",
    "brique.spark-ui-catalog/1",
    "brique.spark-web-bootstrap/1",
    "brique.trust-policy/4",
    "brique.ui-actor/1",
  ].sort();
  const toolchains = {
    go: run("go", ["version"], true).trim(),
    node: process.version,
    npm: run("npm", ["--version"], true).trim(),
    typescript: JSON.parse(await readFile(path.join(repository, "node_modules", "typescript", "package.json"), "utf8")).version,
  };
  const executable = (name) => `bin/${name}${binarySuffix}`;
  const manifest = {
    format: "brique.runtime/1",
    runtime_id: "",
    created_at: provenance.source_time,
    platform: { os: engineBuild.os, arch: engineBuild.arch },
    provenance,
    toolchains,
    engine_build: engineBuild,
    contracts,
    entrypoints: {
      engine: executable("brique"),
      security: executable("brique-sec"),
      launcher: executable("brique-launcher"),
    },
    components: [
      { name: "engine", kind: "binary", path: executable("brique") },
      { name: "security", kind: "binary", path: executable("brique-sec") },
      { name: "launcher", kind: "binary", path: executable("brique-launcher") },
      { name: "web-host", kind: "binary", path: executable("brique-web") },
      { name: "browser-host", kind: "binary", path: executable("brique-browser-host") },
      { name: "browser-extension", kind: "assets", path: "lib/browser-extension" },
      { name: "browser-extension-firefox", kind: "assets", path: "lib/browser-extension-firefox" },
      { name: "spark", kind: "assets", path: "lib/spark" },
      { name: "pulse", kind: "assets", path: "lib/pulse" },
      { name: "brique-editor-ui", kind: "assets", path: "lib/briqueEditorUI" },
    ],
    files,
  };
  const runtimeID = runtimeManifestID(manifest);
  manifest.runtime_id = runtimeID;
  await writeFile(path.join(staging, "brique-runtime.json"), `${JSON.stringify(manifest, null, 2)}\n`, { mode: 0o600 });

  const launcherPath = path.join(staging, "bin", `brique-launcher${binarySuffix}`);
  run(launcherPath, ["runtime", "inspect", staging, "--json"], true);
  await rename(staging, output);
  await rm(stagingParent, { recursive: true, force: true });
  console.log(`Brique runtime ready: ${output}`);
  console.log(`Runtime ID: ${runtimeID}`);
} catch (err) {
  await rm(stagingParent, { recursive: true, force: true });
  throw err;
}

function runtimeManifestID(manifest) {
  const values = [
    manifest.format,
    manifest.platform.os,
    manifest.platform.arch,
    manifest.provenance.vcs_revision ?? "",
    String(manifest.provenance.vcs_modified),
    manifest.provenance.source_time,
    manifest.toolchains.go,
    manifest.toolchains.node,
    manifest.toolchains.npm,
    manifest.toolchains.typescript,
    manifest.engine_build.format,
    manifest.engine_build.engine_version,
    manifest.engine_build.vcs_revision ?? "",
    manifest.engine_build.vcs_modified === undefined ? "unset" : String(manifest.engine_build.vcs_modified),
    manifest.engine_build.vcs_time ?? "",
    manifest.engine_build.go_version,
    manifest.engine_build.os,
    manifest.engine_build.arch,
    manifest.entrypoints.engine,
    manifest.entrypoints.security,
    manifest.entrypoints.launcher,
    String(manifest.components.length),
    ...manifest.components.flatMap((component) => [component.name, component.kind, component.path]),
    String(manifest.contracts.length),
    ...manifest.contracts,
    String(manifest.files.length),
    ...manifest.files.flatMap((file) => [file.path, String(file.size), file.sha256]),
  ];
  const payload = values.map((value) => `${Buffer.byteLength(value, "utf8")}:${value}\n`).join("");
  return `brt1_${createHash("sha256").update(payload).digest("base64url")}`;
}

function parseOutput(args) {
  if (args.length !== 2 || args[0] !== "--output" || !args[1]) {
    throw new Error("usage: node scripts/build-runtime.mjs --output <new-directory>");
  }
  return path.resolve(args[1]);
}

function run(command, args, capture = false) {
  const result = spawnSync(command, args, {
    cwd: repository,
    encoding: capture ? "utf8" : undefined,
    stdio: capture ? ["ignore", "pipe", "pipe"] : "inherit",
  });
  if (result.status !== 0 || result.error) {
    const detail = capture ? String(result.stderr ?? "").trim() : "";
    throw new Error(`${command} failed${detail ? `: ${detail}` : ""}`);
  }
  return capture ? String(result.stdout) : "";
}

function readProvenance() {
  const revision = run("git", ["rev-parse", "HEAD"], true).trim();
  const sourceTime = new Date(run("git", ["show", "-s", "--format=%cI", "HEAD"], true).trim()).toISOString();
  const modified = run("git", ["status", "--porcelain", "--untracked-files=normal"], true).trim().length > 0;
  return { vcs_revision: revision, vcs_modified: modified, source_time: sourceTime };
}

async function inventory(directory) {
  const files = [];
  await walk(directory, "", files);
  files.sort((left, right) => left.path < right.path ? -1 : left.path > right.path ? 1 : 0);
  return files;
}

async function walk(root, relative, files) {
  const current = relative ? path.join(root, relative) : root;
  const entries = await readdir(current, { withFileTypes: true });
  entries.sort((left, right) => left.name < right.name ? -1 : left.name > right.name ? 1 : 0);
  for (const entry of entries) {
    const childRelative = relative ? path.join(relative, entry.name) : entry.name;
    if (entry.isSymbolicLink()) throw new Error(`runtime source contains a symbolic link: ${childRelative}`);
    if (entry.isDirectory()) {
      await walk(root, childRelative, files);
      continue;
    }
    if (!entry.isFile()) throw new Error(`runtime source contains a non-regular file: ${childRelative}`);
    const full = path.join(root, childRelative);
    const content = await readFile(full);
    const metadata = await stat(full);
    files.push({
      path: childRelative.split(path.sep).join("/"),
      size: metadata.size,
      sha256: createHash("sha256").update(content).digest("hex"),
    });
  }
}
