/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

import assert from "node:assert/strict";
import { chmod, mkdir, mkdtemp, readFile, realpath, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import { createServer } from "node:net";
import path from "node:path";
import test from "node:test";
import { once } from "node:events";
import { fileURLToPath } from "node:url";

import {
  directoryObjectIdentity,
  discoveryKey,
  findEngineRuntime,
  verifyEngineRuntime,
  waitForReadyEngineRuntime,
  type DirectoryObjectID,
  type EngineRuntimeDescriptor,
} from "../util/localDiscovery.js";

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");

test("Go and TypeScript share the local discovery vectors", async () => {
  const raw = await readFile(path.join(repoRoot, "engine/localruntime/testdata/discovery_vectors.json"), "utf8");
  const vectors = JSON.parse(raw) as Array<{ identity: DirectoryObjectID; key: string }>;
  assert.ok(vectors.length >= 3);
  for (const vector of vectors) {
    assert.equal(discoveryKey(vector.identity), vector.key);
  }
});

test("local control verifies generation, process, root object, and effective UI address", async (t) => {
  if (process.platform === "win32") {
    t.skip("Unix socket fixture");
    return;
  }
  const temporary = await mkdtemp(path.join(os.tmpdir(), "brique-control-"));
  const root = path.join(temporary, "root");
  const runtime = path.join(temporary, "runtime");
  const socketPath = path.join(temporary, "control.sock");
  await mkdir(root);
  const resolved = await directoryObjectIdentity(root);
  const key = discoveryKey(resolved.identity);
  const descriptor: EngineRuntimeDescriptor = {
    format: "brique.engine-runtime/1",
    discovery_key: key,
    root_path: resolved.rootPath,
    root_object: resolved.identity,
    pid: process.pid,
    generation: "brg1_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
    state: "ready",
    control: {
      protocol: "brique.local-control/1",
      transport: "unix",
      address: socketPath,
    },
    ui_websocket_address: "127.0.0.1:41731",
  };
  await mkdir(path.join(runtime, "instances"), { recursive: true });
  await writeFile(path.join(runtime, "instances", `${key}.json`), JSON.stringify(descriptor), { mode: 0o600 });

  let statusPID = process.pid;
  const server = createServer((socket) => {
    let request = "";
    socket.on("data", (chunk) => {
      request += chunk.toString("utf8");
      if (!request.endsWith("\n")) return;
      const decoded = JSON.parse(request) as { operation: string; generation: string };
      const response = decoded.operation === "status"
        ? {
            format: "brique.local-control/1",
            generation: decoded.generation,
            ok: true,
            state: "ready",
            pid: statusPID,
          }
        : {
            format: "brique.local-control/1",
            generation: decoded.generation,
            ok: true,
            root_path: resolved.rootPath,
            root_object: resolved.identity,
          };
      socket.end(`${JSON.stringify(response)}\n`);
    });
  });
  server.listen(socketPath);
  await once(server, "listening");

  try {
    assert.deepEqual(await verifyEngineRuntime(root, runtime), descriptor);
    assert.deepEqual(await waitForReadyEngineRuntime(root, runtime, 250), descriptor);

    statusPID += 1;
    await assert.rejects(
      verifyEngineRuntime(root, runtime),
      /handshake does not match/u,
    );
  } finally {
    server.close();
    await once(server, "close");
    await rm(temporary, { recursive: true, force: true });
  }
});

test("ready descriptors must publish a loopback UI websocket address", async () => {
  const temporary = await mkdtemp(path.join(os.tmpdir(), "brique-discovery-address-"));
  try {
    const root = path.join(temporary, "root");
    const runtime = path.join(temporary, "runtime");
    await mkdir(root);
    const resolved = await directoryObjectIdentity(root);
    const key = discoveryKey(resolved.identity);
    await mkdir(path.join(runtime, "instances"), { recursive: true });
    const descriptor = {
      format: "brique.engine-runtime/1",
      discovery_key: key,
      root_path: resolved.rootPath,
      root_object: resolved.identity,
      pid: process.pid,
      generation: "brg1_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
      state: "ready",
      control: { protocol: "brique.local-control/1", transport: "unix", address: "/tmp/test.sock" },
      ui_websocket_address: "192.168.1.20:9000",
    };
    await writeFile(path.join(runtime, "instances", `${key}.json`), JSON.stringify(descriptor), { mode: 0o600 });
    await assert.rejects(findEngineRuntime(root, runtime), /invalid Brique engine runtime descriptor/u);
  } finally {
    await rm(temporary, { recursive: true, force: true });
  }
});

test("runtime descriptor v2 carries only a loopback public websocket endpoint", async () => {
  const temporary = await mkdtemp(path.join(os.tmpdir(), "brique-discovery-public-web-"));
  try {
    const root = path.join(temporary, "root");
    const runtime = path.join(temporary, "runtime");
    await mkdir(root);
    const resolved = await directoryObjectIdentity(root);
    const key = discoveryKey(resolved.identity);
    await mkdir(path.join(runtime, "instances"), { recursive: true });
    const descriptorPath = path.join(runtime, "instances", `${key}.json`);
    const descriptor = {
      format: "brique.engine-runtime/2",
      discovery_key: key,
      root_path: resolved.rootPath,
      root_object: resolved.identity,
      pid: process.pid,
      generation: "brg1_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
      state: "starting",
      control: { protocol: "brique.local-control/1", transport: "unix", address: "/tmp/test.sock" },
      public_websocket_address: "ws://127.0.0.1:43121/__brique/public/",
    };
    await writeFile(descriptorPath, JSON.stringify(descriptor), { mode: 0o600 });
    assert.deepEqual(await findEngineRuntime(root, runtime), descriptor);
    descriptor.public_websocket_address = "ws://public.example/__brique/public/";
    await writeFile(descriptorPath, JSON.stringify(descriptor));
    await assert.rejects(findEngineRuntime(root, runtime), /invalid Brique engine runtime descriptor/u);
    descriptor.format = "brique.engine-runtime/1";
    descriptor.public_websocket_address = "ws://127.0.0.1:43121/__brique/public/";
    await writeFile(descriptorPath, JSON.stringify(descriptor));
    await assert.rejects(findEngineRuntime(root, runtime), /invalid Brique engine runtime descriptor/u);
  } finally {
    await rm(temporary, { recursive: true, force: true });
  }
});

test("invalid directory identities do not silently produce discovery keys", () => {
  assert.throws(() => discoveryKey({
    format: "brique.directory-object/1",
    platform: "darwin",
    volume: "NOT-HEX",
    file: "1",
  }));
});

test("Node resolves a canonical root and finds only its matching runtime descriptor", async () => {
  const temporary = await mkdtemp(path.join(os.tmpdir(), "brique-discovery-"));
  try {
    const root = path.join(temporary, "root");
    const runtime = path.join(temporary, "runtime");
    await mkdir(root);
    const resolved = await directoryObjectIdentity(root);
    const key = discoveryKey(resolved.identity);
    await mkdir(path.join(runtime, "instances"), { recursive: true });
    const descriptor = {
      format: "brique.engine-runtime/1" as const,
      discovery_key: key,
      root_path: await realpath(root),
      root_object: resolved.identity,
      build: { format: "brique.build-info/1" },
      pid: process.pid,
      generation: "brg1_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
      state: "ready" as const,
      control: { protocol: "brique.local-control/1", transport: "unix", address: "/tmp/test.sock" },
      published_at: "2026-11-14T12:00:00Z",
    };
    const descriptorPath = path.join(runtime, "instances", `${key}.json`);
    await writeFile(descriptorPath, JSON.stringify(descriptor), { mode: 0o600 });
    assert.deepEqual(await findEngineRuntime(root, runtime), descriptor);
    if (process.platform !== "win32") {
      await chmod(descriptorPath, 0o644);
      await assert.rejects(findEngineRuntime(root, runtime), /not owner-protected/u);
      await chmod(descriptorPath, 0o600);
    }
    descriptor.root_object.file = "0";
    await writeFile(descriptorPath, JSON.stringify(descriptor));
    await assert.rejects(findEngineRuntime(root, runtime), /invalid Brique engine runtime descriptor/u);
  } finally {
    await rm(temporary, { recursive: true, force: true });
  }
});
