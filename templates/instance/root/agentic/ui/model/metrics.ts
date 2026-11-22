// Pure model of the metrics of a conversation (spec: document/agentic_interface.md, "The metrics of a conversation").
// Tokens, not money, and tokens that account for the cache: the real cost of an inference is what is NEW in it
// (written to the cache + uncached input + output); what is read from the cache is shown apart, never added to the cost.
// No React, no Pulse, no clock: a function of the lines of the journal.

import type { JournalLine } from "./journal";

export type Inference = {
  seq: number;
  /** the whole context of the inference (what the provider received) */
  contextTokens: number;
  /** the breakdown below is null for a line written before the breakdown existed, or by a provider that does not report it */
  cacheRead: number | null;
  cacheCreation: number | null;
  uncached: number | null;
  output: number | null;
  durationMs: number | null;
  model: string | null;
  windowTokens: number | null;
  estimated: boolean;
};

export type ToolStat = { capacity: string; calls: number; failures: number };

export type Metrics = {
  inferences: Inference[];
  /** null while no inference carries the breakdown */
  realCost: number | null;
  cacheRead: number | null;
  /** cache read / context, over the inferences that carry the breakdown; null when there is none */
  cacheRate: number | null;
  /** the context of the last inference and its share of the window (null without a window) */
  currentContext: number | null;
  windowShare: number | null;
  modelMs: { total: number; mean: number; max: number } | null;
  tools: ToolStat[];
  userMessages: number;
  pilotMessages: number;
  errors: number;
  elapsedMs: number | null;
};

const count = (value: unknown): number | null => (typeof value === "number" && Number.isFinite(value) && value >= 0 ? value : null);

export function inferenceOf(line: JournalLine): Inference | null {
  const content = line.content;
  if (line.kind !== "event" || content?.event !== "inference_measured") return null;
  const context = count(content.input_tokens);
  if (context === null) return null;
  return {
    seq: line.seq,
    contextTokens: context,
    cacheRead: count(content.cache_read_tokens),
    cacheCreation: count(content.cache_creation_tokens),
    uncached: count(content.uncached_input_tokens),
    output: count(content.output_tokens),
    durationMs: count(content.duration_ms),
    model: typeof content.model === "string" ? content.model : null,
    windowTokens: count(content.window_tokens),
    estimated: content.estimated === true,
  };
}

/** What an inference really cost in tokens: the new ones. Null when the breakdown is not there to say. */
export function realCostOf(inference: Inference): number | null {
  if (inference.uncached === null && inference.cacheCreation === null) return null;
  return (inference.cacheCreation ?? 0) + (inference.uncached ?? 0) + (inference.output ?? 0);
}

export function metricsOf(lines: JournalLine[]): Metrics {
  const sorted = [...lines].sort((a, b) => a.seq - b.seq);
  const inferences: Inference[] = [];
  const tools = new Map<string, ToolStat>();
  const capacityOf = new Map<string, string>();
  let userMessages = 0, pilotMessages = 0, errors = 0;

  for (const line of sorted) {
    const inference = inferenceOf(line);
    if (inference) { inferences.push(inference); continue; }
    switch (line.kind) {
      case "user": userMessages += 1; break;
      case "pilot": pilotMessages += 1; break;
      case "error": errors += 1; break;
      case "capacity_intention": {
        const reference = String(line.content?.target_capacity ?? "");
        const capacity = reference.slice(reference.lastIndexOf("/") + 1);
        capacityOf.set(String(line.content?.id ?? line.id), capacity);
        const stat = tools.get(capacity) ?? { capacity, calls: 0, failures: 0 };
        stat.calls += 1;
        tools.set(capacity, stat);
        break;
      }
      case "capacity_response": {
        const capacity = capacityOf.get(String(line.correlation?.intention_id ?? ""));
        if (capacity && line.content?.response?.status === "error") tools.get(capacity)!.failures += 1;
        break;
      }
      default: break;
    }
  }

  const detailed = inferences.filter((i) => !i.estimated && realCostOf(i) !== null);
  const realCost = detailed.length ? detailed.reduce((sum, i) => sum + (realCostOf(i) ?? 0), 0) : null;
  const withCache = detailed.filter((i) => i.cacheRead !== null);
  const cacheRead = withCache.length ? withCache.reduce((sum, i) => sum + (i.cacheRead ?? 0), 0) : null;
  const contextOfCache = withCache.reduce((sum, i) => sum + i.contextTokens, 0);
  const durations = inferences.map((i) => i.durationMs).filter((d): d is number => d !== null);
  const last = inferences[inferences.length - 1];
  const stamps = sorted.map((l) => Date.parse(l.created_at ?? "")).filter((t) => Number.isFinite(t));

  return {
    inferences,
    realCost,
    cacheRead,
    cacheRate: cacheRead !== null && contextOfCache > 0 ? cacheRead / contextOfCache : null,
    currentContext: last ? last.contextTokens : null,
    windowShare: last && last.windowTokens ? last.contextTokens / last.windowTokens : null,
    modelMs: durations.length ? { total: durations.reduce((a, b) => a + b, 0), mean: durations.reduce((a, b) => a + b, 0) / durations.length, max: Math.max(...durations) } : null,
    tools: [...tools.values()].sort((a, b) => b.calls - a.calls || a.capacity.localeCompare(b.capacity)),
    userMessages, pilotMessages, errors,
    elapsedMs: stamps.length > 1 ? Math.max(...stamps) - Math.min(...stamps) : null,
  };
}
