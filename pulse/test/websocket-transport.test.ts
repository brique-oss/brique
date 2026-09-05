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

import assert from "node:assert/strict";
import test from "node:test";
import {
  deserializeMessage,
  WebSocketTransport,
} from "../runtime";
import {
  createFakeWebSocketServer,
  sampleResponse,
} from "./helpers";

test("WebSocketTransport connects and sends message after connection opens", async () => {
  const server = await createFakeWebSocketServer();
  const transport = new WebSocketTransport(server.url);

  const outbound = sampleResponse("i-send");
  await transport.send(outbound);

  const received = await server.waitForMessage();
  assert.deepEqual(deserializeMessage(received), outbound);

  transport.close();
  await server.close();
});

test("WebSocketTransport waits for open when send is called before connection", async () => {
  const server = await createFakeWebSocketServer();
  const transport = new WebSocketTransport(server.url);
  const outbound = sampleResponse("i-before-open");

  const sendPromise = transport.send(outbound);
  await server.waitForConnection();
  await sendPromise;

  assert.deepEqual(deserializeMessage(await server.waitForMessage()), outbound);

  transport.close();
  await server.close();
});

test("WebSocketTransport receives valid JSON messages", async () => {
  const server = await createFakeWebSocketServer();
  const transport = new WebSocketTransport(server.url);
  const inbound = sampleResponse("i-receive");
  const received: unknown[] = [];

  transport.subscribe((message) => {
    received.push(message);
  });

  await server.waitForConnection();
  server.sendText(JSON.stringify(inbound));
  await waitFor(() => received.length === 1);

  assert.deepEqual(received[0], inbound);

  transport.close();
  await server.close();
});

test("WebSocketTransport ignores invalid JSON messages", async () => {
  const server = await createFakeWebSocketServer();
  const transport = new WebSocketTransport(server.url);
  const received: unknown[] = [];
  const originalConsoleError = console.error;
  console.error = () => {};

  try {
    transport.subscribe((message) => {
      received.push(message);
    });

    await server.waitForConnection();
    server.sendText("{invalid-json");
    await delay(20);

    assert.equal(received.length, 0);
  } finally {
    console.error = originalConsoleError;
  }

  transport.close();
  await server.close();
});

test("WebSocketTransport ignores non-text frames", async () => {
  const server = await createFakeWebSocketServer();
  const transport = new WebSocketTransport(server.url);
  const received: unknown[] = [];
  const originalConsoleError = console.error;
  console.error = () => {};

  try {
    transport.subscribe((message) => {
      received.push(message);
    });

    await server.waitForConnection();
    server.sendBinary(new Uint8Array([1, 2, 3]));
    await delay(20);

    assert.equal(received.length, 0);
  } finally {
    console.error = originalConsoleError;
  }

  transport.close();
  await server.close();
});

test("WebSocketTransport rejects send when server closes before open", async () => {
  const server = await createFakeWebSocketServer();
  const transport = new WebSocketTransport(server.url);

  await server.waitForConnection();
  server.closeConnection();
  await delay(20);

  await assert.rejects(() => transport.send(sampleResponse("i-closed")));

  transport.close();
  await server.close();
});

test("WebSocketTransport reconnects after connection drops", async () => {
  const server = await createFakeWebSocketServer();
  const received: unknown[] = [];
  const transport = new WebSocketTransport(server.url, { baseDelayMs: 10 });

  transport.subscribe((msg) => received.push(msg));

  await server.waitForConnection();
  server.closeConnection();

  await server.waitForReconnection();

  const msg = sampleResponse("i-reconnect");
  server.sendText(JSON.stringify(msg));
  await waitFor(() => received.length === 1);

  assert.deepEqual(received[0], msg);

  transport.close();
  await server.close();
});

test("WebSocketTransport stops reconnecting after close()", async () => {
  const server = await createFakeWebSocketServer();
  const transport = new WebSocketTransport(server.url, { baseDelayMs: 10 });

  await server.waitForConnection();
  transport.close();
  server.closeConnection();

  await delay(50);

  assert.equal(server.received.length, 0);

  await server.close();
});

test("WebSocketTransport resets retry count after successful reconnect", async () => {
  const server = await createFakeWebSocketServer();
  const transport = new WebSocketTransport(server.url, {
    baseDelayMs: 10,
    maxRetries: 2,
  });

  await server.waitForConnection();
  server.closeConnection();

  await server.waitForConnection();

  const msg = sampleResponse("i-after-reconnect");
  server.sendText(JSON.stringify(msg));
  await waitFor(() => server.received.length >= 0);

  transport.close();
  await server.close();
});

async function waitFor(predicate: () => boolean): Promise<void> {
  const start = Date.now();

  while (!predicate()) {
    if (Date.now() - start > 1000) {
      throw new Error("condition timed out");
    }

    await delay(5);
  }
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => {
    setTimeout(resolve, ms);
  });
}
