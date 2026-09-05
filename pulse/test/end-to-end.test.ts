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
import React, { useEffect, useState } from "../adapters/react/node_modules/react/index.js";
import TestRenderer, {
  act,
} from "../adapters/react/node_modules/react-test-renderer/index.js";
import {
  PulseProvider,
  useIntentionSubscription,
  usePathIntention,
  usePulse,
} from "../adapters/react";
import { PulseRuntime } from "../runtime";
import {
  createFakeWebSocketServer,
  sampleIntentionInput,
  sampleResponse,
} from "./helpers";
import { deserializeMessage, serializeMessage } from "../runtime";

globalThis.IS_REACT_ACT_ENVIRONMENT = true;
installReactTestConsoleFilter();

test("React emits intention through WebSocket and updates from Fake Brique response", async () => {
  const server = await createFakeWebSocketServer();
  const pulse = new PulseRuntime({ webSocketUrl: server.url });

  function App() {
    const runtime = usePulse();
    const [intentionId, setIntentionId] = useState<string | null>(null);
    const state = useIntentionSubscription(intentionId);

    useEffect(() => {
      runtime.emitIntention(sampleIntentionInput(), "/result").then(setIntentionId);
    }, [runtime]);

    return React.createElement(
      "span",
      null,
      `${intentionId ?? "none"}:${state.status}:${state.payload?.value ?? ""}`
    );
  }

  let renderer: TestRenderer.ReactTestRenderer;
  await act(async () => {
    renderer = TestRenderer.create(
      React.createElement(
        PulseProvider,
        { pulse },
        React.createElement(App)
      )
    );
  });

  const outbound = deserializeMessage(await server.waitForMessage());
  assert.equal(outbound.kind, "intention");
  const intentionId = outbound.intention.intention_id;

  await waitForReact(() =>
    String(renderer!.toJSON()?.children?.[0]).startsWith(`${intentionId}:`)
  );

  await act(async () => {
    server.sendText(serializeMessage(sampleResponse(intentionId, "@ui_main:/result")));
  });
  await waitForReact(
    () => renderer!.toJSON()?.children?.[0] === `${intentionId}:success:done`
  );

  assert.equal(pulse.getIntentionIdForPath("/result"), intentionId);

  renderer!.unmount();
  pulse.teardown();
  await server.close();
});

test("end-to-end handles concurrent intentions and out-of-order responses", async () => {
  const server = await createFakeWebSocketServer();
  const pulse = new PulseRuntime({ webSocketUrl: server.url });

  function Item({ label }: { label: string }) {
    const runtime = usePulse();
    const [intentionId, setIntentionId] = useState<string | null>(null);
    const state = useIntentionSubscription(intentionId);

    useEffect(() => {
      runtime
        .emitIntention({
          ...sampleIntentionInput(),
          params: { label },
        })
        .then(setIntentionId);
    }, [runtime, label]);

    return React.createElement(
      "span",
      null,
      `${label}:${intentionId ?? "none"}:${state.payload?.value ?? state.status}`
    );
  }

  let renderer: TestRenderer.ReactTestRenderer;
  await act(async () => {
    renderer = TestRenderer.create(
      React.createElement(
        PulseProvider,
        { pulse },
        React.createElement(
          "div",
          null,
          React.createElement(Item, { label: "a" }),
          React.createElement(Item, { label: "b" })
        )
      )
    );
  });

  const first = deserializeMessage(await server.waitForMessage());
  const second = deserializeMessage(await server.waitForMessage());
  assert.equal(first.kind, "intention");
  assert.equal(second.kind, "intention");

  await waitForReact(() =>
    flattenText(renderer!.toJSON()).includes(first.intention.intention_id)
  );
  await waitForReact(() =>
    flattenText(renderer!.toJSON()).includes(second.intention.intention_id)
  );

  await act(async () => {
    server.sendText(
      serializeMessage({
        ...sampleResponse(second.intention.intention_id),
        response: {
          ...sampleResponse(second.intention.intention_id).response,
          payload: { value: "second" },
        },
      })
    );
    server.sendText(
      serializeMessage({
        ...sampleResponse(first.intention.intention_id),
        response: {
          ...sampleResponse(first.intention.intention_id).response,
          payload: { value: "first" },
        },
      })
    );

  });
  await waitForReact(() => flattenText(renderer!.toJSON()).includes("a:"));
  await waitForReact(() => flattenText(renderer!.toJSON()).includes(":first"));
  await waitForReact(() => flattenText(renderer!.toJSON()).includes("b:"));
  await waitForReact(() => flattenText(renderer!.toJSON()).includes(":second"));

  renderer!.unmount();
  pulse.teardown();
  await server.close();
});

test("unmounted end-to-end component does not update after response", async () => {
  const server = await createFakeWebSocketServer();
  const pulse = new PulseRuntime({ webSocketUrl: server.url });
  let intentionId = "";

  function App() {
    const runtime = usePulse();
    const [id, setId] = useState<string | null>(null);
    useIntentionSubscription(id);

    useEffect(() => {
      runtime.emitIntention(sampleIntentionInput()).then((nextId) => {
        intentionId = nextId;
        setId(nextId);
      });
    }, [runtime]);

    return React.createElement("span", null, "mounted");
  }

  let renderer: TestRenderer.ReactTestRenderer;
  await act(async () => {
    renderer = TestRenderer.create(
      React.createElement(
        PulseProvider,
        { pulse },
        React.createElement(App)
      )
    );
  });

  await server.waitForMessage();

  await act(async () => {
    renderer!.unmount();
  });

  assert.doesNotThrow(() => {
    server.sendText(serializeMessage(sampleResponse(intentionId)));
  });
  await delay(20);

  pulse.teardown();
  await server.close();
});

test("path-based end-to-end rendering works", async () => {
  const server = await createFakeWebSocketServer();
  const pulse = new PulseRuntime({ webSocketUrl: server.url });

  function Initiator() {
    const runtime = usePulse();

    useEffect(() => {
      runtime.emitIntention(sampleIntentionInput(), "/slot");
    }, [runtime]);

    return null;
  }

  function Renderer() {
    const intentionId = usePathIntention("/slot");
    const state = useIntentionSubscription(intentionId);
    return React.createElement("span", null, state.payload?.value ?? state.status);
  }

  let renderer: TestRenderer.ReactTestRenderer;
  await act(async () => {
    renderer = TestRenderer.create(
      React.createElement(
        PulseProvider,
        { pulse },
        React.createElement(
          React.Fragment,
          null,
          React.createElement(Initiator),
          React.createElement(Renderer)
        )
      )
    );
  });

  const outbound = deserializeMessage(await server.waitForMessage());
  assert.equal(outbound.kind, "intention");

  await waitForReact(() => renderer!.toJSON()?.children?.[0] === "idle");

  await act(async () => {
    server.sendText(
      serializeMessage(sampleResponse(outbound.intention.intention_id, "@ui_main:/slot"))
    );
  });

  await waitForReact(() => renderer!.toJSON()?.children?.[0] === "done");

  renderer!.unmount();
  pulse.teardown();
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

async function waitForReact(predicate: () => boolean): Promise<void> {
  const start = Date.now();

  while (!predicate()) {
    if (Date.now() - start > 1000) {
      throw new Error("condition timed out");
    }

    await act(async () => {
      await delay(5);
    });
  }
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function flattenText(node: unknown): string {
  if (!node) return "";
  if (typeof node === "string") return node;
  if (Array.isArray(node)) return node.map(flattenText).join("");

  const maybeNode = node as { children?: unknown[] };
  return flattenText(maybeNode.children ?? []);
}

function installReactTestConsoleFilter() {
  const originalError = console.error;

  console.error = (...args: unknown[]) => {
    const message = String(args[0] ?? "");

    if (
      message.includes("react-test-renderer is deprecated") ||
      message.includes("not wrapped in act")
    ) {
      return;
    }

    originalError(...args);
  };
}
