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

import assert from "node:assert/strict";
import { readdir, readFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");

const uiSourceRoots = [
  "spark/test/sandbox/root/app_test/ui",
  "spark/test/sandbox/root/app_test/text_context/ui",
  "spark/test/sandbox/root/app_test/counter_context/ui",
  "spark/test/sandbox/root/app_test/inspector_context/ui",
  "spark/test/sandbox/root/app_test/stream_context/ui",
];

test("sandbox UI sources do not hardcode UI runtime addresses", async () => {
  const files = await collectSourceFiles(uiSourceRoots);
  const violations: string[] = [];

  for (const file of files) {
    const source = await readFile(file, "utf-8");
    if (source.includes("@ui_main")) {
      violations.push(`${file}: contains @ui_main`);
    }
    if (/\bfrom\s*:\s*\{\s*context\s*:/su.test(source)) {
      violations.push(`${file}: manually constructs from.context`);
    }
  }

  assert.deepEqual(violations, []);
});

test("sandbox UI intentions go through Pulse UI intention APIs", async () => {
  const files = await collectSourceFiles(uiSourceRoots);
  const violations: string[] = [];

  for (const file of files) {
    const source = await readFile(file, "utf-8");
    if (/\bemitIntention\s*\(/u.test(source)) {
      violations.push(`${file}: calls low-level emitIntention()`);
    }
    if (/\btransport\s*\.\s*send\s*\(/u.test(source)) {
      violations.push(`${file}: sends directly through transport`);
    }
    if (/\bnew\s+WebSocket\s*\(/u.test(source)) {
      violations.push(`${file}: creates a direct WebSocket transport`);
    }
  }

  assert.deepEqual(violations, []);
});

test("sandbox UI exposes deterministic runtime and matter instrumentation", async () => {
  const requiredMarkers = new Map<string, string[]>([
    [
      "spark/test/sandbox/root/app_test/ui/SandboxRoot.tsx",
      [
        'data-testid="runtime-identity"',
        'data-testid="runtime-ui-name"',
        'data-testid="runtime-ui-address-prefix"',
        'data-testid="runtime-connection-state"',
      ],
    ],
    [
      "spark/test/sandbox/root/app_test/ui/blocks/ProjectionInspector.tsx",
      [
        'data-testid="last-intention-id"',
        'data-testid="last-source-address"',
        'data-testid="last-target-context"',
        'data-testid="last-target-capacity"',
        'data-testid="last-target-path-ui"',
      ],
    ],
    [
      "spark/test/sandbox/root/app_test/ui/blocks/MatterObserverBlock.tsx",
      [
        "useMatter",
        'data-testid={`${testId}-status`}',
        'data-testid={`${testId}-value`}',
        'data-testid={`${testId}-readers`}',
        'data-testid={`${testId}-subscribers`}',
        'data-testid={`${testId}-sub-id`}',
        'data-testid={`${testId}-last-event`}',
        'data-testid={`${testId}-render-count`}',
      ],
    ],
    [
      "spark/test/sandbox/root/app_test/ui/blocks/FlowRendererBlock.tsx",
      [
        'data-testid="flow-renderer-status"',
        'data-testid="flow-renderer-intention-id"',
        'data-testid="flow-renderer-message-kind"',
        'data-testid="flow-renderer-payload"',
      ],
    ],
    [
      "spark/test/sandbox/root/app_test/ui/blocks/PathRendererBlock.tsx",
      [
        'data-testid={`${testId}-status`}',
        'data-testid={`${testId}-intention-id`}',
        'data-testid={`${testId}-payload`}',
      ],
    ],
    [
      "spark/test/sandbox/root/app_test/text_context/ui/TextPlayground.tsx",
      [
        "useBriqueAction",
        'testId="text-matter-observer-a"',
        'testId="text-matter-observer-b"',
      ],
    ],
    [
      "spark/test/sandbox/root/app_test/counter_context/ui/CounterPlayground.tsx",
      [
        "useBriqueAction",
        'testId="counter-matter-observer-a"',
        'testId="counter-matter-observer-b"',
      ],
    ],
  ]);
  const violations: string[] = [];

  for (const [relativePath, markers] of requiredMarkers) {
    const source = await readFile(path.resolve(repoRoot, relativePath), "utf-8");
    for (const marker of markers) {
      if (!source.includes(marker)) {
        violations.push(`${relativePath}: missing ${marker}`);
      }
    }
  }

  assert.deepEqual(violations, []);
});

test("React adapter source does not ship CommonJS browser shims", async () => {
  const files = await collectSourceFiles(["pulse/adapters/react"]);
  const violations: string[] = [];

  for (const file of files) {
    const source = await readFile(file, "utf-8");
    if (/\brequire\s*\(/u.test(source)) {
      violations.push(`${path.relative(repoRoot, file)}: contains require()`);
    }
  }

  assert.deepEqual(violations, []);
});

async function collectSourceFiles(roots: string[]): Promise<string[]> {
  const files: string[] = [];

  for (const root of roots) {
    await collectSourceFilesInto(path.resolve(repoRoot, root), files);
  }

  return files.sort();
}

async function collectSourceFilesInto(dir: string, files: string[]): Promise<void> {
  const entries = await readdir(dir, { withFileTypes: true });

  for (const entry of entries) {
    const fullPath = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      if (entry.name === "node_modules" || entry.name === "dist") continue;
      await collectSourceFilesInto(fullPath, files);
      continue;
    }
    if (entry.isFile() && /\.(ts|tsx|js)$/u.test(entry.name)) {
      files.push(fullPath);
    }
  }
}
