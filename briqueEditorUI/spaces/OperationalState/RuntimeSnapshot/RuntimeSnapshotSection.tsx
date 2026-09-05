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

import { useCallback, useEffect, useMemo, useRef } from "react";
import { useBriqueSubstrate } from "../../../React_Substrate_Adapter/context.js";
import { createCapabilityClient } from "../../../Brique_Substrate/capability/index.js";
import type { ReadStatePayload } from "../../../Brique_Substrate/capability/typed.js";
import type { RuntimeSnapshot } from "../OperationalStateSpace.js";

// ReadStatePayload used by SnapshotData below
type _ReadStatePayload = ReadStatePayload;

export type RuntimeSnapshotSectionProps = {
  selectedContext: string | undefined;
  snapshot: RuntimeSnapshot;
  onSnapshotChange: (s: RuntimeSnapshot) => void;
  readRequestId: number;
};

export function RuntimeSnapshotSection({ selectedContext, snapshot, onSnapshotChange, readRequestId }: RuntimeSnapshotSectionProps) {
  const substrate = useBriqueSubstrate();
  const capabilityClient = useMemo(() => createCapabilityClient(substrate), [substrate]);
  const seqRef = useRef(0);
  const prevContext = useRef<string | undefined>(undefined);

  if (prevContext.current !== selectedContext) {
    prevContext.current = selectedContext;
    if (snapshot.kind !== "empty") onSnapshotChange({ kind: "empty" });
  }

  const handleReadState = useCallback(async () => {
    if (!selectedContext) return;
    const seq = ++seqRef.current;
    onSnapshotChange({ kind: "loading" });
    try {
      const result = await capabilityClient.read.state(selectedContext, {
        include: ["context", "trace", "wrappers", "families"],
      });
      if (seq !== seqRef.current) return;
      if (!result.ok || !result.payload) {
        onSnapshotChange({ kind: "error", message: result.error?.message ?? "read.state failed" });
        return;
      }
      onSnapshotChange({ kind: "ready", payload: result.payload, acquiredAt: new Date() });
    } catch (err) {
      if (seq !== seqRef.current) return;
      onSnapshotChange({ kind: "error", message: err instanceof Error ? err.message : "read.state failed" });
    }
  }, [capabilityClient, selectedContext]);

  const canRead = !!selectedContext && snapshot.kind !== "loading";

  useEffect(() => {
    if (readRequestId > 0) void handleReadState();
  }, [handleReadState, readRequestId]);

  return (
    <section aria-label="Runtime Snapshot" style={styles.root}>
      <div style={styles.sectionHeader}>
        <span style={styles.sectionLabel}>Runtime Snapshot</span>
      </div>
      <button
        disabled={!canRead}
        onClick={handleReadState}
        style={{ ...styles.button, ...(!canRead ? styles.buttonDisabled : undefined) }}
        type="button"
      >
        {snapshot.kind === "loading" ? "Reading…" : "Read State"}
      </button>

      {snapshot.kind === "empty" && (
        <p style={styles.empty}>No snapshot loaded.</p>
      )}

      {snapshot.kind === "error" && (
        <p style={styles.error}>{snapshot.message}</p>
      )}

      {snapshot.kind === "ready" && (
        <div style={styles.body}>
          <SnapshotTimestamp acquiredAt={snapshot.acquiredAt} />
          <SnapshotData payload={snapshot.payload} />
        </div>
      )}
    </section>
  );
}

function SnapshotTimestamp({ acquiredAt }: { acquiredAt: Date }) {
  const formatted = acquiredAt.toISOString().replace("T", " ").slice(0, 19) + " UTC";
  return (
    <div style={styles.timestamp}>
      <span style={styles.timestampLabel}>Acquired</span>
      <span style={styles.timestampValue}>{formatted}</span>
    </div>
  );
}

function SnapshotData({ payload }: { payload: ReadStatePayload }) {
  return (
    <div style={styles.data}>
      {payload.context && (
        <DataSection label="Context">
          <DataRow label="ID" value={payload.context.context_id} />
          {payload.context.context_name && <DataRow label="Name" value={payload.context.context_name} />}
          {payload.context.ctx_version && <DataRow label="Version" value={payload.context.ctx_version} />}
          {payload.context.engine_version && <DataRow label="Engine" value={payload.context.engine_version} />}
        </DataSection>
      )}

      {payload.families && Object.keys(payload.families).length > 0 && (
        <DataSection label="Families">
          {Object.entries(payload.families).sort().map(([name, state]) => (
            <DataRow key={name} label={name} value={state} />
          ))}
        </DataSection>
      )}

    </div>
  );
}

function DataSection({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div style={styles.dataSection}>
      <div style={styles.dataSectionLabel}>{label}</div>
      {children}
    </div>
  );
}

function DataRow({ label, value }: { label: string; value: string }) {
  return (
    <div style={styles.dataRow}>
      <span style={styles.dataLabel}>{label}</span>
      <span style={styles.dataValue}>{value}</span>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  root: {
    display: "flex",
    flexDirection: "column",
    gap: 8,
    padding: "8px 10px",
  },
  sectionHeader: {
    display: "flex",
    alignItems: "center",
  },
  sectionLabel: {
    fontSize: 10,
    fontWeight: 700,
    textTransform: "uppercase",
    letterSpacing: "0.06em",
    opacity: 0.5,
  },
  button: {
    alignSelf: "flex-start",
    border: "1px solid var(--syn-control-border)",
    borderRadius: 4,
    background: "var(--syn-control-bg)",
    color: "inherit",
    cursor: "pointer",
    fontFamily: "inherit",
    fontSize: 11,
    padding: "3px 8px",
  },
  buttonDisabled: {
    opacity: 0.3,
    cursor: "default",
  },
  empty: {
    margin: 0,
    fontSize: 11,
    opacity: 0.45,
  },
  error: {
    margin: 0,
    fontSize: 11,
    color: "var(--syn-feedback-error)",
  },
  body: {
    display: "flex",
    flexDirection: "column",
    gap: 8,
  },
  timestamp: {
    display: "flex",
    gap: 6,
    alignItems: "baseline",
    fontSize: 10,
    opacity: 0.55,
  },
  timestampLabel: {
    fontWeight: 700,
    textTransform: "uppercase",
    letterSpacing: "0.05em",
  },
  timestampValue: {
    fontFamily: "monospace",
  },
  data: {
    display: "flex",
    flexDirection: "column",
    gap: 8,
  },
  dataSection: {
    display: "flex",
    flexDirection: "column",
    gap: 2,
  },
  dataSectionLabel: {
    fontSize: 9,
    fontWeight: 700,
    textTransform: "uppercase",
    letterSpacing: "0.08em",
    opacity: 0.4,
    marginBottom: 2,
  },
  dataRow: {
    display: "flex",
    gap: 6,
    fontSize: 11,
  },
  dataLabel: {
    opacity: 0.55,
    minWidth: 64,
    flexShrink: 0,
  },
  dataValue: {
    fontFamily: "monospace",
    wordBreak: "break-all",
  },
  wrapper: {
    display: "flex",
    flexDirection: "column",
    gap: 2,
    padding: "4px 6px",
    border: "1px solid var(--syn-border-soft)",
    borderRadius: 4,
    marginBottom: 4,
  },
  wrapperName: {
    fontSize: 11,
    fontWeight: 700,
    marginBottom: 2,
  },
};
