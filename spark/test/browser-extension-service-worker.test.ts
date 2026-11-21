/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import vm from "node:vm";

import { attachPagePort } from "../../browser-extension/service-worker.js";

class MockEvent<T extends (...args: any[]) => void> {
  listeners: T[] = [];
  addListener(listener: T): void { this.listeners.push(listener); }
  emit(...args: Parameters<T>): void { for (const listener of this.listeners) listener(...args); }
}

class MockPort {
  name = "brique.browser.bridge/1";
  sender: Record<string, unknown> | undefined;
  sent: unknown[] = [];
  disconnected = false;
  onMessage = new MockEvent<(message: any) => void>();
  onDisconnect = new MockEvent<() => void>();
  constructor(sender?: Record<string, unknown>) { this.sender = sender; }
  postMessage(message: unknown): void { this.sent.push(message); }
  disconnect(): void {
    if (this.disconnected) return;
    this.disconnected = true;
    this.onDisconnect.emit();
  }
}

const session = "bbs1_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA";
const channel = "bbc1_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB";
const sender = { origin: "https://ui.example", documentId: "01234567-89ab-cdef-0123-456789abcdef", frameId: 3 };

function opened(): { page: MockPort; native: MockPort } {
  const page = new MockPort(sender);
  const native = new MockPort();
  let connections = 0;
  attachPagePort(page as any, (name) => {
    assert.equal(name, "com.brique.browser");
    connections++;
    return native as any;
  });
  assert.equal(connections, 0);
  assert.equal(native.sent.length, 0);
  page.onMessage.emit({ kind: "open", uiName: "root", descriptorB64: "e30" });
  assert.equal(connections, 1);
  assert.deepEqual(native.sent.shift(), { format: "brique.browser.bridge/1", kind: "hello", sequence: 1 });
  native.onMessage.emit({ format: "brique.browser.bridge/1", kind: "hello", sequence: 1, ok: true,
    session_id: session, gateway_alias: "alpha", gateway_revision: 1 });
  assert.deepEqual(page.sent.shift(), { kind: "ready" });
  assert.deepEqual(native.sent.shift(), {
    format: "brique.browser.bridge/1", kind: "open", sequence: 2, session_id: session,
    origin: sender.origin, document_id: sender.documentId, frame_id: sender.frameId,
    ui_name: "root", user_activated: true, descriptor_b64: "e30",
  });
  return { page, native };
}

test("service worker replaces page authority with Chrome sender coordinates", () => {
  const { page, native } = opened();
  native.onMessage.emit({ format: "brique.browser.bridge/1", kind: "event", sequence: 1,
    session_id: session, channel_id: channel, payload_b64: "ZWFybHk" });
  assert.equal(page.sent.length, 0);
  native.onMessage.emit({ format: "brique.browser.bridge/1", kind: "open", sequence: 2, ok: true,
    session_id: session, channel_id: channel });
  assert.deepEqual(page.sent.shift(), { kind: "open-result", ok: true });
  assert.deepEqual(page.sent.shift(), { kind: "event", payloadB64: "ZWFybHk" });

  page.onMessage.emit({ kind: "send", requestId: 7, serialized: "intention" });
  assert.deepEqual(native.sent.shift(), { format: "brique.browser.bridge/1", kind: "message", sequence: 3,
    session_id: session, channel_id: channel, payload_b64: "aW50ZW50aW9u" });
  native.onMessage.emit({ format: "brique.browser.bridge/1", kind: "message", sequence: 3, ok: true,
    session_id: session, channel_id: channel });
  assert.deepEqual(page.sent.shift(), { kind: "send-result", requestId: 7, ok: true });
  native.onMessage.emit({ format: "brique.browser.bridge/1", kind: "event", sequence: 1,
    session_id: session, channel_id: channel, payload_b64: "cmVzcG9uc2U" });
  assert.deepEqual(page.sent.shift(), { kind: "event", payloadB64: "cmVzcG9uc2U" });
});

test("service worker refuses HTTPS without descriptor and tears down both ports", () => {
  const page = new MockPort(sender);
  const native = new MockPort();
  let connections = 0;
  attachPagePort(page as any, () => { connections++; return native as any; });
  page.onMessage.emit({ kind: "open", uiName: "root" });
  assert.equal(connections, 0);
  assert.equal(native.disconnected, false);
  assert.deepEqual(page.sent, [{ kind: "disconnected" }]);
});

test("service worker does not start the native host before an open request", () => {
  const page = new MockPort(sender);
  const native = new MockPort();
  let connections = 0;
  attachPagePort(page as any, () => { connections++; return native as any; });
  assert.equal(connections, 0);
  page.disconnect();
  assert.equal(connections, 0);
  assert.equal(native.disconnected, false);
});

test("content relay releases open only on trusted active user input", () => {
  const port = new MockPort();
  const listeners = new Map<string, Array<(event: any) => void>>();
  const pageWindow = { postMessage() {} };
  const navigator = { userActivation: { isActive: false } };
  const sandbox = {
    chrome: { runtime: { connect: () => port } },
    window: pageWindow,
    location: { origin: "http://127.0.0.1:8080" },
    navigator,
    addEventListener: (name: string, listener: (event: any) => void) => {
      const entries = listeners.get(name) ?? [];
      entries.push(listener);
      listeners.set(name, entries);
    },
  };
  vm.runInNewContext(readFileSync(new URL("../../browser-extension/content-relay.js", import.meta.url), "utf8"), sandbox);
  const emit = (name: string, event: any) => { for (const listener of listeners.get(name) ?? []) listener(event); };
  emit("message", { source: pageWindow, origin: sandbox.location.origin, data: {
    marker: "brique.browser.page/1", direction: "page", message: { kind: "open", uiName: "root" },
  } });
  assert.equal(port.sent.length, 0);
  emit("pointerdown", { isTrusted: false });
  assert.equal(port.sent.length, 0);
  navigator.userActivation.isActive = true;
  emit("pointerdown", { isTrusted: true });
  assert.deepEqual(port.sent, [{ kind: "open", uiName: "root" }]);
});

function loadRelay(now: () => number) {
  const port = new MockPort();
  const listeners = new Map<string, Array<(event: any) => void>>();
  const posted: any[] = [];
  const pageWindow = { postMessage: (message: any) => { posted.push(message); } };
  const navigator = { userActivation: { isActive: true } };
  const sandbox = {
    chrome: { runtime: { connect: () => port } }, window: pageWindow, location: { origin: "http://127.0.0.1:8080" }, navigator,
    Date: { now },
    addEventListener: (name: string, listener: (event: any) => void) => {
      const entries = listeners.get(name) ?? [];
      entries.push(listener);
      listeners.set(name, entries);
    },
  };
  vm.runInNewContext(readFileSync(new URL("../../browser-extension/content-relay.js", import.meta.url), "utf8"), sandbox);
  const emit = (name: string, event: any) => { for (const listener of listeners.get(name) ?? []) listener(event); };
  const fromPage = (message: any) => emit("message", { source: pageWindow, origin: sandbox.location.origin,
    data: { marker: "brique.browser.page/1", direction: "page", message } });
  return { port, posted, emit, fromPage };
}

test("content relay asks the page for a fresh descriptor only after a trusted click and rejects a late answer", () => {
  let clock = 1000;
  const relay = loadRelay(() => clock);
  const requests = () => relay.posted.filter((entry) => entry.message?.kind === "descriptor-request").length;
  relay.fromPage({ kind: "open", uiName: "root", refreshDescriptor: true });
  relay.emit("pointerdown", { isTrusted: false });
  assert.equal(requests(), 0, "a synthetic event asked for a descriptor");
  relay.emit("pointerdown", { isTrusted: true });
  assert.equal(requests(), 1);
  assert.equal(relay.port.sent.length, 0, "the open was released before the descriptor arrived");

  clock += 5000; // past the window: this answer belongs to a click that is gone
  relay.fromPage({ kind: "descriptor", descriptorB64: "AAAA" });
  assert.equal(relay.port.sent.length, 0, "a descriptor answered outside the window released the open");

  relay.emit("pointerdown", { isTrusted: true });
  assert.equal(requests(), 2, "a new trusted click must ask again");
  clock += 100;
  relay.fromPage({ kind: "descriptor", descriptorB64: "BBBB" });
  assert.deepEqual(JSON.parse(JSON.stringify(relay.port.sent)), [{ kind: "open", uiName: "root", descriptorB64: "BBBB" }],
    "the open must carry the freshly fetched descriptor and not the refresh flag");
  relay.fromPage({ kind: "descriptor", descriptorB64: "CCCC" });
  assert.equal(relay.port.sent.length, 1, "a second descriptor answer opened another channel");
});

test("content relay opens without a descriptor request when the page has no descriptor to renew", () => {
  const relay = loadRelay(() => 1000);
  relay.fromPage({ kind: "open", uiName: "root" });
  relay.emit("pointerdown", { isTrusted: true });
  assert.deepEqual(relay.port.sent, [{ kind: "open", uiName: "root" }]);
  assert.equal(relay.posted.filter((entry) => entry.message?.kind === "descriptor-request").length, 0);
});

test("page bridge fetches a linked descriptor only when the relay asks for it", async () => {
  const posted: any[] = [];
  const listeners: Array<(event: any) => void> = [];
  let fetches = 0;
  const bytes = new TextEncoder().encode('{"format":"brique.instance-descriptor/1"}');
  const pageWindow: any = { postMessage: (message: any) => { posted.push(message); } };
  const sandbox: any = {
    window: pageWindow, location: { origin: "https://brique.example", href: "https://brique.example/" },
    document: {
      documentElement: { dataset: { briqueUiName: "root" } },
      querySelector: (selector: string) => selector.startsWith("link") ? { href: "/.well-known/brique/instance-descriptor" } : null,
    },
    addEventListener: (_name: string, listener: (event: any) => void) => { listeners.push(listener); },
    fetch: async () => {
      fetches++;
      let done = false;
      return { status: 200, ok: true, body: { getReader: () => ({ read: async () => done ? { done: true } : (done = true, { done: false, value: bytes }) }) } };
    },
    URL, TextEncoder, TextDecoder, Uint8Array, btoa, atob, Error, Promise, Map, Set, String, Number, Math, JSON,
  };
  sandbox.globalThis = sandbox;
  vm.runInNewContext(readFileSync(new URL("../../browser-extension/page-bridge.js", import.meta.url), "utf8"), sandbox);
  const deliver = (message: any) => { for (const listener of listeners) listener({ source: pageWindow, origin: "https://brique.example",
    data: { marker: "brique.browser.page/1", direction: "extension", message } }); };
  sandbox.__SPARK_TRANSPORT_BRIDGE__.open();
  deliver({ kind: "relay-ready" });
  await new Promise((resolve) => setTimeout(resolve, 10));
  assert.equal(fetches, 0, "the descriptor was fetched before any click");
  assert.deepEqual(JSON.parse(JSON.stringify(posted.map((entry) => entry.message))), [{ kind: "open", uiName: "root", refreshDescriptor: true }]);
  deliver({ kind: "descriptor-request" });
  await new Promise((resolve) => setTimeout(resolve, 10));
  assert.equal(fetches, 1);
  const answer = posted.at(-1).message;
  assert.equal(answer.kind, "descriptor");
  assert.match(answer.descriptorB64, /^[A-Za-z0-9_-]+$/);
});
