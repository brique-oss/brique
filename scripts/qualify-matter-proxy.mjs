/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";
import net from "node:net";
import path from "node:path";
import { fileURLToPath } from "node:url";

const isMain = process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url);

if (isMain) {
  try {
    const result = await qualifyMatterProxy(parseArgs(process.argv.slice(2)));
    process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);
  } catch (error) {
    process.stderr.write(`brique matter proxy qualification failed: ${safeError(error)}\n`);
    process.exitCode = 1;
  }
}

export async function qualifyMatterProxy(options) {
  const readHandle = await loadHandle(options.readHandle, "GET", options.allowLoopbackHTTP);
  const expectedRead = await readFile(options.readExpected);
  const started = performance.now();
  const read = await qualifyRead(readHandle, expectedRead);
  let write;
  if (options.writeHandle || options.writeInput) {
    if (!options.writeHandle || !options.writeInput) {
      throw new Error("--write-handle and --write-input must be provided together");
    }
    const writeHandle = await loadHandle(options.writeHandle, "PUT", options.allowLoopbackHTTP);
    if (writeHandle.origin !== readHandle.origin) {
      throw new Error("read and write handles do not use the same public origin");
    }
    write = await qualifyWrite(writeHandle, await readFile(options.writeInput));
  }
  return {
    format: "brique.matter-proxy-qualification/1",
    origin: readHandle.origin,
    tls: readHandle.protocol === "https:",
    read,
    ...(write ? { write } : {}),
    elapsed_ms: round(performance.now() - started),
  };
}

async function qualifyRead(handle, expected) {
  if (expected.length < 2) throw new Error("read fixture must contain at least two bytes");
  if (handle.sizeHint !== undefined && handle.sizeHint !== expected.length) {
    throw new Error(`read size_hint ${handle.sizeHint} does not match fixture size ${expected.length}`);
  }
  const split = Math.floor(expected.length / 2);
  const first = await fetchRange(handle, 0, split - 1);
  const etag = first.response.headers.get("etag");
  if (!etag) throw new Error("first range response has no ETag");
  assertRangeResponse(first.response, first.body, 0, split - 1, expected.length);
  if (!first.body.equals(expected.subarray(0, split))) throw new Error("first range bytes differ from fixture");

  const second = await fetchRange(handle, split, expected.length - 1, etag);
  assertRangeResponse(second.response, second.body, split, expected.length - 1, expected.length);
  if (!second.body.equals(expected.subarray(split))) throw new Error("second range bytes differ from fixture");

  return {
    bytes: expected.length,
    sha256: sha256(expected),
    resumed_ranges: 2,
    etag,
    first_ms: first.elapsed,
    second_ms: second.elapsed,
  };
}

async function fetchRange(handle, start, end, ifRange) {
  const began = performance.now();
  const response = await fetch(handle.url, {
    method: "GET",
    headers: {
      Authorization: `Bearer ${handle.token}`,
      Range: `bytes=${start}-${end}`,
      ...(ifRange ? { "If-Range": ifRange } : {}),
    },
    redirect: "error",
  });
  const body = await readBoundedBody(response, end - start + 1);
  return { response, body, elapsed: round(performance.now() - began) };
}

function assertRangeResponse(response, body, start, end, total) {
  if (response.status !== 206) throw new Error(`range response status ${response.status}, expected 206`);
  const expectedRange = `bytes ${start}-${end}/${total}`;
  if (response.headers.get("content-range") !== expectedRange) {
    throw new Error(`range response is ${response.headers.get("content-range") ?? "missing"}, expected ${expectedRange}`);
  }
  if (body.length !== end - start + 1) throw new Error("range body length is incorrect");
}

async function qualifyWrite(handle, input) {
  const digest = sha256(input);
  const began = performance.now();
  const response = await fetch(handle.url, {
    method: "PUT",
    headers: { Authorization: `Bearer ${handle.token}`, "Content-Type": "application/octet-stream" },
    body: input,
    redirect: "error",
  });
  const raw = (await readBoundedBody(response, 64 << 10)).toString("utf8");
  if (!response.ok) throw new Error(`write response status ${response.status}`);
  let payload;
  try {
    payload = JSON.parse(raw);
  } catch {
    throw new Error("write response is not JSON");
  }
  if (payload.message_sha256 !== digest) throw new Error("write response digest differs from uploaded bytes");
  return { bytes: input.length, sha256: digest, status: response.status, elapsed_ms: round(performance.now() - began) };
}

async function loadHandle(filename, method, allowLoopbackHTTP) {
  if (!filename) throw new Error(`missing --${method === "GET" ? "read" : "write"}-handle`);
  let value;
  try {
    value = JSON.parse(await readFile(filename, "utf8"));
  } catch {
    throw new Error(`${method} handle is not valid JSON`);
  }
  if (!value || typeof value !== "object") throw new Error(`${method} handle must be an object`);
  if (value.method !== undefined && value.method !== method) throw new Error(`${method} handle declares method ${value.method}`);
  if (typeof value.base_url !== "string" || typeof value.path !== "string" || typeof value.tok !== "string" || !value.tok) {
    throw new Error(`${method} handle must contain base_url, path and tok`);
  }
  if (!value.path.startsWith("/substance/") || value.path.includes("?") || value.path.includes("#")) {
    throw new Error(`${method} handle path is invalid`);
  }
  const base = new URL(value.base_url);
  if (base.username || base.password || base.search || base.hash) throw new Error(`${method} base_url contains forbidden URL components`);
  if (base.protocol !== "https:" && !(allowLoopbackHTTP && base.protocol === "http:" && isLoopback(base.hostname))) {
    throw new Error(`${method} handle must use HTTPS`);
  }
  const url = new URL(`${value.base_url.replace(/\/+$/u, "")}${value.path}`);
  if (url.origin !== base.origin || url.search || url.hash) throw new Error(`${method} handle escaped its public origin`);
  return {
    url: url.href,
    origin: url.origin,
    protocol: url.protocol,
    token: value.tok,
    sizeHint: exactNonNegativeInteger(value.size_hint ?? value.total_bytes),
  };
}

async function readBoundedBody(response, maximum) {
  const declared = response.headers.get("content-length");
  if (declared !== null && (!/^\d+$/u.test(declared) || Number(declared) > maximum)) {
    await response.body?.cancel();
    throw new Error("response body exceeds the qualification limit");
  }
  if (!response.body) return Buffer.alloc(0);
  const reader = response.body.getReader();
  const chunks = [];
  let total = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    total += value.byteLength;
    if (total > maximum) {
      await reader.cancel();
      throw new Error("response body exceeds the qualification limit");
    }
    chunks.push(Buffer.from(value));
  }
  return Buffer.concat(chunks, total);
}

function isLoopback(hostname) {
  if (hostname === "localhost") return true;
  const ip = net.isIP(hostname) ? hostname : "";
  return ip === "127.0.0.1" || ip === "::1";
}

function exactNonNegativeInteger(value) {
  return Number.isSafeInteger(value) && value >= 0 ? value : undefined;
}

function sha256(value) {
  return createHash("sha256").update(value).digest("hex");
}

function round(value) { return Math.round(value * 100) / 100; }

function safeError(error) {
  return error instanceof Error ? error.message.replace(/Bearer\s+\S+/giu, "Bearer [redacted]") : "unknown error";
}

function parseArgs(args) {
  const result = { allowLoopbackHTTP: false };
  for (let index = 0; index < args.length; index++) {
    const argument = args[index];
    if (argument === "--allow-loopback-http") {
      result.allowLoopbackHTTP = true;
      continue;
    }
    const key = new Map([
      ["--read-handle", "readHandle"], ["--read-expected", "readExpected"],
      ["--write-handle", "writeHandle"], ["--write-input", "writeInput"],
    ]).get(argument);
    if (!key || index + 1 >= args.length) throw new Error(`unknown or incomplete argument ${argument}`);
    result[key] = args[++index];
  }
  if (!result.readHandle || !result.readExpected) throw new Error("--read-handle and --read-expected are required");
  return result;
}
