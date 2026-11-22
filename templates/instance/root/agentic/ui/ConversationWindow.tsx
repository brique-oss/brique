// The window of a conversation: the journal of a lineage as a chat, and the field where the person writes (spec: document/agentic_interface.md).
import React, { useEffect, useMemo, useRef, useState } from "react";
import type { PulseRuntime } from "@spark/pulse/runtime";
import { useEarlierLines, useMatters } from "./hooks.ts";
import { lineageView, toView } from "./model/journal.ts";
import { metricsOf } from "./model/metrics.ts";
import { METRIC_LABELS, MetricsPanel, summaryLine } from "./Metrics.tsx";
import { InputCard, InputPanel } from "./InputRequest.tsx";
import { openRequests } from "./model/input.ts";
import type { JournalLine, ViewItem } from "./model/journal.ts";
import { cursorOf, linesOf, newId } from "./model/matters.ts";
import { sendWithBusyRetry } from "./model/busy.ts";
import { useSettings } from "./settings.ts";
import type { Settings } from "./settings.ts";
import { STATE_LABELS, lastSeq } from "./model/state.ts";
import type { StateKey } from "./model/state.ts";
import type { LineageNode } from "./model/tree.ts";
import { styles } from "./styles.ts";

export const READER = "user:1";
export const IDENTITY = { id: "agentic-user", kind: "human" };
const TOOL_STATUS = { running: "running", ok: "succeeded", error: "failed", sent: "sent" } as const;

async function call(pulse: PulseRuntime, settings: Settings, context: string, cap: string, type: string, params: Record<string, unknown>): Promise<any> {
  const response: any = await sendWithBusyRetry(() => pulse.emitUIIntentionAndWait({ to: { context, cap, type }, targetPathUI: "/agentic/window", identity: IDENTITY, params } as any), settings);
  if (response.status === "error") throw new Error(response.error?.message ?? JSON.stringify(response.error));
  return response.payload ?? {};
}

function Item({ item }: { item: ViewItem }) {
  switch (item.type) {
    case "user":
      return <div style={styles.bubbleUser}>{item.text}</div>;
    case "pilot":
      return (
        <div style={styles.bubblePilot}>
          {item.recipient && <div style={styles.bubbleLabel}>→ {item.recipient}</div>}
          {item.text}
        </div>
      );
    case "daughter":
      return (
        <div style={styles.bubblePilot}>
          <div style={styles.bubbleLabel}>↩ {item.from}</div>
          {item.text}
        </div>
      );
    case "tool":
      return (
        <details style={styles.toolCard}>
          <summary style={styles.toolSummary}>⚙ {item.capacity} · {TOOL_STATUS[item.status]}</summary>
          <div style={styles.bubbleLabel}>{item.context}</div>
          <pre style={styles.pre}>{JSON.stringify(item.parameters, null, 1)}</pre>
          {item.error && <pre style={{ ...styles.pre, color: "var(--syn-feedback-error, #f28b82)" }}>{item.error}</pre>}
          {item.result !== undefined && <pre style={styles.pre}>{JSON.stringify(item.result, null, 1)}</pre>}
        </details>
      );
    case "summary":
      return (
        <details style={styles.separator}>
          <summary style={styles.toolSummary}>The conversation continues from a summary</summary>
          <pre style={styles.pre}>{item.text}</pre>
        </details>
      );
    case "alert":
      return (
        <div style={styles.alert}>
          ⚠ {item.event === "treatment_failed" ? `The message could not be processed${item.attempts ? ` (${item.attempts} attempts)` : ""}${item.parked ? " and was parked" : ""}` : item.event}
          {item.text && <div style={{ opacity: 0.8 }}>{item.text.slice(0, 200)}</div>}
        </div>
      );
    case "input":
      return <InputCard item={item} />;
    case "control":
      return <div style={styles.note}>{item.action === "suspend" ? "conversation suspended" : item.action === "resume" ? "conversation resumed" : item.action}</div>;
  }
}

function Composer({ placeholder, disabled, note, onSend }: { placeholder: string; disabled?: boolean; note?: string; onSend: (text: string) => Promise<void> }) {
  const [text, setText] = useState("");
  const [sending, setSending] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function submit() {
    const value = text.trim();
    if (!value || sending || disabled) return;
    setSending(true);
    setError(null);
    try {
      await onSend(value);
      setText("");
    } catch (err: any) {
      setError(String(err?.message ?? err));
    } finally {
      setSending(false);
    }
  }

  if (disabled) return <div style={{ ...styles.note, padding: 12, borderTop: styles.composer.borderTop }}>{note}</div>;
  return (
    <>
      {error && <div style={styles.error}>{error}</div>}
      <div style={styles.composer}>
        <textarea
          style={styles.input}
          rows={2}
          value={text}
          placeholder={placeholder}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => { if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); void submit(); } }}
        />
        <button style={{ ...styles.button, ...(sending || !text.trim() ? styles.buttonDisabled : undefined) }} disabled={sending || !text.trim()} onClick={() => void submit()}>
          {sending ? "…" : "Send"}
        </button>
      </div>
    </>
  );
}

/** A conversation that exists: its journal, its state, and where to write. */
export function Conversation({ pulse, context, node, state, reader = READER, onDeleted }: { pulse: PulseRuntime; context: string; node: LineageNode; state: StateKey; reader?: string; onDeleted?: () => void }) {
  const settings = useSettings();
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);
  const [showMetrics, setShowMetrics] = useState(false);
  const control = async (action: "suspend" | "resume" | "delete") => {
    setBusy(true); setFailure(null);
    try {
      await call(pulse, settings, context, "mailbox.send", "user", {
        continuity_id: node.head,
        message: { id: newId("ctl"), from: reader, kind: "control", content: action === "delete" ? { action, cascade: true } : { action } },
      });
      if (action === "delete") onDeleted?.();
    } catch (e) { setFailure(e instanceof Error ? e.message : String(e)); } finally { setBusy(false); }
  };
  const suspended = state === "suspended";
  const specs = node.generations.map((id) => ({ matterId: `journal_${id}`, tail: settings.journalTailBytes, live: id === node.head, ...(id === node.head ? { watchdogMs: settings.watchdogMs } : {}) }));
  const snaps = useMatters(pulse, context, [...specs, { matterId: "reading_cursors", live: true }], settings);
  // the live window is the end of each journal; what is before it is read by pages, so the conversation is whole
  const windows = node.generations.map((id) => { const seqs = linesOf(snaps[`journal_${id}`]).map((l) => l.seq); return { id, firstSeq: seqs.length ? Math.min(...seqs) : null }; });
  const earlier = useEarlierLines(pulse, settings, context, windows);
  const wholeJournal = (id: string) => {
    const bySeq = new Map<number, JournalLine>();
    for (const line of earlier[id] ?? []) bySeq.set(line.seq, line);
    for (const line of linesOf(snaps[`journal_${id}`])) bySeq.set(line.seq, line);
    return [...bySeq.values()].sort((a, b) => a.seq - b.seq);
  };
  const items = lineageView(node.generations.map((id) => ({ id, lines: wholeJournal(id) })));
  // the metrics of the whole lineage: the generations in order (their line numbers restart in each journal)
  const metrics = useMemo(() => metricsOf(node.generations.flatMap((id, n) => wholeJournal(id).map((l) => ({ ...l, seq: n * 1e9 + l.seq })))), [snaps, earlier, node.generations]);
  const summary = summaryLine(metrics);
  const headItems = toView(linesOf(snaps[`journal_${node.head}`]));
  const last = lastSeq(headItems);
  const cursor = cursorOf(snaps.reading_cursors, reader, node.head);
  const stream = useRef<HTMLDivElement | null>(null);
  const marked = useRef(0);

  useEffect(() => { if (stream.current) stream.current.scrollTop = stream.current.scrollHeight; }, [items.length, node.head]);

  // the person has the end of the conversation in front of them: it is read
  useEffect(() => {
    marked.current = 0;
  }, [node.head]);
  useEffect(() => {
    if (last <= cursor || last <= marked.current) return;
    const timer = setTimeout(() => {
      marked.current = last;
      void call(pulse, settings, context, "reading.mark", "user", { lineage_id: node.head, seq: last, reader }).catch(() => { marked.current = 0; });
    }, 300);
    return () => clearTimeout(timer);
  }, [last, cursor, node.head]);

  const workflow = node.nature === "workflow";
  // what the person is waiting for: the pilot works, a tool works, or their last message has not been answered yet
  const lastVisible = [...items].reverse().find((item) => item.type !== "control");
  const asked = openRequests(headItems).length > 0;       // the window under the stream says what is awaited: the person, not a tool
  const waiting = asked ? null : state === "waiting" ? "Waiting for a tool response…"
    : state === "running" ? "The pilot is working…"
    : lastVisible?.type === "user" && state !== "suspended" && state !== "stranded" ? "Message received, waiting for the response…" : null;
  useEffect(() => { if (stream.current) stream.current.scrollTop = stream.current.scrollHeight; }, [waiting]);
  return (
    <div style={styles.main}>
      <div style={styles.mainHeader}>
        <span style={{ ...styles.title, flex: 1, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{node.title}</span>
        <span style={styles.tag}>{workflow ? "workflow" : "conversation"}</span>
        <span style={styles.rowMeta}>{STATE_LABELS[state]}</span>
        <button style={styles.buttonQuiet} onClick={() => setShowMetrics((v) => !v)}>{METRIC_LABELS.button}</button>
        <button style={{ ...styles.buttonQuiet, ...(busy ? styles.buttonDisabled : undefined) }} disabled={busy} onClick={() => void control(suspended ? "resume" : "suspend")}>{suspended ? "Resume" : "Stop"}</button>
        <button style={{ ...styles.buttonQuiet, ...(busy ? styles.buttonDisabled : undefined) }} disabled={busy} onClick={() => { if (window.confirm("Delete this conversation" + (node.children?.length ? " and its sub-Continuities" : "") + "? Its history will be lost.")) void control("delete"); }}>Delete</button>
      </div>
      {failure && <div style={styles.error}>{failure}</div>}
      {summary && <div style={{ ...styles.rowMeta, padding: "3px 14px" }}>{summary}</div>}
      <div style={{ display: "flex", flex: 1, minHeight: 0 }}>
      <div style={{ ...styles.main, flex: 1 }}>
      <div style={styles.stream} ref={stream}>
        {items.length === 0 && <div style={styles.empty}>Nothing in this conversation yet.</div>}
        {items.map((item) => <Item key={`${item.type}-${item.id}-${item.seq}`} item={item} />)}
        {waiting && <div style={styles.waiting}>{waiting}</div>}
      </div>
      <InputPanel
        requests={openRequests(headItems)}
        onAnswer={async (request, answer) => { await call(pulse, settings, context, "input.answer", "user", { continuity_id: node.head, request_id: request.id, answer }); }}
      />
      <Composer
        placeholder="Write here… (Enter to send, Shift+Enter for a new line)"
        disabled={workflow}
        note="A workflow is not conversational; it reports to whoever launched it."
        onSend={async (text) => {
          await call(pulse, settings, context, "mailbox.send", "user", { continuity_id: node.head, message: { id: newId("msg"), from: reader, kind: "user", content: text } });
        }}
      />
      </div>
      {showMetrics && <MetricsPanel metrics={metrics} onClose={() => setShowMetrics(false)} />}
      </div>
    </div>
  );
}

/** A conversation that does not exist yet: the first thing the person writes launches the guide. */
export function Draft({ onStart }: { onStart: (text: string) => Promise<void> }) {
  return (
    <div style={styles.main}>
      <div style={styles.mainHeader}><span style={styles.title}>New conversation</span></div>
      <div style={styles.stream}>
        <div style={styles.empty}>
          The Brique world guide answers here. Ask how Brique works or what exists in the world, for example “which Capacity tracks a shipment?”.
        </div>
      </div>
      <Composer placeholder="Your first question for the guide…" onSend={onStart} />
    </div>
  );
}
