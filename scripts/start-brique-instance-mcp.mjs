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

import { spawn } from "node:child_process";
import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const DEFAULT_CONTEXT = "/root/mcp";
const DEFAULT_WRAPPER = "mcp_server_go";
const DEFAULT_MCP_ADDR = ":18200";
const DEFAULT_WAIT_MS = 30_000;

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(scriptDir, "..");

main().catch((error) => {
  console.error(formatError(error));
  process.exit(1);
});

async function main() {
  const options = parseArgs(process.argv.slice(2));
  const contextPath = await resolveContextPath(options.instance);
  const instanceRoot = path.dirname(contextPath);
  const context = await readJson(contextPath);
  const wsUrl = resolveWebSocketUrl(context, options.wsUrl);
  const mcpAddr = options.mcpAddr ?? DEFAULT_MCP_ADDR;
  const mcpSseUrl = `${mcpAddrToHttpUrl(mcpAddr)}/sse`;

  let engine = null;
  const attachState = await probeExistingEngine(wsUrl, instanceRoot);
  if (attachState.kind === "matching") {
    console.log(`[brique] attached engine ${wsUrl} for ${attachState.contextDir}`);
  } else if (attachState.kind === "mismatch") {
    throw new Error(
      `Brique engine already running at ${wsUrl} for ${attachState.actualContextDir}; expected ${attachState.expectedContextDir}`
    );
  } else {
    const binaryPath = path.resolve(options.binary ?? path.join(repoRoot, "bin", briqueBinaryName()));
    engine = launchEngine(binaryPath, instanceRoot, mcpAddr);
    await waitForWebSocket(wsUrl, options.waitMs);
    console.log(`[brique] engine ready ${wsUrl}`);
  }

  if (await canConnectHttp(mcpSseUrl)) {
    console.log(`[brique] MCP already ready ${mcpSseUrl}`);
  } else {
    await startWrapper(wsUrl, options.context, options.wrapper, options.waitMs);
    await waitForHttp(mcpSseUrl, options.waitMs);
    console.log(`[brique] MCP ready ${mcpSseUrl}`);
  }

  if (options.once) {
    await shutdown({ engine, wsUrl, context: options.context, wrapper: options.wrapper, waitMs: options.waitMs });
    return;
  }

  console.log("[brique] keeping instance process alive. Press Ctrl+C to stop MCP and owned engine.");
  await keepAlive({ engine, wsUrl, context: options.context, wrapper: options.wrapper, waitMs: options.waitMs });
}

function parseArgs(args) {
  const options = {
    instance: undefined,
    binary: undefined,
    wsUrl: undefined,
    context: DEFAULT_CONTEXT,
    wrapper: DEFAULT_WRAPPER,
    mcpAddr: process.env.BRIQUE_MCP_ADDR || DEFAULT_MCP_ADDR,
    waitMs: DEFAULT_WAIT_MS,
    once: false,
  };

  for (let i = 0; i < args.length; i += 1) {
    const arg = args[i];
    if (arg === "--instance" || arg === "-i") options.instance = requireValue(args, ++i, arg);
    else if (arg === "--binary") options.binary = requireValue(args, ++i, arg);
    else if (arg === "--ws-url") options.wsUrl = requireValue(args, ++i, arg);
    else if (arg === "--context") options.context = normalizeContext(requireValue(args, ++i, arg));
    else if (arg === "--wrapper") options.wrapper = requireValue(args, ++i, arg);
    else if (arg === "--mcp-addr") options.mcpAddr = requireValue(args, ++i, arg);
    else if (arg === "--wait-ms") options.waitMs = Number(requireValue(args, ++i, arg));
    else if (arg === "--once") options.once = true;
    else if (arg === "--help" || arg === "-h") printHelpAndExit();
    else if (!options.instance) options.instance = arg;
    else throw new Error(`Unknown argument: ${arg}`);
  }

  if (!options.instance) {
    throw new Error("Missing instance path. Use: node scripts/start-brique-instance-mcp.mjs /path/to/brique-instance");
  }
  if (!Number.isFinite(options.waitMs) || options.waitMs <= 0) {
    throw new Error("--wait-ms must be a positive number");
  }
  return options;
}

function requireValue(args, index, flag) {
  const value = args[index];
  if (!value || value.startsWith("--")) throw new Error(`Missing value for ${flag}`);
  return value;
}

function printHelpAndExit() {
  console.log(`Usage: node scripts/start-brique-instance-mcp.mjs [options] <instance-dir|context.json>

Options:
  --instance, -i <path>   Brique instance directory or root context.json
  --binary <path>         Brique engine binary path (default: ./bin/brique)
  --ws-url <url>          Override engine websocket URL
  --context <context>     MCP context (default: /root/mcp)
  --wrapper <name>        MCP wrapper name (default: mcp_server_go)
  --mcp-addr <addr>       MCP HTTP addr injected into wrapper (default: :18200)
  --wait-ms <ms>          Startup timeout (default: 30000)
  --once                  Start MCP, verify it, stop it, then exit
`);
  process.exit(0);
}

async function resolveContextPath(instancePath) {
  const resolved = path.resolve(instancePath);
  const stat = await fs.stat(resolved);
  return stat.isDirectory() ? path.join(resolved, "context.json") : resolved;
}

async function readJson(file) {
  return JSON.parse(await fs.readFile(file, "utf8"));
}

function resolveWebSocketUrl(context, override) {
  if (override) return override;
  const uiConfig = context?.brique?.ui_config ?? {};
  const addr = context?.brique?.engine_config?.communication?.websocket_listener?.addr
    ?? uiConfig.ws_addr
    ?? ":8080";
  const wsPath = uiConfig.ws_path ?? "/ws";
  const host = addr.startsWith(":") ? `localhost${addr}` : addr;
  return `ws://${host}${wsPath}`;
}

function briqueBinaryName() {
  return process.platform === "win32" ? "brique.exe" : "brique";
}

function launchEngine(binaryPath, instanceRoot, mcpAddr) {
  console.log(`[brique] starting engine ${binaryPath} -root_path ${instanceRoot}`);
  const child = spawn(binaryPath, ["-root_path", instanceRoot], {
    stdio: ["ignore", "pipe", "pipe"],
    env: { ...process.env, BRIQUE_MCP_ADDR: mcpAddr },
  });

  child.stdout.on("data", (data) => process.stdout.write(`[brique] ${data}`));
  child.stderr.on("data", (data) => process.stderr.write(`[brique:error] ${data}`));
  child.on("exit", (code, signal) => {
    console.log(`[brique] engine exited code=${code ?? "null"} signal=${signal ?? "null"}`);
  });

  return {
    child,
    stop: () => {
      if (!child.killed) child.kill();
    },
  };
}

async function probeExistingEngine(wsUrl, expectedContextDir) {
  let state = null;
  try {
    state = await readEngineState(wsUrl, 1_500);
  } catch {
    return { kind: "none" };
  }

  const actualContextDir = state.contextDir ? path.resolve(state.contextDir) : "";
  const resolvedExpected = path.resolve(expectedContextDir);
  if (actualContextDir === resolvedExpected) {
    return { kind: "matching", contextDir: actualContextDir };
  }
  return {
    kind: "mismatch",
    expectedContextDir: resolvedExpected,
    actualContextDir: actualContextDir || "unknown",
  };
}

async function readEngineState(wsUrl, waitMs) {
  const response = await sendCapabilityAndWait({
    wsUrl,
    context: "/root",
    capability: "read.state",
    type: "reflexive",
    params: { include: ["context"] },
    waitMs,
    sourceContext: "@ui_cli_probe:/",
    identity: { id: "brique-cli-probe", kind: "cli" },
  });
  return { contextDir: extractStateContextDir(response.payload) };
}

function extractStateContextDir(payload) {
  if (!payload || typeof payload !== "object") return null;
  const contextPayload = payload.context;
  if (!contextPayload || typeof contextPayload !== "object") return null;
  return typeof contextPayload.context_dir === "string" ? contextPayload.context_dir : null;
}

function startWrapper(wsUrl, context, wrapper, waitMs) {
  return sendWrapperCommand(wsUrl, context, "wrapper.start", wrapper, waitMs);
}

function stopWrapper(wsUrl, context, wrapper, waitMs) {
  return sendWrapperCommand(wsUrl, context, "wrapper.stop", wrapper, Math.min(waitMs, 5_000));
}

async function sendWrapperCommand(wsUrl, context, capability, wrapper, waitMs) {
  const response = await sendCapabilityAndWait({
    wsUrl,
    context,
    capability,
    type: "execution",
    params: { name: wrapper },
    waitMs,
  });
  if (response.status !== "ok") {
    throw new Error(`${capability} failed: ${JSON.stringify(response)}`);
  }
  return response;
}

function sendCapabilityAndWait({ wsUrl, context, capability, type, params, waitMs, sourceContext = "@ui_cli:/", identity = { id: "brique-cli", kind: "cli" } }) {
  const intentionId = crypto.randomUUID();
  const message = {
    kind: "intention",
    ts: new Date().toISOString(),
    intention: {
      intention_id: intentionId,
      await_response: true,
      to: { context, cap: capability, type },
      from: { context: sourceContext, cap: "ui", type: "user" },
      identity,
      params,
      correlation: { root_intention_id: intentionId, parent_intention_id: null },
    },
  };
  return sendAndWait({ wsUrl, message, intentionId, waitMs, label: capability });
}

function sendAndWait({ wsUrl, message, intentionId, waitMs, label }) {
  return new Promise((resolve, reject) => {
    const ws = new WebSocket(wsUrl);
    const timeout = setTimeout(() => {
      ws.close();
      reject(new Error(`Timed out waiting for ${label} response: ${intentionId}`));
    }, waitMs);

    const cleanup = () => {
      clearTimeout(timeout);
      ws.onopen = null;
      ws.onerror = null;
      ws.onclose = null;
      ws.onmessage = null;
    };

    ws.onopen = () => ws.send(JSON.stringify(message));
    ws.onerror = () => {
      cleanup();
      reject(new Error(`Unable to connect to Brique websocket: ${wsUrl}`));
    };
    ws.onmessage = (event) => {
      if (typeof event.data !== "string") return;
      const incoming = JSON.parse(event.data);
      const response = incoming.response ?? incoming;
      if (response?.intention_id !== intentionId) return;
      if (response.status === "running") return;
      cleanup();
      ws.close();
      resolve(response);
    };
  });
}

async function waitForWebSocket(url, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (await canConnectWebSocket(url)) return;
    await delay(200);
  }
  throw new Error(`Brique WebSocket at ${url} did not become ready within ${timeoutMs}ms`);
}

function canConnectWebSocket(url) {
  return new Promise((resolve) => {
    const ws = new WebSocket(url);
    const timeout = setTimeout(() => {
      cleanup();
      try { ws.close(); } catch {}
      resolve(false);
    }, 500);
    const cleanup = () => {
      clearTimeout(timeout);
      ws.onopen = null;
      ws.onerror = null;
      ws.onclose = null;
    };
    ws.onopen = () => {
      cleanup();
      ws.close();
      resolve(true);
    };
    ws.onerror = () => {
      cleanup();
      resolve(false);
    };
    ws.onclose = () => {
      cleanup();
      resolve(false);
    };
  });
}

async function canConnectHttp(url) {
  try {
    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), 500);
    const res = await fetch(url, { signal: controller.signal });
    clearTimeout(timeout);
    await res.body?.cancel().catch(() => undefined);
    return res.ok;
  } catch {
    return false;
  }
}

async function waitForHttp(url, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (await canConnectHttp(url)) return;
    await delay(200);
  }
  throw new Error(`Brique MCP endpoint at ${url} did not become ready within ${timeoutMs}ms`);
}

function mcpAddrToHttpUrl(addr) {
  if (/^https?:\/\//u.test(addr)) return addr.replace(/\/$/u, "");
  if (addr.startsWith(":")) return `http://localhost${addr}`;
  return `http://${addr}`;
}

function normalizeContext(context) {
  const trimmed = context.trim();
  return trimmed.startsWith("/") ? trimmed : `/${trimmed}`;
}

function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function keepAlive(args) {
  return new Promise((resolve) => {
    let stopping = false;
    const stop = () => {
      if (stopping) return;
      stopping = true;
      shutdown(args).finally(resolve);
    };
    process.once("SIGINT", stop);
    process.once("SIGTERM", stop);
    args.engine?.child?.once("exit", resolve);
  });
}

async function shutdown({ engine, wsUrl, context, wrapper, waitMs }) {
  console.log("[brique] stopping MCP wrapper...");
  await stopWrapper(wsUrl, context, wrapper, waitMs).catch((error) => {
    console.warn(`[brique] wrapper.stop failed: ${formatError(error)}`);
  });
  if (engine) {
    console.log("[brique] stopping owned engine...");
    engine.stop();
  }
}

function formatError(error) {
  return error instanceof Error ? error.message : String(error);
}
