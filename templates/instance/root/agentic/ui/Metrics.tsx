// The metrics of the open conversation: a summary line and a panel (spec: document/agentic_interface.md, "The metrics of a conversation").
// Tokens only, and tokens that account for the cache. The texts below are PROVISIONAL, to be validated by the architect.
import React from "react";
import type { Metrics } from "./model/metrics.ts";
import { styles } from "./styles.ts";

export const METRIC_LABELS = {
  button: "Metrics",
  close: "Close",
  fresh: "new tokens",
  cached: "read from cache",
  inferences: "inferences",
  inference: "inference",
  cacheRate: "cache rate",
  context: "context",
  window: "of the window",
  unavailable: "unavailable",
  modelTime: "time in model",
  elapsed: "elapsed time",
  tools: "Tools",
  calls: "calls",
  failures: "failed",
  messages: "Messages",
  user: "from the person",
  pilot: "from the pilot",
  errors: "errors",
  perInference: "Per inference",
  columns: ["#", "context", "read", "written", "uncached", "output", "duration"],
} as const;

const L = METRIC_LABELS;

export const compact = (n: number): string => (n >= 1_000_000 ? `${(n / 1_000_000).toFixed(1)} M` : n >= 10_000 ? `${Math.round(n / 1000)} k` : n >= 1000 ? `${(n / 1000).toFixed(1)} k` : String(Math.round(n)));
export const percent = (x: number): string => `${Math.round(x * 100)} %`;
export const seconds = (ms: number): string => (ms < 60_000 ? `${(ms / 1000).toFixed(ms < 10_000 ? 1 : 0)} s` : `${Math.floor(ms / 60_000)} min ${Math.round((ms % 60_000) / 1000)} s`);

/** "12 inferences · 3.1k new tokens · 54k read from cache · 41s" — a part is left out when the journal cannot say it. */
export function summaryLine(m: Metrics): string {
  if (m.inferences.length === 0) return "";
  const parts = [`${m.inferences.length} ${m.inferences.length === 1 ? L.inference : L.inferences}`];
  if (m.realCost !== null) parts.push(`${compact(m.realCost)} ${L.fresh}`);
  if (m.cacheRead !== null) parts.push(`${compact(m.cacheRead)} ${L.cached}`);
  if (m.modelMs) parts.push(seconds(m.modelMs.total));
  return parts.join(" · ");
}

const dash = (value: number | null, show: (n: number) => string = compact): string => (value === null ? "—" : show(value));

function Row({ label, value }: { label: string; value: string }) {
  return <div style={{ display: "flex", justifyContent: "space-between", gap: 12 }}><span style={styles.rowMeta}>{label}</span><span>{value}</span></div>;
}

export function MetricsPanel({ metrics, onClose }: { metrics: Metrics; onClose: () => void }) {
  const m = metrics;
  return (
    <div style={styles.metricsPanel}>
      <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between" }}>
        <span style={styles.title}>{L.button}</span>
        <button style={styles.buttonQuiet} onClick={onClose}>{L.close}</button>
      </div>
      <Row label={`${L.inferences}`} value={String(m.inferences.length)} />
      <Row label={L.fresh} value={m.realCost === null ? L.unavailable : compact(m.realCost)} />
      <Row label={L.cached} value={m.cacheRead === null ? L.unavailable : compact(m.cacheRead)} />
      <Row label={L.cacheRate} value={m.cacheRate === null ? L.unavailable : percent(m.cacheRate)} />
      <Row label={L.context} value={m.currentContext === null ? "—" : `${compact(m.currentContext)}${m.windowShare !== null ? ` (${percent(m.windowShare)} ${L.window})` : ""}`} />
      <Row label={L.modelTime} value={m.modelMs ? `${seconds(m.modelMs.total)} (max ${seconds(m.modelMs.max)})` : L.unavailable} />
      <Row label={L.elapsed} value={m.elapsedMs === null ? "—" : seconds(m.elapsedMs)} />

      <div style={styles.title}>{L.messages}</div>
      <Row label={L.user} value={String(m.userMessages)} />
      <Row label={L.pilot} value={String(m.pilotMessages)} />
      <Row label={L.errors} value={String(m.errors)} />

      {m.tools.length > 0 && <div style={styles.title}>{L.tools}</div>}
      {m.tools.map((t) => <Row key={t.capacity} label={t.capacity} value={`${t.calls} ${L.calls}${t.failures ? ` · ${t.failures} ${L.failures}` : ""}`} />)}

      {m.inferences.length > 0 && <div style={styles.title}>{L.perInference}</div>}
      {m.inferences.length > 0 && (
        <table style={{ fontSize: 11, borderCollapse: "collapse", width: "100%" }}>
          <thead><tr>{L.columns.map((c) => <th key={c} style={{ textAlign: "right", fontWeight: 500, color: "var(--syn-text-muted, rgba(232,234,237,0.55))" }}>{c}</th>)}</tr></thead>
          <tbody>
            {m.inferences.map((i, n) => (
              <tr key={i.seq} style={{ textAlign: "right" }}>
                <td>{n + 1}</td><td>{compact(i.contextTokens)}{i.estimated ? "~" : ""}</td><td>{dash(i.cacheRead)}</td><td>{dash(i.cacheCreation)}</td>
                <td>{dash(i.uncached)}</td><td>{dash(i.output)}</td><td>{dash(i.durationMs, seconds)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}
