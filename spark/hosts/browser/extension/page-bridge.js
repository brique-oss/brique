/* Copyright 2026 Nicolas Cassan. Licensed under Apache-2.0. */

(() => {
  if (globalThis.__SPARK_TRANSPORT_BRIDGE__) return;
  const marker = "brique.browser.page/1";
  const subscribers = new Set();
  const stateSubscribers = new Set();
  const pending = new Map();
  let relayReady = false;
  let openRequested = false;
  let openSent = false;
  let connected = false;
  let closed = false;
  let nextRequest = 1;

  const toExtension = (message) => window.postMessage({ marker, direction: "page", message }, location.origin);
  const publishState = (state) => { for (const callback of stateSubscribers) callback(state); };
  const failPending = () => {
    for (const entry of pending.values()) entry.reject(new Error("Brique browser bridge disconnected"));
    pending.clear();
  };
  const descriptor = async () => {
    const embedded = document.querySelector('meta[name="brique-instance-descriptor"]')?.content;
    if (embedded) return embedded;
    const link = document.querySelector('link[rel="brique-instance-descriptor"]')?.href;
    if (!link) return undefined;
    const target = new URL(link, location.href);
    if (target.origin !== location.origin) throw new Error("Brique descriptor must be same-origin");
    const response = await fetch(target, { cache: "no-store", credentials: "same-origin", redirect: "error" });
    if (response.status === 404) return undefined;
    if (!response.ok) throw new Error("Brique descriptor unavailable");
    if (!response.body) throw new Error("Brique descriptor has no body");
    const reader = response.body.getReader();
    const chunks = [];
    let size = 0;
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      size += value.byteLength;
      if (size > (64 << 10)) { await reader.cancel(); throw new Error("Invalid Brique descriptor size"); }
      chunks.push(value);
    }
    if (size === 0) throw new Error("Invalid Brique descriptor size");
    const bytes = new Uint8Array(size);
    let cursor = 0;
    for (const chunk of chunks) { bytes.set(chunk, cursor); cursor += chunk.byteLength; }
    let binary = "";
    for (let offset = 0; offset < bytes.length; offset += 0x8000) binary += String.fromCharCode(...bytes.subarray(offset, offset + 0x8000));
    return btoa(binary).replace(/\+/gu, "-").replace(/\//gu, "_").replace(/=+$/u, "");
  };
  const requestOpen = async () => {
    if (!relayReady || !openRequested || openSent || connected || closed) return;
    openSent = true;
    const uiName = document.documentElement.dataset.briqueUiName || "root";
    try {
      const embedded = document.querySelector('meta[name="brique-instance-descriptor"]')?.content;
      const linked = document.querySelector('link[rel="brique-instance-descriptor"]');
      // A linked descriptor is renewed by the server: ask the relay to have it fetched
      // after the trusted click instead of holding one that will have expired.
      if (!embedded && linked) toExtension({ kind: "open", uiName, refreshDescriptor: true });
      else toExtension({ kind: "open", uiName, descriptorB64: await descriptor() });
    } catch { openSent = false; publishState("disconnected"); }
  };
  const answerDescriptorRequest = async () => {
    try { toExtension({ kind: "descriptor", descriptorB64: await descriptor() }); }
    catch { openSent = false; publishState("disconnected"); }
  };

  addEventListener("message", (event) => {
    if (event.source !== window || event.origin !== location.origin || event.data?.marker !== marker ||
        event.data?.direction !== "extension") return;
    const message = event.data.message;
    if (message?.kind === "relay-ready") { relayReady = true; void requestOpen(); }
    else if (message?.kind === "descriptor-request") { void answerDescriptorRequest(); }
    else if (message?.kind === "open-result") {
      connected = message.ok === true;
      publishState(connected ? "connected" : "disconnected");
    } else if (message?.kind === "send-result" && Number.isSafeInteger(message.requestId)) {
      const entry = pending.get(message.requestId);
      if (entry) { pending.delete(message.requestId); message.ok === true ? entry.resolve() : entry.reject(new Error("Brique message refused")); }
    } else if (message?.kind === "event" && typeof message.payloadB64 === "string") {
      try {
        const standard = message.payloadB64.replace(/-/gu, "+").replace(/_/gu, "/") + "=".repeat((4 - message.payloadB64.length % 4) % 4);
        const serialized = new TextDecoder("utf-8", { fatal: true }).decode(Uint8Array.from(atob(standard), (value) => value.charCodeAt(0)));
        for (const callback of subscribers) callback(serialized);
      } catch {}
    } else if (message?.kind === "disconnected") {
      connected = false; closed = true; failPending(); publishState("disconnected");
    }
  });

  globalThis.__SPARK_TRANSPORT_BRIDGE__ = {
    open() { if (!closed) { openRequested = true; void requestOpen(); } },
    send(serialized) {
      if (!connected || closed || typeof serialized !== "string") return Promise.reject(new Error("Brique browser bridge is not connected"));
      if (pending.size >= 32) return Promise.reject(new Error("Brique browser bridge is saturated"));
      const requestId = nextRequest++;
      return new Promise((resolve, reject) => { pending.set(requestId, { resolve, reject }); toExtension({ kind: "send", requestId, serialized }); });
    },
    subscribe(callback) { subscribers.add(callback); return () => subscribers.delete(callback); },
    subscribeToConnectionState(callback) { stateSubscribers.add(callback); callback(connected ? "connected" : "disconnected"); return () => stateSubscribers.delete(callback); },
    close() { if (!closed) { closed = true; toExtension({ kind: "close" }); failPending(); publishState("disconnected"); } },
  };
})();
