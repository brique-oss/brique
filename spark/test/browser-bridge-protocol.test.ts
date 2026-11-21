/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

import assert from "node:assert/strict";
import test from "node:test";

import {
  BROWSER_BRIDGE_PROTOCOL,
  canonicalOrigin,
  channelCommand,
  channelMessage,
  hello,
  openChannel,
  parseHostMessage,
} from "../browser/bridgeProtocol.js";

const session = "bbs1_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA";
const channel = "bbc1_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB";

test("browser bridge requests use the frozen strict schema", () => {
  assert.deepEqual(hello(1), { format: BROWSER_BRIDGE_PROTOCOL, kind: "hello", sequence: 1 });
  assert.deepEqual(openChannel({ sequence: 2, sessionId: session, origin: "https://example.test", documentId: "doc-1",
    frameId: 0, uiName: "root", userActivated: true }), {
    format: BROWSER_BRIDGE_PROTOCOL, kind: "open", sequence: 2, session_id: session,
    origin: "https://example.test", document_id: "doc-1", frame_id: 0, ui_name: "root", user_activated: true,
  });
  assert.equal(channelMessage(3, session, channel, new TextEncoder().encode("intent")).payload_b64, "aW50ZW50");
  assert.deepEqual(channelCommand("close", 4, session, channel), {
    format: BROWSER_BRIDGE_PROTOCOL, kind: "close", sequence: 4, session_id: session, channel_id: channel,
  });
});

test("browser bridge host messages reject unknown fields and malformed identifiers", () => {
  const event = parseHostMessage({ format: BROWSER_BRIDGE_PROTOCOL, kind: "event", sequence: 1,
    session_id: session, channel_id: channel, payload_b64: "YW5zd2Vy" });
  assert.equal(event.kind, "event");
  assert.throws(() => parseHostMessage({ ...event, authority: "admin" }), /field/u);
  assert.throws(() => parseHostMessage({ ...event, channel_id: "chosen" }), /event/u);
  assert.throws(() => parseHostMessage({ ...event, payload_b64: "***" }), /base64/u);
});

test("browser origin canonicalization matches the engine policy", () => {
  for (const origin of ["https://example.test", "https://example.test:8443", "http://localhost:3000", "http://127.0.0.1", "http://[::1]:8080"]) {
    assert.equal(canonicalOrigin(origin), origin);
  }
  for (const origin of ["http://example.test", "https://EXAMPLE.test", "https://example.test/", "https://example.test:443", "https://user@example.test"]) {
    assert.throws(() => canonicalOrigin(origin));
  }
});
