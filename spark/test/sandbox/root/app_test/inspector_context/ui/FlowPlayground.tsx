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
import { usePulse } from "@spark/pulse/react";
import { FlowRendererBlock } from "../../ui/blocks/FlowRendererBlock.js";
import {
  childContext,
  describeSandboxEmission,
  emitSandboxIntention,
  type SandboxEmission,
  type SandboxTarget,
} from "../../ui/blocks/sandboxPulse.js";
import { sandboxUIPaths } from "../../ui/blocks/sandboxPaths.js";

export function FlowPlayground({
  rootContextId,
  onIntention = () => {},
}: {
  rootContextId?: string;
  onIntention?: (emission: SandboxEmission) => void;
}) {
  const pulse = usePulse();
  const inspectorContext = childContext(rootContextId, "inspector_context");
  const [flowIntentionId, setFlowIntentionId] = useState<string | null>(null);

  async function emit(cap: string, params: Record<string, unknown> = {}) {
    const target: SandboxTarget = {
      context: inspectorContext,
      cap,
      params,
      targetPathUI: sandboxUIPaths.flowResult,
    };
    const id = await emitSandboxIntention(pulse, target);
    setFlowIntentionId(id);
    onIntention(describeSandboxEmission(pulse, target, id));
  }

  return (
    <section style={styles.section}>
      <h2 style={styles.heading}>Flow</h2>
      <div style={styles.actions}>
        <button data-testid="echo-button" onClick={() => emit("echo", { message: "echo" })}>Echo</button>
        <button
          data-testid="delayed-echo-button"
          onClick={() => emit("delayed_echo", { message: "delayed", delay_ms: 500 })}
        >
          Delayed Echo
        </button>
        <button data-testid="error-button" onClick={() => emit("error_test")}>Error Test</button>
      </div>
      <FlowRendererBlock intentionId={flowIntentionId} pathUI={sandboxUIPaths.flowResult} />
    </section>
  );
}

const styles: Record<string, React.CSSProperties> = {
  section: {
    border: "1px solid #d0d7de",
    borderRadius: 8,
    padding: 14,
    background: "#f3f0ff",
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
};
