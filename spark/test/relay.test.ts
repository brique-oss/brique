/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

import assert from "node:assert/strict";
import test from "node:test";

import { LocalWebSocketRelay } from "../hosts/LocalWebSocketRelay.js";
import { createFakeWebSocketServer } from "../../pulse/test/helpers.js";
import {
  BridgeTransport,
  type SerializedTransportBridge,
  type TransportConnectionState,
} from "../../pulse/runtime/index.js";

test("LocalWebSocketRelay carries text messages without a browser Origin", async () => {
  const server = await createFakeWebSocketServer();
  const relay = new LocalWebSocketRelay(server.url);
  const states: string[] = [];
  const received: string[] = [];
  relay.subscribeToConnectionState((state) => states.push(state));
  relay.subscribe((message) => received.push(message));

  relay.open();
  await server.waitForConnection();
  await relay.send('{"kind":"response"}');
  assert.equal(await server.waitForMessage(), '{"kind":"response"}');

  server.sendText('{"kind":"intention"}');
  await waitFor(() => received.length === 1);
  assert.deepEqual(received, ['{"kind":"intention"}']);
  assert.ok(states.includes("connected"));

  relay.close();
  await server.close();
});

test("LocalWebSocketRelay does not reconnect when the route was taken by another client", async () => {
  const server = await createFakeWebSocketServer();
  const relay = new LocalWebSocketRelay(server.url, { baseRetryDelayMs: 10 });
  const states: string[] = [];
  relay.subscribeToConnectionState((state) => states.push(state));

  relay.open();
  await server.waitForConnection();
  server.closeWithCode(4001);
  await new Promise((resolve) => setTimeout(resolve, 150));

  assert.equal(states.filter((state) => state === "connected").length, 1);
  assert.equal(states[states.length - 1], "disconnected");
  relay.close();
  await server.close();
});

test("LocalWebSocketRelay retry settings: an invalid value is ignored, a valid one applies", async () => {
  const server = await createFakeWebSocketServer();
  const relay = new LocalWebSocketRelay(server.url, { maxRetries: 0, baseRetryDelayMs: 10 });
  let connections = 0;
  relay.subscribeToConnectionState((state) => { if (state === "connected") connections += 1; });
  relay.open();
  await server.waitForConnection();
  server.closeConnection();
  await new Promise((resolve) => setTimeout(resolve, 150));
  assert.equal(connections, 1, "maxRetries 0 means no reconnection");
  relay.close();
  await server.close();

  const invalid = new LocalWebSocketRelay("ws://127.0.0.1:1", { maxRetries: Number.NaN, baseRetryDelayMs: -5 });
  assert.equal((invalid as unknown as { maxRetries: number }).maxRetries, 12);
  assert.equal((invalid as unknown as { baseRetryDelayMs: number }).baseRetryDelayMs, 200);
  invalid.close();
});

test("LocalWebSocketRelay refuses non-WebSocket URLs", () => {
  assert.throws(() => new LocalWebSocketRelay("https://example.invalid"), /ws:\/\/ or wss:\/\//i);
});

test("BridgeTransport carries serialized messages across the trusted host boundary", async () => {
  let inbound: ((data: string) => void) | null = null;
  let stateInbound: ((state: TransportConnectionState) => void) | null = null;
  const sent: string[] = [];
  const bridge: SerializedTransportBridge = {
    open() {},
    async send(data) { sent.push(data); },
    subscribe(callback) { inbound = callback; return () => { inbound = null; }; },
    subscribeToConnectionState(callback) {
      stateInbound = callback;
      return () => { stateInbound = null; };
    },
    close() {},
  };
  const transport = new BridgeTransport(bridge);
  const received: string[] = [];
  const states: string[] = [];
  transport.subscribe((message) => received.push(message.kind));
  transport.subscribeToConnectionState((state) => states.push(state));

  await transport.send({
    kind: "response",
    ts: "2026-11-01T00:00:00.000Z",
    response: {
      intention_id: "i-bridge",
      to: { context: "@ui_main", cap: "ui", type: "user" },
      from: { context: "/root", cap: "read.state", type: "reflexive" },
      identity: { id: "brique" },
      status: "ok",
    },
  });
  assert.equal(JSON.parse(sent[0]).response.intention_id, "i-bridge");

  inbound?.(sent[0]);
  stateInbound?.("connected");
  assert.deepEqual(received, ["response"]);
  assert.deepEqual(states, ["connected"]);
  transport.close();
  assert.equal(inbound, null);
  assert.equal(stateInbound, null);
});

async function waitFor(predicate: () => boolean): Promise<void> {
  const deadline = Date.now() + 1_000;
  while (!predicate()) {
    if (Date.now() >= deadline) throw new Error("condition timed out");
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
}
