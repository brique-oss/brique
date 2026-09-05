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

import { useCallback, useMemo, useState } from "react";
import { useBriqueSubstrate } from "../../../React_Substrate_Adapter/context.js";
import { createCapabilityClient } from "../../../Brique_Substrate/capability/index.js";
import type { StructureNode } from "../../../Brique_Substrate/capability/typed.js";

type RebuildState =
  | { kind: "idle" }
  | { kind: "running" }
  | { kind: "done"; updatedCount: number; durationMs: number }
  | { kind: "error"; message: string };

export type SemanticFieldSectionProps = {
  selectedContext: string | undefined;
};

export function SemanticFieldSection({ selectedContext }: SemanticFieldSectionProps) {
  const substrate = useBriqueSubstrate();
  const capabilityClient = useMemo(() => createCapabilityClient(substrate), [substrate]);
  const [state, setState] = useState<RebuildState>({ kind: "idle" });
  const [flash, setFlash] = useState(false);
  const [flashTree, setFlashTree] = useState(false);

  const canRebuild = !!selectedContext && state.kind !== "running";

  const runUpdate = useCallback(async (contexts: string[]) => {
    setState({ kind: "running" });
    try {
      const result = await capabilityClient.meaning.update("/root", { context: contexts });
      if (!result.ok || !result.payload) {
        setState({ kind: "error", message: result.error?.message ?? "meaning.update failed" });
        return;
      }
      setState({
        kind: "done",
        updatedCount: result.payload.updated_elements_count,
        durationMs: result.payload.duration_ms,
      });
    } catch (err) {
      setState({ kind: "error", message: err instanceof Error ? err.message : "meaning.update failed" });
    }
  }, [capabilityClient]);

  const handleRebuild = useCallback(async () => {
    if (!selectedContext) return;
    await runUpdate([selectedContext]);
  }, [runUpdate, selectedContext]);

  const handleRebuildTree = useCallback(async () => {
    if (!selectedContext) return;
    setState({ kind: "running" });
    try {
      // Collect all context IDs in the subtree via read.structure (depth 8)
      const structResult = await capabilityClient.read.structure(selectedContext, { depth: 8 });
      const contexts: string[] = [selectedContext];
      if (structResult.ok && structResult.payload?.root) {
        function collectContexts(node: StructureNode, parentPath: string) {
          for (const child of node.children ?? []) {
            if (child.kind === "context" && child.name) {
              const childPath = `${parentPath}/${child.name}`;
              contexts.push(childPath);
              collectContexts(child, childPath);
            }
          }
        }
        collectContexts(structResult.payload.root, selectedContext);
      }
      await runUpdate(contexts);
    } catch (err) {
      setState({ kind: "error", message: err instanceof Error ? err.message : "meaning.update tree failed" });
    }
  }, [capabilityClient, runUpdate, selectedContext]);

  return (
    <section aria-label="Semantic Field" style={styles.root}>
      <div style={styles.label}>Semantic Field</div>
      <button
        disabled={!canRebuild}
        onClick={handleRebuild}
        onMouseDown={() => canRebuild && setFlash(true)}
        onMouseUp={() => setFlash(false)}
        onMouseLeave={() => setFlash(false)}
        style={{
          ...styles.button,
          ...(!canRebuild ? styles.buttonDisabled : undefined),
          ...(flash ? styles.buttonFlash : undefined),
        }}
        type="button"
      >
        {state.kind === "running" ? "Rebuilding…" : "Rebuild"}
      </button>
      <button
        disabled={!canRebuild}
        onClick={handleRebuildTree}
        onMouseDown={() => canRebuild && setFlashTree(true)}
        onMouseUp={() => setFlashTree(false)}
        onMouseLeave={() => setFlashTree(false)}
        style={{
          ...styles.button,
          ...(!canRebuild ? styles.buttonDisabled : undefined),
          ...(flashTree ? styles.buttonFlash : undefined),
        }}
        type="button"
      >
        {state.kind === "running" ? "Rebuilding…" : "Rebuild Tree"}
      </button>

      {state.kind === "running" && (
        <p style={styles.status}>Updating semantic field…</p>
      )}
      {state.kind === "done" && (
        <p style={styles.success}>
          {state.updatedCount} element{state.updatedCount !== 1 ? "s" : ""} updated
          {" "}· {state.durationMs}ms
        </p>
      )}
      {state.kind === "error" && (
        <p style={styles.error}>{state.message}</p>
      )}
    </section>
  );
}

const styles: Record<string, React.CSSProperties> = {
  root: { display: "flex", flexDirection: "column", gap: 6, padding: "8px 10px" },
  label: { fontSize: 9, fontWeight: 700, textTransform: "uppercase", letterSpacing: "0.08em", opacity: 0.4 },
  button: { alignSelf: "flex-start", border: "1px solid var(--syn-control-border)", borderRadius: 4, background: "var(--syn-control-bg)", color: "inherit", cursor: "pointer", fontFamily: "inherit", fontSize: 11, padding: "3px 8px" },
  buttonDisabled: { opacity: 0.3, cursor: "default" },
  buttonFlash: { background: "var(--syn-control-active-bg)", borderColor: "var(--syn-selected-border)", color: "var(--syn-feedback-warning)" },
  status: { margin: 0, fontSize: 10, opacity: 0.55 },
  success: { margin: 0, fontSize: 10, color: "var(--syn-feedback-success)" },
  error: { margin: 0, fontSize: 10, color: "var(--syn-feedback-error)" },
};
