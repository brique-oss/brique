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
  createCapabilityClient,
  createDefaultRawResolver,
  createPulseRuntimeBridge,
  createBriqueSubstrate,
  executeThroughBridge,
  formatBriqueCapabilityIntention,
  formatBriqueCapabilityParams,
  listBriqueCapabilityContracts,
  meaningElementKeyId,
  parseBriqueCapabilityResponse,
  resolveDefaultRawRead,
  resolveDefaultRawReadResolution,
  toPulseUIIntentionInput,
  transformProjection,
  type CanonicalKey,
  type ExecuteInput,
  type PulseBridge,
} from "../substrate.ts";

const key: CanonicalKey = {
  context: "/root",
  kind: "read.structure",
  id: "current",
};

function responseBridge(
  responseFor: (input: ExecuteInput, call: number) => unknown
): PulseBridge & { calls: ExecuteInput[] } {
  const calls: ExecuteInput[] = [];
  const responses = new Map<string, unknown>();
  const listeners = new Map<string, (message: unknown) => void>();

  return {
    calls,
    prepare(input) {
      calls.push(input);
      const intentionId = `i-${calls.length}`;
      responses.set(intentionId, responseFor(input, calls.length));
      return {
        intentionId,
        // Response delivery is scheduled from send(), which fires only
        // after the caller has already subscribed — mirrors the real
        // WebSocketTransport timing where a fast reply can otherwise
        // arrive before a listener registered after emit() returns.
        send: () =>
          new Promise<void>((resolve) => {
            queueMicrotask(() => {
              const listener = listeners.get(intentionId);
              listener?.({
                response: {
                  intention_id: intentionId,
                  ...(responses.get(intentionId) as Record<string, unknown>),
                },
              });
              resolve();
            });
          }),
      };
    },
    subscribe(intentionId, callback) {
      listeners.set(intentionId, callback);
      return () => {
        listeners.delete(intentionId);
      };
    },
  };
}

test("executeThroughBridge correlates the matching response", async () => {
  const listeners = new Map<string, (message: unknown) => void>();
  const bridge: PulseBridge = {
    prepare: () => ({
      intentionId: "i-1",
      send: () =>
        new Promise<void>((resolve) => {
          queueMicrotask(() => {
            const listener = listeners.get("i-1");
            listener?.({ intention_id: "other", status: "ok" });
            listener?.({ intention_id: "i-1", status: "ok", payload: { value: 1 } });
            resolve();
          });
        }),
    }),
    subscribe: (intentionId, callback) => {
      listeners.set(intentionId, callback);
      return () => {
        listeners.delete(intentionId);
      };
    },
  };

  const result = await executeThroughBridge(bridge, {
    context: "/root",
    capability: "read.state",
  });

  assert.equal(result.intentionId, "i-1");
  assert.deepEqual(result.response, {
    intention_id: "i-1",
    status: "ok",
    payload: { value: 1 },
  });
});

test("executeThroughBridge subscribes before send, so a response delivered synchronously inside send() is not dropped", async () => {
  // Regression test for a real race: an earlier implementation called
  // bridge.emit(input) and only wired bridge.subscribe() in the .then()
  // that followed, so a response arriving before that second microtask ran
  // was silently lost (no buffering in the subscription registry) and the
  // caller timed out despite the engine having answered correctly.
  let subscribedBeforeSend = false;
  const bridge: PulseBridge = {
    prepare: () => ({
      intentionId: "i-1",
      send: () => {
        // Deliver the response synchronously, inside send() itself — the
        // tightest possible race: if subscribe() were wired after prepare()
        // resolves rather than before send() is even called, this callback
        // would already have nowhere to land.
        subscribedBeforeSend = deliverCalled;
        deliver?.({ intention_id: "i-1", status: "ok", payload: { value: 42 } });
        return Promise.resolve();
      },
    }),
    subscribe: (_intentionId, callback) => {
      deliverCalled = true;
      deliver = callback;
      return () => {
        deliver = undefined;
      };
    },
  };
  let deliver: ((message: unknown) => void) | undefined;
  let deliverCalled = false;

  const result = await executeThroughBridge(bridge, {
    context: "/root",
    capability: "read.state",
  });

  assert.equal(subscribedBeforeSend, true);
  assert.deepEqual(result.response, {
    intention_id: "i-1",
    status: "ok",
    payload: { value: 42 },
  });
});

test("executeThroughBridge stays subscribed through intermediate running responses and resolves on the final one", async () => {
  let deliver: ((message: unknown) => void) | undefined;
  const bridge: PulseBridge = {
    prepare: () => ({
      intentionId: "i-1",
      send: () => Promise.resolve(),
    }),
    subscribe: (_intentionId, callback) => {
      deliver = callback;
      return () => {
        deliver = undefined;
      };
    },
  };

  const promise = executeThroughBridge(bridge, {
    context: "/root",
    capability: "read.state",
  });

  deliver?.({ intention_id: "i-1", status: "running", payload: {} });
  deliver?.({ intention_id: "i-1", status: "running", payload: {} });
  deliver?.({ intention_id: "i-1", status: "ok", payload: { value: 7 } });

  const result = await promise;
  assert.deepEqual(result.response, {
    intention_id: "i-1",
    status: "ok",
    payload: { value: 7 },
  });
});

test("executeThroughBridge times out if only running responses ever arrive", async () => {
  let deliver: ((message: unknown) => void) | undefined;
  const bridge: PulseBridge = {
    prepare: () => ({
      intentionId: "i-1",
      send: () => Promise.resolve(),
    }),
    subscribe: (_intentionId, callback) => {
      deliver = callback;
      return () => {
        deliver = undefined;
      };
    },
  };

  const promise = executeThroughBridge(
    bridge,
    { context: "/root", capability: "read.state" },
    20
  );

  deliver?.({ intention_id: "i-1", status: "running", payload: {} });

  await assert.rejects(() => promise, /BriqueSubstrate execute timed out/);
});

test("get executes a fresh intention for every call", async () => {
  const bridge = responseBridge((_input, call) => ({
    status: "ok",
    payload: { call },
  }));
  const substrate = createBriqueSubstrate({
    bridge,
    rawResolver: createDefaultRawResolver(),
  });

  const first = await substrate.get(key);
  const second = await substrate.get(key);

  assert.deepEqual(first, { status: "ok", payload: { call: 1 } });
  assert.deepEqual(second, { status: "ok", payload: { call: 2 } });
  assert.equal(bridge.calls.length, 2);
});

test("concurrent get calls are not deduplicated", async () => {
  const bridge = responseBridge((_input, call) => ({
    status: "ok",
    payload: { call },
  }));
  const substrate = createBriqueSubstrate({
    bridge,
    rawResolver: createDefaultRawResolver(),
  });

  const [first, second] = await Promise.all([
    substrate.get(key),
    substrate.get(key),
  ]);

  assert.equal(bridge.calls.length, 2);
  assert.notDeepEqual(first, second);
});

test("get reports absent keys and Brique response errors", async () => {
  const absent = createBriqueSubstrate({
    bridge: responseBridge(() => ({ status: "ok" })),
    rawResolver: { resolve: () => undefined },
  });
  assert.deepEqual(await absent.get(key), {
    status: "absent",
    payload: undefined,
  });

  const failed = createBriqueSubstrate({
    bridge: responseBridge(() => ({
      status: "error",
      error: { code: "failed", message: "No structure" },
    })),
    rawResolver: createDefaultRawResolver(),
  });
  const failedResult = await failed.get(key);
  assert.equal(failedResult.status, "error");
  if (failedResult.status === "error") {
    assert.equal(failedResult.error.code, "failed");
    assert.equal(failedResult.error.message, "No structure");
  }
});

test("project reads and transforms again for every call", async () => {
  const flowKey: CanonicalKey = {
    context: "/root",
    kind: "read.meaning",
    id: "flow-source",
  };
  const bridge = responseBridge((_input, call) => ({
    status: "ok",
    payload: {
      call,
      functional: {
        "#root": {},
      },
    },
  }));
  const substrate = createBriqueSubstrate({
    bridge,
    rawResolver: createDefaultRawResolver(),
  });

  const first = await substrate.project(flowKey, "flow");
  const second = await substrate.project(flowKey, "flow");

  assert.equal(bridge.calls.length, 2);
  assert.notDeepEqual(first, second);
});

test("projection dispatch exposes the three derived transform families", () => {
  const input = {
    key,
    raw: {},
  };

  assert.throws(
    () => transformProjection({ ...input, kind: "semantic" }),
    /Semantic projection transform is not implemented/
  );
  assert.throws(
    () => transformProjection({ ...input, kind: "trace" }),
    /Trace projection transform is not implemented/
  );
  assert.doesNotThrow(() =>
    transformProjection({
      ...input,
      kind: "flow",
      raw: { functional: { "#root": {} } },
    })
  );
});

test("mutate executes exactly one intention and retains no refresh contract", async () => {
  const bridge = responseBridge(() => ({
    status: "ok",
    payload: { updated: true },
  }));
  const substrate = createBriqueSubstrate({
    bridge,
    rawResolver: createDefaultRawResolver(),
  });

  const result = await substrate.mutate({
    context: "/root",
    capability: "structure.patch",
    params: { structure_id: "main", patch: [] },
  });

  assert.equal(bridge.calls.length, 1);
  assert.deepEqual(result.response, {
    intention_id: "i-1",
    status: "ok",
    payload: { updated: true },
  });
});

test("Pulse runtime bridge remains a thin execution adapter", async () => {
  const emitted: unknown[] = [];
  const listeners = new Map<string, (message: unknown) => void>();
  const pulse = {
    prepareUIIntention(input: unknown) {
      emitted.push(input);
      return { intentionId: "i-1", message: input };
    },
    async sendPrepared(prepared: { intentionId: string }) {
      queueMicrotask(() => {
        listeners.get(prepared.intentionId)?.({
          intention_id: prepared.intentionId,
          status: "ok",
          payload: {},
        });
      });
    },
    subscribeToIntention(id: string, callback: (message: unknown) => void) {
      listeners.set(id, callback);
      return () => {
        listeners.delete(id);
      };
    },
  };
  const bridge = createPulseRuntimeBridge(pulse);

  await executeThroughBridge(bridge, {
    context: "/root",
    capability: "read.structure",
    params: { depth: 1 },
  });

  assert.deepEqual(emitted, [{
    to: { context: "/root", cap: "read.structure", type: "reflexive" },
    sourcePathUI: undefined,
    targetPathUI: undefined,
    params: { depth: 1 },
    awaitResponse: undefined,
  }]);
  assert.equal("onMatterEvent" in bridge, false);
});

test("toPulseUIIntentionInput resolves capability address types", () => {
  assert.deepEqual(
    toPulseUIIntentionInput({
      context: "/root",
      capability: "matter.read",
      params: { matter_id: "seed" },
    }),
    {
      to: { context: "/root", cap: "matter.read", type: "matter" },
      sourcePathUI: undefined,
      targetPathUI: undefined,
      params: { matter_id: "seed" },
      awaitResponse: undefined,
    }
  );
});

test("default resolver maps canonical keys and contains no cache policy", () => {
  const resolution = resolveDefaultRawReadResolution(key);
  assert.ok(resolution && "input" in resolution);
  assert.equal("cache" in resolution, false);
  assert.deepEqual(resolveDefaultRawRead(key), {
    context: "/root",
    capability: "read.structure",
    params: { depth: 1, max_per_path: 50 },
  });
});

test("targeted read.meaning resolves the requested element descriptor", () => {
  const capacityKey: CanonicalKey = {
    context: "/root",
    kind: "read.meaning",
    id: meaningElementKeyId("capacity", "counter"),
  };
  const resolution = resolveDefaultRawReadResolution(capacityKey);

  assert.ok(resolution && "input" in resolution);
  assert.deepEqual(resolution.input, {
    context: "/root",
    capability: "read.meaning",
    params: {
      input: [{
        element_kind: "capacity",
        element_name: "counter",
        sections: ["brique", "objective", "subjective", "functional"],
        include_resolution: true,
      }],
    },
  });
  assert.deepEqual(
    resolution.parse?.({
      intention_id: "i-1",
      status: "ok",
      payload: {
        result: [
          {
            ok: true,
            element_kind: "capacity",
            element_name: "counter",
            descriptor: {
              functional: {
                "#root": {},
              },
            },
          },
        ],
      },
    }),
    {
      functional: {
        "#root": {},
      },
    }
  );
});

test("default resolver maps Matter and Structure identifiers", () => {
  assert.deepEqual(
    resolveDefaultRawRead({
      context: "/root",
      kind: "matter.read",
      id: "seed",
    }),
    {
      context: "/root",
      capability: "matter.read",
      params: { matter_id: "seed" },
    }
  );
  assert.deepEqual(
    resolveDefaultRawRead({
      context: "/root",
      kind: "structure.read",
      id: "main",
    }),
    {
      context: "/root",
      capability: "structure.read",
      params: { structure_id: "main" },
    }
  );
});

test("capability contracts format intentions and parse responses", () => {
  assert.ok(listBriqueCapabilityContracts().length >= 32);
  assert.deepEqual(
    formatBriqueCapabilityParams("matter.read", { matter_id: "seed" }),
    { matter_id: "seed" }
  );
  assert.deepEqual(
    formatBriqueCapabilityIntention({
      context: "/root",
      capability: "matter.read",
      params: { matter_id: "seed" },
    }),
    {
      context: "/root",
      capability: "matter.read",
      params: { matter_id: "seed" },
    }
  );
  assert.deepEqual(
    parseBriqueCapabilityResponse("matter.exists", {
      status: "ok",
      intention_id: "i-1",
      payload: { matter_id: "seed", exist: true },
    }).payload,
    { matter_id: "seed", exist: true }
  );
});

test("every declared capability is accepted by contracts and the default resolver", () => {
  for (const contract of listBriqueCapabilityContracts()) {
    const capabilityKey: CanonicalKey = {
      context: "/root",
      kind: contract.capability,
      id: "target",
    };

    assert.ok(
      resolveDefaultRawReadResolution(capabilityKey),
      `${contract.capability} must resolve`
    );
    assert.deepEqual(
      formatBriqueCapabilityParams(contract.capability, {}),
      {},
      `${contract.capability} must accept an empty generic parameter set`
    );
    assert.equal(
      parseBriqueCapabilityResponse(
        contract.capability,
        { intention_id: "i-1", status: "ok", payload: {} },
        { strictPayload: true }
      ).ok,
      true,
      `${contract.capability} must parse its declared envelope`
    );
  }
});

test("CapabilityClient executes and parses typed capability responses", async () => {
  const bridge = responseBridge((input) => ({
    status: "ok",
    payload: {
      matter_id: input.params?.matter_id,
      exist: true,
    },
  }));
  const substrate = createBriqueSubstrate({
    bridge,
    rawResolver: createDefaultRawResolver(),
  });
  const client = createCapabilityClient(substrate);

  const result = await client.matter.exists("/root", { matter_id: "seed" });

  assert.equal(result.ok, true);
  assert.deepEqual(result.payload, { matter_id: "seed", exist: true });
  assert.equal(bridge.calls.length, 1);
});
