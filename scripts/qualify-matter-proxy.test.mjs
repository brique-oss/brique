/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import http from "node:http";
import os from "node:os";
import path from "node:path";
import test from "node:test";

import { qualifyMatterProxy } from "./qualify-matter-proxy.mjs";

test("qualifies resumed read and digest-checked write without putting tokens in URLs", async (t) => {
  const temporary = await mkdtemp(path.join(os.tmpdir(), "brique-matter-proxy-test-"));
  t.after(() => rm(temporary, { recursive: true, force: true }));
  const readBytes = Buffer.alloc((1 << 20) + 17, 0x5a);
  const writeBytes = Buffer.from("proxy-qualified-write");
  const seen = { ranges: [], write: null, queryTokens: [] };
  const etag = '"brique-rev-7"';
  const server = http.createServer(async (request, response) => {
    seen.queryTokens.push(new URL(request.url, "http://loopback").searchParams.has("tok"));
    if (request.headers.authorization !== "Bearer qualification-secret") {
      response.writeHead(404).end();
      return;
    }
    if (request.method === "GET" && request.url === "/brique/substance/read") {
      const match = /^bytes=(\d+)-(\d+)$/u.exec(request.headers.range ?? "");
      if (!match || (request.headers["if-range"] && request.headers["if-range"] !== etag)) {
        response.writeHead(409).end();
        return;
      }
      const start = Number.parseInt(match[1], 10);
      const end = Number.parseInt(match[2], 10);
      seen.ranges.push([start, end]);
      const body = readBytes.subarray(start, end + 1);
      response.writeHead(206, {
        ETag: etag,
        "Content-Range": `bytes ${start}-${end}/${readBytes.length}`,
        "Content-Length": body.length,
      });
      response.end(body);
      return;
    }
    if (request.method === "PUT" && request.url === "/brique/substance/write") {
      const chunks = [];
      for await (const chunk of request) chunks.push(chunk);
      seen.write = Buffer.concat(chunks);
      const digest = createHash("sha256").update(seen.write).digest("hex");
      response.writeHead(200, { "Content-Type": "application/json" });
      response.end(JSON.stringify({ ok: true, message_sha256: digest }));
      return;
    }
    response.writeHead(404).end();
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  t.after(() => new Promise((resolve) => server.close(resolve)));
  const address = server.address();
  const baseURL = `http://127.0.0.1:${address.port}/brique`;
  const files = {
    readHandle: path.join(temporary, "read-handle.json"),
    readExpected: path.join(temporary, "read.data"),
    writeHandle: path.join(temporary, "write-handle.json"),
    writeInput: path.join(temporary, "write.data"),
  };
  await Promise.all([
    writeFile(files.readHandle, JSON.stringify({ base_url: baseURL, path: "/substance/read", tok: "qualification-secret", method: "GET", size_hint: readBytes.length })),
    writeFile(files.readExpected, readBytes),
    writeFile(files.writeHandle, JSON.stringify({ base_url: baseURL, path: "/substance/write", tok: "qualification-secret", method: "PUT" })),
    writeFile(files.writeInput, writeBytes),
  ]);

  const result = await qualifyMatterProxy({ ...files, allowLoopbackHTTP: true });
  assert.equal(result.format, "brique.matter-proxy-qualification/1");
  assert.equal(result.tls, false);
  assert.equal(result.read.bytes, readBytes.length);
  assert.equal(result.read.resumed_ranges, 2);
  assert.equal(result.write.bytes, writeBytes.length);
  assert.deepEqual(seen.ranges, [[0, Math.floor(readBytes.length / 2) - 1], [Math.floor(readBytes.length / 2), readBytes.length - 1]]);
  assert.deepEqual(seen.write, writeBytes);
  assert.deepEqual(seen.queryTokens, [false, false, false]);
  assert.equal(JSON.stringify(result).includes("qualification-secret"), false);
});

test("refuses cleartext non-loopback handles before any transfer", async (t) => {
  const temporary = await mkdtemp(path.join(os.tmpdir(), "brique-matter-proxy-http-"));
  t.after(() => rm(temporary, { recursive: true, force: true }));
  const readHandle = path.join(temporary, "read-handle.json");
  const readExpected = path.join(temporary, "read.data");
  await writeFile(readHandle, JSON.stringify({ base_url: "http://192.0.2.10/brique", path: "/substance/read", tok: "secret", method: "GET" }));
  await writeFile(readExpected, "xx");
  await assert.rejects(() => qualifyMatterProxy({ readHandle, readExpected, allowLoopbackHTTP: true }), /must use HTTPS/u);
});

test("refuses a same-origin handle outside the Matter data-plane", async (t) => {
  const directory = await mkdtemp(path.join(os.tmpdir(), "brique-matter-proxy-path-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const readHandle = path.join(directory, "read-handle.json");
  const readExpected = path.join(directory, "read.data");
  await writeFile(readHandle, JSON.stringify({ base_url: "http://127.0.0.1:9", path: "/admin", tok: "secret", method: "GET" }));
  await writeFile(readExpected, "xx");
  await assert.rejects(() => qualifyMatterProxy({ readHandle, readExpected, allowLoopbackHTTP: true }), /path is invalid/u);
});
