/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { chmod, mkdir, mkdtemp, readdir, rm, stat, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { verifyLockfile } from "./lockfile-check.mjs";

const repository = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

// A fake `npm` first on PATH: `ci` succeeds or fails as asked, every other command fails,
// which interrupts the build after the lockfile gate and before anything is published.
async function fakeNpm(root, ciExitCode) {
  const bin = path.join(root, "bin");
  await mkdir(bin, { recursive: true });
  const file = path.join(bin, "npm");
  await writeFile(file, `#!/bin/sh\nif [ "$1" = "ci" ]; then exit ${ciExitCode}; fi\nexit 1\n`);
  await chmod(file, 0o755);
  return bin;
}

function runBuild(bin, output) {
  return spawnSync(process.execPath, ["scripts/build-runtime.mjs", "--output", output], {
    cwd: repository, encoding: "utf8", env: { ...process.env, PATH: `${bin}:${process.env.PATH}` },
  });
}

async function leftovers(parent) {
  return (await readdir(parent)).filter((name) => name.startsWith(".brique-runtime-stage-"));
}

test("a divergent lockfile stops the runtime build before any staging or publication", async () => {
  const root = await mkdtemp(path.join(os.tmpdir(), "bq-lock-"));
  try {
    const output = path.join(root, "out", "runtime");
    const result = runBuild(await fakeNpm(root, 1), output);
    assert.notEqual(result.status, 0);
    assert.match(result.stderr, /package-lock\.json does not match/);
    await assert.rejects(stat(output));
    assert.deepEqual(await leftovers(path.dirname(output)).catch(() => []), []);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("an interrupted runtime build publishes nothing and removes its staging directory", async () => {
  const root = await mkdtemp(path.join(os.tmpdir(), "bq-interrupt-"));
  try {
    const output = path.join(root, "out", "runtime");
    const result = runBuild(await fakeNpm(root, 0), output);
    assert.notEqual(result.status, 0);
    await assert.rejects(stat(output));
    assert.deepEqual(await leftovers(path.dirname(output)), []);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("the repository's own lockfile is in sync", () => {
  assert.doesNotThrow(() => verifyLockfile(repository));
});
