"use strict";

// The token comes in the URL fragment the first time (`brique-launcher ui` prints it) and is then
// kept in this browser, so the plain address works as a bookmark. It is removed from the address bar.
let token = new URLSearchParams(location.hash.slice(1)).get("token") || "";
try {
  if (token) localStorage.setItem("brique-launcher-token", token);
  else token = localStorage.getItem("brique-launcher-token") || "";
} catch (_) { /* storage unavailable: the token stays in memory only */ }
history.replaceState(null, "", location.pathname);

const STATE_LABELS = {
  stopped: "stopped", resolving_runtime: "preparing", starting_engine: "starting the engine",
  waiting_engine: "waiting for the engine", starting_web: "starting the web host", ready: "ready",
  stopping: "stopping", failed: "failed", interrupted: "interrupted",
};
const BUSY = new Set(["resolving_runtime", "starting_engine", "waiting_engine", "starting_web", "stopping"]);

const $ = (id) => document.getElementById(id);
let logsAlias = "";
let stopped = false;           // set once everything was stopped: the page no longer asks anything

async function api(path, method = "GET", body) {
  const response = await fetch(path, {
    method,
    headers: { Authorization: "Bearer " + token, ...(body ? { "Content-Type": "application/json" } : {}) },
    body: body ? JSON.stringify(body) : undefined,
  });
  const data = await response.json().catch(() => ({}));
  if (response.status === 401) {
    try { localStorage.removeItem("brique-launcher-token"); } catch (_) { /* ignore */ }
    throw new Error("Token missing or refused: run \"brique-launcher ui\" to open the page with a valid token.");
  }
  if (!response.ok) throw new Error(data.error || "error " + response.status);
  return data;
}

function say(text, isError) {
  const node = $("message");
  node.textContent = text || "";
  node.hidden = !text;
  node.className = "message" + (isError ? " error" : "");
}

function cell(row, content) {
  const td = document.createElement("td");
  if (content instanceof Node) td.appendChild(content); else td.textContent = content;
  row.appendChild(td);
  return td;
}

function actionButton(label, handler, disabled) {
  const button = document.createElement("button");
  button.textContent = label;
  button.disabled = !!disabled;
  button.addEventListener("click", async () => {
    button.disabled = true;
    try { await handler(); } catch (error) { say(error.message, true); }
    refresh();
    refreshBrowser();
  });
  return button;
}

let refreshing = false;
async function refresh() {
  if (refreshing || stopped) return;
  refreshing = true;
  try { await draw(); } finally { refreshing = false; }
}

async function draw() {
  let data;
  try { data = await api("/api/instances"); } catch (error) { say(error.message, true); return; }
  const create = data.create || {};
  $("create-reason").textContent = create.available ? "" : "Creation unavailable: " + (create.reason || "");
  $("create-submit").disabled = !create.available;
  if (create.parent && !$("create-parent").value) $("create-parent").value = create.parent;
  const supervisor = $("supervisor");
  supervisor.textContent = data.supervisor_up ? "supervisor running" : "supervisor stopped";
  supervisor.className = "badge " + (data.supervisor_up ? "ok" : "bad");
  const body = $("instances");
  body.replaceChildren();
  $("empty").hidden = data.instances.length > 0;
  for (const instance of data.instances) {
    const row = document.createElement("tr");
    cell(row, instance.alias);
    const foreign = instance.state === "ready" && !instance.owned;
    const state = cell(row, (STATE_LABELS[instance.state] || instance.state) + (foreign ? " (started outside the launcher)" : ""));
    state.className = instance.state === "ready" ? "ok" : instance.state === "failed" ? "bad" : "";
    if (instance.error) state.title = instance.error;
    cell(row, webCell(instance, data));
    const auto = document.createElement("label");
    auto.className = "inline";
    const box = document.createElement("input");
    box.type = "checkbox"; box.checked = !!instance.autostart;
    box.addEventListener("change", async () => {
      try { await api(`/api/instances/${instance.alias}/autostart`, "POST", { enabled: box.checked }); }
      catch (error) { box.checked = !box.checked; say(error.message, true); }
    });
    auto.append(box, " start");
    cell(row, auto);
    const actions = document.createElement("span");
    const up = data.supervisor_up, ready = instance.state === "ready", busy = BUSY.has(instance.state);
    actions.append(
      actionButton("Start", () => api(`/api/instances/${instance.alias}/start`, "POST", {}), !up || ready || busy),
      actionButton("Stop", () => api(`/api/instances/${instance.alias}/stop`, "POST", {}), !up || instance.state === "stopped" || busy || foreign),
      actionButton("Restart", () => api(`/api/instances/${instance.alias}/restart`, "POST", {}), !up || busy || foreign),
      actionButton("Logs", async () => { logsAlias = instance.alias; await showLogs(); }, false),
    );
    cell(row, actions);
    const mcp = cell(row, "");
    mcp.className = "mcp-cell";
    body.appendChild(row);
    if (ready) {
      renderMcp(mcp, instance.alias);       // from the last answer, so the cell does not blink
      refreshMcp(instance.alias, mcp);
    } else {
      mcpCache.delete(instance.alias);
      mcp.textContent = "—";
      mcp.title = "Start the instance to enable its MCP.";
    }
  }
  if (logsAlias) showLogs();
}

async function showLogs() {
  try {
    const data = await api(`/api/instances/${logsAlias}/logs`);
    $("logs-alias").textContent = logsAlias;
    $("logs").textContent = data.lines.length ? data.lines.join("\n") : "(no log)";
    $("logs-section").hidden = false;
  } catch (error) { say(error.message, true); }
}

const PUBLICATION_LABELS = { none: "not published", current: "published", obsolete: "publication out of date", unknown: "published (unknown origin)" };
const PUBLISH_STEPS = { preparation: "preparation", build: "building the pages", stop: "stopping the instance", runtime: "runtime package", associate: "association", start: "restart" };

// The web side of an instance: whether its interfaces are published (and up to date), the button
// that publishes them all, and the button that opens its home page once it is served.
function webCell(instance, data) {
  const box = document.createElement("div");
  const state = document.createElement("div");
  state.textContent = PUBLICATION_LABELS[instance.publication] || instance.publication;
  const changed = instance.changed_interfaces || [];
  if (changed.length) state.textContent += " : " + changed.map((name) => name.replace(/^\/root\//, "")).join(", ");
  state.className = instance.publication === "obsolete" ? "warn" : instance.publication === "current" ? "ok" : "muted";
  box.appendChild(state);
  const job = instance.publishing || {};
  const running = job.state === "running";
  if (running) {
    const progress = document.createElement("div");
    progress.className = "muted";
    progress.textContent = "In progress: " + (PUBLISH_STEPS[job.step] || job.step) + "…";
    box.appendChild(progress);
  } else if (job.state === "failed") {
    const failure = document.createElement("div");
    failure.className = "bad";
    failure.textContent = "Failed (" + (PUBLISH_STEPS[job.step] || job.step) + "): " + job.message;
    box.appendChild(failure);
  }
  const tools = data.publish_tools || {};
  const publish = actionButton(instance.publication === "none" ? "Publish" : "Republish", async () => {
    const wasUp = instance.state === "ready";
    const unchanged = instance.publication === "current";
    if (unchanged && !confirm("No interface changed since the last publication. Republish anyway?")) return;
    if (wasUp && !confirm("The instance will be stopped and restarted to publish. Continue?")) return;
    await api(`/api/instances/${instance.alias}/publish`, "POST", { force: unchanged });
  }, !data.supervisor_up || running || !tools.available);
  if (!tools.available) publish.title = tools.reason || "";
  box.appendChild(publish);
  if (instance.web_url) {
    const open = document.createElement("button");
    open.textContent = "Open";
    open.addEventListener("click", () => window.open(instance.web_url, "_blank", "noopener"));
    box.appendChild(open);
  }
  return box;
}

const MCP_LABELS = { absent: "absent", stopped: "stopped", starting: "starting…", running: "running", error: "error" };
const MCP_NAMES = { reader: "reader", architect: "architect" };
const mcpCache = new Map(); // alias -> { states } or { error }

const MCP_POLL_MS = 10000; // each read opens a short connection to the engine: not every 3 s

async function refreshMcp(alias, cellNode) {
  const known = mcpCache.get(alias);
  const starting = known && known.states && known.states.some((s) => s.state === "starting");
  if (known && !known.stale && !starting && Date.now() - known.at < MCP_POLL_MS) return;
  try { mcpCache.set(alias, { states: await api(`/api/instances/${alias}/mcp`), at: Date.now() }); }
  catch (error) { mcpCache.set(alias, { error: error.message, at: Date.now() }); }
  if (cellNode.isConnected) renderMcp(cellNode, alias);
}

// One line per profile, in the row of the instance: its state and one button.
function renderMcp(cellNode, alias) {
  const known = mcpCache.get(alias);
  cellNode.replaceChildren();
  if (!known) { cellNode.textContent = "…"; return; }
  if (known.error) {
    const note = document.createElement("span");
    note.className = "muted"; note.textContent = known.error;
    cellNode.appendChild(note);
    return;
  }
  for (const state of known.states) {
    if (state.state === "absent") continue; // this instance does not have that profile
    const line = document.createElement("div");
    line.className = "mcp-line";
    const label = document.createElement("span");
    label.textContent = `MCP ${MCP_NAMES[state.name] || state.name}: ${MCP_LABELS[state.state] || state.state} `;
    label.className = state.state === "running" ? "ok" : state.state === "error" ? "bad" : "";
    label.title = (state.error ? state.error + " — " : "") + state.rights;
    line.appendChild(label);
    const running = state.state === "running";
    const toggle = running ? "stop" : "start";
    line.appendChild(actionButton(running ? "Stop" : "Enable", async () => {
      if (toggle === "start" && state.needs_confirmation &&
          !confirm("The architect profile gives the connected agent every right on the instance. Continue?")) return;
      await api(`/api/instances/${alias}/mcp`, "POST", { profile: state.name, action: toggle, confirmed: true });
      const stale = mcpCache.get(alias);
      if (stale) stale.stale = true; // read the new state at the next refresh
    }, state.state === "starting"));
    if (running) {
      const details = document.createElement("details");
      const summary = document.createElement("summary");
      summary.textContent = "Connect";
      const entry = document.createElement("code");
      entry.textContent = JSON.stringify({ mcpServers: { [`brique-${alias}`]: { type: "sse", url: state.url } } });
      details.append(summary, entry);
      line.appendChild(details);
    }
    cellNode.appendChild(line);
  }
}


// ---- Browser extension -------------------------------------------------------------------------

async function copyText(text) {
  try { await navigator.clipboard.writeText(text); say("Copied.", false); }
  catch (_) { say("Copy failed: select the path by hand.", true); }
}

// ---- Browser extension -------------------------------------------------------------------------
// One line per browser: the instances its extension vouches for, the one that carries the identity,
// and what can be done. The launcher prepares the extension and registers the native host by itself.

let showAllBrowsers = false;
// The browser the launcher page is open in, as the launcher names it (null when it cannot tell).
async function currentBrowser() {
  const agent = navigator.userAgent;
  if (/Firefox\//.test(agent)) return "firefox";
  if (/Edg\/|OPR\//.test(agent)) return null;
  if (navigator.brave && await navigator.brave.isBrave().catch(() => false)) return "brave";
  if (/Chrome\//.test(agent)) return "chrome";
  return null;
}

const browserDraft = {};      // what the person is choosing, kept while the table is redrawn

async function refreshBrowser() {
  if (stopped) return;
  const table = $("browsers");
  // Do not rebuild the table while the person is choosing in it.
  if (table.contains(document.activeElement) && ["INPUT", "SELECT"].includes(document.activeElement.tagName)) return;
  let section;
  try { section = await api("/api/browser"); } catch (error) { say(error.message, true); return; }
  const body = table.querySelector("tbody");
  body.replaceChildren();
  // Only the browser the page is open in is shown; the others are one click away.
  const current = await currentBrowser();
  const shown = section.browsers.filter((browser) => showAllBrowsers || !current || browser.name === current || browser.associated);
  const hidden = section.browsers.length - shown.length;
  $("other-browsers").hidden = hidden === 0 && !showAllBrowsers;
  $("other-browsers").textContent = showAllBrowsers ? "Show only this browser" : "Other browsers (" + hidden + ")";
  for (const browser of shown) {
    const row = document.createElement("tr");
    cell(row, browser.label + (browser.installed ? "" : " (not detected)"));

    const draft = browserDraft[browser.name] ??= { instances: new Set(browser.instances), identity: browser.identity || "" };
    const choice = document.createElement("div");
    if (!section.instances.length) {
      choice.className = "muted";
      choice.textContent = "No instance uses a runtime package (npm run setup).";
    }
    for (const alias of section.instances) {
      const line = document.createElement("div");
      line.className = "mcp-line";
      const box = document.createElement("input");
      box.type = "checkbox"; box.checked = draft.instances.has(alias);
      const identity = document.createElement("input");
      identity.type = "radio"; identity.name = "identity-" + browser.name; identity.checked = draft.identity === alias;
      identity.disabled = !box.checked;            // only an associated instance can carry the identity
      box.addEventListener("change", () => {
        if (box.checked) draft.instances.add(alias); else { draft.instances.delete(alias); if (draft.identity === alias) { draft.identity = ""; identity.checked = false; } }
        identity.disabled = !box.checked;
      });
      identity.addEventListener("change", () => { draft.identity = alias; });
      const label = document.createElement("label");
      label.className = "inline";
      label.append(box, " " + alias + " ");
      const who = document.createElement("label");
      who.className = "inline muted";
      who.append(identity, " identity");
      line.append(label, who);
      choice.appendChild(line);
    }
    if (browser.associated) {
      const folder = document.createElement("div");
      folder.className = "muted";
      folder.append("Folder to load: ");
      const path = document.createElement("code");
      path.textContent = browser.folder;
      const copy = document.createElement("button");
      copy.textContent = "Copy";
      copy.addEventListener("click", () => copyText(browser.folder));
      folder.append(path, " ", copy);
      choice.appendChild(folder);
    }
    cell(row, choice);

    const actions = document.createElement("span");
    actions.appendChild(actionButton(browser.associated ? "Update" : "Associate", async () => {
      const instances = [...draft.instances].filter((alias) => section.instances.includes(alias));
      await api("/api/browser/associate", "POST", { browser: browser.name, instances, identity: draft.identity && instances.includes(draft.identity) ? draft.identity : "" });
      delete browserDraft[browser.name];
      say("Extension of " + browser.label + " associated. Load it from the folder shown, or reload it in the browser if it is already loaded.", false);
    }, !section.instances.length));
    actions.appendChild(actionButton("Dissociate", async () => {
      await api("/api/browser/dissociate", "POST", { browser: browser.name });
      delete browserDraft[browser.name];
    }, !browser.associated));
    actions.appendChild(actionButton("Open the extensions page", () => api("/api/browser/open", "POST", { browser: browser.name, target: "extensions" }), !browser.installed));
    for (const alias of browser.instances) {
      actions.appendChild(actionButton("Open " + alias, () => api("/api/browser/open", "POST", { browser: browser.name, target: "instance", alias }), !browser.installed));
    }
    cell(row, actions);
    body.appendChild(row);
  }
  table.hidden = false;
}

$("create-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const form = new FormData(event.target);
  const submit = $("create-submit");
  submit.disabled = true;
  say("Creating the instance (the Python environment is built, which can take a minute)…", false);
  try {
    const created = await api("/api/create-instance", "POST", { name: form.get("name"), parent: form.get("parent") });
    event.target.reset();
    $("create-details").open = false;
    const warnings = created.warnings || [];
    say(`Instance "${created.alias}" created in ${created.root}. You can start it.` + (warnings.length ? " " + warnings.join(" ") : ""), warnings.length > 0);
  } catch (error) { say(error.message, true); }
  submit.disabled = false;
  refresh();
});

$("add-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const form = new FormData(event.target);
  try {
    await api("/api/instances", "POST", {
      alias: form.get("alias"), root: form.get("root"), runtime: form.get("runtime"), engine: form.get("engine"),
      autostart: form.get("autostart") === "on",
    });
    event.target.reset();
    say("Instance registered.", false);
  } catch (error) { say(error.message, true); }
  refresh();
});

$("diagnose-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const form = new FormData(event.target);
  const table = $("diagnosis");
  const submit = event.target.querySelector("button");
  submit.disabled = true;
  say("Diagnosis in progress…", false);
  try {
    const result = await api("/api/diagnose", "POST", { address: form.get("address") });
    const body = table.querySelector("tbody");
    body.replaceChildren();
    for (const step of result.steps || []) {
      const row = document.createElement("tr");
      cell(row, step.name);
      cell(row, step.status).className = step.status;
      cell(row, step.duration_ms !== undefined ? step.duration_ms + " ms" : "");
      cell(row, step.code || "");
      let detail = step.status === "ok" ? "" : step.message || "";
      if (step.status === "skipped") {
        if (detail.includes("not implemented")) detail = "not available yet";
        else if (detail.includes("earlier step")) detail = "earlier step failed";
      }
      cell(row, detail);
      body.appendChild(row);
    }
    table.hidden = false;
    say(result.ok ? "The peer answers. (Its identity and signature are not verified.)" : "A step failed: see the table.", !result.ok);
  } catch (error) { table.hidden = true; say(error.message, true); }
  submit.disabled = false;
});

refresh();
refreshBrowser();
setInterval(refresh, 3000);
setInterval(refreshBrowser, 8000);

$("other-browsers").addEventListener("click", () => { showAllBrowsers = !showAllBrowsers; refreshBrowser(); });

// Stops everything Brique that this launcher runs: its instances, then the launcher, which serves this page.
$("stop-all").addEventListener("click", async () => {
  let running = [];
  try { running = (await api("/api/instances")).instances.filter((i) => i.state !== "stopped" && i.owned).map((i) => i.alias); } catch (_) { /* the confirmation still makes sense */ }
  const what = running.length ? "These instances will be stopped: " + running.join(", ") + ".\n\n" : "";
  if (!confirm(what + "The launcher will stop too, and so will this page. To use Brique again, start the launcher from its icon (or npm run launcher). Stop everything?")) return;
  $("stop-all").disabled = true;
  say("Stopping…", false);
  try {
    await api("/api/shutdown", "POST", {});
  } catch (error) { say(error.message, true); $("stop-all").disabled = false; return; }
  stopped = true;
  document.querySelector("main").replaceChildren(Object.assign(document.createElement("p"), {
    className: "message",
    textContent: "Brique is stopping" + (running.length ? " (" + running.join(", ") + ")" : "") + ". You can close this page. To start again, open the launcher from its icon, or run npm run launcher.",
  }));
  $("supervisor").textContent = "stopped"; $("supervisor").className = "badge bad";
});
