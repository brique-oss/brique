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

import React, { useState } from "react";
import { usePulse, useBriqueAction } from "@spark/pulse/react";
import { MatterObserverBlock } from "../../ui/blocks/MatterObserverBlock.js";
import { PathRendererBlock } from "../../ui/blocks/PathRendererBlock.js";
import {
  childContext,
  type SandboxEmission,
} from "../../ui/blocks/sandboxPulse.js";
import { sandboxUIPaths } from "../../ui/blocks/sandboxPaths.js";

const sandboxIdentity = {
  id: "sandbox-user",
  kind: "human",
};

export function CounterPlayground({
  rootContextId,
  onIntention = () => {},
}: {
  rootContextId?: string;
  onIntention?: (emission: SandboxEmission) => void;
}) {
  const counterContext = childContext(rootContextId, "counter_context");
  const increment = useCounterAction(counterContext, "increment");
  const reset = useCounterAction(counterContext, "reset");
  const readCounter = useCounterAction(counterContext, "read_counter");
  const [observerAMounted, setObserverAMounted] = useState(true);
  const [observerBMounted, setObserverBMounted] = useState(true);
  const [subscribe, setSubscribe] = useState(false);

  function toggleObserverA() {
    const next = !observerAMounted;
    setObserverAMounted(next);
    if (!next && !observerBMounted) setSubscribe(false);
  }

  function toggleObserverB() {
    const next = !observerBMounted;
    setObserverBMounted(next);
    if (!next && !observerAMounted) setSubscribe(false);
  }

  async function emit(action: ReturnType<typeof useCounterAction>, cap: string) {
    const id = await action.emit();
    onIntention({
      intentionId: id,
      sourceAddress: action.sourceAddress,
      targetContext: counterContext,
      targetCapacity: cap,
      targetPathUI: sandboxUIPaths.counterResult,
    });
  }

  return (
    <section style={styles.section}>
      <h2 style={styles.heading}>Counter</h2>
      <div style={styles.actions}>
        <button data-testid="increment-button" onClick={() => emit(increment, "increment")}>Increment</button>
        <button data-testid="reset-button" onClick={() => emit(reset, "reset")}>Reset</button>
        <button data-testid="read-counter-button" onClick={() => emit(readCounter, "read_counter")}>Read Counter</button>
      </div>
      <div style={styles.controls}>
        <button
          data-testid="counter-observer-a-toggle"
          onClick={toggleObserverA}
        >
          {observerAMounted ? "Unmount A" : "Mount A"}
        </button>
        <button
          data-testid="counter-observer-b-toggle"
          onClick={toggleObserverB}
        >
          {observerBMounted ? "Unmount B" : "Mount B"}
        </button>
        <button
          data-testid="counter-subscribe-toggle"
          onClick={() => setSubscribe((v) => !v)}
        >
          {subscribe ? "Switch to read" : "Switch to subscribe"}
        </button>
      </div>
      <div style={styles.observers}>
        {observerAMounted ? (
          <MatterObserverBlock
            context={counterContext}
            matterId="counter_value"
            label="Counter Observer A"
            testId="counter-matter-observer-a"
            subscribe={subscribe}
          />
        ) : (
          <div data-testid="counter-matter-observer-a-unmounted" style={styles.unmounted}>
            Observer A unmounted
          </div>
        )}
        {observerBMounted ? (
          <MatterObserverBlock
            context={counterContext}
            matterId="counter_value"
            label="Counter Observer B"
            testId="counter-matter-observer-b"
            subscribe={subscribe}
          />
        ) : (
          <div data-testid="counter-matter-observer-b-unmounted" style={styles.unmounted}>
            Observer B unmounted
          </div>
        )}
      </div>
      <PathRendererBlock pathUI={sandboxUIPaths.counterResult} testId="counter-result-renderer" />
    </section>
  );
}

function useCounterAction(context: string, cap: string) {
  const pulse = usePulse();
  const action = useBriqueAction({
    to: {
      context,
      cap,
      type: "user",
    },
    targetPathUI: sandboxUIPaths.counterResult,
    identity: sandboxIdentity,
  });

  return {
    ...action,
    sourceAddress: pulse.ui.addressForPath(sandboxUIPaths.counterResult),
  };
}

const styles: Record<string, React.CSSProperties> = {
  section: {
    border: "1px solid #d0d7de",
    borderRadius: 8,
    padding: 14,
    background: "#fff5e6",
  },
  heading: {
    fontSize: 16,
    margin: "0 0 10px",
  },
  actions: {
    display: "flex",
    flexWrap: "wrap",
    gap: 8,
    marginBottom: 10,
  },
  observers: {
    display: "grid",
    gridTemplateColumns: "repeat(auto-fit, minmax(160px, 1fr))",
    gap: 8,
    marginBottom: 10,
  },
  controls: {
    display: "flex",
    flexWrap: "wrap",
    gap: 6,
    marginBottom: 8,
  },
  unmounted: {
    border: "1px dashed #8c959f",
    borderRadius: 6,
    padding: 8,
    fontSize: 12,
    color: "#8c959f",
    background: "rgba(255,255,255,0.4)",
    textAlign: "center" as const,
  },
};
