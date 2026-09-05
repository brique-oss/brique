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

import { useCallback, useEffect, useRef, useState } from "react";
import { useBriqueSubstrate } from "../../../React_Substrate_Adapter/context.js";
import type { WrapperSnapshot } from "../../../Brique_Substrate/capability/typed.js";

type WrapperActionState = "idle" | "start" | "stop" | "restart";

export type WrappersSectionProps = {
  selectedContext: string | undefined;
  wrappers: WrapperSnapshot[] | undefined;
};

export function WrappersSection({ selectedContext, wrappers }: WrappersSectionProps) {
  const substrate = useBriqueSubstrate();
  // actionState per wrapper — drives display and button disabled state
  const [pending, setPending] = useState<Record<string, WrapperActionState>>({});
  // timestamp when action was fired — used to ignore stale polls
  const sentAtRef = useRef<Record<string, number>>({});
  // timeout handles to force-clear pending if poll never catches a transient state
  const timeoutRef = useRef<Record<string, ReturnType<typeof setTimeout>>>({});

  const clearPending = useCallback((wrapperName: string) => {
    setPending((p) => {
      if ((p[wrapperName] ?? "idle") === "idle") return p;
      const next = { ...p };
      next[wrapperName] = "idle";
      return next;
    });
    clearTimeout(timeoutRef.current[wrapperName]);
    delete timeoutRef.current[wrapperName];
    delete sentAtRef.current[wrapperName];
  }, []);

  const sendWrapperCommand = useCallback((wrapperName: string, action: "wrapper.start" | "wrapper.stop" | "wrapper.restart") => {
    if (!selectedContext) return;
    const actionKey = action.replace("wrapper.", "") as WrapperActionState;
    sentAtRef.current[wrapperName] = Date.now();
    setPending((p) => ({ ...p, [wrapperName]: actionKey }));
    // Safety timeout: force idle after 15s if poll never resolves
    clearTimeout(timeoutRef.current[wrapperName]);
    timeoutRef.current[wrapperName] = setTimeout(() => clearPending(wrapperName), 15000);
    void substrate.mutate({
      context: selectedContext,
      capability: action,
      params: { name: wrapperName },
    });
  }, [substrate, selectedContext, clearPending]);

  // Watch wrappers props — clear pending when state is stable AND poll is fresh
  useEffect(() => {
    if (!wrappers) return;
    setPending((prev) => {
      let changed = false;
      const next = { ...prev };
      for (const [name, action] of Object.entries(prev)) {
        if (action === "idle") continue;
        const w = wrappers.find((x) => x.name === name);
        if (!w) continue;
        const sentAt = sentAtRef.current[name] ?? 0;
        // Ignore polls that arrived before the command was sent
        if (sentAt === 0) continue;
        const state = w.proc_state;
        const isTerminal =
          (action === "stop" && state === "exited") ||
          ((action === "start" || action === "restart") && (state === "running" || !!w.ready_error));
        if (isTerminal) {
          next[name] = "idle";
          clearTimeout(timeoutRef.current[name]);
          delete timeoutRef.current[name];
          delete sentAtRef.current[name];
          changed = true;
        }
      }
      return changed ? next : prev;
    });
  }, [wrappers]);

  // Cleanup on unmount
  useEffect(() => {
    return () => { Object.values(timeoutRef.current).forEach(clearTimeout); };
  }, []);

  const canAct = !!selectedContext;

  return (
    <section aria-label="Wrappers" style={styles.root}>
      <div style={styles.label}>Wrappers</div>
      {!canAct || !wrappers || wrappers.length === 0 ? (
        <p style={styles.empty}>No wrappers defined.</p>
      ) : (
        <div style={styles.list}>
          {wrappers.map((w) => (
            <WrapperItem
              key={w.name}
              wrapper={w}
              actionState={pending[w.name] ?? "idle"}
              onStart={() => sendWrapperCommand(w.name, "wrapper.start")}
              onStop={() => sendWrapperCommand(w.name, "wrapper.stop")}
              onRestart={() => sendWrapperCommand(w.name, "wrapper.restart")}
            />
          ))}
        </div>
      )}
    </section>
  );
}

function WrapperItem({
  wrapper: w,
  actionState,
  onStart,
  onStop,
  onRestart,
}: {
  wrapper: WrapperSnapshot;
  actionState: WrapperActionState;
  onStart: () => void;
  onStop: () => void;
  onRestart: () => void;
}) {
  const busy = actionState !== "idle";
  const displayState = resolveDisplayState(w, actionState);
  const stateColor = procStateColor(displayState);
  const error = w.ready_error ?? w.last_error;

  return (
    <div style={styles.wrapper}>
      <div style={styles.wrapperHeader}>
        <span style={styles.wrapperName}>{w.name}</span>
        <span style={{ ...styles.wrapperState, color: stateColor }}>{displayState}</span>
      </div>
      <div style={styles.wrapperInfo}>
        <span>PID: {w.pid > 0 ? w.pid : "—"}</span>
        <span>Ready: {w.ready ? "yes" : "no"}</span>
      </div>
      {error && <span style={styles.error}>{error}</span>}
      <div style={styles.actions}>
        <ActionButton label="Start" busy={busy} onClick={onStart} />
        <ActionButton label="Stop" busy={busy} onClick={onStop} />
        <ActionButton label="Restart" busy={busy} onClick={onRestart} />
      </div>
    </div>
  );
}

function resolveDisplayState(w: WrapperSnapshot, actionState: WrapperActionState): string {
  if (actionState === "start" || actionState === "restart") return "starting…";
  if (actionState === "stop") return "stop requested";
  if (w.proc_state === "stopping") return "stop requested";
  return w.proc_state;
}

function procStateColor(state: string): string {
  switch (state) {
    case "running": return "var(--syn-feedback-success)";
    case "starting…":
    case "starting":
    case "building": return "var(--syn-feedback-warning)";
    case "stop requested":
    case "exited": return "var(--syn-feedback-warning)";
    case "unknown": return "var(--syn-text-muted)";
    default: return "var(--syn-text-secondary)";
  }
}

function ActionButton({ label, busy, onClick }: { label: string; busy: boolean; onClick: () => void }) {
  const [flash, setFlash] = useState(false);
  return (
    <button
      disabled={busy}
      onClick={onClick}
      onMouseDown={() => !busy && setFlash(true)}
      onMouseUp={() => setFlash(false)}
      onMouseLeave={() => setFlash(false)}
      style={{
        ...styles.button,
        ...(busy ? styles.buttonBusy : undefined),
        ...(flash ? styles.buttonFlash : undefined),
      }}
      type="button"
    >
      {label}
    </button>
  );
}

const styles: Record<string, React.CSSProperties> = {
  root: { display: "flex", flexDirection: "column", gap: 8, padding: "8px 10px" },
  label: { fontSize: 9, fontWeight: 700, textTransform: "uppercase", letterSpacing: "0.08em", opacity: 0.4 },
  empty: { margin: 0, fontSize: 11, opacity: 0.4 },
  list: { display: "flex", flexDirection: "column", gap: 8 },
  wrapper: { display: "flex", flexDirection: "column", gap: 4, padding: "6px 8px", border: "1px solid var(--syn-border-soft)", borderRadius: 4, background: "var(--syn-operational-card-bg)" },
  wrapperHeader: { display: "flex", alignItems: "center", justifyContent: "space-between", gap: 6 },
  wrapperName: { fontSize: 11, fontWeight: 700 },
  wrapperState: { fontSize: 10, fontFamily: "monospace" },
  wrapperInfo: { display: "flex", gap: 10, fontSize: 10, opacity: 0.55 },
  error: { fontSize: 10, color: "var(--syn-feedback-error)", wordBreak: "break-all" },
  actions: { display: "flex", gap: 4 },
  button: { border: "1px solid var(--syn-control-border)", borderRadius: 4, background: "var(--syn-control-bg)", color: "inherit", cursor: "pointer", fontFamily: "inherit", fontSize: 10, padding: "2px 6px" },
  buttonBusy: { opacity: 0.35, cursor: "default" },
  buttonFlash: { background: "var(--syn-control-active-bg)", borderColor: "var(--syn-selected-border)", color: "var(--syn-feedback-warning)" },
};
