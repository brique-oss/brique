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
import { useMatter, usePulse } from "@spark/pulse/react";
import { childContext } from "../../ui/blocks/sandboxPulse.js";

export function StreamPlayground({
  rootContextId,
}: {
  rootContextId?: string;
}) {
  const [mounted, setMounted] = useState(true);
  const streamContext = childContext(rootContextId, "stream_context");

  return (
    <section style={styles.section}>
      <h2 style={styles.heading}>HTTP Substance Stream</h2>
      <RefreshButton context={streamContext} />
      <div style={styles.observerControls}>
        <button
          data-testid="stream-observer-toggle"
          onClick={() => setMounted((v) => !v)}
        >
          {mounted ? "Unmount Observer" : "Mount Observer"}
        </button>
      </div>
      {mounted ? (
        <StreamObserverBlock context={streamContext} />
      ) : (
        <div data-testid="stream-observer-unmounted" style={styles.unmounted}>
          Observer unmounted
        </div>
      )}
    </section>
  );
}

function RefreshButton({
  context,
}: {
  context: string;
}) {
  const pulse = usePulse();
  const [status, setStatus] = useState<"idle" | "running" | "success" | "error">("idle");
  const [error, setError] = useState<unknown>(null);

  async function refreshStream(): Promise<void> {
    setStatus("running");
    setError(null);

    try {
      await pulse.matter.read({
        context,
        matterId: "stream_payload",
      });
      setStatus("success");
    } catch (err) {
      setError(err);
      setStatus("error");
    }
  }

  return (
    <div style={styles.actions}>
      <button
        data-testid="generate-stream-button"
        onClick={() => void refreshStream()}
        disabled={status === "running"}
      >
        {status === "running" ? "Reading..." : "Read Stream"}
      </button>
      {status === "error" && (
        <span style={styles.error} data-testid="generate-stream-error">
          {String(error)}
        </span>
      )}
      {status === "success" && (
        <span style={styles.success} data-testid="generate-stream-success">
          Read
        </span>
      )}
    </div>
  );
}

function StreamObserverBlock({ context }: { context: string }) {
  const matter = useMatter({
    context,
    matterId: "stream_payload",
  }, {
    autoRead: false,
  });

  return (
    <div data-testid="stream-observer" style={styles.observer}>
      <strong>Stream Observer</strong>
      <div data-testid="stream-status">status: {matter.status}</div>
      <div data-testid="stream-bytes-received">
        bytes received: {matter.streamBytesReceived}
      </div>
      <div data-testid="stream-chunks-received">
        chunks received: {matter.streamChunksReceived}
      </div>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  section: {
    border: "1px solid #d0d7de",
    borderRadius: 8,
    padding: 14,
    background: "#f0fff4",
  },
  heading: {
    fontSize: 16,
    margin: "0 0 10px",
  },
  actions: {
    display: "flex",
    alignItems: "center",
    gap: 8,
    marginBottom: 10,
  },
  observerControls: {
    marginBottom: 8,
  },
  observer: {
    border: "1px dashed #8c959f",
    borderRadius: 6,
    padding: 8,
    fontSize: 12,
    background: "rgba(255,255,255,0.7)",
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
  error: {
    color: "#cf222e",
    fontSize: 12,
  },
  success: {
    color: "#1a7f37",
    fontSize: 12,
  },
};

export default StreamPlayground;
