// The hooks of the interface agentic. Pulse's own hooks take one Matter each; a list of conversations has as many Matters as it has rows, so
// this one follows a list: it reads them, subscribes to the ones asked live, and reads again when one changes (the event only says "stale").
import { useEffect, useReducer, useRef, useState } from "react";
import type { PulseRuntime } from "@spark/pulse/runtime";
import { sendWithBusyRetry } from "./model/busy.ts";
import type { BusySettings } from "./model/busy.ts";
import { DEFAULT_SETTINGS } from "./settings.ts";
import type { JournalLine } from "./model/journal.ts";
import { NO_ANSWER, journalTextOf, readEarlier } from "./model/earlier.ts";
import type { ReadLines } from "./model/earlier.ts";

export type MatterSpec = {
  matterId: string;
  tail?: number;          // read the last bytes of a journal (the first line may then be partial: the readers skip it)
  live?: boolean;         // follow it: read again at each event
  throttleMs?: number;    // live: at least this long between two reads of it (a list row does not need every line the instant it is written)
  watchdogMs?: number;    // live: read it again every so often even if no event came (only for what the person is looking at)
};

const keyOf = (pulse: any, context: string, spec: MatterSpec) =>
  pulse.matter.normalizeKey({ context, matterId: spec.matterId, readMode: null, dataRange: spec.tail ? { unit: "bytes", tail: spec.tail } : null });
const idOf = (spec: MatterSpec) => `${spec.matterId}|${spec.tail ?? ""}|${spec.live ? 1 : 0}`;
const wait = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

// The link to the engine and what follows it are Pulse's business: when it comes back, Pulse subscribes again every Matter that is held (under the same
// subscription identifier) and reads it again, it rejects at once the requests the lost link took with it, and a re-read keeps the data it had until the
// new read lands (pulse/runtime/core/PulseRuntime.ts, matter/MatterRuntime.ts). This block no longer does any of that itself.

/** Is the link to the engine up? (false only once it has been seen down: a block that never heard of the link says it is up) */
export function useConnection(pulse: PulseRuntime): "connected" | "disconnected" {
  const [state, setState] = useState<"connected" | "disconnected">("connected");
  useEffect(() => {
    let timer: ReturnType<typeof setTimeout> | null = null;
    const stop = (pulse as any).subscribeToConnectionState((next: "connected" | "disconnected") => {
      if (timer) { clearTimeout(timer); timer = null; }
      if (next === "connected") setState("connected");
      else timer = setTimeout(() => setState("disconnected"), 1500);      // a blink of the link is not worth a message
    });
    return () => { if (timer) clearTimeout(timer); stop(); };
  }, [pulse]);
  return state;
}

/** The snapshots of a list of Matters, by Matter identifier. Only what enters or leaves the list is subscribed or released: the others keep their
 *  subscription (tearing them all down at each change costs a subscription and a read per Matter, for every conversation added). */
export function useMatters(pulse: PulseRuntime, context: string, specs: MatterSpec[], _busy: BusySettings = DEFAULT_SETTINGS): Record<string, any> {   // _busy: kept for the callers; a refresh is not asked again here, the watchdog and the next event read it
  const [, bump] = useReducer((n: number) => n + 1, 0);
  const signature = specs.map(idOf).join(",");
  const active = useRef<Map<string, () => void>>(new Map());

  function follow(spec: MatterSpec): () => void {
    const runtime = pulse as any;
    const key = keyOf(pulse, context, spec);
    runtime.matter.acquireReader(key);
    if (spec.live) runtime.matter.acquireSubscription(key);
    // An event says "stale". A read already in flight (the first one, or the one of the previous event) was asked before that change and ends
    // with the old content: so every event is counted, and the Matter is read again until a read has started after the last event.
    let seen = 0, done = 0, running = false, dead = false;
    let lastEvent: unknown = runtime.matter.getSnapshot(key).lastEvent;
    const pull = async () => {
      if (running) return;
      running = true;
      try {
        while (!dead && done < seen) {
          done = seen;
          await runtime.matter.refresh(key).catch(() => undefined);
          if (spec.throttleMs && done < seen) await wait(spec.throttleMs);
        }
      } finally { running = false; if (!dead) bump(); }
    };
    const stop = runtime.matter.subscribeToSnapshot(key, () => {
      if (spec.live) {
        const snap = runtime.matter.getSnapshot(key);
        if (snap.lastEvent !== lastEvent || snap.status === "stale") { lastEvent = snap.lastEvent; seen += 1; void pull(); }
      }
      bump();
    });
    const timer = spec.live && spec.watchdogMs ? setInterval(() => { seen += 1; void pull(); }, spec.watchdogMs) : null;
    return () => {
      dead = true;
      stop();
      if (timer) clearInterval(timer);
      if (spec.live) runtime.matter.releaseSubscription(key);
      runtime.matter.releaseReader(key);
    };
  }

  useEffect(() => {
    const wanted = new Map(specs.map((spec) => [idOf(spec), spec] as const));
    for (const [id, release] of [...active.current]) if (!wanted.has(id)) { release(); active.current.delete(id); }
    for (const [id, spec] of wanted) if (!active.current.has(id)) active.current.set(id, follow(spec));
    bump();
  }, [pulse, context, signature]);
  useEffect(() => () => { active.current.forEach((release) => release()); active.current.clear(); }, [pulse, context]);

  // A re-read keeps the previous content (status "loading" with its data): nothing flickers and nothing is unmounted while the new one is read.
  const out: Record<string, any> = {};
  for (const spec of specs) out[spec.matterId] = (pulse as any).matter.getSnapshot(keyOf(pulse, context, spec));
  return out;
}

/** The identifiers of the continuities of the Structure `continuities`, read again every few seconds and when asked. */
/** What a request says when the connection it was on is gone or not there yet: transient, never a reason to show a red error for a whole poll period. */
export const TRANSIENT_CONNECTION_ERROR = /WebSocket is not open|Connection lost before the response/i;
const RETRY_AFTER_CONNECTION_ERROR_MS = 700;

export function useContinuityIds(pulse: PulseRuntime, context: string, identity: { id: string; kind: string }, everyMs = DEFAULT_SETTINGS.listPollMs, busy: BusySettings = DEFAULT_SETTINGS): { ids: string[]; error: string | null; refresh: () => void } {
  const [ids, setIds] = useState<string[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [tick, setTick] = useState(0);
  const alive = useRef(true);

  useEffect(() => {
    alive.current = true;
    let retry: ReturnType<typeof setTimeout> | undefined;
    const read = async () => {
      try {
        const response: any = await sendWithBusyRetry(() => pulse.emitUIIntentionAndWait({
          to: { context, cap: "structure.read", type: "matter" }, targetPathUI: "/agentic/list", identity,
          params: { structure_id: "continuities", want_meaning: false, want_brique: false },
        } as any), busy);
        if (!alive.current) return;
        if (response.status === "error") { setError(JSON.stringify(response.error)); return; }
        const items: Array<{ matter_id?: string }> = response.payload?.functional?.items ?? [];
        const next = items.map((item) => String(item.matter_id ?? "").replace(/^continuity_/, "")).filter(Boolean).sort();
        setError(null);
        setIds((previous) => (previous.join("|") === next.join("|") ? previous : next));
      } catch (err) {
        if (!alive.current) return;
        setError(String(err));
        // A request that was in flight when the connection changed (the page switches from the anonymous connection to the one the browser
        // extension opens after the first click) fails with "connection lost". That is not a state to stay in for a whole period: read again soon.
        if (TRANSIENT_CONNECTION_ERROR.test(String(err))) { clearTimeout(retry); retry = setTimeout(read, RETRY_AFTER_CONNECTION_ERROR_MS); }
      }
    };
    void read();
    const timer = setInterval(read, everyMs);
    return () => { alive.current = false; clearInterval(timer); clearTimeout(retry); };
  }, [pulse, context, everyMs, tick, busy.busyAttempts, busy.busyBaseMs, busy.busyCapMs]);

  return { ids, error, refresh: () => setTick((n) => n + 1) };
}

/**
 * The lines of the journals that the live window (the end of each journal) does not reach: the conversation is whole. `windows` says, for each
 * journal, the first seq its window holds; what is before it is read backwards by pages (model/earlier.ts) and kept.
 */
const EARLIER_ANSWER_MS = 8000;

export function useEarlierLines(pulse: PulseRuntime, settings: BusySettings, context: string, windows: Array<{ id: string; firstSeq: number | null }>): Record<string, JournalLine[]> {
  const known = useRef<Map<string, Map<number, JournalLine>>>(new Map());
  const [, bump] = useReducer((n: number) => n + 1, 0);
  const signature = windows.map((w) => `${w.id}:${w.firstSeq ?? ""}`).join("|");
  useEffect(() => {
    let cancelled = false;
    (async () => {
      for (const { id, firstSeq } of windows) {
        if (firstSeq === null || firstSeq <= 1 || cancelled) continue;
        const lines = known.current.get(id) ?? new Map<number, JournalLine>();
        known.current.set(id, lines);
        const read: ReadLines = async (offset, length) => {
          const request = sendWithBusyRetry(() => (pulse as any).emitUIIntentionAndWait({
            to: { context, cap: "matter.read", type: "matter" }, targetPathUI: "/agentic/earlier", identity: { id: "agentic-user", kind: "human" },
            params: { matter_id: `journal_${id}`, data_range: { unit: "lines", offset, length } },
          }), settings);
          // an answer the link cannot carry may never come back (an engine that does not say so): after a while it counts as too large
          let timer: ReturnType<typeof setTimeout> | undefined;
          const silence = new Promise<never>((_, reject) => { timer = setTimeout(() => reject(Object.assign(new Error("no answer"), { code: NO_ANSWER })), EARLIER_ANSWER_MS); });
          const response: any = await Promise.race([request, silence]).finally(() => clearTimeout(timer));
          if (response.status === "error") throw Object.assign(new Error(response.error?.message ?? "read failed"), { code: response.error?.code });
          return journalTextOf(response.payload);
        };
        await readEarlier(read, firstSeq, lines, () => { if (!cancelled) bump(); }, () => cancelled);
      }
    })();
    return () => { cancelled = true; };
  }, [pulse, context, signature]);
  const out: Record<string, JournalLine[]> = {};
  for (const [id, lines] of known.current) out[id] = [...lines.values()].sort((a, b) => a.seq - b.seq);
  return out;
}
