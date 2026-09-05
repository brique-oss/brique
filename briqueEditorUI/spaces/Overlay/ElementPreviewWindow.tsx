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

import { useEffect, useRef, useState } from "react";
import { useBriqueSubstrate } from "../../React_Substrate_Adapter/context.js";
import { createCapabilityClient } from "../../Brique_Substrate/capability/index.js";
import type { PreviewTarget } from "./contracts.js";

const WIDTH = 440;
const OFFSET = 12;

type LoadState =
  | { kind: "loading" }
  | { kind: "ready"; objective: Record<string, unknown> | undefined; subjective: Record<string, unknown> | undefined }
  | { kind: "error"; message: string };

function computePos(pos: { x: number; y: number }): { left?: number; right?: number; top?: number; bottom?: number } {
  const midX = window.innerWidth / 2;
  const midY = window.innerHeight / 2;
  const onRight = pos.x > midX;
  const onBottom = pos.y > midY;

  return {
    ...(onRight ? { right: window.innerWidth - pos.x + OFFSET } : { left: pos.x + OFFSET }),
    ...(onBottom ? { bottom: window.innerHeight - pos.y + OFFSET } : { top: pos.y + OFFSET }),
  };
}

function getRecord(v: unknown): Record<string, unknown> | undefined {
  return v && typeof v === "object" && !Array.isArray(v) ? v as Record<string, unknown> : undefined;
}

function SectionBlock({ title, data }: { title: string; data: Record<string, unknown> }) {
  return (
    <div style={styles.section}>
      <div style={styles.sectionTitle}>{title}</div>
      {Object.entries(data).map(([key, val]) => (
        <div key={key} style={styles.field}>
          <span style={styles.fieldKey}>{key}</span>
          <span style={styles.fieldVal}>
            {Array.isArray(val)
              ? val.join(", ") || "—"
              : typeof val === "object" && val !== null
              ? JSON.stringify(val)
              : String(val ?? "—")}
          </span>
        </div>
      ))}
    </div>
  );
}

export type ElementPreviewWindowProps = {
  target: PreviewTarget;
};

export function ElementPreviewWindow({ target }: ElementPreviewWindowProps) {
  const substrate = useRef(useBriqueSubstrate());
  const capabilityClient = useRef(createCapabilityClient(substrate.current));
  const [loadState, setLoadState] = useState<LoadState>({ kind: "loading" });

  const placement = computePos(target.pos);
  const isContext = target.elementKind === "context";
  const displayName = isContext ? target.context : `${target.context}::${target.elementName}`;

  useEffect(() => {
    setLoadState({ kind: "loading" });

    const client = capabilityClient.current;
    const { elementKind, elementName, context } = target;

    async function fetchMeaning(): Promise<{ objective?: Record<string, unknown>; subjective?: Record<string, unknown> }> {
      if (elementKind === "matter") {
        const r = await client.matter.read(context, { matter_id: elementName, read_mode: "meaning" });
        if (!r.ok || !r.payload) throw new Error(r.error?.message ?? "matter.read failed");
        return { objective: getRecord(r.payload.meaning?.objective), subjective: getRecord(r.payload.meaning?.subjective) };
      }

      if (elementKind === "structure") {
        const r = await client.structure.read(context, { structure_id: elementName, want_meaning: true });
        if (!r.ok || !r.payload) throw new Error(r.error?.message ?? "structure.read failed");
        return { objective: getRecord(r.payload.meaning?.objective), subjective: getRecord(r.payload.meaning?.subjective) };
      }

      // schema, capacity, document, context → read.meaning
      const r = await client.read.meaning(context, {
        input: [{ element_kind: elementKind, element_name: elementName, sections: ["objective", "subjective"] }],
      });
      if (!r.ok) throw new Error(r.error?.message ?? "read.meaning failed");
      const items = r.payload?.result ?? [];
      const item = items[0] as Record<string, unknown> | undefined;
      if (!item?.ok) {
        const errObj = item?.error as Record<string, unknown> | string | undefined;
        const errMsg = typeof errObj === "object" && errObj !== null
          ? (errObj.message as string | undefined) ?? JSON.stringify(errObj)
          : (errObj as string | undefined) ?? "Element not found.";
        throw new Error(errMsg);
      }
      const descriptor = getRecord(item.descriptor) ?? getRecord(item.desc) ?? getRecord(item.meaning) ?? item;
      return { objective: getRecord(descriptor.objective), subjective: getRecord(descriptor.subjective) };
    }

    void fetchMeaning().then(
      ({ objective, subjective }) => setLoadState({ kind: "ready", objective, subjective }),
      (err: unknown) => setLoadState({ kind: "error", message: err instanceof Error ? err.message : "Read failed." })
    );
  }, [target.context, target.elementKind, target.elementName]);

  const hasContent = loadState.kind === "ready" && (loadState.objective ?? loadState.subjective);

  return (
    <div style={{ ...styles.window, ...placement }}>
      <div style={styles.header}>
        <span style={styles.kind}>{target.elementKind}</span>
        <span style={styles.name}>{displayName}</span>
      </div>
      <div style={styles.body}>
        {loadState.kind === "loading" && (
          <span style={styles.status}>Loading…</span>
        )}
        {loadState.kind === "error" && (
          <span style={{ ...styles.status, ...styles.statusError }}>{loadState.message}</span>
        )}
        {loadState.kind === "ready" && !hasContent && (
          <span style={styles.status}>No objective or subjective data.</span>
        )}
        {loadState.kind === "ready" && loadState.objective && (
          <SectionBlock title="objective" data={loadState.objective} />
        )}
        {loadState.kind === "ready" && loadState.subjective && (
          <SectionBlock title="subjective" data={loadState.subjective} />
        )}
      </div>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  window: {
    position: "fixed",
    width: WIDTH,
    maxHeight: 480,
    pointerEvents: "auto",
    border: "1px solid var(--syn-border-medium)",
    borderRadius: 10,
    background: "var(--syn-surface-overlay)",
    boxShadow: "0 24px 80px rgba(0,0,0,0.55)",
    color: "var(--syn-text-primary)",
    fontFamily: "monospace",
    display: "flex",
    flexDirection: "column",
    overflow: "hidden",
  },
  header: {
    display: "flex",
    flexDirection: "column",
    gap: 4,
    padding: "12px 14px 10px",
    borderBottom: "1px solid var(--syn-border-soft)",
    flexShrink: 0,
  },
  kind: {
    fontSize: 10,
    fontWeight: 900,
    letterSpacing: "0.1em",
    textTransform: "uppercase",
    color: "var(--syn-text-muted)",
  },
  name: {
    fontSize: 13,
    fontWeight: 700,
    color: "var(--syn-text-primary)",
    wordBreak: "break-all",
  },
  body: {
    overflowY: "auto",
    flex: 1,
    padding: "10px 14px 14px",
    display: "flex",
    flexDirection: "column",
    gap: 12,
  },
  status: {
    fontSize: 11,
    color: "var(--syn-text-muted)",
    fontStyle: "italic",
  },
  statusError: {
    color: "var(--syn-feedback-error)",
    fontStyle: "normal",
  },
  section: {
    display: "flex",
    flexDirection: "column",
    gap: 4,
  },
  sectionTitle: {
    fontSize: 9,
    fontWeight: 900,
    letterSpacing: "0.1em",
    textTransform: "uppercase",
    color: "var(--syn-text-muted)",
    marginBottom: 4,
  },
  field: {
    display: "flex",
    flexDirection: "column",
    gap: 2,
    paddingBottom: 6,
    borderBottom: "1px solid var(--syn-border-soft)",
  },
  fieldKey: {
    fontSize: 10,
    color: "var(--syn-text-secondary)",
    fontWeight: 600,
  },
  fieldVal: {
    fontSize: 12,
    color: "var(--syn-text-primary)",
    wordBreak: "break-word",
    lineHeight: 1.5,
  },
};
