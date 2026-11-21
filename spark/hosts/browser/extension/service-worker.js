/* Copyright 2026 Nicolas Cassan. Licensed under Apache-2.0. */

import { BRIDGE_FORMAT, NATIVE_HOST, RELAY_NAME, canonicalSender, encodeUTF8, parseHostMessage, parsePageRequest } from "./protocol.js";

const MAX_PENDING_REQUESTS = 32;

export function attachPagePort(pagePort, connectNative) {
  if (pagePort.name !== RELAY_NAME) return pagePort.disconnect();
  const sender = canonicalSender(pagePort.sender);
  if (!sender) return pagePort.disconnect();

  let nativePort;
  let sequence = 1;
  let sessionId = "";
  let channelId = "";
  let closed = false;
  let pendingOpen;
  const earlyEvents = [];
  const pending = new Map();

  const shutdown = () => {
    if (closed) return;
    closed = true;
    try { nativePort?.disconnect(); } catch {}
    try { pagePort.postMessage({ kind: "disconnected" }); } catch {}
    pending.clear();
    earlyEvents.length = 0;
  };

  const sendOpen = () => {
    if (!nativePort || !sessionId || !pendingOpen || [...pending.values()].some((entry) => entry.kind === "open")) return;
    const request = pendingOpen;
    pendingOpen = undefined;
    const current = ++sequence;
    pending.set(current, { kind: "open" });
    nativePort.postMessage({ format: BRIDGE_FORMAT, kind: "open", sequence: current,
      session_id: sessionId, origin: sender.origin, document_id: sender.documentId, frame_id: sender.frameId,
      ui_name: request.uiName, user_activated: true,
      ...(request.descriptorB64 === undefined ? {} : { descriptor_b64: request.descriptorB64 }) });
  };
  // Merely injecting the extension into a page must not start a native
  // process. content-relay releases `open` only after trusted user input;
  // that request is the first point at which the native rendez-vous exists.
  const startNative = () => {
    if (nativePort || closed) return;
    try { nativePort = connectNative(NATIVE_HOST); } catch { return shutdown(); }
    pending.set(1, { kind: "hello" });
    nativePort.onMessage.addListener((raw) => {
      const message = parseHostMessage(raw);
      if (!message) return shutdown();
      if (message.kind === "event") {
        if (message.session_id !== sessionId) return shutdown();
        if (!channelId && [...pending.values()].some((entry) => entry.kind === "open") && earlyEvents.length < 8) {
          earlyEvents.push(message);
          return;
        }
        if (message.channel_id !== channelId) return shutdown();
        pagePort.postMessage({ kind: "event", payloadB64: message.payload_b64 });
        return;
      }
      const request = pending.get(message.sequence);
      if (!request) return shutdown();
      pending.delete(message.sequence);
      if (message.kind === "error") {
        pagePort.postMessage(request.kind === "send" ? { kind: "send-result", requestId: request.requestId, ok: false } : { kind: "open-result", ok: false });
        if (request.kind === "hello" || request.kind === "open") shutdown();
        return;
      }
      if (message.kind !== request.kind) return shutdown();
      if (message.kind === "hello") {
        sessionId = message.session_id;
        pagePort.postMessage({ kind: "ready" });
        sendOpen();
      } else if (message.kind === "open") {
        if (message.session_id !== sessionId) return shutdown();
        channelId = message.channel_id;
        pagePort.postMessage({ kind: "open-result", ok: true });
        for (const event of earlyEvents.splice(0)) {
          if (event.channel_id !== channelId) return shutdown();
          pagePort.postMessage({ kind: "event", payloadB64: event.payload_b64 });
        }
      } else if (message.kind === "message") {
        if (message.session_id !== sessionId || message.channel_id !== channelId) return shutdown();
        pagePort.postMessage({ kind: "send-result", requestId: request.requestId, ok: true });
      } else if (message.kind === "close") {
        channelId = "";
        shutdown();
      }
    });
    nativePort.onDisconnect.addListener(() => {
      const detail = globalThis.chrome?.runtime?.lastError?.message;
      if (detail) console.warn("Brique native host disconnected:", detail);
      shutdown();
    });
    nativePort.postMessage({ format: BRIDGE_FORMAT, kind: "hello", sequence: 1 });
  };
  pagePort.onDisconnect.addListener(shutdown);
  pagePort.onMessage.addListener((raw) => {
    const request = parsePageRequest(raw);
    if (!request || closed) return shutdown();
    if (request.kind === "open") {
      if (channelId || pendingOpen || [...pending.values()].some((entry) => entry.kind === "open")) return shutdown();
      if (sender.origin.startsWith("https:") && request.descriptorB64 === undefined) return shutdown();
      pendingOpen = request;
      startNative();
      sendOpen();
    } else if (request.kind === "send") {
      if (!nativePort || !sessionId || !channelId || pending.size >= MAX_PENDING_REQUESTS) return shutdown();
      const current = ++sequence;
      pending.set(current, { kind: "message", requestId: request.requestId });
      nativePort.postMessage({ format: BRIDGE_FORMAT, kind: "message", sequence: current,
        session_id: sessionId, channel_id: channelId, payload_b64: encodeUTF8(request.serialized) });
    } else {
      if (!nativePort || !sessionId || !channelId) return shutdown();
      const current = ++sequence;
      pending.set(current, { kind: "close" });
      nativePort.postMessage({ format: BRIDGE_FORMAT, kind: "close", sequence: current, session_id: sessionId, channel_id: channelId });
    }
  });
}

if (globalThis.chrome?.runtime?.onConnect) {
  chrome.runtime.onConnect.addListener((pagePort) => attachPagePort(pagePort, (name) => chrome.runtime.connectNative(name)));
}
