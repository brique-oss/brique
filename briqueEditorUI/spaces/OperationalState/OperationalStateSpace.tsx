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

import { useEffect, useMemo, useRef, useState } from "react";
import { useBriqueSubstrate } from "../../React_Substrate_Adapter/context.js";
import { createCapabilityClient } from "../../Brique_Substrate/capability/index.js";
import { CollapsedActions } from "./CollapsedActions.js";
import { RuntimeSnapshotSection } from "./RuntimeSnapshot/RuntimeSnapshotSection.js";
import { ContextLifecycleSection } from "./ContextLifecycle/ContextLifecycleSection.js";
import { UiProjectionsSection } from "./UiProjections/UiProjectionsSection.js";
import { SemanticFieldSection } from "./SemanticField/SemanticFieldSection.js";
import { WrappersSection } from "./Wrappers/WrappersSection.js";
import { TraceSection } from "./Trace/TraceSection.js";
import type { ReadStatePayload } from "../../Brique_Substrate/capability/typed.js";

export type RuntimeSnapshot =
  | { kind: "empty" }
  | { kind: "loading" }
  | { kind: "ready"; payload: ReadStatePayload; acquiredAt: Date }
  | { kind: "error"; message: string };

export type OperationalStateSpaceProps = {
  selectedContext: string | undefined;
};

export function OperationalStateSpace({ selectedContext }: OperationalStateSpaceProps) {
  const substrate = useBriqueSubstrate();
  const client = useMemo(() => createCapabilityClient(substrate), [substrate]);
  const [expanded, setExpanded] = useState(false);
  const [readRequestId, setReadRequestId] = useState(0);
  const [snapshot, setSnapshot] = useState<RuntimeSnapshot>({ kind: "empty" });
  const seqRef = useRef(0);
  const failRef = useRef(0);

  const payload = snapshot.kind === "ready" ? snapshot.payload : undefined;

  // Silent auto-poll every second when expanded — never goes through "loading"
  useEffect(() => {
    if (!expanded || !selectedContext) return;
    const t = setInterval(async () => {
      const seq = ++seqRef.current;
      try {
        const result = await client.read.state(selectedContext, {
          include: ["context", "trace", "wrappers", "families"],
        });
        if (seq !== seqRef.current) return;
        if (result.ok && result.payload) {
          failRef.current = 0;
          setSnapshot({ kind: "ready", payload: result.payload, acquiredAt: new Date() });
        } else {
          if (++failRef.current >= 3) {
            setSnapshot({ kind: "error", message: result.error?.message ?? "read.state failed" });
          }
        }
      } catch (err) {
        if (seq !== seqRef.current) return;
        if (++failRef.current >= 3) {
          setSnapshot({ kind: "error", message: err instanceof Error ? err.message : "unreachable" });
        }
      }
    }, 1000);
    return () => clearInterval(t);
  }, [expanded, selectedContext, client]);

  return (
    <section
      aria-label="Brique Editor operational state"
      data-space="operational-state"
      style={{ ...styles.root, ...(expanded ? styles.expanded : styles.collapsed) }}
    >
      <div
        onClick={() => { if (!expanded) setReadRequestId((v) => v + 1); setExpanded((v) => !v); }}
        style={styles.header}
        role="button"
        title={expanded ? "Collapse" : "Expand Operational State"}
      >
        <span style={styles.headerTitle}>{selectedContext ?? "Operational State"}</span>
        {!expanded && <CollapsedActions selectedContext={selectedContext} />}
        <span style={styles.headerToggle}>{expanded ? "▼" : "▲"}</span>
      </div>
      {expanded && (
        <div style={styles.columns}>
          {/* Col 1 — Runtime Snapshot */}
          <div style={styles.column}>
            <RuntimeSnapshotSection
              selectedContext={selectedContext}
              snapshot={snapshot}
              onSnapshotChange={setSnapshot}
              readRequestId={readRequestId}
            />
          </div>

          {/* Col 2 — Context Lifecycle + UI Projections + Semantic Field */}
          <div style={styles.column}>
            <ContextLifecycleSection selectedContext={selectedContext} />
            <UiProjectionsSection selectedContext={selectedContext} />
            <SemanticFieldSection selectedContext={selectedContext} />
          </div>

          {/* Col 3 — Wrappers */}
          <div style={styles.column}>
            <WrappersSection
              selectedContext={selectedContext}
              wrappers={payload?.wrappers}
            />
          </div>

          {/* Col 4 — Trace */}
          <div style={styles.column}>
            <TraceSection selectedContext={selectedContext} trace={payload?.trace} />
          </div>
        </div>
      )}
    </section>
  );
}

const styles: Record<string, React.CSSProperties> = {
  root: {
    display: "flex",
    flexDirection: "column",
    boxSizing: "border-box",
    background: "var(--syn-operational-bg)",
    color: "var(--syn-text-primary)",
    fontFamily: "monospace",
    overflow: "hidden",
  },
  collapsed: {
    height: "100%",
    cursor: "pointer",
  },
  expanded: {
    position: "absolute",
    bottom: 0,
    left: 0,
    right: 0,
    height: "35%",
    zIndex: 10,
    borderTop: "1px solid var(--syn-observation-border)",
  },
  header: {
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between",
    padding: "0 10px",
    flexShrink: 0,
    cursor: "pointer",
    userSelect: "none",
    background: "var(--syn-operational-panel-bg)",
    borderTop: "1px solid var(--syn-border-soft)",
    minHeight: 20,
  },
  headerTitle: {
    fontSize: 8,
    fontWeight: 700,
    textTransform: "uppercase",
    letterSpacing: "0.08em",
    opacity: 0.6,
    marginRight: 6,
    flexShrink: 0,
  },
  headerToggle: {
    fontSize: 10,
    opacity: 0.5,
  },
  columns: {
    display: "grid",
    gridTemplateColumns: "30fr 20fr 30fr 20fr",
    flex: 1,
    minHeight: 0,
    overflow: "hidden",
  },
  column: {
    display: "flex",
    flexDirection: "column",
    overflowY: "auto",
    borderRight: "1px solid var(--syn-border-soft)",
    minHeight: 0,
  },
};
