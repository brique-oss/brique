/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

import { createHash } from "node:crypto";
import { open, realpath, stat } from "node:fs/promises";
import { createConnection, isIP } from "node:net";
import path from "node:path";

export const DIRECTORY_OBJECT_FORMAT = "brique.directory-object/1";
export const DISCOVERY_KEY_PREFIX = "brd1_";

export interface DirectoryObjectID {
  format: typeof DIRECTORY_OBJECT_FORMAT;
  platform: string;
  volume: string;
  file: string;
}

export interface EngineRuntimeDescriptor {
  format: "brique.engine-runtime/1" | "brique.engine-runtime/2";
  discovery_key: string;
  root_path: string;
  root_object: DirectoryObjectID;
  generation: string;
  state: "starting" | "ready" | "stopping" | "failed";
  pid: number;
  control: {
    protocol: "brique.local-control/1";
    transport: "unix" | "windows_named_pipe";
    address: string;
  };
  ui_websocket_address?: string;
  public_websocket_address?: string;
  [key: string]: unknown;
}

export interface EngineControlResponse {
  format: "brique.local-control/1";
  generation: string;
  ok: boolean;
  error_code?: string;
  state?: EngineRuntimeDescriptor["state"];
  pid?: number;
  root_path?: string;
  root_object?: DirectoryObjectID;
  build?: unknown;
}

const MAX_LOCAL_CONTROL_MESSAGE_BYTES = 64 << 10;
const MAX_RUNTIME_DESCRIPTOR_BYTES = 64 << 10;
const DEFAULT_CONTROL_TIMEOUT_MS = 5_000;
const DEFAULT_DISCOVERY_TIMEOUT_MS = 30_000;
const DISCOVERY_POLL_INTERVAL_MS = 50;

const lowercaseHex = /^[0-9a-f]+$/u;

export function directoryObjectKey(identity: DirectoryObjectID): string {
  if (
    identity.format !== DIRECTORY_OBJECT_FORMAT ||
    identity.platform.length === 0 ||
    !lowercaseHex.test(identity.volume) ||
    !lowercaseHex.test(identity.file)
  ) {
    throw new Error("invalid Brique directory object identity");
  }
  return `${identity.format}:${identity.platform}:${identity.volume}:${identity.file}`;
}

export function discoveryKey(identity: DirectoryObjectID): string {
  const digest = createHash("sha256").update(directoryObjectKey(identity), "utf8").digest();
  return `${DISCOVERY_KEY_PREFIX}${digest.subarray(0, 18).toString("base64url")}`;
}

export async function directoryObjectIdentity(rootPath: string): Promise<{ rootPath: string; identity: DirectoryObjectID }> {
  const canonicalRoot = await realpath(rootPath);
  const metadata = await stat(canonicalRoot, { bigint: true });
  if (!metadata.isDirectory()) {
    throw new Error("Brique root is not a directory");
  }
  const platform = process.platform === "win32" ? "windows" : process.platform;
  const volume = BigInt.asUintN(64, metadata.dev).toString(16);
  let file = BigInt.asUintN(64, metadata.ino).toString(16);
  if (platform === "windows") {
    file = file.padStart(16, "0");
  }
  return {
    rootPath: canonicalRoot,
    identity: { format: DIRECTORY_OBJECT_FORMAT, platform, volume, file },
  };
}

export function defaultRuntimeDirectory(): string {
  if (process.platform === "win32") {
    const localAppData = process.env.LOCALAPPDATA;
    if (!localAppData || !path.isAbsolute(localAppData)) {
      throw new Error("LOCALAPPDATA is unavailable");
    }
    return path.join(localAppData, "Brique", "runtime");
  }
  const uid = process.getuid?.();
  if (uid === undefined) {
    throw new Error("current Unix uid is unavailable");
  }
  return path.join("/tmp", `brique-runtime-${uid}`);
}

export async function findEngineRuntime(rootPath: string, runtimeDirectory = defaultRuntimeDirectory()): Promise<EngineRuntimeDescriptor> {
  if (!path.isAbsolute(runtimeDirectory)) {
    throw new Error("Brique runtime directory must be absolute");
  }
  const root = await directoryObjectIdentity(rootPath);
  const key = discoveryKey(root.identity);
  const raw = await readProtectedDescriptor(path.join(runtimeDirectory, "instances", `${key}.json`));
  const descriptor = JSON.parse(raw) as EngineRuntimeDescriptor;
  if (
    !["brique.engine-runtime/1", "brique.engine-runtime/2"].includes(descriptor.format) ||
    descriptor.discovery_key !== key ||
    descriptor.root_path !== root.rootPath ||
    descriptor.root_object?.format !== root.identity.format ||
    descriptor.root_object.platform !== root.identity.platform ||
    descriptor.root_object.volume !== root.identity.volume ||
    descriptor.root_object.file !== root.identity.file ||
    !Number.isSafeInteger(descriptor.pid) || descriptor.pid < 1 ||
    !/^brg1_[A-Za-z0-9_-]{32}$/u.test(descriptor.generation) ||
    !["starting", "ready", "stopping", "failed"].includes(descriptor.state) ||
    !validControlEndpoint(descriptor.control) ||
    (descriptor.ui_websocket_address !== undefined && !validLoopbackAddress(descriptor.ui_websocket_address)) ||
    (descriptor.format === "brique.engine-runtime/1" && descriptor.public_websocket_address !== undefined) ||
    (descriptor.public_websocket_address !== undefined && !validLoopbackPublicWebSocket(descriptor.public_websocket_address))
  ) {
    throw new Error("invalid Brique engine runtime descriptor");
  }
  return descriptor;
}

function validLoopbackPublicWebSocket(value: string): boolean {
  try {
    const endpoint = new URL(value);
    const host = endpoint.hostname.replace(/^\[|\]$/gu, "");
    const loopback = host === "127.0.0.1" || host === "::1" || (isIP(host) === 4 && host.startsWith("127."));
    return endpoint.protocol === "ws:" && loopback && endpoint.port.length > 0 &&
      endpoint.pathname === "/__brique/public/" && endpoint.search.length === 0 && endpoint.hash.length === 0 &&
      endpoint.username.length === 0 && endpoint.password.length === 0;
  } catch {
    return false;
  }
}

async function readProtectedDescriptor(descriptorPath: string): Promise<string> {
  const handle = await open(descriptorPath, "r");
  try {
    const metadata = await handle.stat();
    if (!metadata.isFile() || metadata.size < 1 || metadata.size > MAX_RUNTIME_DESCRIPTOR_BYTES) {
      throw new Error("invalid Brique engine runtime descriptor size");
    }
    if (process.platform !== "win32") {
      const uid = process.getuid?.();
      if (uid === undefined || metadata.uid !== uid || (metadata.mode & 0o077) !== 0) {
        throw new Error("Brique engine runtime descriptor is not owner-protected");
      }
    }
    const raw = await handle.readFile();
    if (raw.length > MAX_RUNTIME_DESCRIPTOR_BYTES) {
      throw new Error("invalid Brique engine runtime descriptor size");
    }
    return raw.toString("utf8");
  } finally {
    await handle.close();
  }
}

export async function callEngineControl(
  descriptor: EngineRuntimeDescriptor,
  operation: "status" | "version" | "root" | "generation",
  timeoutMs = DEFAULT_CONTROL_TIMEOUT_MS,
): Promise<EngineControlResponse> {
  if (!Number.isSafeInteger(timeoutMs) || timeoutMs < 1) {
    throw new Error("invalid Brique local control timeout");
  }
  if (!validControlEndpoint(descriptor.control)) {
    throw new Error("invalid Brique local control endpoint");
  }

  const request = `${JSON.stringify({
    format: "brique.local-control/1",
    generation: descriptor.generation,
    operation,
  })}\n`;

  return await new Promise<EngineControlResponse>((resolve, reject) => {
    const socket = createConnection(descriptor.control.address);
    let settled = false;
    let response = Buffer.alloc(0);

    const finish = (err?: Error, value?: EngineControlResponse) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      socket.destroy();
      if (err) reject(err);
      else resolve(value as EngineControlResponse);
    };
    const timer = setTimeout(
      () => finish(new Error(`Brique local control timed out during ${operation}`)),
      timeoutMs,
    );

    socket.once("connect", () => socket.write(request));
    socket.on("data", (chunk: Buffer) => {
      response = Buffer.concat([response, chunk]);
      if (response.length > MAX_LOCAL_CONTROL_MESSAGE_BYTES) {
        finish(new Error("Brique local control response is too large"));
        return;
      }
      const newline = response.indexOf(0x0a);
      if (newline < 0) return;
      if (newline !== response.length - 1) {
        finish(new Error("invalid trailing Brique local control data"));
        return;
      }
      try {
        const decoded = JSON.parse(response.subarray(0, newline).toString("utf8")) as EngineControlResponse;
        if (
          decoded.format !== "brique.local-control/1" ||
          decoded.generation !== descriptor.generation ||
          typeof decoded.ok !== "boolean"
        ) {
          throw new Error("invalid Brique local control response");
        }
        finish(undefined, decoded);
      } catch (err) {
        finish(err instanceof Error ? err : new Error("invalid Brique local control response"));
      }
    });
    socket.once("error", (err) => finish(err));
    socket.once("end", () => finish(new Error("Brique local control closed before responding")));
  });
}

export async function verifyEngineRuntime(
  rootPath: string,
  runtimeDirectory = defaultRuntimeDirectory(),
): Promise<EngineRuntimeDescriptor> {
  const descriptor = await findEngineRuntime(rootPath, runtimeDirectory);
  const [status, root] = await Promise.all([
    callEngineControl(descriptor, "status"),
    callEngineControl(descriptor, "root"),
  ]);
  if (
    !status.ok ||
    status.state !== descriptor.state ||
    status.pid !== descriptor.pid ||
    !root.ok ||
    root.root_path !== descriptor.root_path ||
    !sameDirectoryObject(root.root_object, descriptor.root_object)
  ) {
    throw new Error("Brique engine runtime handshake does not match its descriptor");
  }
  if (descriptor.state === "ready" && !descriptor.ui_websocket_address) {
    throw new Error("ready Brique engine did not publish its UI websocket address");
  }
  return descriptor;
}

export async function tryVerifyEngineRuntime(
  rootPath: string,
  runtimeDirectory = defaultRuntimeDirectory(),
): Promise<EngineRuntimeDescriptor | null> {
  try {
    return await verifyEngineRuntime(rootPath, runtimeDirectory);
  } catch {
    return null;
  }
}

export async function waitForReadyEngineRuntime(
  rootPath: string,
  runtimeDirectory = defaultRuntimeDirectory(),
  timeoutMs = DEFAULT_DISCOVERY_TIMEOUT_MS,
): Promise<EngineRuntimeDescriptor> {
  if (!Number.isSafeInteger(timeoutMs) || timeoutMs < 1) {
    throw new Error("invalid Brique discovery timeout");
  }
  const deadline = Date.now() + timeoutMs;
  let lastError: unknown;
  while (Date.now() < deadline) {
    let descriptor: EngineRuntimeDescriptor | null = null;
    try {
      descriptor = await verifyEngineRuntime(rootPath, runtimeDirectory);
    } catch (err) {
      lastError = err;
    }
    if (descriptor?.state === "ready") return descriptor;
    if (descriptor?.state === "failed" || descriptor?.state === "stopping") {
      throw new Error(`Brique engine entered terminal state ${descriptor.state}`);
    }
    await new Promise((resolve) => setTimeout(resolve, DISCOVERY_POLL_INTERVAL_MS));
  }
  const detail = lastError instanceof Error ? `: ${lastError.message}` : "";
  throw new Error(`timed out waiting for Brique engine local readiness${detail}`);
}

function validControlEndpoint(value: EngineRuntimeDescriptor["control"] | undefined): boolean {
  if (
    value?.protocol !== "brique.local-control/1" ||
    !["unix", "windows_named_pipe"].includes(value.transport) ||
    typeof value.address !== "string" ||
    value.address.length === 0 ||
    value.address.length > 512
  ) {
    return false;
  }
  if (value.transport === "unix") {
    return path.isAbsolute(value.address) && path.normalize(value.address) === value.address;
  }
  return /^\\\\\.\\pipe\\brique-/iu.test(value.address);
}

function validLoopbackAddress(address: string): boolean {
  try {
    const url = new URL(`ws://${address}`);
    const hostname = url.hostname.replace(/^\[|\]$/gu, "").toLowerCase();
    return hostname === "localhost" || hostname === "::1" || /^127(?:\.\d{1,3}){3}$/u.test(hostname);
  } catch {
    return false;
  }
}

function sameDirectoryObject(left: DirectoryObjectID | undefined, right: DirectoryObjectID): boolean {
  return left?.format === right.format &&
    left.platform === right.platform &&
    left.volume === right.volume &&
    left.file === right.file;
}
