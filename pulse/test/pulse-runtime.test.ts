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
  FlowRegistry,
  PathRegistry,
  PulseRuntime,
  MatterRegistry,
  serializeMessage,
  SubscriptionRegistry,
  TransportManager,
} from "../runtime";
import { ManualTransport, sampleIntentionInput, sampleResponse } from "./helpers";

test("wire protocol serializes and deserializes intention messages", () => {
  const message = {
    kind: "intention" as const,
    ts: "2026-04-25T00:00:00.000Z",
    intention: {
      ...sampleIntentionInput(),
      intention_id: "i-1",
    },
  };

  const serialized = serializeMessage(message);
  assert.equal(JSON.parse(serialized).kind, "intention");
  assert.deepEqual(deserializeMessage(serialized), message);
});

test("wire protocol serializes and deserializes response messages", () => {
  const message = sampleResponse("i-1");

  const serialized = serializeMessage(message);
  assert.equal(JSON.parse(serialized).kind, "response");
  assert.deepEqual(deserializeMessage(serialized), message);
});

test("wire protocol tolerates unknown fields", () => {
  const message = deserializeMessage(
    JSON.stringify({
      ...sampleResponse("i-1"),
      extra: true,
    })
  );

  assert.equal(message.kind, "response");
});

test("wire protocol handles unknown kind best-effort", () => {
  const message = deserializeMessage(
    JSON.stringify({
      kind: "progress",
      ts: "2026-04-25T00:00:00.000Z",
      intention_id: "i-progress",
      value: 1,
    })
  ) as unknown as { kind: string; intention_id: string };

  assert.equal(message.kind, "progress");
  assert.equal(message.intention_id, "i-progress");
});

test("wire protocol tolerates missing fields best-effort", () => {
  const message = deserializeMessage(
    JSON.stringify({
      kind: "response",
      ts: "2026-04-25T00:00:00.000Z",
    })
  );

  assert.equal(message.kind, "response");
});

test("emitIntention generates an id, preserves input, sends, and binds pathUI", async () => {
  const transport = new ManualTransport();
  const manager = new TransportManager();
  manager.register("default", transport);
  const pulse = new PulseRuntime({
    transportManager: manager,
    transportName: "default",
  });

  const input = sampleIntentionInput();
  const intentionId = await pulse.emitIntention(input, "/panel/result");

  assert.equal(typeof intentionId, "string");
  assert.equal(transport.sent.length, 1);
  assert.equal(transport.sent[0].kind, "intention");
  assert.equal(transport.sent[0].intention.intention_id, intentionId);
  assert.equal(transport.sent[0].intention.await_response, true);
  assert.deepEqual(transport.sent[0].intention.to, input.to);
  assert.deepEqual(transport.sent[0].intention.from, input.from);
  assert.deepEqual(transport.sent[0].intention.identity, input.identity);
  assert.deepEqual(transport.sent[0].intention.params, input.params);
  assert.deepEqual(transport.sent[0].intention.correlation, {
    root_intention_id: intentionId,
    parent_intention_id: null,
  });
  assert.equal(pulse.getIntentionIdForPath("/panel/result"), intentionId);

  pulse.teardown();
});

test("emitIntention does not bind pathUI when absent", async () => {
  const transport = new ManualTransport();
  const manager = new TransportManager();
  manager.register("default", transport);
  const pulse = new PulseRuntime({
    transportManager: manager,
    transportName: "default",
  });

  await pulse.emitIntention(sampleIntentionInput());

  assert.equal(pulse.getIntentionIdForPath("/panel/result"), null);
  pulse.teardown();
});

test("emitIntention propagates transport errors", async () => {
  const transport = new ManualTransport();
  transport.close();
  const manager = new TransportManager();
  manager.register("default", transport);
  const pulse = new PulseRuntime({
    transportManager: manager,
    transportName: "default",
  });

  await assert.rejects(() => pulse.emitIntention(sampleIntentionInput()));
});

test("emitIntentionAndWait resolves on the final response and ignores intermediate running responses", async () => {
  const transport = new ManualTransport();
  const manager = new TransportManager();
  manager.register("default", transport);
  const pulse = new PulseRuntime({
    transportManager: manager,
    transportName: "default",
    responseTimeoutMs: 200,
  });

  const promise = pulse.emitIntentionAndWait(sampleIntentionInput());
  const intentionId = transport.sent[0].kind === "intention"
    ? transport.sent[0].intention.intention_id
    : "";
  assert.notEqual(intentionId, "");

  const running = sampleResponse(intentionId);
  running.response.status = "running";
  running.response.payload = {};
  transport.receive(running);
  transport.receive(running);

  const final = sampleResponse(intentionId);
  transport.receive(final);

  const response = await promise;
  assert.equal(response.status, "ok");
  assert.deepEqual(response.payload, { value: "done" });
});

test("emitIntentionAndWait rejects on timeout when only running responses arrive", async () => {
  const transport = new ManualTransport();
  const manager = new TransportManager();
  manager.register("default", transport);
  const pulse = new PulseRuntime({
    transportManager: manager,
    transportName: "default",
    responseTimeoutMs: 20,
  });

  const promise = pulse.emitIntentionAndWait(sampleIntentionInput());
  const intentionId = transport.sent[0].kind === "intention"
    ? transport.sent[0].intention.intention_id
    : "";

  const running = sampleResponse(intentionId);
  running.response.status = "running";
  transport.receive(running);

  await assert.rejects(
    () => promise,
    /Timed out waiting for response/
  );
});

test("dispatch routes only matching intention subscribers and updates flow/path", () => {
  const transport = new ManualTransport();
  const manager = new TransportManager();
  manager.register("default", transport);
  const pulse = new PulseRuntime({
    transportManager: manager,
    transportName: "default",
  });

  const receivedA: unknown[] = [];
  const receivedB: unknown[] = [];
  pulse.subscribeToIntention("i-a", (message) => receivedA.push(message));
  pulse.subscribeToIntention("i-b", (message) => receivedB.push(message));

  const message = sampleResponse("i-a", "@ui_main:/workspace/result");
  transport.receive(message);

  assert.equal(receivedA.length, 1);
  assert.equal(receivedB.length, 0);
  assert.equal(pulse.getFlow("i-a")?.lastMessage, message);
  assert.equal(pulse.getIntentionIdForPath("/workspace/result"), "i-a");

  pulse.teardown();
});

test("dispatch ignores messages without intention_id", () => {
  const transport = new ManualTransport();
  const manager = new TransportManager();
  manager.register("default", transport);
  const pulse = new PulseRuntime({
    transportManager: manager,
    transportName: "default",
  });
  let calls = 0;

  pulse.subscribeToIntention("i-a", () => {
    calls += 1;
  });

  transport.receive({
    kind: "response",
    ts: "2026-04-25T00:00:00.000Z",
    response: undefined,
  } as never);

  assert.equal(calls, 0);
  pulse.teardown();
});

test("dispatch routes unknown correlated message kinds", () => {
  const transport = new ManualTransport();
  const manager = new TransportManager();
  manager.register("default", transport);
  const pulse = new PulseRuntime({
    transportManager: manager,
    transportName: "default",
  });
  const received: unknown[] = [];

  pulse.subscribeToIntention("i-progress", (message) => {
    received.push(message);
  });

  transport.receive({
    kind: "progress",
    ts: "2026-04-25T00:00:00.000Z",
    intention_id: "i-progress",
  } as never);

  assert.equal(received.length, 1);
  pulse.teardown();
});

test("SubscriptionRegistry supports unsubscribe, multiple subscribers, and isolation", () => {
  const registry = new SubscriptionRegistry();
  const calls: string[] = [];
  const message = sampleResponse("i-1");

  const unsubscribeA = registry.subscribe("i-1", () => calls.push("a"));
  registry.subscribe("i-1", () => calls.push("b"));
  registry.subscribe("i-2", () => calls.push("wrong"));

  registry.notify("i-1", message);
  assert.deepEqual(calls, ["a", "b"]);

  unsubscribeA();
  unsubscribeA();
  registry.notify("i-1", message);
  assert.deepEqual(calls, ["a", "b", "b"]);
});

test("FlowRegistry stores last message, isolates flows, and respects max history", () => {
  const registry = new FlowRegistry(2);
  const first = sampleResponse("i-1");
  const second = sampleResponse("i-1");
  const third = sampleResponse("i-1");
  const other = sampleResponse("i-2");

  registry.update("i-1", first);
  registry.update("i-1", second);
  registry.update("i-1", third);
  registry.update("i-2", other);

  assert.equal(registry.get("i-1")?.lastMessage, third);
  assert.deepEqual(registry.get("i-1")?.messages, [second, third]);
  assert.deepEqual(registry.get("i-2")?.messages, [other]);

  registry.clear();
  assert.equal(registry.has("i-1"), false);
});

test("FlowRegistry exposes stable idle and active snapshots", () => {
  const registry = new FlowRegistry(2);
  const idleA = registry.getSnapshot("missing");
  const idleB = registry.getSnapshot("missing");

  assert.equal(idleA, idleB);
  assert.equal(idleA.status, "idle");
  assert.equal(idleA.intentionId, null);

  const first = sampleResponse("i-1");
  registry.update("i-1", first);

  const snapshotA = registry.getSnapshot("i-1");
  const snapshotB = registry.getSnapshot("i-1");

  assert.equal(snapshotA, snapshotB);
  assert.equal(snapshotA.intentionId, "i-1");
  assert.equal(snapshotA.status, "success");
  assert.equal(snapshotA.message, first);
  assert.deepEqual(snapshotA.payload, { value: "done" });
  assert.equal(snapshotA.terminal, true);
});

test("FlowRegistry notifies only subscribers for changed flow", () => {
  const registry = new FlowRegistry();
  const calls: string[] = [];
  const unsubscribeA = registry.subscribe("i-a", () => calls.push("a"));
  registry.subscribe("i-b", () => calls.push("b"));

  registry.update("i-a", sampleResponse("i-a"));
  assert.deepEqual(calls, ["a"]);

  unsubscribeA();
  unsubscribeA();
  registry.update("i-a", sampleResponse("i-a"));
  assert.deepEqual(calls, ["a"]);

  registry.update("i-b", sampleResponse("i-b"));
  assert.deepEqual(calls, ["a", "b"]);
});

test("FlowRegistry notifies subscribers when terminal flow is purged", async () => {
  const registry = new FlowRegistry(100, 30);
  const snapshots: string[] = [];
  registry.subscribe("i-terminal", () => {
    snapshots.push(registry.getSnapshot("i-terminal").status);
  });

  registry.update("i-terminal", sampleResponse("i-terminal"));
  assert.deepEqual(snapshots, ["success"]);

  await delay(60);

  assert.deepEqual(snapshots, ["success", "idle"]);
});

test("FlowRegistry purges terminal flows after TTL", async () => {
  const registry = new FlowRegistry(100, 30);

  registry.update("i-terminal", sampleResponse("i-terminal"));
  assert.equal(registry.has("i-terminal"), true);

  await delay(60);

  assert.equal(registry.has("i-terminal"), false);
});

test("FlowRegistry does not purge non-terminal flows", async () => {
  const registry = new FlowRegistry(100, 30);

  const nonTerminal = {
    ...sampleResponse("i-progress"),
    response: { ...sampleResponse("i-progress").response, status: "progress" },
  };
  registry.update("i-progress", nonTerminal as never);

  await delay(60);

  assert.equal(registry.has("i-progress"), true);

  registry.clear();
});

test("FlowRegistry cancel clears pending terminal TTL timers", () => {
  const registry = new FlowRegistry(100, 30);

  registry.update("i-terminal", sampleResponse("i-terminal"));
  assert.equal(registry.has("i-terminal"), true);

  registry.delete("i-terminal");
  assert.equal(registry.has("i-terminal"), false);
});

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

test("PathRegistry binds, replaces, clears, and notifies", () => {
  const registry = new PathRegistry();
  const changes: Array<string | null> = [];
  registry.subscribe((path, intentionId) => {
    if (path === "/result") changes.push(intentionId);
  });

  registry.activatePath("/result", "i-1");
  registry.activatePath("/result", "i-2");
  registry.deactivatePath("/result");

  assert.equal(registry.isPathActive("/result"), false);
  assert.equal(registry.getIntentionIdForPath("/result"), null);
  assert.deepEqual(changes, ["i-1", "i-2", null]);
});

test("PathRegistry exposes stable inactive and active snapshots", () => {
  const registry = new PathRegistry();
  const inactiveA = registry.getSnapshot("/result");
  const inactiveB = registry.getSnapshot("/result");

  assert.equal(inactiveA, inactiveB);
  assert.deepEqual(inactiveA, {
    pathUI: "/result",
    intentionId: null,
    active: false,
  });

  registry.activatePath("/result", "i-1");

  const activeA = registry.getSnapshot("/result");
  const activeB = registry.getSnapshot("/result");

  assert.equal(activeA, activeB);
  assert.deepEqual(activeA, {
    pathUI: "/result",
    intentionId: "i-1",
    active: true,
  });
});

test("PathRegistry path subscriptions are isolated and idempotent", () => {
  const registry = new PathRegistry();
  const calls: string[] = [];
  const unsubscribeA = registry.subscribePath("/a", () => calls.push("a"));
  registry.subscribePath("/b", () => calls.push("b"));

  registry.activatePath("/a", "i-a");
  assert.deepEqual(calls, ["a"]);

  unsubscribeA();
  unsubscribeA();
  registry.activatePath("/a", "i-a2");
  assert.deepEqual(calls, ["a"]);

  registry.activatePath("/b", "i-b");
  assert.deepEqual(calls, ["a", "b"]);
});

test("PathRegistry clear notifies active path snapshot subscribers", () => {
  const registry = new PathRegistry();
  const snapshots: Array<string | null> = [];
  registry.subscribePath("/a", () => {
    snapshots.push(registry.getSnapshot("/a").intentionId);
  });
  registry.subscribePath("/b", () => {
    snapshots.push(registry.getSnapshot("/b").intentionId);
  });

  registry.activatePath("/a", "i-a");
  registry.activatePath("/b", "i-b");
  registry.clear();

  assert.deepEqual(snapshots, ["i-a", "i-b", null, null]);
});

test("PulseRuntime exposes path snapshots and path snapshot subscriptions", () => {
  const transport = new ManualTransport();
  const manager = new TransportManager();
  manager.register("default", transport);
  const pulse = new PulseRuntime({
    transportManager: manager,
    transportName: "default",
  });
  const snapshots: Array<string | null> = [];

  pulse.subscribeToPathSnapshot("/slot", () => {
    snapshots.push(pulse.getPathSnapshot("/slot").intentionId);
  });

  pulse.activatePath("/slot", "i-1");

  assert.deepEqual(pulse.getPathSnapshot("/slot"), {
    pathUI: "/slot",
    intentionId: "i-1",
    active: true,
  });
  assert.deepEqual(snapshots, ["i-1"]);

  pulse.teardown();
});

test("MatterRegistry normalizes contextualized keys and exposes stable active snapshots", () => {
  const registry = new MatterRegistry();
  const key = {
    context: " /root/text_context ",
    matterId: " text_buffer ",
    readMode: " data|brique ",
  };

  registry.registerReader(key);
  const snapshotA = registry.getSnapshot(key);
  const snapshotB = registry.getSnapshot({
    context: "/root/text_context",
    matterId: "text_buffer",
    readMode: "data|brique",
  });

  assert.equal(snapshotA, snapshotB);
  assert.equal(snapshotA.address.context, "/root/text_context");
  assert.equal(snapshotA.address.matterId, "text_buffer");
  assert.equal(snapshotA.readMode, "data|brique");
  assert.equal(snapshotA.status, "idle");
});

test("MatterRegistry separates same matter id by context", () => {
  const registry = new MatterRegistry();
  const textKey = {
    context: "/root/text_context",
    matterId: "value",
    readMode: null,
  };
  const counterKey = {
    context: "/root/counter_context",
    matterId: "value",
    readMode: null,
  };

  registry.registerReader(textKey);
  registry.registerReader(counterKey);
  registry.setReady(textKey, { matter_id: "value", data: { value: "text" } });
  registry.setReady(counterKey, { matter_id: "value", data: { value: 1 } });

  assert.deepEqual(registry.getSnapshot(textKey).data, { value: "text" });
  assert.deepEqual(registry.getSnapshot(counterKey).data, { value: 1 });
});

test("MatterRegistry refcounts readers and removes entry after last reader", () => {
  const registry = new MatterRegistry();
  const key = {
    context: "/root/text_context",
    matterId: "text_buffer",
    readMode: null,
  };

  registry.registerReader(key);
  registry.registerReader(key);
  registry.setReady(key, { data: { value: "hello" }, revision: 2 });

  assert.equal(registry.has(key), true);
  assert.equal(registry.getReaderCount(key), 2);
  assert.deepEqual(registry.getSnapshot(key).data, { value: "hello" });
  assert.equal(registry.getSnapshot(key).revision, 2);

  registry.unregisterReader(key);
  assert.equal(registry.has(key), true);
  assert.equal(registry.getReaderCount(key), 1);

  registry.unregisterReader(key);
  assert.equal(registry.has(key), false);
  assert.equal(registry.getSnapshot(key).status, "idle");
  assert.equal(registry.getSnapshot(key).data, null);
});

test("MatterRegistry refcounts subscribers and clears subscription on last subscriber", () => {
  const registry = new MatterRegistry();
  const key = {
    context: "/root/text_context",
    matterId: "text_buffer",
    readMode: null,
  };

  registry.registerSubscriber(key);
  registry.registerSubscriber(key);
  registry.setSubscription(key, "sub-1");

  assert.equal(registry.getSubscriberCount(key), 2);
  assert.equal(registry.getSnapshot(key).subscribed, true);
  assert.equal(registry.getSnapshot(key).subId, "sub-1");

  registry.unregisterSubscriber(key);
  assert.equal(registry.has(key), true);
  assert.equal(registry.getSubscriberCount(key), 1);
  assert.equal(registry.getSnapshot(key).subId, "sub-1");

  registry.unregisterSubscriber(key);
  assert.equal(registry.has(key), false);
  assert.equal(registry.getSnapshot(key).subscribed, false);
  assert.equal(registry.getSnapshot(key).subId, null);
});

test("MatterRegistry notifies only subscribers for changed matter key", () => {
  const registry = new MatterRegistry();
  const textKey = {
    context: "/root/text_context",
    matterId: "value",
    readMode: null,
  };
  const counterKey = {
    context: "/root/counter_context",
    matterId: "value",
    readMode: null,
  };
  const calls: string[] = [];
  const unsubscribeText = registry.subscribeToSnapshot(textKey, () => calls.push("text"));
  registry.subscribeToSnapshot(counterKey, () => calls.push("counter"));

  registry.registerReader(textKey);
  assert.deepEqual(calls, []);

  registry.setReady(textKey, { data: { value: "text" } });
  assert.deepEqual(calls, ["text"]);

  unsubscribeText();
  unsubscribeText();
  registry.setReady(textKey, { data: { value: "next" } });
  assert.deepEqual(calls, ["text"]);

  registry.registerReader(counterKey);
  registry.setReady(counterKey, { data: { value: 1 } });
  assert.deepEqual(calls, ["text", "counter"]);
});

test("MatterRegistry marks active entries stale and deleted", () => {
  const registry = new MatterRegistry();
  const key = {
    context: "/root/text_context",
    matterId: "text_buffer",
    readMode: null,
  };

  registry.registerReader(key);
  registry.setReady(key, { data: { value: "hello" } });
  registry.markStale(key, {
    event: "matter_written",
    matterId: "text_buffer",
    revision: 3,
    substanceMode: "brique",
    sourceIntentionId: "i-write",
  });

  assert.equal(registry.getSnapshot(key).status, "stale");
  assert.equal(registry.getSnapshot(key).revision, 3);
  assert.equal(registry.getSnapshot(key).lastEvent?.event, "matter_written");

  registry.markDeleted(key, {
    event: "matter_deleted",
    matterId: "text_buffer",
    revision: 4,
    substanceMode: "brique",
    sourceIntentionId: "i-delete",
  });

  assert.equal(registry.getSnapshot(key).status, "deleted");
  assert.equal(registry.getSnapshot(key).data, null);
  assert.equal(registry.getSnapshot(key).payload, null);
  assert.equal(registry.getSnapshot(key).lastEvent?.event, "matter_deleted");
});

test("PulseRuntime owns an active MatterRegistry and clears it on teardown", () => {
  const transport = new ManualTransport();
  const manager = new TransportManager();
  manager.register("default", transport);
  const pulse = new PulseRuntime({
    transportManager: manager,
    transportName: "default",
  });
  const key = {
    context: "/root/text_context",
    matterId: "text_buffer",
    readMode: null,
  };

  pulse.matter.registerReader(key);
  pulse.matter.setReady(key, { data: { value: "hello" } });

  assert.equal(pulse.matter.has(key), true);

  pulse.teardown();

  assert.equal(pulse.matter.has(key), false);
  assert.equal(pulse.matter.getSnapshot(key).status, "idle");
});

test("PulseRuntime matter.read emits canonical matter intention and updates snapshot", async () => {
  const transport = new ManualTransport();
  const manager = new TransportManager();
  manager.register("default", transport);
  const pulse = new PulseRuntime({
    transportManager: manager,
    transportName: "default",
  });
  const key = {
    context: "/root/text_context",
    matterId: "text_buffer",
    readMode: "data|brique",
  };

  pulse.matter.registerReader(key);
  const read = pulse.matter.read(key);

  assert.equal(pulse.matter.getSnapshot(key).status, "loading");
  assert.equal(transport.sent.length, 1);
  assert.equal(transport.sent[0].kind, "intention");
  assert.equal(transport.sent[0].intention.to.context, "/root/text_context");
  assert.equal(transport.sent[0].intention.to.cap, "matter.read");
  assert.equal(transport.sent[0].intention.to.type, "matter");
  assert.deepEqual(transport.sent[0].intention.correlation, {
    root_intention_id: transport.sent[0].intention.intention_id,
    parent_intention_id: null,
  });
  assert.equal(transport.sent[0].intention.from.context, "@ui_main:/matter");
  assert.deepEqual(transport.sent[0].intention.params, {
    matter_id: "text_buffer",
    read_mode: "data|brique",
  });

  transport.receive(matterResponse(transport.sent[0].intention.intention_id, {
    matter_id: "text_buffer",
    data: { value: "hello" },
    revision: 7,
  }));

  const snapshot = await read;
  assert.equal(snapshot.status, "ready");
  assert.deepEqual(snapshot.data, { value: "hello" });
  assert.equal(snapshot.revision, 7);

  pulse.teardown();
});

test("PulseRuntime matter.read streams Brique HTTP lease handles", async () => {
  const originalFetch = globalThis.fetch;
  const transport = new ManualTransport();
  const manager = new TransportManager();
  manager.register("default", transport);
  const pulse = new PulseRuntime({
    transportManager: manager,
    transportName: "default",
  });
  const key = {
    context: "/root/stream_context",
    matterId: "stream_payload",
    readMode: null,
  };

  globalThis.fetch = async (input) => {
    assert.equal(
      String(input),
      "http://127.0.0.1:18204/substance/read-1?tok=secret"
    );

    return new Response(new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(new Uint8Array([1, 2]));
        controller.enqueue(new Uint8Array([3, 4]));
        controller.close();
      },
    }));
  };

  try {
    pulse.matter.registerReader(key);
    const read = pulse.matter.read(key);

    transport.receive(matterResponse(transport.sent[0].intention.intention_id, {
      matter_id: "stream_payload",
      data: {
        kind: "http",
        http: {
          base_url: "http://127.0.0.1:18204",
          path: "/substance/read-1",
          tok: "secret",
        },
      },
    }));

    const snapshot = await read;
    assert.equal(snapshot.status, "ready");
    assert.deepEqual(snapshot.data, new Uint8Array([1, 2, 3, 4]));
    assert.equal(snapshot.streamBytesReceived, 4);
    assert.equal(snapshot.streamChunksReceived, 2);
  } finally {
    globalThis.fetch = originalFetch;
    pulse.teardown();
  }
});

test("MatterRegistry resets stream counters when a new read starts", () => {
  const registry = new MatterRegistry();
  const key = {
    context: "/root/stream_context",
    matterId: "stream_payload",
    readMode: null,
  };

  registry.registerReader(key);
  registry.setStreaming(key);
  registry.appendChunk(key, new Uint8Array([1, 2, 3]));

  assert.equal(registry.getSnapshot(key).streamBytesReceived, 3);
  assert.equal(registry.getSnapshot(key).streamChunksReceived, 1);

  registry.setLoading(key);

  const snapshot = registry.getSnapshot(key);
  assert.equal(snapshot.status, "loading");
  assert.equal(snapshot.data, null);
  assert.equal(snapshot.payload, null);
  assert.equal(snapshot.streamBytesReceived, 0);
  assert.equal(snapshot.streamChunksReceived, 0);
});

test("PulseRuntime matter.read stores refused responses as snapshot errors", async () => {
  const transport = new ManualTransport();
  const manager = new TransportManager();
  manager.register("default", transport);
  const pulse = new PulseRuntime({
    transportManager: manager,
    transportName: "default",
  });
  const key = {
    context: "/root/text_context",
    matterId: "missing",
    readMode: null,
  };

  pulse.matter.registerReader(key);
  const read = pulse.matter.read(key);

  transport.receive(matterResponse(
    transport.sent[0].intention.intention_id,
    {},
    "error",
    {
      code: "refused",
      message: "matter not in runtime catalog",
    }
  ));

  await assert.rejects(read);
  assert.equal(pulse.matter.getSnapshot(key).status, "error");
  assert.deepEqual(pulse.matter.getSnapshot(key).error, {
    code: "refused",
    message: "matter not in runtime catalog",
  });

  pulse.teardown();
});

test("PulseRuntime matter.acquireReader deduplicates active reads", async () => {
  const transport = new ManualTransport();
  const manager = new TransportManager();
  manager.register("default", transport);
  const pulse = new PulseRuntime({
    transportManager: manager,
    transportName: "default",
  });
  const key = {
    context: "/root/text_context",
    matterId: "text_buffer",
    readMode: null,
  };

  pulse.matter.acquireReader(key);
  pulse.matter.acquireReader(key);

  assert.equal(pulse.matter.getReaderCount(key), 2);
  assert.equal(transport.sent.length, 1);
  assert.equal(transport.sent[0].intention.to.cap, "matter.read");
  assert.equal(pulse.matter.getReadRequestCount(key), 1);
  assert.equal(pulse.matter.hasPendingRead(key), true);

  transport.receive(matterResponse(transport.sent[0].intention.intention_id, {
    data: { value: "shared" },
    revision: 1,
  }));

  await waitForMicrotasks();
  assert.equal(pulse.matter.getSnapshot(key).status, "ready");
  assert.deepEqual(pulse.matter.getSnapshot(key).data, { value: "shared" });
  assert.equal(pulse.matter.hasPendingRead(key), false);

  pulse.teardown();
});

test("PulseRuntime matter.releaseReader prevents late reads from recreating state", async () => {
  const transport = new ManualTransport();
  const manager = new TransportManager();
  manager.register("default", transport);
  const pulse = new PulseRuntime({
    transportManager: manager,
    transportName: "default",
  });
  const key = {
    context: "/root/text_context",
    matterId: "text_buffer",
    readMode: null,
  };

  pulse.matter.acquireReader(key);
  const intentionId = transport.sent[0].intention.intention_id;
  pulse.matter.releaseReader(key);

  assert.equal(pulse.matter.has(key), false);

  transport.receive(matterResponse(intentionId, {
    data: { value: "late" },
    revision: 2,
  }));

  await waitForMicrotasks();
  assert.equal(pulse.matter.has(key), false);

  pulse.teardown();
});

test("PulseRuntime matter reacquire after full release starts a new read and ignores the old response", async () => {
  const transport = new ManualTransport();
  const manager = new TransportManager();
  manager.register("default", transport);
  const pulse = new PulseRuntime({
    transportManager: manager,
    transportName: "default",
  });
  const key = {
    context: "/root/text_context",
    matterId: "text_buffer",
    readMode: null,
  };

  pulse.matter.acquireReader(key);
  const oldReadId = transport.sent[0].intention.intention_id;
  pulse.matter.releaseReader(key);

  assert.equal(pulse.matter.has(key), false);

  pulse.matter.acquireReader(key);
  const newReadId = transport.sent[1].intention.intention_id;

  assert.notEqual(newReadId, oldReadId);
  assert.equal(transport.sent.length, 2);
  assert.equal(pulse.matter.getSnapshot(key).status, "loading");

  transport.receive(matterResponse(oldReadId, {
    data: { value: "old" },
    revision: 1,
  }));
  await waitForMicrotasks();

  assert.equal(pulse.matter.getSnapshot(key).status, "loading");
  assert.equal(pulse.matter.getSnapshot(key).data, null);

  transport.receive(matterResponse(newReadId, {
    data: { value: "new" },
    revision: 2,
  }));
  await waitForMicrotasks();

  assert.equal(pulse.matter.getSnapshot(key).status, "ready");
  assert.deepEqual(pulse.matter.getSnapshot(key).data, { value: "new" });
  assert.equal(pulse.matter.getSnapshot(key).revision, 2);

  pulse.teardown();
});

test("PulseRuntime matter.subscribe stores returned sub_id", async () => {
  const transport = new ManualTransport();
  const manager = new TransportManager();
  manager.register("default", transport);
  const pulse = new PulseRuntime({
    transportManager: manager,
    transportName: "default",
  });
  const key = {
    context: "/root/text_context",
    matterId: "text_buffer",
    readMode: null,
  };

  const subscribe = pulse.matter.subscribe(key);

  assert.equal(transport.sent.length, 1);
  assert.equal(transport.sent[0].intention.to.cap, "matter.subscribe");
  assert.deepEqual(transport.sent[0].intention.params, {
    matter_id: "text_buffer",
  });

  transport.receive(matterResponse(transport.sent[0].intention.intention_id, {
    ok: true,
    matter_id: "text_buffer",
    sub_id: "sub-1",
    substance_mode: "brique",
  }));

  const snapshot = await subscribe;
  assert.equal(snapshot.subscribed, true);
  assert.equal(snapshot.subId, "sub-1");
  assert.equal(pulse.matter.getSubscriberCount(key), 1);

  pulse.teardown();
});

test("PulseRuntime matter.acquireSubscription shares Brique subscription", async () => {
  const transport = new ManualTransport();
  const manager = new TransportManager();
  manager.register("default", transport);
  const pulse = new PulseRuntime({
    transportManager: manager,
    transportName: "default",
  });
  const key = {
    context: "/root/text_context",
    matterId: "text_buffer",
    readMode: null,
  };

  pulse.matter.acquireSubscription(key);
  pulse.matter.acquireSubscription(key);

  assert.equal(pulse.matter.getSubscriberCount(key), 2);
  assert.equal(transport.sent.length, 1);
  assert.equal(transport.sent[0].intention.to.cap, "matter.subscribe");

  transport.receive(matterResponse(transport.sent[0].intention.intention_id, {
    matter_id: "text_buffer",
    sub_id: "sub-1",
  }));
  await waitForMicrotasks();

  assert.equal(pulse.matter.getSnapshot(key).subId, "sub-1");

  pulse.matter.releaseSubscription(key);
  assert.equal(pulse.matter.getSubscriberCount(key), 1);
  assert.equal(transport.sent.length, 1);

  pulse.matter.releaseSubscription(key);
  assert.equal(transport.sent.length, 2);
  assert.equal(transport.sent[1].intention.to.cap, "matter.unsubscribe");

  transport.receive(matterResponse(transport.sent[1].intention.intention_id, {
    matter_id: "text_buffer",
    sub_id: "sub-1",
    removed: true,
  }));
  await waitForMicrotasks();

  assert.equal(pulse.matter.has(key), false);

  pulse.teardown();
});

test("PulseRuntime matter.acquireSubscription cleans up late subscribe responses", async () => {
  const transport = new ManualTransport();
  const manager = new TransportManager();
  manager.register("default", transport);
  const pulse = new PulseRuntime({
    transportManager: manager,
    transportName: "default",
  });
  const key = {
    context: "/root/text_context",
    matterId: "text_buffer",
    readMode: null,
  };

  pulse.matter.acquireSubscription(key);
  const subscribeIntentionId = transport.sent[0].intention.intention_id;
  pulse.matter.releaseSubscription(key);

  assert.equal(pulse.matter.has(key), false);

  transport.receive(matterResponse(subscribeIntentionId, {
    matter_id: "text_buffer",
    sub_id: "sub-late",
  }));
  await waitForMicrotasks();

  assert.equal(transport.sent.length, 2);
  assert.equal(transport.sent[1].intention.to.cap, "matter.unsubscribe");
  assert.deepEqual(transport.sent[1].intention.params, {
    matter_id: "text_buffer",
    sub_id: "sub-late",
  });

  transport.receive(matterResponse(transport.sent[1].intention.intention_id, {
    matter_id: "text_buffer",
    sub_id: "sub-late",
    removed: true,
  }));
  await waitForMicrotasks();

  assert.equal(pulse.matter.has(key), false);

  pulse.teardown();
});

test("PulseRuntime matter.unsubscribe clears subscription state", async () => {
  const transport = new ManualTransport();
  const manager = new TransportManager();
  manager.register("default", transport);
  const pulse = new PulseRuntime({
    transportManager: manager,
    transportName: "default",
  });
  const key = {
    context: "/root/text_context",
    matterId: "text_buffer",
    readMode: null,
  };

  const subscribe = pulse.matter.subscribe(key);
  transport.receive(matterResponse(transport.sent[0].intention.intention_id, {
    ok: true,
    matter_id: "text_buffer",
    sub_id: "sub-1",
  }));
  await subscribe;

  const unsubscribe = pulse.matter.unsubscribe({
    ...key,
    subId: "sub-1",
  });

  assert.equal(transport.sent.length, 2);
  assert.equal(transport.sent[1].intention.to.cap, "matter.unsubscribe");
  assert.deepEqual(transport.sent[1].intention.params, {
    matter_id: "text_buffer",
    sub_id: "sub-1",
  });

  transport.receive(matterResponse(transport.sent[1].intention.intention_id, {
    ok: true,
    matter_id: "text_buffer",
    sub_id: "sub-1",
    removed: true,
  }));

  const snapshot = await unsubscribe;
  assert.equal(snapshot.subscribed, false);
  assert.equal(snapshot.subId, null);
  assert.equal(pulse.matter.has(key), false);

  pulse.teardown();
});

test("PulseRuntime builds UI-origin intentions from pathUI", () => {
  const transport = new ManualTransport();
  const manager = new TransportManager();
  manager.register("default", transport);
  const pulse = new PulseRuntime({
    transportManager: manager,
    transportName: "default",
  });

  const intention = pulse.createUIIntention({
    to: {
      context: "/root/text_context",
      cap: "set_text",
      type: "user",
    },
    targetPathUI: "/text/result",
    params: { value: "hello" },
  });

  assert.equal(intention.await_response, true);
  assert.deepEqual(intention.to, {
    context: "/root/text_context",
    cap: "set_text",
    type: "user",
  });
  assert.deepEqual(intention.from, {
    context: "@ui_main:/text/result",
    cap: "ui",
    type: "user",
  });
  assert.deepEqual(intention.identity, {
    id: "pulse-ui",
    kind: "ui",
  });
  assert.deepEqual(intention.params, { value: "hello" });

  pulse.teardown();
});

test("PulseRuntime UI-origin intention supports source path and custom identity", () => {
  const transport = new ManualTransport();
  const manager = new TransportManager();
  manager.register("default", transport);
  const pulse = new PulseRuntime({
    transportManager: manager,
    transportName: "default",
    identity: {
      id: "sandbox-user",
      kind: "human",
    },
  });

  const intention = pulse.createUIIntention({
    to: {
      context: "/root/text_context",
      cap: "set_text",
      type: "user",
    },
    sourcePathUI: "/toolbar/input",
    targetPathUI: "/text/result",
  });

  assert.equal(intention.from.context, "@ui_main:/toolbar/input");
  assert.deepEqual(intention.identity, {
    id: "sandbox-user",
    kind: "human",
  });

  pulse.teardown();
});

test("PulseRuntime emits UI-origin intention and binds target path", async () => {
  const transport = new ManualTransport();
  const manager = new TransportManager();
  manager.register("default", transport);
  const pulse = new PulseRuntime({
    transportManager: manager,
    transportName: "default",
    uiName: "panel",
  });

  const intentionId = await pulse.emitUIIntention({
    to: {
      context: "/root/text_context",
      cap: "set_text",
      type: "user",
    },
    targetPathUI: "/text/result",
    params: { value: "hello" },
  });

  assert.equal(transport.sent.length, 1);
  assert.equal(transport.sent[0].kind, "intention");
  assert.equal(transport.sent[0].intention.intention_id, intentionId);
  assert.equal(transport.sent[0].intention.from.context, "@ui_panel:/text/result");
  assert.equal(pulse.getIntentionIdForPath("/text/result"), intentionId);

  pulse.teardown();
});

test("PulseRuntime dispatches matter_written event to matching subscription", async () => {
  const transport = new ManualTransport();
  const manager = new TransportManager();
  manager.register("default", transport);
  const pulse = new PulseRuntime({
    transportManager: manager,
    transportName: "default",
  });
  const key = {
    context: "/root/text_context",
    matterId: "text_buffer",
    readMode: null,
  };

  const subscribe = pulse.matter.subscribe(key);
  transport.receive(matterResponse(transport.sent[0].intention.intention_id, {
    ok: true,
    matter_id: "text_buffer",
    sub_id: "sub-1",
  }));
  await subscribe;

  transport.receive(matterEvent("matter_written", "text_buffer", {
    sub_id: "sub-1",
    revision: 9,
    source_intention_id: "i-write",
  }));

  const snapshot = pulse.matter.getSnapshot(key);
  assert.equal(snapshot.status, "stale");
  assert.equal(snapshot.revision, 9);
  assert.equal(snapshot.lastEvent?.event, "matter_written");
  assert.equal(snapshot.lastEvent?.sourceIntentionId, "i-write");

  pulse.teardown();
});

test("PulseRuntime dispatches matter_deleted event to matching subscription", async () => {
  const transport = new ManualTransport();
  const manager = new TransportManager();
  manager.register("default", transport);
  const pulse = new PulseRuntime({
    transportManager: manager,
    transportName: "default",
  });
  const key = {
    context: "/root/text_context",
    matterId: "text_buffer",
    readMode: null,
  };

  pulse.matter.registerReader(key);
  pulse.matter.setReady(key, { data: { value: "hello" } });
  pulse.matter.setSubscription(key, "sub-1");

  transport.receive(matterEvent("matter_deleted", "text_buffer", {
    sub_id: "sub-1",
    revision: 10,
  }));

  const snapshot = pulse.matter.getSnapshot(key);
  assert.equal(snapshot.status, "deleted");
  assert.equal(snapshot.data, null);
  assert.equal(snapshot.payload, null);
  assert.equal(snapshot.lastEvent?.event, "matter_deleted");

  pulse.teardown();
});

test("PulseRuntime ignores matter events for inactive matters", () => {
  const transport = new ManualTransport();
  const manager = new TransportManager();
  manager.register("default", transport);
  const pulse = new PulseRuntime({
    transportManager: manager,
    transportName: "default",
  });
  const key = {
    context: "/root/text_context",
    matterId: "text_buffer",
    readMode: null,
  };

  transport.receive(matterEvent("matter_written", "text_buffer", {
    sub_id: "missing-sub",
    revision: 1,
  }));

  assert.equal(pulse.matter.has(key), false);
  assert.equal(pulse.matter.getSnapshot(key).status, "idle");

  pulse.teardown();
});

test("PulseRuntime does not apply ambiguous matter events by matter_id only", () => {
  const transport = new ManualTransport();
  const manager = new TransportManager();
  manager.register("default", transport);
  const pulse = new PulseRuntime({
    transportManager: manager,
    transportName: "default",
  });
  const textKey = {
    context: "/root/text_context",
    matterId: "shared",
    readMode: null,
  };
  const counterKey = {
    context: "/root/counter_context",
    matterId: "shared",
    readMode: null,
  };

  pulse.matter.registerReader(textKey);
  pulse.matter.registerReader(counterKey);
  pulse.matter.setReady(textKey, { data: { value: "text" } });
  pulse.matter.setReady(counterKey, { data: { value: 1 } });

  transport.receive(matterEvent("matter_written", "shared", {
    revision: 2,
  }));

  assert.equal(pulse.matter.getSnapshot(textKey).status, "ready");
  assert.equal(pulse.matter.getSnapshot(counterKey).status, "ready");

  pulse.teardown();
});

test("displayMode has default value, updates, notifies, and resets", () => {
  const transport = new ManualTransport();
  const manager = new TransportManager();
  manager.register("default", transport);
  const pulse = new PulseRuntime({
    transportManager: manager,
    transportName: "default",
  });
  const changes: string[] = [];

  assert.equal(pulse.getDisplayMode(), "idle");
  pulse.subscribeToDisplayMode((mode) => changes.push(mode));
  pulse.setDisplayMode("workspace");

  assert.equal(pulse.getDisplayMode(), "workspace");
  assert.deepEqual(changes, ["workspace"]);

  pulse.clearProjection();
  assert.equal(pulse.getDisplayMode(), "idle");
  assert.deepEqual(changes, ["workspace", "idle"]);
  pulse.teardown();
});

function matterResponse(
  intentionId: string,
  payload: Record<string, unknown>,
  status = "ok",
  error?: {
    code?: string;
    message?: string;
  }
) {
  return {
    kind: "response" as const,
    ts: "2026-04-25T00:00:00.000Z",
    response: {
      intention_id: intentionId,
      to: {
        context: "@ui_main:/matter",
        cap: "ui",
        type: "user",
      },
      from: {
        context: "/root/text_context",
        cap: "matter.read",
        type: "matter",
      },
      identity: {
        id: "brique",
      },
      status,
      payload,
      error,
    },
  };
}

function matterEvent(
  event: "matter_written" | "matter_deleted",
  matterId: string,
  params: Record<string, unknown> = {}
) {
  return {
    kind: "intention" as const,
    ts: "2026-04-25T00:00:00.000Z",
    intention: {
      intention_id: `evt-${event}-${matterId}`,
      await_response: false,
      to: {
        context: "@ui_main:/matter",
        cap: "ui",
        type: "user",
      },
      from: {
        context: "/root/text_context",
        cap: "matter_event",
        type: "matter",
      },
      identity: {
        id: "brique",
      },
      params: {
        event,
        matter_id: matterId,
        substance_mode: "brique",
        ...params,
      },
    },
  };
}

function waitForMicrotasks(): Promise<void> {
  return new Promise((resolve) => {
    setImmediate(resolve);
  });
}

test("TransportManager uses default/named transports and throws for unknown names", () => {
  const manager = new TransportManager();
  const first = new ManualTransport();
  const second = new ManualTransport();

  manager.register("first", first);
  manager.register("second", second);

  assert.equal(manager.get(), first);
  assert.equal(manager.get("second"), second);
  assert.throws(() => manager.get("missing"), /Transport not found/);

  manager.clear();
  assert.equal(first.closed, true);
  assert.equal(second.closed, true);
});

test("PulseRuntime uses default transport when no transportName is provided", async () => {
  const first = new ManualTransport();
  const second = new ManualTransport();
  const manager = new TransportManager();
  manager.register("first", first);
  manager.register("second", second);

  const pulse = new PulseRuntime({ transportManager: manager });
  await pulse.emitIntention(sampleIntentionInput());

  assert.equal(first.sent.length, 1);
  assert.equal(second.sent.length, 0);
  pulse.teardown();
});

test("PulseRuntime exposes default UI runtime identity", () => {
  const transport = new ManualTransport();
  const manager = new TransportManager();
  manager.register("default", transport);
  const pulse = new PulseRuntime({
    transportManager: manager,
    transportName: "default",
  });

  assert.equal(pulse.ui.getName(), "main");
  assert.equal(pulse.ui.getAddressPrefix(), "@ui_main");
  assert.equal(pulse.ui.addressForPath("/text/result"), "@ui_main:/text/result");
  assert.equal(pulse.ui.addressForPath("text/result"), "@ui_main:/text/result");

  pulse.teardown();
});

test("PulseRuntime supports custom UI runtime identity", () => {
  const transport = new ManualTransport();
  const manager = new TransportManager();
  manager.register("default", transport);
  const pulse = new PulseRuntime({
    transportManager: manager,
    transportName: "default",
    uiName: "panel",
  });

  assert.equal(pulse.ui.getName(), "panel");
  assert.equal(pulse.ui.getAddressPrefix(), "@ui_panel");
  assert.equal(pulse.ui.addressForPath("/slot"), "@ui_panel:/slot");

  pulse.teardown();
});

test("PulseRuntime supports explicit UI address prefix", () => {
  const transport = new ManualTransport();
  const manager = new TransportManager();
  manager.register("default", transport);
  const pulse = new PulseRuntime({
    transportManager: manager,
    transportName: "default",
    uiName: "panel",
    uiAddressPrefix: "custom_panel",
  });

  assert.equal(pulse.ui.getName(), "panel");
  assert.equal(pulse.ui.getAddressPrefix(), "@custom_panel");
  assert.equal(pulse.ui.addressForPath("slot"), "@custom_panel:/slot");

  pulse.teardown();
});

test("PulseRuntime uses named transport and throws for unknown transport", async () => {
  const first = new ManualTransport();
  const second = new ManualTransport();
  const manager = new TransportManager();
  manager.register("first", first);
  manager.register("second", second);

  const pulse = new PulseRuntime({
    transportManager: manager,
    transportName: "second",
  });
  await pulse.emitIntention(sampleIntentionInput());

  assert.equal(first.sent.length, 0);
  assert.equal(second.sent.length, 1);
  pulse.teardown();

  assert.throws(
    () =>
      new PulseRuntime({
        transportManager: manager,
        transportName: "missing",
      }),
    /Transport not found/
  );
});

test("PulseRuntime teardown closes selected transport and clears subscribers", () => {
  const transport = new ManualTransport();
  const manager = new TransportManager();
  manager.register("default", transport);
  const pulse = new PulseRuntime({
    transportManager: manager,
    transportName: "default",
  });
  let calls = 0;

  pulse.subscribeToIntention("i-1", () => {
    calls += 1;
  });

  pulse.teardown();
  transport.receive(sampleResponse("i-1"));

  assert.equal(transport.closed, true);
  assert.equal(calls, 0);
});
