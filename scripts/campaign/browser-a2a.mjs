/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

// Browser campaign driver (D-04): replays the local A->A journey of the Brique
// browser bridge in a real Chromium-family browser, from a verified runtime
// package up to a Matter-free `ui.list` round trip answered by the engine
// through the native host.
//
//   node scripts/campaign/browser-a2a.mjs --browser brave|chrome|firefox [--runtime <dir>] [--headed] [--keep]
//
// Everything the campaign writes lives under one short temporary HOME, which is
// also the HOME of the browser and of the native host it launches. The user's
// real launcher registry, security directories and browser profiles are never
// read or written. The campaign is skipped (exit 0) when the browser is absent.
import { spawn, spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { cp, mkdir, mkdtemp, readFile, realpath, rm, writeFile } from "node:fs/promises";
import { existsSync, readFileSync, readdirSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { BiDi } from "./bidi.mjs";
import { CDP, freePort, waitFor, sleep } from "./cdp.mjs";

const repository = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");
const BROWSERS = {
  brave: { executable: "/Applications/Brave Browser.app/Contents/MacOS/Brave Browser", launcherName: "brave" },
  firefox: { executable: "/Applications/Firefox.app/Contents/MacOS/firefox", launcherName: "firefox", gecko: true },
  chrome: { executable: "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", launcherName: "chrome" },
};

const options = parseArguments(process.argv.slice(2));
const browser = BROWSERS[options.browser];
if (!browser) fail(`unknown browser ${options.browser}; expected ${Object.keys(BROWSERS).join(" or ")}`);
if (process.platform !== "darwin") {
  console.log("campaign skipped: the Chromium driver currently targets macOS paths");
  process.exit(0);
}
if (!existsSync(browser.executable)) {
  console.log(`campaign skipped: ${browser.executable} is unavailable`);
  process.exit(0);
}

const ROOT_VIEW = `import React from "react";
const message = JSON.stringify({ kind: "intention", ts: "", intention: {
  intention_id: "123e4567-e89b-42d3-a456-426614174001", await_response: true,
  to: { context: "/root", cap: "ui.list", type: "reflexive" },
  from: { context: "/forged", cap: "", type: "admin" }, identity: { id: "admin" } } });
export function RootView() {
  React.useEffect(() => {
    const root = document.documentElement;
    const bridge = globalThis.__SPARK_TRANSPORT_BRIDGE__;
    if (!bridge) { root.dataset.briqueCampaign = "error:no-bridge"; return; }
    bridge.subscribe((serialized) => {
      try {
        const response = JSON.parse(serialized);
        root.dataset.briqueCampaignTo = response?.response?.to?.context ?? "";
        const result = response?.response?.payload?.result;
        root.dataset.briqueCampaign = response?.response?.status === "ok" && Array.isArray(result) && result.length >= 1
          ? "ok" : "error:bad-response:" + String(response?.response?.status);
      } catch (error) { root.dataset.briqueCampaign = "error:" + String(error); }
    });
    bridge.subscribeToConnectionState((state) => {
      root.dataset.briqueCampaignState = state;
      if (state === "connected") bridge.send(message).catch((error) => { root.dataset.briqueCampaign = "error:" + String(error); });
    });
    root.dataset.briqueCampaign = "ready";
  }, []);
  return React.createElement("button", { id: "brique-campaign-open", style: { width: 200, height: 80 },
    onClick: () => globalThis.__SPARK_TRANSPORT_BRIDGE__?.open() }, "Connect");
}
`;

const home = await mkdtemp(path.join("/tmp", "bqc-"));
const env = { ...process.env, HOME: home };
const bin = (name) => path.join(runtimePackage, "bin", name);
let runtimePackage = options.runtime;
const children = [];
const evidence = { browser: options.browser, platform: `${process.platform}/${process.arch}` };
let browserProcess;
let realManifest = "";
let base = "";

try {
  if (!runtimePackage) {
    runtimePackage = path.join(home, "runtime");
    run("node", ["scripts/build-runtime.mjs", "--output", runtimePackage], { cwd: repository, env: process.env });
  }
  evidence.browserVersion = run(browser.executable, ["--version"]).trim();
  const inspected = JSON.parse(run(bin("brique-launcher"), ["runtime", "inspect", runtimePackage, "--json"]));
  evidence.runtimeId = inspected.manifest?.runtime_id ?? inspected.runtime_id;

  // Instance root, real Spark/Pulse-free publication, security policy.
  const root = path.join(home, "inst", "root");
  await mkdir(root, { recursive: true });
  await writeFile(path.join(root, "context.json"), JSON.stringify(rootContext(), null, 2));
  const view = path.join(root, "RootView.tsx");
  await writeFile(view, ROOT_VIEW);
  const publication = path.join(home, "publication");
  run("node", ["--experimental-strip-types", "--loader", "./spark/test/ts-resolve-loader.mjs",
    "./spark/test/publish-browser-e2e.ts", view, path.join(root, "context.json"), publication, repository], { cwd: repository, env: process.env });
  const security = path.join(home, "security");
  const sec = (...args) => run(bin("brique-sec"), ["--root", root, "--dir", security, ...args]);
  sec("init", "--instance", "campaign", "--solo");
  sec("principal", "add", "local.ui", "--kind", "local");
  sec("grant", "add", "local.ui", "read", "/root");
  sec("apply");

  // Launcher state is under the temporary HOME because the native host the
  // browser starts resolves the default launcher directory from HOME.
  const launcher = (...args) => run(bin("brique-launcher"), args);
  launcher("instance", "add", "--alias", "campaign", "--root", root, "--runtime", runtimePackage,
    "--publication", publication, "--security-dir", security);
  launcher("browser", "gateway", "set", "campaign");
  const supervisor = spawn(bin("brique-launcher"), ["serve"], { env, stdio: ["ignore", "pipe", "pipe"] });
  children.push(supervisor);
  supervisor.stderr.on("data", (chunk) => process.stderr.write(`[launcher] ${chunk}`));
  await sleep(500);
  const started = JSON.parse(launcher("instance", "start", "campaign", "--json"));
  if (started.state !== "ready" || !started.web_url) fail(`instance did not become ready: ${JSON.stringify(started)}`);
  const webURL = started.web_url;
  evidence.webURL = webURL;

  // The extension identity of an unpacked extension is derived from its path.
  const extensionPath = await realpath(options.extension || path.join(runtimePackage, "lib", browser.gecko ? "browser-extension-firefox" : "browser-extension"));
  const extensionId = browser.gecko ? "brique-browser-bridge@brique.dev" : unpackedExtensionId(extensionPath);
  const installation = JSON.parse(launcher("browser", "host", "install", "--browser", browser.launcherName,
    "--extension-id", extensionId, "--json"));
  evidence.extensionId = extensionId;
  const profile = path.join(home, "profile");
  // A custom --user-data-dir looks for native hosts beside the profile.
  if (!browser.gecko) {
    await mkdir(path.join(profile, "NativeMessagingHosts"), { recursive: true });
    await cp(installation.manifest_path, path.join(profile, "NativeMessagingHosts", path.basename(installation.manifest_path)));
  }
  if (options.browser === "brave" || browser.gecko) {
    // Brave (like Firefox with Mozilla's directory) resolves the user-level native host directory from the real account
    // home (Chrome's directory on macOS), not from $HOME nor --user-data-dir.
    // The file is installed only if nothing is there and is always removed.
    const realDirectory = path.join(os.userInfo().homedir, "Library", "Application Support",
      ...(browser.gecko ? ["Mozilla"] : ["Google", "Chrome"]), "NativeMessagingHosts");
    realManifest = path.join(realDirectory, path.basename(installation.manifest_path));
    if (existsSync(realManifest)) { realManifest = ""; fail(`${realManifest || realDirectory} already holds a Brique manifest; refusing to replace it`); }
    await mkdir(realDirectory, { recursive: true });
    await cp(installation.manifest_path, realManifest);
  }

  const hostBefore = hostProcesses(home);
  const port = await freePort();
  let page;
  let workerSession;
  let browserLog = "";
  if (browser.gecko) {
    const args = ["--headless", "--no-remote", "--profile", profile, `--remote-debugging-port=${port}`, "about:blank"];
    await mkdir(profile, { recursive: true });
    await writeFile(path.join(profile, "user.js"), 'user_pref("remote.active-protocols", 3);\nuser_pref("datareporting.policy.dataSubmissionEnabled", false);\n');
    browserProcess = spawn(browser.executable, args, { env, stdio: ["ignore", "pipe", "pipe"] });
    browserProcess.stderr.on("data", (chunk) => { browserLog += chunk; });
    page = await BiDi.connect(port);
    await page.installExtension(extensionPath);
    evidence.extensionLoaded = true;
  } else {
    const args = [`--user-data-dir=${profile}`, `--remote-debugging-port=${port}`, "--remote-debugging-address=127.0.0.1",
      `--disable-extensions-except=${extensionPath}`, `--load-extension=${extensionPath}`,
      "--no-first-run", "--no-default-browser-check", "--disable-gpu", "about:blank"];
    if (!options.headed) args.unshift("--headless=new");
    browserProcess = spawn(browser.executable, args, { env, stdio: ["ignore", "pipe", "pipe"] });
    browserProcess.stderr.on("data", (chunk) => { browserLog += chunk; });
    base = `http://127.0.0.1:${port}`;
    const target = await waitFor(async () => {
      const list = await (await fetch(`${base}/json/list`)).json().catch(() => []);
      return list.find((entry) => entry.type === "page");
    }, 15000, "browser DevTools page");
    const targets = await waitFor(async () => {
      const list = await (await fetch(`${base}/json/list`)).json();
      return list.some((entry) => entry.type === "service_worker" && entry.url.startsWith(`chrome-extension://${extensionId}/`)) ? list : undefined;
    }, 10000, "extension service worker").catch(async () => (await (await fetch(`${base}/json/list`)).json()));
    evidence.extensionLoaded = targets.some((entry) => String(entry.url).startsWith(`chrome-extension://${extensionId}/`));
    page = await CDP.connect(target.webSocketDebuggerUrl);
    const worker = targets.find((entry) => entry.type === "service_worker");
    workerSession = worker ? await CDP.connect(worker.webSocketDebuggerUrl) : undefined;
    await workerSession?.send("Runtime.enable").catch(() => {});
    await workerSession?.evaluate(`globalThis.__briqueSenders = []; chrome.runtime.onConnect.addListener((port) => {
      globalThis.__briqueSenders.push(JSON.stringify({ name: port.name, sender: port.sender })); });
      globalThis.__nativeErrors = [];
      const connectNative = chrome.runtime.connectNative.bind(chrome.runtime);
      chrome.runtime.connectNative = (name) => { const port = connectNative(name);
        port.onDisconnect.addListener(() => __nativeErrors.push(String(chrome.runtime.lastError?.message))); return port; };
      true`).catch(() => {}); // diagnostics only: a worker that has just started may not expose chrome.runtime yet
  }
  await page.preload(`globalThis.__briqueTrace = [];
    addEventListener("message", (event) => { if (event.data?.marker) __briqueTrace.push(event.data.direction + ":" + event.data.message?.kind +
      (event.data.message?.ok === undefined ? "" : ":" + event.data.message.ok) + (event.data.message?.kind === "debug" ? JSON.stringify(event.data.message) : "")); }, true);`);
  await page.navigate(webURL);
  await waitFor(async () => (await page.evaluate("document.documentElement.dataset.briqueCampaign || ''")) === "ready",
    15000, "campaign page readiness");
  evidence.relayInjected = await page.evaluate("typeof globalThis.__SPARK_TRANSPORT_BRIDGE__ === 'object'");
  assert(evidence.relayInjected, "the page bridge was not injected");

  // T-05: merely loading the page must not start a native host.
  await sleep(1000);
  evidence.hostBeforeClick = hostProcesses(home).length - hostBefore.length;
  assert(evidence.hostBeforeClick === 0, "a native host started before any user activation");

  // A synthetic click must not release the open; a trusted CDP click must.
  await page.evaluate("document.getElementById('brique-campaign-open').click()");
  await sleep(500);
  assert(hostProcesses(home).length === hostBefore.length, "a synthetic click started the native host");
  await page.evaluate(`addEventListener("pointerdown", (event) => { document.documentElement.dataset.briqueProbe =
    JSON.stringify({ trusted: event.isTrusted, active: navigator.userActivation.isActive }); }, true)`);
  const box = JSON.parse(await page.evaluate(`(() => { const r = document.getElementById('brique-campaign-open').getBoundingClientRect();
    return JSON.stringify({ x: r.x + r.width / 2, y: r.y + r.height / 2 }); })()`));
  await page.click(box.x, box.y);
  const outcome = await waitFor(async () => {
    const value = await page.evaluate("document.documentElement.dataset.briqueCampaign || ''");
    return value.startsWith("ok") || value.startsWith("error") ? value : undefined;
  }, 20000, "local A->A round trip").catch(async (error) => {
    evidence.pageState = await page.evaluate("JSON.stringify({...document.documentElement.dataset})").catch(() => "");
    evidence.hostProcesses = hostProcesses(home).length;
    evidence.workerSenders = await workerSession?.evaluate("JSON.stringify(globalThis.__briqueSenders)").catch((e) => String(e));
    evidence.nativeErrors = await workerSession?.evaluate("JSON.stringify(globalThis.__nativeErrors)").catch((e) => String(e));
    evidence.workerConsole = (workerSession?.events ?? []).filter((event) => event.method === "Runtime.consoleAPICalled" || event.method === "Runtime.exceptionThrown")
      .map((event) => JSON.stringify(event.params.args?.map((arg) => arg.value ?? arg.description) ?? event.params.exceptionDetails?.text));
    evidence.messageTrace = await page.evaluate("JSON.stringify(globalThis.__briqueTrace)").catch(() => "");
    if (base) evidence.targets = (await (await fetch(`${base}/json/list`)).json()).map((entry) => `${entry.type} ${entry.url}`);
    evidence.activationAtPointerdown = await page.evaluate("document.documentElement.dataset.briqueProbe || ''").catch(() => "");
    evidence.browserLog = browserLog.split("\n").slice(-15).join("\n");
    throw error;
  });
  evidence.roundTrip = outcome;
  assert(outcome === "ok", `A->A round trip = ${outcome}\n${browserLog}`);
  evidence.responseDestination = await page.evaluate("document.documentElement.dataset.briqueCampaignTo || ''");
  assert(evidence.responseDestination.startsWith("@local_ui:/"), `unexpected response destination ${evidence.responseDestination}`);
  const hosts = hostProcesses(home);
  evidence.hostAfterClick = hosts.length - hostBefore.length;
  assert(evidence.hostAfterClick === 1, `expected one native host after the click, saw ${evidence.hostAfterClick}`);

  // T-27: killing the host disconnects the page and the same document recreates nothing.
  for (const pid of hosts) process.kill(Number(pid), "SIGKILL");
  const state = await waitFor(async () => {
    const value = await page.evaluate("document.documentElement.dataset.briqueCampaignState || ''");
    return value === "disconnected" ? value : undefined;
  }, 10000, "disconnect after host loss");
  evidence.afterHostLoss = state;
  await sleep(500);
  assert(hostProcesses(home).length === hostBefore.length, "the same document recreated a native host after loss");
  const status = JSON.parse(launcher("instance", "status", "campaign", "--json"));
  evidence.engineAfterHostLoss = status.state;
  assert(status.state === "ready", "the engine did not survive the loss of the native host");

  // T-29: no private key material in anything the launcher, the page or the bridge exposes.
  const secrets = privateKeyNeedles(security);
  const scanStatus = JSON.parse(launcher("instance", "status", "campaign", "--json"));
  const surfaces = {
    "launcher status": JSON.stringify(scanStatus),
    "launcher instance record": launcher("instance", "show", "campaign", "--json"),
    "launcher log": scanStatus.log_path && existsSync(scanStatus.log_path) ? readFileSync(scanStatus.log_path, "utf8") : "",
    "page markup and data": await page.evaluate("document.documentElement.outerHTML"),
    "bridge message trace": await page.evaluate("JSON.stringify(globalThis.__briqueTrace)"),
    "browser log": browserLog,
  };
  for (const [name, text] of Object.entries(surfaces)) {
    for (const needle of secrets) assert(!text.includes(needle), `${name} contains private key material`);
  }
  evidence.privateKeyScan = { needles: secrets.length, surfaces: Object.keys(surfaces) };

  // Navigation and loss scenarios. Each fresh document must need a new trusted
  // click and obtain a new host and a new channel; nothing is resumed by identifier.
  const firstChannel = evidence.responseDestination;
  const liveHosts = async (expected, description) => waitFor(async () => {
    const current = hostProcesses(home).filter((pid) => !hostBefore.includes(pid));
    return current.length === expected ? current : undefined;
  }, 10000, description);
  const freshRoundTrip = async (label) => {
    await page.evaluate("globalThis.__previousDocument = true");
    await page.navigate(webURL);
    await waitFor(async () => (await page.evaluate("globalThis.__previousDocument === undefined && " +
      "document.documentElement.dataset.briqueCampaign === 'ready'")), 15000, `${label}: fresh document`);
    const before = hostProcesses(home).filter((pid) => !hostBefore.includes(pid));
    const box = JSON.parse(await page.evaluate(`(() => { const r = document.getElementById('brique-campaign-open').getBoundingClientRect();
      return JSON.stringify({ x: r.x + r.width / 2, y: r.y + r.height / 2 }); })()`));
    await page.click(box.x, box.y);
    const result = await waitFor(async () => {
      const value = await page.evaluate("document.documentElement.dataset.briqueCampaign || ''");
      return value === "ok" || value.startsWith("error") ? value : undefined;
    }, 20000, `${label}: round trip`);
    assert(result === "ok", `${label}: round trip = ${result}`);
    const channel = await page.evaluate("document.documentElement.dataset.briqueCampaignTo || ''");
    const current = await liveHosts(1, `${label}: exactly one native host`);
    return { pid: current[0], channel, hostsBeforeClick: before.length };
  };
  const afterLoss = await freshRoundTrip("reload after host loss");
  assert(afterLoss.channel !== firstChannel, "a reload reused the previous channel");
  evidence.reloadAfterHostLoss = { freshChannel: true, hostBeforeClick: afterLoss.hostsBeforeClick };

  const afterNavigation = await freshRoundTrip("navigation of a live document");
  assert(afterNavigation.pid !== afterLoss.pid && afterNavigation.channel !== afterLoss.channel,
    "navigation kept the previous host or channel");
  evidence.navigationReplacesHostAndChannel = true;

  if (!browser.gecko) {
    // T-27: terminate the MV3 service worker. The page must disconnect and the
    // same document must not recreate anything; a reload and a click recover.
    const version = await (await fetch(`${base}/json/version`)).json();
    const browserSession = await CDP.connect(version.webSocketDebuggerUrl);
    const { targetInfos } = await browserSession.send("Target.getTargets");
    const workerTarget = targetInfos.find((entry) => entry.type === "service_worker" && entry.url.startsWith(`chrome-extension://${extensionId}/`));
    assert(workerTarget, "extension service worker target not found");
    await browserSession.send("Target.closeTarget", { targetId: workerTarget.targetId }).catch(() => {});
    await waitFor(async () => (await page.evaluate("document.documentElement.dataset.briqueCampaignState")) === "disconnected" || undefined,
      10000, "disconnect after service worker termination").catch(async () => {
      // Closing a service worker target is not honoured everywhere; stop it explicitly.
      await browserSession.send("ServiceWorker.enable");
      await browserSession.send("ServiceWorker.stopAllWorkers");
    });
    await waitFor(async () => (await page.evaluate("document.documentElement.dataset.briqueCampaignState")) === "disconnected" || undefined,
      10000, "disconnect after service worker termination");
    await liveHosts(0, "no native host after the service worker is lost");
    await sleep(500);
    assert(hostProcesses(home).filter((pid) => !hostBefore.includes(pid)).length === 0, "the same document recreated a host after MV3 loss");
    evidence.mv3Loss = "disconnected";
    const recovered = await freshRoundTrip("recovery after MV3 loss");
    assert(recovered.channel !== afterNavigation.channel, "recovery reused a channel");
    evidence.mv3Recovery = "fresh host and channel";
    await browserSession.close();
  } else {
    evidence.mv3Loss = "not applicable: Firefox runs the extension as an event page";
  }

  // A framed copy of the page must not obtain a host nor a second channel.
  const livePids = hostProcesses(home).filter((pid) => !hostBefore.includes(pid));
  const frame = JSON.parse(await page.evaluate(`(async () => {
    const frame = document.createElement("iframe");
    frame.id = "brique-campaign-frame"; frame.src = location.href;
    frame.style.cssText = "position:fixed;left:0;top:120px;width:300px;height:200px";
    document.body.append(frame);
    await new Promise((resolve) => { frame.onload = resolve; setTimeout(resolve, 3000); });
    const r = frame.getBoundingClientRect();
    return JSON.stringify({ x: r.x + r.width / 2, y: r.y + r.height / 2 });
  })()`));
  await page.click(frame.x, frame.y);
  await sleep(1500);
  const afterFrame = hostProcesses(home).filter((pid) => !hostBefore.includes(pid));
  assert(afterFrame.length === livePids.length && afterFrame.every((pid) => livePids.includes(pid)),
    "a framed page started or replaced a native host");
  evidence.iframeStartsNoHost = true;
  await page.close();

  console.log(JSON.stringify({ campaign: "browser-a2a", result: "passed", ...evidence }, null, 2));
} catch (error) {
  console.error(JSON.stringify({ campaign: "browser-a2a", result: "failed", ...evidence }, null, 2));
  console.error(error?.stack ?? String(error));
  process.exitCode = 1;
} finally {
  if (browserProcess) browserProcess.kill("SIGKILL");
  if (realManifest) await rm(realManifest, { force: true });
  spawnSync(path.join(runtimePackage ?? "", "bin", "brique-launcher"), ["shutdown"], { env });
  for (const child of children) child.kill("SIGTERM");
  await sleep(300);
  if (!options.keep) await rm(home, { recursive: true, force: true });
  else console.error(`kept ${home}`);
}

function run(command, args, extra = {}) {
  const result = spawnSync(command, args, { env, encoding: "utf8", ...extra });
  if (result.status !== 0) fail(`${path.basename(command)} ${args.join(" ")} failed (${result.status}): ${result.stderr || result.stdout}`);
  return result.stdout;
}
function hostProcesses(homeDirectory) {
  const out = spawnSync("pgrep", ["-f", path.join(homeDirectory, "runtime", "bin", "brique-browser-host")], { encoding: "utf8" });
  const own = runtimePackage ? spawnSync("pgrep", ["-f", path.join(runtimePackage, "bin", "brique-browser-host")], { encoding: "utf8" }) : out;
  return [...new Set(`${out.stdout}\n${own.stdout}`.split("\n").filter(Boolean))];
}
function unpackedExtensionId(extensionDirectory) {
  const digest = createHash("sha256").update(extensionDirectory).digest("hex").slice(0, 32);
  return [...digest].map((digit) => String.fromCharCode("a".charCodeAt(0) + parseInt(digit, 16))).join("");
}
// Every encoding of the private seeds found under a security directory.
function privateKeyNeedles(directory) {
  const needles = [];
  const visit = (current) => {
    for (const entry of readdirSync(current, { withFileTypes: true })) {
      const target = path.join(current, entry.name);
      if (entry.isDirectory()) visit(target);
      else if (entry.name.endsWith(".pem")) {
        const text = readFileSync(target, "utf8");
        if (!text.includes("PRIVATE KEY")) continue;
        const der = Buffer.from(text.replace(/-----[^-]+-----/g, "").replace(/\s+/g, ""), "base64");
        const seed = der.subarray(der.length - 32);
        needles.push(text.replace(/-----[^-]+-----/g, "").replace(/\s+/g, ""), seed.toString("hex"), seed.toString("base64"),
          seed.toString("base64url"), seed.toString("base64").replace(/=+$/, ""));
      }
    }
  };
  visit(directory);
  if (needles.length < 10) throw new Error("expected at least two private keys under the security directory");
  return needles;
}
function assert(condition, message) { if (!condition) fail(message); }
function fail(message) { throw new Error(message); }
function parseArguments(args) {
  const result = { browser: "brave", runtime: "", headed: false, keep: false };
  for (let index = 0; index < args.length; index++) {
    if (args[index] === "--browser") result.browser = args[++index];
    else if (args[index] === "--runtime") result.runtime = path.resolve(args[++index]);
    else if (args[index] === "--extension") result.extension = path.resolve(args[++index]);
    else if (args[index] === "--headed") result.headed = true;
    else if (args[index] === "--keep") result.keep = true;
    else throw new Error(`unknown argument ${args[index]}`);
  }
  return result;
}
function rootContext() {
  return {
    objective: { name: "root", description: "browser campaign" },
    functional: { role: "root" }, subjective: {},
    brique: {
      children_list: [], ctx_ext_name: "root", ctx_name: "root", ctx_type: "root", ctx_version: "1",
      ui_config: { root_view: "RootView", adapter: "react", name: "main" },
      engine_config: { communication: { websocket_listener: { addr: "127.0.0.1:1" }, browser_bridge: { enabled: true } },
        execution: {}, reflexive: {}, trace: {} },
      engine_version: "0.0.1", type: "local",
    },
  };
}

