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

export function TextPlayground({
  rootContextId,
  onIntention = () => {},
}: {
  rootContextId?: string;
  onIntention?: (emission: SandboxEmission) => void;
}) {
  const textContext = childContext(rootContextId, "text_context");
  const [value, setValue] = useState("");
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
  const setText = useTextAction(textContext, "set_text");
  const uppercaseText = useTextAction(textContext, "uppercase_text");
  const appendText = useTextAction(textContext, "append_text");
  const readText = useTextAction(textContext, "read_text");

  async function emit(
    action: ReturnType<typeof useTextAction>,
    cap: string,
    params?: Record<string, unknown>
  ) {
    const id = await action.emit(params);
    onIntention({
      intentionId: id,
      sourceAddress: action.sourceAddress,
      targetContext: textContext,
      targetCapacity: cap,
      targetPathUI: sandboxUIPaths.textResult,
    });
  }

  return (
    <section style={styles.section}>
      <h2 style={styles.heading}>Text</h2>
      <input
        data-testid="text-input"
        value={value}
        onChange={(event) => setValue(event.currentTarget.value)}
        style={styles.input}
      />
      <div style={styles.actions}>
        <button data-testid="set-text-button" onClick={() => emit(setText, "set_text", { value })}>Set Text</button>
        <button data-testid="uppercase-button" onClick={() => emit(uppercaseText, "uppercase_text")}>Uppercase</button>
        <button data-testid="append-button" onClick={() => emit(appendText, "append_text", { value: "!" })}>Append !</button>
        <button data-testid="read-text-button" onClick={() => emit(readText, "read_text")}>Read Text</button>
      </div>
      <div style={styles.controls}>
        <button
          data-testid="text-observer-a-toggle"
          onClick={toggleObserverA}
        >
          {observerAMounted ? "Unmount A" : "Mount A"}
        </button>
        <button
          data-testid="text-observer-b-toggle"
          onClick={toggleObserverB}
        >
          {observerBMounted ? "Unmount B" : "Mount B"}
        </button>
        <button
          data-testid="text-subscribe-toggle"
          onClick={() => setSubscribe((v) => !v)}
        >
          {subscribe ? "Switch to read" : "Switch to subscribe"}
        </button>
      </div>
      <div style={styles.observers}>
        {observerAMounted ? (
          <MatterObserverBlock
            context={textContext}
            matterId="text_buffer"
            label="Text Observer A"
            testId="text-matter-observer-a"
            subscribe={subscribe}
          />
        ) : (
          <div data-testid="text-matter-observer-a-unmounted" style={styles.unmounted}>
            Observer A unmounted
          </div>
        )}
        {observerBMounted ? (
          <MatterObserverBlock
            context={textContext}
            matterId="text_buffer"
            label="Text Observer B"
            testId="text-matter-observer-b"
            subscribe={subscribe}
          />
        ) : (
          <div data-testid="text-matter-observer-b-unmounted" style={styles.unmounted}>
            Observer B unmounted
          </div>
        )}
      </div>
      <PathRendererBlock pathUI={sandboxUIPaths.textResult} testId="text-result-renderer" />
    </section>
  );
}

function useTextAction(context: string, cap: string) {
  const pulse = usePulse();
  const action = useBriqueAction({
    to: {
      context,
      cap,
      type: "user",
    },
    targetPathUI: sandboxUIPaths.textResult,
    identity: sandboxIdentity,
  });

  return {
    ...action,
    sourceAddress: pulse.ui.addressForPath(sandboxUIPaths.textResult),
  };
}

const styles: Record<string, React.CSSProperties> = {
  section: {
    border: "1px solid #d0d7de",
    borderRadius: 8,
    padding: 14,
    background: "#eef6ff",
  },
  heading: {
    fontSize: 16,
    margin: "0 0 10px",
  },
  input: {
    boxSizing: "border-box",
    width: "100%",
    minHeight: 34,
    marginBottom: 10,
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
