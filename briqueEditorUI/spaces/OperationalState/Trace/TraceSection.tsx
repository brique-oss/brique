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

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useBriqueSubstrate } from "../../../React_Substrate_Adapter/context.js";
import { useHostBridge } from "../../../HostBridge/context.js";
import { createCapabilityClient } from "../../../Brique_Substrate/capability/index.js";
import type { CapabilityClient } from "../../../Brique_Substrate/capability/index.js";

const TRACE_LEVELS = ["minimal", "normal", "debug"] as const;
type TraceLevel = (typeof TRACE_LEVELS)[number];

type MutationState =
  | { kind: "idle" }
  | { kind: "saving" }
  | { kind: "error"; message: string };

export type TraceSectionProps = {
  selectedContext: string | undefined;
  trace: { enabled: boolean; level: string } | undefined;
};

export function TraceSection({ selectedContext, trace }: TraceSectionProps) {
  const { hostBridge } = useHostBridge();
  const substrate = useBriqueSubstrate();
  const capabilityClient = useMemo(() => createCapabilityClient(substrate), [substrate]);
  const [level, setLevel] = useState<TraceLevel | undefined>(parseTraceLevel(trace?.level));
  const [mutation, setMutation] = useState<MutationState>({ kind: "idle" });
  const [pressed, setPressed] = useState<"enable" | "disable" | "enable-subtree" | "disable-subtree" | "clear" | "clear-subtree" | undefined>();
  const requestRef = useRef(0);

  useEffect(() => {
    requestRef.current += 1;
    setMutation({ kind: "idle" });
    setPressed(undefined);
    setLevel(parseTraceLevel(trace?.level));
  }, [selectedContext, trace?.level]);

  const patchTraceConfig = useCallback(async (updates: { enabled?: boolean; level?: TraceLevel }): Promise<boolean | undefined> => {
    if (!selectedContext || mutation.kind === "saving") return false;
    const request = ++requestRef.current;
    setMutation({ kind: "saving" });

    try {
      await writeTraceConfig(capabilityClient, selectedContext, updates);
      if (request !== requestRef.current) return undefined;
      setMutation({ kind: "idle" });
      return true;
    } catch (error) {
      if (request !== requestRef.current) return undefined;
      setMutation({
        kind: "error",
        message: error instanceof Error ? error.message : "Unable to update trace configuration",
      });
      return false;
    }
  }, [capabilityClient, mutation.kind, selectedContext]);

  const patchTraceSubtree = useCallback(async (enabled: boolean) => {
    if (!selectedContext || !level || mutation.kind === "saving") return;
    const request = ++requestRef.current;
    setMutation({ kind: "saving" });

    try {
      const failures: Array<{ context: string; message: string }> = [];
      const visited = new Set<string>();
      const pending = [selectedContext];

      while (pending.length > 0) {
        if (request !== requestRef.current) return;
        const context = pending.shift();
        if (!context || visited.has(context)) continue;
        visited.add(context);

        try {
          const brique = await readContextBrique(capabilityClient, context);
          for (const child of childContextNames(brique)) {
            pending.push(`${context}/${child}`);
          }
          await writeTraceConfig(capabilityClient, context, { enabled, level }, brique);
        } catch (error) {
          failures.push({
            context,
            message: error instanceof Error ? error.message : "Unable to update trace configuration",
          });
        }
      }

      if (request !== requestRef.current) return;
      if (failures.length > 0) {
        const first = failures[0];
        setMutation({
          kind: "error",
          message: `${failures.length}/${visited.size} context${failures.length === 1 ? "" : "s"} failed. ${first.context}: ${first.message}`,
        });
      } else {
        setMutation({ kind: "idle" });
      }
    } catch (error) {
      if (request !== requestRef.current) return;
      setMutation({
        kind: "error",
        message: error instanceof Error ? error.message : "Unable to update trace subtree",
      });
    }
  }, [capabilityClient, level, mutation.kind, selectedContext]);

  const clearTrace = useCallback(async (subtree: boolean) => {
    if (!selectedContext || mutation.kind === "saving") return;
    const request = ++requestRef.current;
    setMutation({ kind: "saving" });
    try {
      const contextDirs: string[] = [];
      for (const context of [selectedContext]) {
        if (request !== requestRef.current) return;
        const result = await capabilityClient.read.state(context, { include: ["context"] });
        const contextDir = result.payload?.context?.context_dir;
        if (!result.ok || !contextDir) {
          throw new Error(result.error?.message ?? `Unable to locate context on disk: ${context}`);
        }
        contextDirs.push(contextDir);
      }
      if (request !== requestRef.current) return;
      if (!hostBridge?.clearTraceFiles) {
        throw new Error("Trace deletion host bridge is unavailable. Rebuild and restart Spark.");
      }
      await hostBridge.clearTraceFiles({ contextDirs, recursive: subtree });
      if (request !== requestRef.current) return;
      setMutation({ kind: "idle" });
    } catch (error) {
      if (request !== requestRef.current) return;
      setMutation({
        kind: "error",
        message: error instanceof Error ? error.message : "Unable to clear trace files",
      });
    }
  }, [capabilityClient, hostBridge, mutation.kind, selectedContext]);

  const canAct = !!selectedContext;
  const canActOnSubtree = canAct && !!level;
  const canClear = canAct;

  return (
    <section aria-label="Trace" style={styles.root}>
      <div style={styles.label}>Trace</div>

      {!selectedContext && <p style={styles.empty}>No context selected.</p>}
      {selectedContext && !trace && <p style={styles.empty}>Trace state unknown. Read State to inspect it.</p>}
      {trace && (
        <div style={styles.info}>
          <div style={styles.infoRow}>
            <span style={styles.infoLabel}>Enabled</span>
            <span style={styles.infoValue}>{String(trace.enabled)}</span>
          </div>
          <div style={styles.infoRow}>
            <span style={styles.infoLabel}>Level</span>
            <span style={styles.infoValue}>{trace.level}</span>
          </div>
        </div>
      )}

      <label style={styles.levelControl}>
        <span style={styles.levelLabel}>Trace level</span>
        <select
          disabled={!canAct}
          onChange={(event) => {
            const previousLevel = level;
            const nextLevel = event.target.value as TraceLevel;
            setLevel(nextLevel);
            void patchTraceConfig({ level: nextLevel }).then((saved) => {
              if (saved === false) setLevel(previousLevel);
            });
          }}
          style={{ ...styles.select, ...(!canAct ? styles.controlDisabled : undefined) }}
          value={level ?? ""}
        >
          <option disabled value="">Unknown</option>
          {TRACE_LEVELS.map((candidate) => <option key={candidate} value={candidate}>{candidate}</option>)}
        </select>
      </label>

      <div style={styles.actions}>
        <button
          disabled={!canAct}
          onClick={() => void patchTraceConfig({ enabled: true })}
          onMouseDown={() => canAct && setPressed("enable")}
          onMouseLeave={() => setPressed(undefined)}
          onMouseUp={() => setPressed(undefined)}
          style={{ ...styles.button, ...(pressed === "enable" ? styles.buttonPressed : undefined), ...(!canAct ? styles.controlDisabled : undefined) }}
          type="button"
        >Enable</button>
        <button
          disabled={!canAct}
          onClick={() => void patchTraceConfig({ enabled: false })}
          onMouseDown={() => canAct && setPressed("disable")}
          onMouseLeave={() => setPressed(undefined)}
          onMouseUp={() => setPressed(undefined)}
          style={{ ...styles.button, ...(pressed === "disable" ? styles.buttonPressed : undefined), ...(!canAct ? styles.controlDisabled : undefined) }}
          type="button"
        >Disable</button>
        <button
          disabled={!canActOnSubtree}
          onClick={() => void patchTraceSubtree(true)}
          onMouseDown={() => canActOnSubtree && setPressed("enable-subtree")}
          onMouseLeave={() => setPressed(undefined)}
          onMouseUp={() => setPressed(undefined)}
          style={{ ...styles.button, ...(pressed === "enable-subtree" ? styles.buttonPressed : undefined), ...(!canActOnSubtree ? styles.controlDisabled : undefined) }}
          type="button"
        >Enable Subtree</button>
        <button
          disabled={!canActOnSubtree}
          onClick={() => void patchTraceSubtree(false)}
          onMouseDown={() => canActOnSubtree && setPressed("disable-subtree")}
          onMouseLeave={() => setPressed(undefined)}
          onMouseUp={() => setPressed(undefined)}
          style={{ ...styles.button, ...(pressed === "disable-subtree" ? styles.buttonPressed : undefined), ...(!canActOnSubtree ? styles.controlDisabled : undefined) }}
          type="button"
        >Disable Subtree</button>
        <button
          disabled={!canClear}
          onClick={() => void clearTrace(false)}
          onMouseDown={() => canClear && setPressed("clear")}
          onMouseLeave={() => setPressed(undefined)}
          onMouseUp={() => setPressed(undefined)}
          style={{ ...styles.buttonDestructive, ...(pressed === "clear" ? styles.buttonPressed : undefined), ...(!canClear ? styles.controlDisabled : undefined) }}
          type="button"
        >Clear</button>
        <button
          disabled={!canClear}
          onClick={() => void clearTrace(true)}
          onMouseDown={() => canClear && setPressed("clear-subtree")}
          onMouseLeave={() => setPressed(undefined)}
          onMouseUp={() => setPressed(undefined)}
          style={{ ...styles.buttonDestructive, ...(pressed === "clear-subtree" ? styles.buttonPressed : undefined), ...(!canClear ? styles.controlDisabled : undefined) }}
          type="button"
        >Clear Subtree</button>
      </div>

      {mutation.kind === "error" && <p style={styles.error}>{mutation.message}</p>}
    </section>
  );
}

async function writeTraceConfig(
  capabilityClient: CapabilityClient,
  context: string,
  updates: { enabled?: boolean; level?: TraceLevel },
  existingBrique?: Record<string, unknown>,
): Promise<{ enabled: boolean; level: TraceLevel }> {
  const brique = existingBrique ?? await readContextBrique(capabilityClient, context);
  const engineConfig = asRecord(brique.engine_config);
  const currentTrace = asRecord(engineConfig.trace);
  const nextBrique = {
    ...brique,
    engine_config: {
      ...engineConfig,
      trace: { ...currentTrace, ...updates },
    },
  };

  const patchResult = await capabilityClient.edit.patchMeaning(context, {
    items: [{
      ctx_id: context,
      element_kind: "context",
      element_name: context,
      patch: { brique: nextBrique },
    }],
  });
  const patchItem = patchResult.payload?.result?.[0];
  if (!patchResult.ok || !patchItem?.ok) {
    throw new Error(patchResult.error?.message ?? itemErrorMessage(patchItem) ?? "Unable to update context configuration");
  }
  return traceConfigFromBrique(nextBrique);
}

async function readContextBrique(
  capabilityClient: CapabilityClient,
  context: string,
): Promise<Record<string, unknown>> {
  const readResult = await capabilityClient.read.meaning(context, {
    input: [{ element_kind: "context", element_name: context, sections: ["brique"] }],
  });
  const readItem = readResult.payload?.result?.[0];
  if (!readResult.ok || !readItem?.ok) {
    throw new Error(readResult.error?.message ?? itemErrorMessage(readItem) ?? "Unable to read context configuration");
  }

  const brique = readItem.descriptor?.brique;
  if (!brique) throw new Error("Context configuration has no brique section");
  return brique;
}

function childContextNames(brique: Record<string, unknown>): string[] {
  if (!Array.isArray(brique.children_list)) return [];
  return brique.children_list.filter((value): value is string =>
    typeof value === "string"
    && value.length > 0
    && value !== "."
    && value !== ".."
    && !value.includes("/")
    && !value.includes("\\")
  );
}

function traceConfigFromBrique(brique: Record<string, unknown> | undefined): { enabled: boolean; level: TraceLevel } {
  const engineConfig = asRecord(brique?.engine_config);
  const traceConfig = asRecord(engineConfig.trace);
  return {
    enabled: traceConfig.enabled === true,
    level: toTraceLevel(typeof traceConfig.level === "string" ? traceConfig.level : undefined),
  };
}

function toTraceLevel(value: string | undefined): TraceLevel {
  return parseTraceLevel(value) ?? "normal";
}

function parseTraceLevel(value: string | undefined): TraceLevel | undefined {
  return TRACE_LEVELS.includes(value as TraceLevel) ? value as TraceLevel : undefined;
}

function asRecord(value: unknown): Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : {};
}

function itemErrorMessage(item: Record<string, unknown> | undefined): string | undefined {
  const error = asRecord(item?.error);
  return typeof error.message === "string" ? error.message : undefined;
}

const styles: Record<string, React.CSSProperties> = {
  root: { display: "flex", flexDirection: "column", gap: 8, padding: "8px 10px" },
  label: { fontSize: 9, fontWeight: 700, textTransform: "uppercase", letterSpacing: "0.08em", opacity: 0.4 },
  info: { display: "flex", flexDirection: "column", gap: 2 },
  infoRow: { display: "flex", gap: 6, fontSize: 11 },
  infoLabel: { opacity: 0.55, minWidth: 52, flexShrink: 0 },
  infoValue: { fontFamily: "monospace" },
  empty: { margin: 0, fontSize: 10, opacity: 0.45 },
  levelControl: { display: "flex", flexDirection: "column", alignItems: "flex-start", gap: 3 },
  levelLabel: { fontSize: 10, opacity: 0.55 },
  select: { border: "1px solid var(--syn-control-border)", borderRadius: 4, background: "var(--syn-control-bg)", color: "inherit", fontFamily: "inherit", fontSize: 11, padding: "3px 6px" },
  actions: { display: "flex", flexDirection: "column", gap: 4 },
  button: { alignSelf: "flex-start", border: "1px solid var(--syn-control-border)", borderRadius: 4, background: "var(--syn-control-bg)", color: "inherit", cursor: "pointer", fontFamily: "inherit", fontSize: 11, padding: "3px 8px" },
  buttonPressed: { background: "var(--syn-control-active-bg)", borderColor: "var(--syn-selected-border)", color: "var(--syn-feedback-warning)" },
  buttonDestructive: { alignSelf: "flex-start", border: "1px solid var(--syn-feedback-error)", borderRadius: 4, background: "var(--syn-feedback-error-bg)", color: "var(--syn-feedback-error)", cursor: "pointer", fontFamily: "inherit", fontSize: 11, padding: "3px 8px" },
  controlDisabled: { opacity: 0.3, cursor: "default" },
  error: { margin: 0, fontSize: 10, color: "var(--syn-feedback-error)" },
};
