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

import React, { useMemo, useState } from "react";
import { PulseProvider, useIntentionSubscription, usePulse } from "@spark/pulse/react";
import { PulseRuntime } from "@spark/pulse/runtime";

const REPL_PATH_UI = "/repl/response";

const DEFAULT_INTENTION = JSON.stringify(
  {
    to: {
      context: "/root",
      cap: "read.state",
      type: "reflexive",
    },
    params: {},
  },
  null,
  2
);

export type IntentionReplProps = {
  pulse?: PulseRuntime;
  transportUrl?: string;
  rootContextId?: string;
};

export function IntentionRepl({ pulse: providedPulse, transportUrl }: IntentionReplProps) {
  const pulse = useMemo(() => {
    if (providedPulse) return providedPulse;
    return new PulseRuntime({
      webSocketUrl: transportUrl ?? resolveTransportUrl(),
      maxMessagesPerFlow: 20,
      terminalFlowTtlMs: 30000,
    });
  }, [providedPulse, transportUrl]);

  return (
    <PulseProvider pulse={pulse}>
      <ReplInner />
    </PulseProvider>
  );
}

function ReplInner() {
  const pulse = usePulse();
  const [raw, setRaw] = useState(DEFAULT_INTENTION);
  const [parseError, setParseError] = useState<string | null>(null);
  const [intentionId, setIntentionId] = useState<string | null>(null);
  const [sending, setSending] = useState(false);
  const state = useIntentionSubscription(intentionId);

  async function send() {
    setParseError(null);

    let parsed: { to: { context: string; cap: string; type?: string }; params?: Record<string, unknown> };
    try {
      parsed = JSON.parse(raw);
    } catch (err) {
      setParseError(`JSON parse error: ${String(err)}`);
      return;
    }

    if (!parsed?.to?.context || !parsed?.to?.cap) {
      setParseError('Missing required fields: "to.context" and "to.cap"');
      return;
    }

    setSending(true);
    try {
      const id = await pulse.emitUIIntention({
        to: {
          context: parsed.to.context,
          cap: parsed.to.cap,
          type: (parsed.to.type as "user" | "reflexive" | "matter") ?? "user",
        },
        targetPathUI: REPL_PATH_UI,
        identity: { id: "repl-user", kind: "human" },
        params: parsed.params ?? {},
      });
      setIntentionId(id);
    } finally {
      setSending(false);
    }
  }

  const responseText = useMemo(() => {
    if (!intentionId) return "";
    if (state.error) return JSON.stringify(state.error, null, 2);
    if (state.payload) return JSON.stringify(state.payload, null, 2);
    return "";
  }, [intentionId, state.error, state.payload]);

  return (
    <main style={styles.root}>
      <header style={styles.header}>
        <span style={styles.title}>Brique REPL</span>
        <span style={styles.status}>
          {state.status !== "idle" ? state.status : ""}
        </span>
      </header>

      <section style={styles.pane}>
        <label style={styles.label}>Intention</label>
        <textarea
          style={styles.textarea}
          value={raw}
          onChange={(e) => setRaw(e.target.value)}
          spellCheck={false}
          rows={12}
        />
        {parseError && <div style={styles.error}>{parseError}</div>}
        <button
          style={{ ...styles.button, ...(sending ? styles.buttonDisabled : undefined) }}
          onClick={() => void send()}
          disabled={sending}
        >
          {sending ? "Sending…" : "Send"}
        </button>
      </section>

      <section style={styles.pane}>
        <label style={styles.label}>
          Response
          {intentionId && <span style={styles.intentionId}> — {intentionId}</span>}
        </label>
        <textarea
          style={{ ...styles.textarea, ...styles.textareaReadonly }}
          value={responseText}
          readOnly
          rows={12}
          spellCheck={false}
        />
      </section>
    </main>
  );
}

function resolveTransportUrl(): string {
  const env = (globalThis as { process?: { env?: Record<string, string | undefined> } }).process?.env;
  return env?.SPARK_UI_TRANSPORT_URL ?? "ws://localhost:8082/ws";
}

const styles: Record<string, React.CSSProperties> = {
  root: {
    fontFamily: "monospace",
    padding: 20,
    display: "flex",
    flexDirection: "column",
    gap: 16,
    minHeight: "100vh",
    background: "var(--syn-surface-overlay, #1a1b1e)",
    color: "var(--syn-text-primary, #e8eaed)",
    boxSizing: "border-box",
  },
  header: {
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between",
  },
  title: {
    fontSize: 14,
    fontWeight: 700,
    letterSpacing: "0.06em",
    textTransform: "uppercase",
    color: "var(--syn-text-muted, rgba(232,234,237,0.55))",
  },
  status: {
    fontSize: 11,
    color: "var(--syn-text-muted, rgba(232,234,237,0.45))",
  },
  pane: {
    display: "flex",
    flexDirection: "column",
    gap: 6,
  },
  label: {
    fontSize: 10,
    fontWeight: 700,
    textTransform: "uppercase",
    letterSpacing: "0.08em",
    color: "var(--syn-text-muted, rgba(232,234,237,0.55))",
  },
  intentionId: {
    fontWeight: 400,
    textTransform: "none",
    letterSpacing: 0,
    fontSize: 10,
    color: "var(--syn-text-muted, rgba(232,234,237,0.35))",
  },
  textarea: {
    width: "100%",
    boxSizing: "border-box",
    background: "var(--syn-surface-0, rgba(0,0,0,0.25))",
    border: "1px solid var(--syn-border-medium, rgba(255,255,255,0.14))",
    borderRadius: 6,
    color: "var(--syn-text-primary, #e8eaed)",
    fontFamily: "monospace",
    fontSize: 12,
    padding: "10px 12px",
    resize: "vertical",
    outline: "none",
  },
  textareaReadonly: {
    color: "var(--syn-text-secondary, rgba(232,234,237,0.7))",
    cursor: "default",
  },
  button: {
    alignSelf: "flex-end",
    padding: "7px 20px",
    border: "1px solid var(--syn-selected-border, rgba(138,180,248,0.6))",
    borderRadius: 6,
    background: "var(--syn-feedback-info, #8ab4f8)",
    color: "var(--syn-text-inverse, #071014)",
    fontFamily: "monospace",
    fontWeight: 700,
    fontSize: 12,
    cursor: "pointer",
  },
  buttonDisabled: {
    opacity: 0.5,
    cursor: "not-allowed",
  },
  error: {
    fontSize: 11,
    padding: "6px 10px",
    borderRadius: 4,
    background: "var(--syn-feedback-error-bg)",
    color: "var(--syn-feedback-error)",
  },
};

export default IntentionRepl;
