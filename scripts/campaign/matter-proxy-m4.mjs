/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

// M4 qualification driver: a real engine, brique-web and Caddy on a real
// (non-loopback) LAN address with a TLS certificate issued by Caddy's local CA,
// then the Matter proxy probe and the mandatory failure checks of
// root/document/recette_m4_proxy_matter_sans_docker.md.
//
//   node scripts/campaign/matter-proxy-m4.mjs --runtime <runtime-package> [--keep]
//
// What this proves, and what it does not: server and client run on the SAME
// machine, the client reaching the proxy through the machine's LAN address. It
// exercises the proxy, TLS verification, Host/Origin, Range/ETag and digest paths
// over a non-loopback address. It is not a measurement of a second host or of a
// public CA; the report says so. Everything lives under one temporary HOME.
import { spawn, spawnSync } from "node:child_process";
import { createHash, randomBytes } from "node:crypto";
import { existsSync, readFileSync } from "node:fs";
import { chmod, mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import https from "node:https";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import WebSocket from "ws";
import { sleep, waitFor } from "./cdp.mjs";

const repository = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");
const options = parseArguments(process.argv.slice(2));
const caddy = spawnSync("which", ["caddy"], { encoding: "utf8" }).stdout.trim();
if (!caddy) {
  console.log("M4 skipped: caddy is not installed");
  process.exit(0);
}
const lanAddress = Object.values(os.networkInterfaces()).flat()
  .find((entry) => entry && entry.family === "IPv4" && !entry.internal)?.address;
if (!lanAddress) {
  console.log("M4 skipped: no non-loopback IPv4 address");
  process.exit(0);
}

const PROXY_PORT = 8443;
const WEB_PORT = 19080;
const MATTER_PORT = 19091;
const authority = `${lanAddress}:${PROXY_PORT}`;
const origin = `https://${authority}`;
const READ_ID = "m_m4_read";
const WRITE_ID = "m_m4_write";
const BIG_ID = "m_m4_big";
const BIG_BYTES = 48 << 20; // larger than the socket and proxy buffers, so a stalled reader exerts back-pressure
const READ_BYTES = 5 << 20;
const WRITE_BYTES = 4 << 20;
const MAX_TRANSFERS = 3;

class PublicSession {
  static async open(ca) {
    const socket = await new Promise((resolve, reject) => {
      const candidate = new WebSocket(`wss://${authority}/__brique/public/main`, "brique.public-web.v1",
        { ca, headers: { Origin: origin }, perMessageDeflate: false });
      candidate.once("open", () => resolve(candidate));
      candidate.once("error", reject);
    });
    return new PublicSession(socket);
  }
  constructor(socket) {
    this.socket = socket;
    this.waiting = new Map();
    this.counter = 0;
    socket.on("message", (data) => {
      const message = JSON.parse(data.toString());
      const entry = this.waiting.get(message?.response?.intention_id);
      if (entry) { this.waiting.delete(message.response.intention_id); entry(message.response); }
    });
  }
  call(cap, params) {
    const id = `123e4567-e89b-42d3-a456-${String(++this.counter).padStart(12, "0")}`;
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error(`${cap} timed out`)), 15000);
      this.waiting.set(id, (response) => { clearTimeout(timer); resolve(response); });
      this.socket.send(JSON.stringify({ kind: "intention", ts: "", intention: { intention_id: id, await_response: true,
        to: { context: "/root", type: "matter", cap }, params } }));
    });
  }
  async matterHandle(cap, params, pick) {
    const response = await this.call(cap, params);
    if (response.status !== "ok") fail(`${cap} answered ${response.status}: ${JSON.stringify(response.error)}`);
    const handle = pick(response.payload);
    if (!handle) fail(`${cap} returned no handle: ${JSON.stringify(Object.keys(response.payload ?? {}))}`);
    return handle;
  }
  close() { this.socket.close(); }
}

const home = await mkdtemp(path.join("/tmp", "bqm-"));
const env = { ...process.env, HOME: home, XDG_DATA_HOME: path.join(home, "xdg") };
const bin = (name) => path.join(options.runtime, "bin", name);
const children = [];
const evidence = { platform: `${process.platform}/${process.arch}`, scope: "single machine, non-loopback LAN address; not a second-host proof" };
const checks = {};
let caFile = "";

try {
  evidence.caddy = run(caddy, ["version"]).trim().split(" ")[0];
  evidence.node = process.version;
  evidence.runtimeId = JSON.parse(readFileSync(path.join(options.runtime, "brique-runtime.json"), "utf8")).runtime_id;
  evidence.origin = origin;

  // Instance: a root with two Brique-mode text Matters seeded on disk.
  const root = path.join(home, "inst", "root");
  await mkdir(path.join(root, "matter"), { recursive: true });
  await writeFile(path.join(root, "context.json"), JSON.stringify(rootContext(), null, 2));
  const readBody = textFixture(READ_BYTES);
  const writeBody = textFixture(WRITE_BYTES);
  await seedMatter(root, READ_ID, readBody);
  await seedMatter(root, WRITE_ID, Buffer.alloc(0));
  await seedMatter(root, BIG_ID, textFixture(BIG_BYTES));
  const fixtures = path.join(home, "fixtures");
  await mkdir(fixtures, { recursive: true });
  await writeFile(path.join(fixtures, "read-expected.data"), readBody);
  await writeFile(path.join(fixtures, "write-input.data"), writeBody);
  evidence.readFixture = { bytes: readBody.length, sha256: sha(readBody) };
  evidence.writeFixture = { bytes: writeBody.length, sha256: sha(writeBody) };

  const publication = path.join(home, "publication");
  run("node", ["--experimental-strip-types", "--loader", "./spark/test/ts-resolve-loader.mjs", "./spark/test/publish-browser-e2e.ts",
    await writeRootView(root), path.join(root, "context.json"), publication, repository], { cwd: repository, env: process.env });

  const security = path.join(home, "security");
  const sec = (...args) => run(bin("brique-sec"), ["--root", root, "--dir", security, ...args]);
  sec("init", "--instance", "m4", "--solo");
  sec("principal", "add", "public.web", "--kind", "public");
  sec("grant", "add", "public.web", "read", "/root/**");
  sec("grant", "add", "public.web", "write", "/root/**");
  sec("apply");

  // The launcher server profile is part of what is qualified here.
  const launcher = (...args) => run(bin("brique-launcher"), args);
  launcher("instance", "add", "--alias", "m4", "--root", root, "--runtime", options.runtime, "--publication", publication,
    "--security-dir", security, "--web-profile", "server", "--web-addr", `127.0.0.1:${WEB_PORT}`,
    "--allowed-host", authority, "--public-origin", origin, "--trusted-proxy", "127.0.0.1");
  const supervisor = spawn(bin("brique-launcher"), ["serve"], { env, stdio: ["ignore", "ignore", "pipe"] });
  children.push(supervisor);
  supervisor.stderr.on("data", (chunk) => process.stderr.write(`[launcher] ${chunk}`));
  await sleep(500);
  const started = JSON.parse(launcher("instance", "start", "m4", "--json"));
  if (started.state !== "ready") fail(`instance not ready: ${JSON.stringify(started)}`);
  evidence.webHost = started.web_url;

  // Caddy: the only listener on the LAN address; TLS from its local CA.
  const caddyfile = path.join(home, "Caddyfile");
  await writeFile(caddyfile, `{
	skip_install_trust
	admin off
	auto_https disable_redirects
	default_bind ${lanAddress}
}
https://${authority} {
	tls internal
	handle /substance/* {
		reverse_proxy 127.0.0.1:${MATTER_PORT} {
			header_up -X-Brique-Client-Address
			header_up -X-Brique-Observed-Address
		}
	}
	handle {
		reverse_proxy 127.0.0.1:${WEB_PORT} {
			header_up X-Brique-Client-Address {remote_host}
			header_up -X-Brique-Observed-Address
		}
	}
}
`);
  run(caddy, ["validate", "--config", caddyfile, "--adapter", "caddyfile"]);
  const proxy = spawn(caddy, ["run", "--config", caddyfile, "--adapter", "caddyfile"], { env, stdio: ["ignore", "ignore", "pipe"] });
  children.push(proxy);
  let proxyLog = "";
  proxy.stderr.on("data", (chunk) => { proxyLog += chunk; });
  caFile = path.join(home, "xdg", "caddy", "pki", "authorities", "local", "root.crt");
  await waitFor(() => existsSync(caFile), 20000, "Caddy local CA");
  await waitFor(async () => (await httpsGet("/", { ca: readFileSync(caFile) })).status === 200, 20000, "proxied web host");
  const ca = readFileSync(caFile);

  // Handles by the normal Intentions, through the proxy, as a public visitor.
  const session = await PublicSession.open(ca);
  const readHandle = await session.matterHandle("matter.read", { matter_id: READ_ID, read_mode: "data" }, (payload) => payload?.data?.http);
  const writeHandle = await session.matterHandle("matter.write",
    { matter_id: WRITE_ID, http_data: true, expected_sha256: sha(writeBody) }, (payload) => payload?.http_data);
  assert(readHandle?.base_url === origin, `read handle base_url ${readHandle?.base_url} is not the public origin`);
  const readFile_ = path.join(fixtures, "read-handle.json");
  const writeFile_ = path.join(fixtures, "write-handle.json");
  await writeFile(readFile_, JSON.stringify(readHandle), { mode: 0o600 });
  await writeFile(writeFile_, JSON.stringify(writeHandle), { mode: 0o600 });

  const probeArgs = ["scripts/qualify-matter-proxy.mjs", "--read-handle", readFile_, "--read-expected", path.join(fixtures, "read-expected.data"),
    "--write-handle", writeFile_, "--write-input", path.join(fixtures, "write-input.data")];
  const probe = spawnSync("node", probeArgs, { cwd: repository, env: { ...process.env, NODE_EXTRA_CA_CERTS: caFile }, encoding: "utf8" });
  assert(probe.status === 0, `qualification probe failed: ${probe.stderr}`);
  const probeResult = JSON.parse(probe.stdout);
  assert(!JSON.stringify(probeResult).includes(readHandle.tok), "the probe output contains a bearer token");
  evidence.probe = probeResult;

  // Read the Matter back through a new lease: byte-for-byte equality.
  const reread = await session.matterHandle("matter.read", { matter_id: WRITE_ID, read_mode: "data" }, (payload) => payload?.data?.http);
  const back = await httpsFetch(reread, { ca });
  assert(back.status === 200 && back.body.equals(writeBody), `re-read of the written Matter differs (status ${back.status}, ${back.body.length} bytes)`);
  evidence.rereadSha256 = sha(back.body);

  // Mandatory failure checks, one by one.
  checks.untrustedCertificate = await expectFailure("certificate not trusted", async () => {
    const r = spawnSync("node", probeArgs, { cwd: repository, env: { ...process.env, NODE_EXTRA_CA_CERTS: "" }, encoding: "utf8" });
    assert(r.status !== 0 && !r.stderr.includes(readHandle.tok), "probe without the CA must fail without leaking the token");
    return "probe refused";
  });
  // A wrong Host is refused either by the proxy at the TLS layer or by an HTTP status; never served.
  checks.wrongHost = await rawRequest({ path: "/", ca, headers: { Host: "evil.example" } })
    .then((response) => response.status, (error) => `connection refused (${error.code ?? "error"})`);
  assert(checks.wrongHost !== 200, "a wrong Host was served");
  checks.wrongWebSocketOrigin = await wsRefused(ca, { Origin: "https://evil.example" });
  const fresh = await session.matterHandle("matter.read", { matter_id: READ_ID, read_mode: "data" }, (payload) => payload?.data?.http);
  checks.bearerAbsent = (await httpsFetch({ ...fresh, tok: "" }, { ca, noAuth: true })).status;
  assert(checks.bearerAbsent >= 400 && checks.bearerAbsent < 500, `request without Bearer = ${checks.bearerAbsent}`);
  const wrongBearer = (await httpsFetch({ ...fresh, tok: "A".repeat(32) }, { ca })).status;
  assert(wrongBearer === checks.bearerAbsent, `wrong Bearer (${wrongBearer}) differs from absent Bearer (${checks.bearerAbsent})`);
  checks.wrongBearerSameAsAbsent = true;
  const first = await httpsFetch(fresh, { ca, headers: { Range: "bytes=0-9" } });
  assert(first.status === 206, `first range = ${first.status}`);
  checks.wrongIfRange = (await httpsFetch(fresh, { ca, headers: { Range: "bytes=10-19", "If-Range": '"not-the-etag"' } })).status;
  assert(checks.wrongIfRange === 409 || checks.wrongIfRange === 200 || checks.wrongIfRange === 412, `If-Range mismatch = ${checks.wrongIfRange}`);
  const complete = await httpsFetch(fresh, { ca });
  assert(complete.status === 200 || complete.status === 206, `completion read = ${complete.status}`);
  checks.oldRid = (await httpsFetch(fresh, { ca })).status;
  assert(checks.oldRid >= 400 && checks.oldRid < 500, `a closed lease still answered ${checks.oldRid}`);
  const badDigest = await session.matterHandle("matter.write",
    { matter_id: WRITE_ID, http_data: true, expected_sha256: sha(Buffer.from("something else")) }, (payload) => payload?.http_data);
  const bad = await httpsFetch(badDigest, { ca, method: "PUT", body: textFixture(1 << 16) });
  checks.wrongWriteDigest = bad.status;
  assert(bad.status >= 400, `a write with a wrong digest returned ${bad.status}`);
  const afterBad = await session.matterHandle("matter.read", { matter_id: WRITE_ID, read_mode: "data" }, (payload) => payload?.data?.http);
  assert((await httpsFetch(afterBad, { ca })).body.equals(writeBody), "a refused digest modified the Matter");
  checks.digestRefusalKeepsMatter = true;
  for (const [name, value] of Object.entries(checks)) if (typeof value === "string" && /leak|token/i.test(value)) fail(`${name} leaked: ${value}`);

  // A client that stops reading is cut at the idle deadline (configured to 3 s here).
  const idle = await session.matterHandle("matter.read", { matter_id: BIG_ID, read_mode: "data" }, (payload) => payload?.data?.http);
  checks.stalledReader = await stalledReaderOutcome(idle, ca, 8000);
  assert(checks.stalledReader.cut, "the server kept a stalled reader beyond the idle deadline");

  // Transfer ceiling (lease_max_transfers is 3 in this run, not the default 32).
  const ceilingSession = await PublicSession.open(ca); // a fresh channel: public leases are capped at 4 per channel
  const holders = [];
  for (let index = 0; index < MAX_TRANSFERS; index++) {
    const handle = await ceilingSession.matterHandle("matter.read", { matter_id: BIG_ID, read_mode: "data" }, (payload) => payload?.data?.http);
    holders.push(await holdTransfer(handle, ca));
  }
  const extra = await ceilingSession.matterHandle("matter.read", { matter_id: BIG_ID, read_mode: "data" }, (payload) => payload?.data?.http);
  const refused = await httpsFetch(extra, { ca });
  checks.transferCeiling = { configured: MAX_TRANSFERS, status: refused.status, retryAfter: refused.headers["retry-after"] ?? null };
  assert(refused.status === 503 && checks.transferCeiling.retryAfter, `transfer ceiling = ${JSON.stringify(checks.transferCeiling)}`);
  holders.forEach((holder) => holder.release());

  // Public responses must not carry local paths or internal causes.
  for (const sample of [first, bad, refused]) {
    assert(!/\/Users\/|\/tmp\/|\.go:|panic|goroutine/.test(sample.body.toString("utf8")), "a public response carries an internal detail");
  }
  checks.noInternalDetailInResponses = true;
  evidence.checks = checks;
  evidence.notRun = ["33rd transfer at the default ceiling (mechanism verified at 3)", "a second physical host", "a publicly trusted certificate"];
  ceilingSession.close();
  session.close();
  console.log(JSON.stringify({ campaign: "matter-proxy-m4", result: "passed", ...evidence }, null, 2));
} catch (error) {
  evidence.checks = checks;
  console.error(JSON.stringify({ campaign: "matter-proxy-m4", result: "failed", ...evidence }, null, 2));
  console.error(error?.stack ?? String(error));
  process.exitCode = 1;
} finally {
  spawnSync(bin("brique-launcher"), ["shutdown"], { env });
  for (const child of children) child.kill("SIGTERM");
  await sleep(500);
  if (!options.keep) await rm(home, { recursive: true, force: true });
  else console.error(`kept ${home}`);
}

function rootContext() {
  return {
    objective: { name: "root", description: "M4 qualification" }, functional: { role: "root" }, subjective: {},
    brique: { children_list: [], ctx_ext_name: "root", ctx_name: "root", ctx_type: "root", ctx_version: "1",
      ui_config: { root_view: "RootView", adapter: "react", name: "main" },
      engine_config: { communication: { websocket_listener: { addr: "127.0.0.1:1" } }, execution: {}, reflexive: {}, trace: {},
        matter: { lease_listen: `127.0.0.1:${MATTER_PORT}`, public_url: origin, lease_max_transfers: MAX_TRANSFERS,
          lease_ttl_ms: 30000, lease_max_life_ms: 600000, lease_read_idle_ms: 3000, owner_bytes_burst_mib: 0, heavy_burst: 0 } },
      engine_version: "0.0.1", type: "local" },
  };
}
async function writeRootView(root) {
  const file = path.join(root, "RootView.tsx");
  await writeFile(file, `import React from "react";\nexport function RootView() { return React.createElement("main", null, "M4"); }\n`);
  return file;
}
async function seedMatter(root, id, body) {
  await writeFile(path.join(root, "matter", `${id}.matter.json`), JSON.stringify({
    brique: { kind: "matter", revision: 1, substance_mode: "brique", wrapper_name: "" },
    objective: { name: id, description: "M4 fixture" }, functional: { format: "text" }, subjective: {} }, null, 2));
  await writeFile(path.join(root, "matter", `${id}.text`), body);
}
function textFixture(bytes) {
  const lines = [];
  let size = 0;
  while (size < bytes) {
    const line = randomBytes(36).toString("base64") + "\n";
    lines.push(line);
    size += line.length;
  }
  // ASCII only, so byte slicing is safe; the fixture ends with a newline.
  return Buffer.concat([Buffer.from(lines.join("")).subarray(0, bytes - 1), Buffer.from("\n")]);
}
function sha(buffer) { return createHash("sha256").update(buffer).digest("hex"); }
function run(command, args, extra = {}) {
  const result = spawnSync(command, args, { env, encoding: "utf8", ...extra });
  if (result.status !== 0) fail(`${path.basename(command)} ${args.slice(0, 3).join(" ")} failed (${result.status}): ${result.stderr || result.stdout}`);
  return result.stdout;
}
function rawRequest({ path: requestPath, method = "GET", headers = {}, ca, body }) {
  return new Promise((resolve, reject) => {
    const request = https.request({ host: lanAddress, port: PROXY_PORT, path: requestPath, method, headers, ca, rejectUnauthorized: Boolean(ca) },
      (response) => {
        const chunks = [];
        response.on("data", (chunk) => chunks.push(chunk));
        response.on("end", () => resolve({ status: response.statusCode, headers: response.headers, body: Buffer.concat(chunks) }));
      });
    request.on("error", reject);
    request.end(body);
  });
}
function httpsGet(requestPath, { ca }) { return rawRequest({ path: requestPath, ca }); }
function httpsFetch(handle, { ca, method = "GET", headers = {}, body, noAuth = false }) {
  const url = new URL(handle.path, handle.base_url);
  return rawRequest({ path: url.pathname + url.search, method, ca, body,
    headers: { ...(noAuth || !handle.tok ? {} : { Authorization: `Bearer ${handle.tok}` }), ...headers, ...(body ? { "Content-Length": body.length } : {}) } });
}
async function expectFailure(label, action) {
  try { return await action(); } catch (error) { fail(`${label}: ${error.message}`); }
}
function wsRefused(ca, headers) {
  return new Promise((resolve) => {
    const socket = new WebSocket(`wss://${authority}/__brique/public/main`, "brique.public-web.v1", { ca, headers, perMessageDeflate: false });
    socket.once("open", () => { socket.close(); resolve("accepted"); });
    socket.once("unexpected-response", (_request, response) => resolve(response.statusCode));
    socket.once("error", () => resolve("refused"));
  });
}
// The reader stops after the first chunk, waits past the idle deadline, then resumes.
// A paused Node stream cannot observe a server close, so the verdict is the body:
// complete means the server waited for the stalled reader, truncated means it cut it.
function stalledReaderOutcome(handle, ca, pauseMilliseconds) {
  return new Promise((resolve) => {
    const url = new URL(handle.path, handle.base_url);
    let received = 0;
    let finished = false;
    const finish = (cut, reason) => { if (!finished) { finished = true; request.destroy(); resolve({ cut, received, pausedMs: pauseMilliseconds, reason }); } };
    const request = https.request({ host: lanAddress, port: PROXY_PORT, path: url.pathname + url.search, ca,
      headers: { Authorization: `Bearer ${handle.tok}` } }, (response) => {
      response.once("data", (chunk) => {
        received += chunk.length;
        response.pause();
        setTimeout(() => {
          response.on("data", (more) => { received += more.length; });
          response.on("end", () => finish(received < BIG_BYTES, received < BIG_BYTES ? "truncated" : "complete"));
          response.on("aborted", () => finish(true, "aborted"));
          response.on("error", () => finish(true, "error"));
          response.on("close", () => finish(received < BIG_BYTES, "closed"));
          response.resume();
        }, pauseMilliseconds);
      });
    });
    request.on("error", () => finish(true, "request error"));
    setTimeout(() => finish(false, "timeout"), pauseMilliseconds + 30000);
    request.end();
  });
}
function holdTransfer(handle, ca) {
  return new Promise((resolve) => {
    const url = new URL(handle.path, handle.base_url);
    const request = https.request({ host: lanAddress, port: PROXY_PORT, path: url.pathname + url.search, ca,
      headers: { Authorization: `Bearer ${handle.tok}` } }, (response) => {
      response.once("data", () => { response.pause(); resolve({ release: () => request.destroy() }); });
      response.on("error", () => {});
    });
    request.on("error", () => {});
    request.end();
  });
}
function assert(condition, message) { if (!condition) fail(message); }
function fail(message) { throw new Error(message); }
function parseArguments(args) {
  const result = { runtime: "", keep: false };
  for (let index = 0; index < args.length; index++) {
    if (args[index] === "--runtime") result.runtime = path.resolve(args[++index]);
    else if (args[index] === "--keep") result.keep = true;
    else throw new Error(`unknown argument ${args[index]}`);
  }
  if (!result.runtime) throw new Error("--runtime <runtime-package> is required");
  return result;
}
