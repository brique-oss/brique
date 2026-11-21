/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

import {
  BRIDGE_FORMAT,
  canonicalSender,
  decodeUTF8,
  encodeUTF8,
  parseHostMessage,
  parsePageRequest,
} from "../hosts/browser/extension/protocol.js";

test("browser extension derives immutable coordinates from Chrome sender facts", () => {
  assert.deepEqual(canonicalSender({ origin: "https://ui.example", documentId: "01234567-89ab-cdef-0123-456789abcdef", frameId: 7 }), {
    origin: "https://ui.example", documentId: "01234567-89ab-cdef-0123-456789abcdef", frameId: 7,
  });
  for (const sender of [
    { origin: "http://ui.example", documentId: "doc", frameId: 0 },
    { origin: "https://UI.example", documentId: "doc", frameId: 0 },
    { origin: "https://ui.example", documentId: "doc", frameId: -1 },
    { origin: "null", documentId: "doc", frameId: 0 },
  ]) assert.equal(canonicalSender(sender), null);
});

test("browser extension sanitizes page requests and host messages", () => {
  assert.deepEqual(parsePageRequest({ kind: "open", uiName: "root" }), { kind: "open", uiName: "root" });
  assert.equal(parsePageRequest({ kind: "open", uiName: "root", userActivated: true }), null);
  assert.equal(parsePageRequest({ kind: "send", requestId: 1, serialized: "{}", authority: "admin" }), null);
  const hello = { format: BRIDGE_FORMAT, kind: "hello", sequence: 1, ok: true,
    session_id: "bbs1_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", gateway_alias: "alpha", gateway_revision: 1 };
  assert.deepEqual(parseHostMessage(hello), hello);
  assert.equal(parseHostMessage({ ...hello, key: "private" }), null);
});

test("browser extension UTF-8 payload codec is canonical and bounded", () => {
  const encoded = encodeUTF8("intention é");
  assert.equal(decodeUTF8(encoded), "intention é");
  assert.throws(() => decodeUTF8(encoded + "="));
});

test("Firefox package keeps the bridge contract with an explicit Gecko identity", () => {
  const chromium = JSON.parse(readFileSync(new URL("../hosts/browser/extension/manifest.json", import.meta.url), "utf8"));
  const firefox = JSON.parse(readFileSync(new URL("../hosts/browser/extension/manifest.firefox.json", import.meta.url), "utf8"));
  assert.equal(firefox.manifest_version, 3);
  assert.equal(firefox.browser_specific_settings.gecko.id, "brique-browser-bridge@brique.dev");
  assert.deepEqual(firefox.browser_specific_settings.gecko.data_collection_permissions, { required: ["none"] });
  assert.deepEqual(firefox.background, { scripts: ["service-worker.js"], type: "module" });
  assert.deepEqual(firefox.permissions, chromium.permissions);
  assert.deepEqual(firefox.content_scripts, chromium.content_scripts);
  assert.equal(firefox.background.service_worker, undefined);
});
