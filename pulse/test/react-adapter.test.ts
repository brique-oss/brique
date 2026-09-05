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
import React, { StrictMode } from "../adapters/react/node_modules/react/index.js";
import TestRenderer, {
  act,
} from "../adapters/react/node_modules/react-test-renderer/index.js";
import {
  PulseProvider,
  useDisplayMode,
  useFlow,
  useIntentionSubscription,
  useMatter,
  useMatterEvent,
  useMatterSubscription,
  usePathFlow,
  useProjectionPath,
  usePulse,
  useSetDisplayMode,
  useBriqueAction,
} from "../adapters/react";
import type { CirculationMessage, Transport, Unsubscribe } from "../runtime";
import { PulseRuntime, TransportManager } from "../runtime";
import { ManualTransport, sampleResponse } from "./helpers";

globalThis.IS_REACT_ACT_ENVIRONMENT = true;
installReactTestConsoleFilter();

test("PulseProvider provides runtime", async () => {
  const pulse = { id: "pulse" };

  function Reader() {
    return React.createElement("span", null, usePulse() === pulse ? "ok" : "bad");
  }

  let renderer: TestRenderer.ReactTestRenderer;
  await act(async () => {
    renderer = TestRenderer.create(
      React.createElement(
        PulseProvider,
        { pulse: pulse as never },
        React.createElement(Reader)
      )
    );
  });

  assert.equal(renderer!.toJSON()?.children?.[0], "ok");
  renderer!.unmount();
});

test("usePulse outside provider throws", async () => {
  function Reader() {
    usePulse();
    return React.createElement("span", null, "bad");
  }

  await assert.rejects(async () => {
    await act(async () => {
      TestRenderer.create(React.createElement(Reader));
    });
  }, /usePulse must be used inside a PulseProvider/);
});

test("useIntentionSubscription subscribes, updates, and unsubscribes", async () => {
  const { pulse, transport } = createRuntimePulse();

  function Reader() {
    const state = useIntentionSubscription("i-1");
    return React.createElement("span", null, state.status);
  }

  let renderer: TestRenderer.ReactTestRenderer;
  await act(async () => {
    renderer = TestRenderer.create(
      React.createElement(
        PulseProvider,
        { pulse },
        React.createElement(Reader)
      )
    );
  });

  assert.equal(renderer!.toJSON()?.children?.[0], "idle");

  await act(async () => {
    transport.receive(sampleResponse("i-1"));
  });

  assert.equal(renderer!.toJSON()?.children?.[0], "success");

  await act(async () => {
    renderer!.unmount();
  });

  pulse.teardown();
});

test("useIntentionSubscription switches intentionId and ignores stale callbacks", async () => {
  const { pulse, transport } = createRuntimePulse();

  function Reader({ intentionId }: { intentionId: string }) {
    const state = useIntentionSubscription(intentionId);
    return React.createElement("span", null, `${intentionId}:${state.status}`);
  }

  let renderer: TestRenderer.ReactTestRenderer;
  await act(async () => {
    renderer = TestRenderer.create(
      React.createElement(
        PulseProvider,
        { pulse },
        React.createElement(Reader, { intentionId: "i-1" })
      )
    );
  });

  await act(async () => {
    renderer!.update(
      React.createElement(
        PulseProvider,
        { pulse },
        React.createElement(Reader, { intentionId: "i-2" })
      )
    );
  });

  await act(async () => {
    transport.receive(sampleResponse("i-1"));
  });
  assert.equal(renderer!.toJSON()?.children?.[0], "i-2:idle");

  await act(async () => {
    transport.receive(sampleResponse("i-2"));
  });
  assert.equal(renderer!.toJSON()?.children?.[0], "i-2:success");

  renderer!.unmount();
  pulse.teardown();
});

test("useIntentionSubscription does not duplicate subscriptions in StrictMode", async () => {
  const { pulse, transport } = createRuntimePulse();

  function Reader() {
    useIntentionSubscription("i-1");
    return React.createElement("span", null, "ok");
  }

  let renderer: TestRenderer.ReactTestRenderer;
  await act(async () => {
    renderer = TestRenderer.create(
      React.createElement(
        StrictMode,
        null,
        React.createElement(
          PulseProvider,
          { pulse },
          React.createElement(Reader)
        )
      )
    );
  });

  await act(async () => {
    transport.receive(sampleResponse("i-1"));
  });

  assert.equal(renderer!.toJSON()?.children?.[0], "ok");
  renderer!.unmount();
  pulse.teardown();
});

test("useFlow exposes runtime flow snapshots", async () => {
  const { pulse, transport } = createRuntimePulse();

  function Reader() {
    const flow = useFlow("i-1");
    return React.createElement("span", null, `${flow.status}:${String(flow.payload)}`);
  }

  let renderer: TestRenderer.ReactTestRenderer;
  await act(async () => {
    renderer = TestRenderer.create(
      React.createElement(
        PulseProvider,
        { pulse },
        React.createElement(Reader)
      )
    );
  });

  assert.equal(renderer!.toJSON()?.children?.[0], "idle:null");

  await act(async () => {
    transport.receive(sampleResponse("i-1"));
  });

  assert.equal(renderer!.toJSON()?.children?.[0], "success:[object Object]");
  pulse.teardown();
});

test("useProjectionPath exposes active path snapshots", async () => {
  const { pulse } = createRuntimePulse();

  function Reader() {
    const path = useProjectionPath("/panel");
    return React.createElement(
      "span",
      null,
      `${path.pathUI}:${path.active}:${path.intentionId ?? "none"}`
    );
  }

  let renderer: TestRenderer.ReactTestRenderer;
  await act(async () => {
    renderer = TestRenderer.create(
      React.createElement(
        PulseProvider,
        { pulse },
        React.createElement(Reader)
      )
    );
  });

  assert.equal(renderer!.toJSON()?.children?.[0], "/panel:false:none");

  await act(async () => {
    pulse.activatePath("/panel", "i-1");
  });

  assert.equal(renderer!.toJSON()?.children?.[0], "/panel:true:i-1");

  await act(async () => {
    pulse.deactivatePath("/panel");
  });

  assert.equal(renderer!.toJSON()?.children?.[0], "/panel:false:none");
  pulse.teardown();
});

test("usePathFlow follows the flow bound to a UI path", async () => {
  const { pulse, transport } = createRuntimePulse();

  function Reader() {
    const flow = usePathFlow("/slot");
    return React.createElement("span", null, `${flow.intentionId ?? "none"}:${flow.status}`);
  }

  let renderer: TestRenderer.ReactTestRenderer;
  await act(async () => {
    renderer = TestRenderer.create(
      React.createElement(
        PulseProvider,
        { pulse },
        React.createElement(Reader)
      )
    );
  });

  assert.equal(renderer!.toJSON()?.children?.[0], "none:idle");

  await act(async () => {
    pulse.activatePath("/slot", "i-1");
  });

  assert.equal(renderer!.toJSON()?.children?.[0], "none:idle");

  await act(async () => {
    transport.receive(sampleResponse("i-1", "@ui_main:/slot"));
  });

  assert.equal(renderer!.toJSON()?.children?.[0], "i-1:success");
  pulse.teardown();
});

test("useDisplayMode and useSetDisplayMode bind local projection display state", async () => {
  const { pulse, transport } = createRuntimePulse();
  let setDisplayMode!: ReturnType<typeof useSetDisplayMode>;

  function Reader() {
    const mode = useDisplayMode();
    setDisplayMode = useSetDisplayMode();
    return React.createElement("span", null, mode);
  }

  let renderer: TestRenderer.ReactTestRenderer;
  await act(async () => {
    renderer = TestRenderer.create(
      React.createElement(
        PulseProvider,
        { pulse },
        React.createElement(Reader)
      )
    );
  });

  assert.equal(renderer!.toJSON()?.children?.[0], "idle");

  await act(async () => {
    setDisplayMode("debug");
  });

  assert.equal(renderer!.toJSON()?.children?.[0], "debug");
  assert.equal(transport.sent.length, 0);
  pulse.teardown();
});

test("PulseProvider isolates display state between runtimes", async () => {
  const first = createRuntimePulse();
  const second = createRuntimePulse();
  let setFirst!: ReturnType<typeof useSetDisplayMode>;

  function FirstReader() {
    const mode = useDisplayMode();
    setFirst = useSetDisplayMode();
    return React.createElement("span", { id: "first" }, `first:${mode}`);
  }

  function SecondReader() {
    const mode = useDisplayMode();
    return React.createElement("span", { id: "second" }, `second:${mode}`);
  }

  let renderer: TestRenderer.ReactTestRenderer;
  await act(async () => {
    renderer = TestRenderer.create(
      React.createElement(
        "div",
        null,
        React.createElement(
          PulseProvider,
          { pulse: first.pulse },
          React.createElement(FirstReader)
        ),
        React.createElement(
          PulseProvider,
          { pulse: second.pulse },
          React.createElement(SecondReader)
        )
      )
    );
  });

  assert.deepEqual(readSpanTexts(renderer!), ["first:idle", "second:idle"]);

  await act(async () => {
    setFirst("preview");
  });

  assert.deepEqual(readSpanTexts(renderer!), ["first:preview", "second:idle"]);
  first.pulse.teardown();
  second.pulse.teardown();
});

test("useMatter reads through the runtime matter registry", async () => {
  const { pulse, transport } = createRuntimePulse();

  function Reader() {
    const matter = useMatter({
      context: "/root/text_context",
      matterId: "text_buffer",
    });
    return React.createElement(
      "span",
      null,
      `${matter.status}:${String((matter.data as { text?: string } | null)?.text ?? "none")}`
    );
  }

  let renderer: TestRenderer.ReactTestRenderer;
  await act(async () => {
    renderer = TestRenderer.create(
      React.createElement(
        PulseProvider,
        { pulse },
        React.createElement(Reader)
      )
    );
  });

  assert.equal(transport.sent.length, 1);
  assert.equal(transport.sent[0].intention.to.cap, "matter.read");
  assert.equal(renderer!.toJSON()?.children?.[0], "loading:none");

  await act(async () => {
    transport.receive(matterResponse(transport.sent[0].intention.intention_id, {
      data: { text: "hello" },
      revision: 1,
    }));
  });

  assert.equal(renderer!.toJSON()?.children?.[0], "ready:hello");
  pulse.teardown();
});

test("useMatter can observe without starting a read", async () => {
  const { pulse, transport } = createRuntimePulse();

  function Observer() {
    const matter = useMatter({
      context: "/root/text_context",
      matterId: "text_buffer",
    }, {
      autoRead: false,
    });
    return React.createElement("span", null, matter.status);
  }

  let renderer: TestRenderer.ReactTestRenderer;
  await act(async () => {
    renderer = TestRenderer.create(
      React.createElement(
        PulseProvider,
        { pulse },
        React.createElement(Observer)
      )
    );
  });

  assert.equal(transport.sent.length, 0);
  assert.equal(renderer!.toJSON()?.children?.[0], "idle");

  await act(async () => {
    const read = pulse.matter.read({
      context: "/root/text_context",
      matterId: "text_buffer",
    });
    transport.receive(matterResponse(transport.sent[0].intention.intention_id, {
      data: { text: "hello" },
      revision: 1,
    }));
    await read;
  });

  assert.equal(renderer!.toJSON()?.children?.[0], "ready");
  pulse.teardown();
});

test("useMatter shares an active read between identical consumers", async () => {
  const { pulse, transport } = createRuntimePulse();

  function Reader() {
    useMatter({
      context: "/root/text_context",
      matterId: "text_buffer",
    });
    return React.createElement("span", null, "reader");
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
          React.createElement(Reader),
          React.createElement(Reader)
        )
      )
    );
  });

  const key = {
    context: "/root/text_context",
    matterId: "text_buffer",
    readMode: null,
  };
  assert.equal(pulse.matter.getReaderCount(key), 2);
  assert.equal(pulse.matter.getSnapshot(key).status, "loading");

  await act(async () => {
    transport.receive(matterResponse(transport.sent[0].intention.intention_id, {
      data: { text: "shared" },
      revision: 1,
    }));
  });

  await act(async () => {
    renderer!.unmount();
  });

  assert.equal(pulse.matter.has(key), false);
  pulse.teardown();
});

test("useMatter keeps distinct read modes isolated", async () => {
  const { pulse, transport } = createRuntimePulse();

  function Reader({ readMode }: { readMode: string }) {
    const matter = useMatter(
      {
        context: "/root/text_context",
        matterId: "text_buffer",
      },
      { readMode }
    );
    return React.createElement(
      "span",
      null,
      `${readMode}:${matter.status}:${String((matter.data as { value?: string } | null)?.value ?? "none")}`
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
          React.createElement(Reader, { readMode: "data|brique" }),
          React.createElement(Reader, { readMode: "meta|brique" })
        )
      )
    );
  });

  assert.equal(countSentByCap(transport, "matter.read"), 2);
  assert.deepEqual(transport.sent.map((message) => {
    assert.equal(message.kind, "intention");
    return message.intention.params.read_mode;
  }), ["data|brique", "meta|brique"]);

  await act(async () => {
    transport.receive(matterResponse(transport.sent[0].intention.intention_id, {
      data: { value: "data" },
      revision: 1,
    }));
  });

  assert.deepEqual(readSpanTexts(renderer!), [
    "data|brique:ready:data",
    "meta|brique:loading:none",
  ]);

  renderer!.unmount();
  pulse.teardown();
});

test("useMatter does not recreate matter state after unmount and late response", async () => {
  const { pulse, transport } = createRuntimePulse();

  function Reader() {
    useMatter({
      context: "/root/text_context",
      matterId: "text_buffer",
    });
    return React.createElement("span", null, "reader");
  }

  let renderer: TestRenderer.ReactTestRenderer;
  await act(async () => {
    renderer = TestRenderer.create(
      React.createElement(
        PulseProvider,
        { pulse },
        React.createElement(Reader)
      )
    );
  });

  const key = {
    context: "/root/text_context",
    matterId: "text_buffer",
    readMode: null,
  };
  const intentionId = transport.sent[0].intention.intention_id;

  await act(async () => {
    renderer!.unmount();
  });

  assert.equal(pulse.matter.has(key), false);

  await act(async () => {
    transport.receive(matterResponse(intentionId, {
      data: { text: "late" },
      revision: 2,
    }));
  });

  assert.equal(pulse.matter.has(key), false);
  pulse.teardown();
});

test("useMatter releases the old key and ignores stale responses after address changes", async () => {
  const { pulse, transport } = createRuntimePulse();

  function Reader({ context, matterId }: { context: string; matterId: string }) {
    const matter = useMatter({
      context,
      matterId,
    });
    return React.createElement(
      "span",
      null,
      `${matter.address.context}:${matter.address.matterId}:${matter.status}:${String((matter.data as { value?: string } | null)?.value ?? "none")}`
    );
  }

  let renderer: TestRenderer.ReactTestRenderer;
  await act(async () => {
    renderer = TestRenderer.create(
      React.createElement(
        PulseProvider,
        { pulse },
        React.createElement(Reader, {
          context: "/root/text_context",
          matterId: "text_buffer",
        })
      )
    );
  });

  const textReadId = transport.sent[0].intention.intention_id;
  const textKey = {
    context: "/root/text_context",
    matterId: "text_buffer",
    readMode: null,
  };
  const counterKey = {
    context: "/root/counter_context",
    matterId: "counter_value",
    readMode: null,
  };

  await act(async () => {
    renderer!.update(
      React.createElement(
        PulseProvider,
        { pulse },
        React.createElement(Reader, {
          context: "/root/counter_context",
          matterId: "counter_value",
        })
      )
    );
  });

  assert.equal(pulse.matter.has(textKey), false);
  assert.equal(pulse.matter.has(counterKey), true);
  assert.equal(transport.sent.length, 2);

  await act(async () => {
    transport.receive(matterResponse(textReadId, {
      data: { value: "late-text" },
      revision: 2,
    }));
  });

  assert.equal(pulse.matter.has(textKey), false);
  assert.equal(renderer!.toJSON()?.children?.[0], "/root/counter_context:counter_value:loading:none");

  await act(async () => {
    transport.receive(matterResponse(transport.sent[1].intention.intention_id, {
      data: { value: "counter" },
      revision: 3,
    }));
  });

  assert.equal(renderer!.toJSON()?.children?.[0], "/root/counter_context:counter_value:ready:counter");

  renderer!.unmount();
  pulse.teardown();
});

test("useMatter rerenders only observers of the changed matter key", async () => {
  const { pulse, transport } = createRuntimePulse();
  const renders = {
    text: 0,
    counter: 0,
  };

  function Reader({
    context,
    matterId,
    label,
  }: {
    context: string;
    matterId: string;
    label: "text" | "counter";
  }) {
    renders[label] += 1;
    const matter = useMatter({
      context,
      matterId,
    });
    return React.createElement(
      "span",
      null,
      `${label}:${matter.status}:${String((matter.data as { value?: string | number } | null)?.value ?? "none")}`
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
          React.createElement(Reader, {
            context: "/root/text_context",
            matterId: "text_buffer",
            label: "text",
          }),
          React.createElement(Reader, {
            context: "/root/counter_context",
            matterId: "counter_value",
            label: "counter",
          })
        )
      )
    );
  });

  const afterMount = { ...renders };

  await act(async () => {
    transport.receive(matterResponse(transport.sent[0].intention.intention_id, {
      data: { value: "hello" },
      revision: 1,
    }));
  });

  assert.equal(renders.text, afterMount.text + 1);
  assert.equal(renders.counter, afterMount.counter);
  assert.deepEqual(readSpanTexts(renderer!), [
    "text:ready:hello",
    "counter:loading:none",
  ]);

  renderer!.unmount();
  pulse.teardown();
});

test("useMatter with subscribe option acquires and releases a matter subscription", async () => {
  const { pulse, transport } = createRuntimePulse();

  function Reader() {
    const matter = useMatter(
      {
        context: "/root/text_context",
        matterId: "text_buffer",
      },
      { subscribe: true }
    );
    return React.createElement(
      "span",
      null,
      `${matter.status}:${matter.subscribed}:${matter.subId ?? "none"}`
    );
  }

  let renderer: TestRenderer.ReactTestRenderer;
  await act(async () => {
    renderer = TestRenderer.create(
      React.createElement(
        PulseProvider,
        { pulse },
        React.createElement(Reader)
      )
    );
  });

  const read = findSentByCap(transport, "matter.read");
  const subscribe = findSentByCap(transport, "matter.subscribe");
  assert.ok(read);
  assert.ok(subscribe);

  await act(async () => {
    transport.receive(matterResponse(read.intention.intention_id, {
      data: { text: "hello" },
      revision: 1,
    }));
    transport.receive(matterResponse(subscribe.intention.intention_id, {
      sub_id: "sub-1",
    }));
  });

  assert.equal(renderer!.toJSON()?.children?.[0], "ready:true:sub-1");

  await act(async () => {
    renderer!.unmount();
  });

  const unsubscribe = findSentByCap(transport, "matter.unsubscribe");
  assert.ok(unsubscribe);

  await act(async () => {
    transport.receive(matterResponse(unsubscribe.intention.intention_id, {
      sub_id: "sub-1",
      removed: true,
    }));
  });

  assert.equal(pulse.matter.has({
    context: "/root/text_context",
    matterId: "text_buffer",
    readMode: null,
  }), false);
  pulse.teardown();
});

test("useMatterSubscription shares one Brique subscription between consumers", async () => {
  const { pulse, transport } = createRuntimePulse();

  function Reader() {
    const matter = useMatterSubscription({
      context: "/root/text_context",
      matterId: "text_buffer",
    });
    return React.createElement("span", null, matter.subscribed ? "subscribed" : "idle");
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
          React.createElement(Reader),
          React.createElement(Reader)
        )
      )
    );
  });

  const key = {
    context: "/root/text_context",
    matterId: "text_buffer",
    readMode: null,
  };

  assert.equal(pulse.matter.getSubscriberCount(key), 2);
  assert.equal(countSentByCap(transport, "matter.subscribe"), 1);

  const subscribe = findSentByCap(transport, "matter.subscribe");
  assert.ok(subscribe);

  await act(async () => {
    transport.receive(matterResponse(subscribe.intention.intention_id, {
      sub_id: "sub-1",
    }));
  });

  await act(async () => {
    renderer!.unmount();
  });

  assert.equal(countSentByCap(transport, "matter.unsubscribe"), 1);

  const unsubscribe = findSentByCap(transport, "matter.unsubscribe");
  assert.ok(unsubscribe);

  await act(async () => {
    transport.receive(matterResponse(unsubscribe.intention.intention_id, {
      sub_id: "sub-1",
      removed: true,
    }));
  });

  assert.equal(pulse.matter.has(key), false);
  pulse.teardown();
});

test("useMatterEvent invokes callback for routed matter events", async () => {
  const { pulse, transport } = createRuntimePulse();
  const events: string[] = [];

  function Reader() {
    const matter = useMatterEvent(
      {
        context: "/root/text_context",
        matterId: "text_buffer",
      },
      (event) => {
        events.push(`${event.event}:${event.revision ?? "none"}`);
      }
    );
    return React.createElement("span", null, matter.lastEvent?.event ?? "none");
  }

  let renderer: TestRenderer.ReactTestRenderer;
  await act(async () => {
    renderer = TestRenderer.create(
      React.createElement(
        PulseProvider,
        { pulse },
        React.createElement(Reader)
      )
    );
  });

  const subscribe = findSentByCap(transport, "matter.subscribe");
  assert.ok(subscribe);

  await act(async () => {
    transport.receive(matterResponse(subscribe.intention.intention_id, {
      sub_id: "sub-1",
    }));
  });

  await act(async () => {
    transport.receive(matterEvent("matter_written", "text_buffer", {
      sub_id: "sub-1",
      revision: 9,
    }));
  });

  assert.equal(renderer!.toJSON()?.children?.[0], "matter_written");
  assert.deepEqual(events, ["matter_written:9"]);

  await act(async () => {
    renderer!.unmount();
  });

  const unsubscribe = findSentByCap(transport, "matter.unsubscribe");
  assert.ok(unsubscribe);

  await act(async () => {
    transport.receive(matterResponse(unsubscribe.intention.intention_id, {
      sub_id: "sub-1",
      removed: true,
    }));
  });

  pulse.teardown();
});

test("useBriqueAction emits UI-origin intentions and tracks success", async () => {
  const { pulse, transport } = createRuntimePulse();
  let action!: ReturnType<typeof useBriqueAction>;

  function Reader() {
    action = useBriqueAction({
      to: {
        context: "/root/text_context",
        cap: "set_text",
        type: "user",
      },
      targetPathUI: "/text/result",
      params: {
        base: true,
      },
      identity: {
        id: "sandbox-user",
        kind: "human",
      },
    });

    return React.createElement("span", null, `${action.status}:${action.intentionId ?? "none"}`);
  }

  let renderer: TestRenderer.ReactTestRenderer;
  await act(async () => {
    renderer = TestRenderer.create(
      React.createElement(
        PulseProvider,
        { pulse },
        React.createElement(Reader)
      )
    );
  });

  let intentionId = "";
  await act(async () => {
    intentionId = await action.emit({ value: "hello" });
  });

  assert.equal(renderer!.toJSON()?.children?.[0], `success:${intentionId}`);
  assert.equal(transport.sent.length, 1);
  assert.equal(transport.sent[0].intention.intention_id, intentionId);
  assert.deepEqual(transport.sent[0].intention.from, {
    context: "@ui_main:/text/result",
    cap: "ui",
    type: "user",
  });
  assert.deepEqual(transport.sent[0].intention.params, {
    base: true,
    value: "hello",
  });
  assert.equal(pulse.getIntentionIdForPath("/text/result"), intentionId);

  pulse.teardown();
});

test("useBriqueAction reset returns action state to idle", async () => {
  const { pulse } = createRuntimePulse();
  let action!: ReturnType<typeof useBriqueAction>;

  function Reader() {
    action = useBriqueAction({
      to: {
        context: "/root/text_context",
        cap: "set_text",
        type: "user",
      },
    });

    return React.createElement("span", null, action.status);
  }

  let renderer: TestRenderer.ReactTestRenderer;
  await act(async () => {
    renderer = TestRenderer.create(
      React.createElement(
        PulseProvider,
        { pulse },
        React.createElement(Reader)
      )
    );
  });

  await act(async () => {
    await action.emit();
  });
  assert.equal(renderer!.toJSON()?.children?.[0], "success");

  await act(async () => {
    action.reset();
  });
  assert.equal(renderer!.toJSON()?.children?.[0], "idle");

  pulse.teardown();
});

test("useBriqueAction represents transport failures as state", async () => {
  const transport = new RejectingTransport(new Error("send failed"));
  const pulse = createPulseWithTransport(transport);
  let action!: ReturnType<typeof useBriqueAction>;

  function Reader() {
    action = useBriqueAction({
      to: {
        context: "/root/text_context",
        cap: "set_text",
        type: "user",
      },
    });

    return React.createElement("span", null, action.status);
  }

  let renderer: TestRenderer.ReactTestRenderer;
  await act(async () => {
    renderer = TestRenderer.create(
      React.createElement(
        PulseProvider,
        { pulse },
        React.createElement(Reader)
      )
    );
  });

  let caught: unknown;
  await act(async () => {
    try {
      await action.emit();
    } catch (err) {
      caught = err;
    }
  });

  assert.match(String((caught as Error).message), /send failed/);
  assert.equal(renderer!.toJSON()?.children?.[0], "error");
  pulse.teardown();
});

test("useBriqueAction ignores pending completion after unmount", async () => {
  const transport = new DeferredTransport();
  const pulse = createPulseWithTransport(transport);
  let action!: ReturnType<typeof useBriqueAction>;

  function Reader() {
    action = useBriqueAction({
      to: {
        context: "/root/text_context",
        cap: "set_text",
        type: "user",
      },
      targetPathUI: "/text/result",
    });

    return React.createElement("span", null, action.status);
  }

  let renderer: TestRenderer.ReactTestRenderer;
  await act(async () => {
    renderer = TestRenderer.create(
      React.createElement(
        PulseProvider,
        { pulse },
        React.createElement(Reader)
      )
    );
  });

  let emitted!: Promise<string>;
  await act(async () => {
    emitted = action.emit();
    await Promise.resolve();
  });
  assert.equal(renderer!.toJSON()?.children?.[0], "running");

  await act(async () => {
    renderer!.unmount();
  });

  transport.resolveSend();
  const intentionId = await emitted;

  assert.equal(transport.sent[0].intention.intention_id, intentionId);
  pulse.teardown();
});

test("useBriqueAction ignores stale completion after default params change", async () => {
  const transport = new DeferredTransport();
  const pulse = createPulseWithTransport(transport);
  let action!: ReturnType<typeof useBriqueAction>;

  function Reader({ value }: { value: string }) {
    action = useBriqueAction({
      to: {
        context: "/root/text_context",
        cap: "set_text",
        type: "user",
      },
      params: { value },
    });

    return React.createElement("span", null, action.status);
  }

  let renderer: TestRenderer.ReactTestRenderer;
  await act(async () => {
    renderer = TestRenderer.create(
      React.createElement(
        PulseProvider,
        { pulse },
        React.createElement(Reader, { value: "old" })
      )
    );
  });

  let emitted!: Promise<string>;
  await act(async () => {
    emitted = action.emit();
    await Promise.resolve();
  });
  assert.equal(renderer!.toJSON()?.children?.[0], "running");

  await act(async () => {
    renderer!.update(
      React.createElement(
        PulseProvider,
        { pulse },
        React.createElement(Reader, { value: "new" })
      )
    );
  });

  transport.resolveSend();
  await emitted;

  assert.equal(renderer!.toJSON()?.children?.[0], "running");
  assert.deepEqual(transport.sent[0].intention.params, { value: "old" });
  pulse.teardown();
});

function createRuntimePulse() {
  const transport = new ManualTransport();
  return {
    pulse: createPulseWithTransport(transport),
    transport,
  };
}

function createPulseWithTransport(transport: Transport) {
  const manager = new TransportManager();
  manager.register("manual", transport);

  return new PulseRuntime({
    transportManager: manager,
    transportName: "manual",
  });
}

function matterResponse(intentionId: string, payload: Record<string, unknown>) {
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
      status: "ok",
      payload,
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

function findSentByCap(transport: ManualTransport, cap: string) {
  for (const msg of transport.sent) {
    if (msg.kind === "intention" && msg.intention.to.cap === cap) {
      return msg;
    }
  }

  return null;
}

function countSentByCap(transport: ManualTransport, cap: string) {
  return transport.sent.filter(
    (msg) => msg.kind === "intention" && msg.intention.to.cap === cap
  ).length;
}

function readSpanTexts(renderer: TestRenderer.ReactTestRenderer): string[] {
  return renderer.root
    .findAllByType("span")
    .map((span) => span.children.join(""));
}

class RejectingTransport implements Transport {
  closed = false;
  private readonly error: Error;

  constructor(error: Error) {
    this.error = error;
  }

  async send(): Promise<void> {
    throw this.error;
  }

  subscribe(): Unsubscribe {
    return () => {};
  }

  close(): void {
    this.closed = true;
  }
}

class DeferredTransport implements Transport {
  sent: CirculationMessage[] = [];
  closed = false;
  private resolve: (() => void) | null = null;

  send(message: CirculationMessage): Promise<void> {
    this.sent.push(message);

    return new Promise((resolve) => {
      this.resolve = resolve;
    });
  }

  subscribe(): Unsubscribe {
    return () => {};
  }

  resolveSend(): void {
    this.resolve?.();
    this.resolve = null;
  }

  close(): void {
    this.closed = true;
  }
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
