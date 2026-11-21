/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

import { spawnSync } from "node:child_process";

// A runtime is only built from the dependencies its lockfile fixes (INV-12).
// `npm ci --dry-run` resolves package.json against package-lock.json and fails on
// any divergence without installing, running scripts or touching the network.
export function verifyLockfile(directory) {
  const result = spawnSync("npm", ["ci", "--dry-run", "--ignore-scripts", "--offline"], {
    cwd: directory, encoding: "utf8", stdio: ["ignore", "pipe", "pipe"],
  });
  if (result.status !== 0 || result.error) {
    throw new Error("package-lock.json does not match package.json (or a dependency is not locked): refusing to build a runtime");
  }
}
