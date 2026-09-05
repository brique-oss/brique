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
import type { CapabilityMeaningQueryFilter } from "../../Brique_Substrate/capability/typed.js";
import type { CurrentSemanticFilter } from "../Projection/Semantic/contracts.js";
import type { InspectionTarget, SemanticBrowseItem, SemanticBrowseState } from "./contracts.js";
import { usePreview } from "../Overlay/PreviewContext.js";
import { useOpen } from "../Overlay/OpenContext.js";

const ROOT_CONTEXT = "/root";

export type BrowseViewProps = {
  semanticFilter: CurrentSemanticFilter | null;
  onTargetSelect: (target: InspectionTarget) => void;
  onActivateContext: (contextPath: string) => void;
  onOpenInNewTab?: (target: InspectionTarget) => void;
};

export function BrowseView({ semanticFilter, onTargetSelect, onActivateContext, onOpenInNewTab }: BrowseViewProps) {
  const substrate = useRef(useBriqueSubstrate());
  const capabilityClient = useRef(createCapabilityClient(substrate.current));
  const querySeq = useRef(0);
  const [browseState, setBrowseState] = useState<SemanticBrowseState>({ kind: "idle" });

  useEffect(() => {
    if (!semanticFilter) {
      setBrowseState({ kind: "idle" });
      return;
    }

    const seq = ++querySeq.current;
    setBrowseState({ kind: "loading" });

    void runBrowseQuery(capabilityClient.current, semanticFilter).then(
      (items) => {
        if (seq !== querySeq.current) return;
        setBrowseState({ kind: "ready", items });
      },
      (err: unknown) => {
        if (seq !== querySeq.current) return;
        setBrowseState({
          kind: "error",
          message: err instanceof Error ? err.message : "Query failed.",
        });
      }
    );
  }, [semanticFilter]);

  return (
    <section aria-label="Browse Brique elements" style={styles.root}>
      {browseState.kind === "idle" && (
        <p style={styles.status}>
          Configure a filter in Semantic Projection, then press{" "}
          <kbd style={styles.kbd}>⌘ Enter</kbd> to browse.
        </p>
      )}
      {browseState.kind === "loading" && (
        <p style={styles.status}>Querying…</p>
      )}
      {browseState.kind === "error" && (
        <p style={{ ...styles.status, ...styles.statusError }}>{browseState.message}</p>
      )}
      {browseState.kind === "ready" && browseState.items.length === 0 && (
        <p style={styles.status}>No elements match the current filter.</p>
      )}
      {browseState.kind === "ready" && browseState.items.length > 0 && (
        <ul style={styles.list}>
          {browseState.items.map((item) => (
            <BrowseItem
              key={item.element_id}
              item={item}
              onSelect={onTargetSelect}
              onActivateContext={onActivateContext}
              onOpenInNewTab={onOpenInNewTab}
            />
          ))}
        </ul>
      )}
    </section>
  );
}

function BrowseItem({
  item,
  onSelect,
  onActivateContext,
  onOpenInNewTab,
}: {
  item: SemanticBrowseItem;
  onSelect: (target: InspectionTarget) => void;
  onActivateContext: (contextPath: string) => void;
  onOpenInNewTab?: (target: InspectionTarget) => void;
}) {
  const { requestPreview } = usePreview();
  const { requestOpen } = useOpen();
  const ctxId = (item.ctx_id ?? "") || ROOT_CONTEXT;
  const elementName = item.element_type === "context" ? ctxId : item.name;
  const target: InspectionTarget = {
    context: ctxId,
    elementKind: item.element_type,
    elementName,
    sourcePath: deriveSourcePath(item.element_type, item.name),
  };

  return (
    <li style={styles.item}>
      <button
        style={styles.itemButton}
        type="button"
        onClick={(e) => {
          if (e.metaKey) { onActivateContext(ctxId); return; }
          if (e.shiftKey && (target.elementKind === "document" || target.elementKind === "matter")) requestOpen({ elementKind: target.elementKind, elementName: target.elementName, context: target.context });
          else onSelect(target);
        }}
        onAuxClick={(e) => {
          if (e.button === 1) { e.preventDefault(); onOpenInNewTab?.(target); }
        }}
        onMouseEnter={(e) => {
          if (e.ctrlKey) requestPreview({ elementKind: target.elementKind, elementName: target.elementName, context: target.context, pos: { x: e.clientX, y: e.clientY } });
        }}
      >
        <span style={styles.itemKind}>{item.element_type}</span>
        <span style={styles.itemName}>{item.name}</span>
        <span style={styles.itemCtx}>{ctxId}</span>
      </button>
    </li>
  );
}

function deriveSourcePath(elementType: string, name: string): string | undefined {
  if (elementType === "matter") return `matter/${name}`;
  if (elementType === "capacity") return `capacity/${name}`;
  if (elementType === "structure") return `structure/${name}`;
  if (elementType === "schema") return `schema/${name}`;
  if (elementType === "document") return `document/${name}`;
  return undefined;
}

type CapabilityClient = ReturnType<typeof createCapabilityClient>;

async function runBrowseQuery(
  client: CapabilityClient,
  filter: CurrentSemanticFilter
): Promise<SemanticBrowseItem[]> {
  const filters: CapabilityMeaningQueryFilter[] = filter.windows
    .filter((w) => w.selectedKeys.length > 0 && w.activePath.length > 0)
    .flatMap((w): CapabilityMeaningQueryFilter[] => {
      if (w.valueLevel) {
        return [{
          path: w.activePath.join("."),
          op: "EQ",
          value: w.selectedKeys,
          match: w.combinator === "AND" ? "ALL" : "ANY",
        }];
      }
      return w.selectedKeys.map((key) => ({
        path: [...w.activePath, key].join("."),
        op: "EXISTS",
      }));
    });

  const kinds = filter.kinds.length > 0 ? filter.kinds : [undefined];

  const results = await Promise.all(
    kinds.map((kind) =>
      client.meaning.query(ROOT_CONTEXT, {
        ...(kind ? { element_kind: kind } : {}),
        ...(filter.context ? { ctx_id: filter.context.ctxId, ctx_mode: filter.context.mode } : {}),
        ...(filters.length > 0 ? { filters } : {}),
        limit: 200,
        offset: 0,
      })
    )
  );

  const merged = new Map<string, SemanticBrowseItem>();
  for (const r of results) {
    if (!r.ok) continue;
    const rows = (r.payload as { result?: unknown[] } | undefined)?.result ?? [];
    for (const row of rows) {
      const item = row as SemanticBrowseItem;
      if (item.element_id) merged.set(item.element_id, item);
    }
  }
  return [...merged.values()];
}

const styles: Record<string, React.CSSProperties> = {
  root: {
    display: "flex",
    flexDirection: "column",
    flex: 1,
    minWidth: 0,
    minHeight: 0,
    overflow: "hidden",
  },
  status: {
    padding: "16px 14px",
    margin: 0,
    color: "var(--syn-text-muted)",
    fontSize: 11,
    fontStyle: "italic",
    lineHeight: 1.6,
  },
  statusError: {
    color: "var(--syn-feedback-error)",
    fontStyle: "normal",
  },
  kbd: {
    fontSize: 10,
    fontFamily: "monospace",
    background: "var(--syn-control-bg)",
    border: "1px solid var(--syn-control-border)",
    borderRadius: 3,
    padding: "1px 4px",
  },
  list: {
    listStyle: "none",
    margin: 0,
    padding: "4px 0",
    overflowY: "auto",
    flex: 1,
  },
  item: {
    margin: 0,
    padding: 0,
  },
  itemButton: {
    width: "100%",
    display: "grid",
    gridTemplateColumns: "auto 1fr auto",
    alignItems: "center",
    gap: 8,
    padding: "7px 14px",
    background: "transparent",
    border: "none",
    borderBottom: "1px solid var(--syn-border-soft)",
    color: "inherit",
    cursor: "pointer",
    textAlign: "left" as const,
    fontFamily: "monospace",
  },
  itemKind: {
    flexShrink: 0,
    fontSize: 9,
    fontWeight: 900,
    letterSpacing: "0.08em",
    textTransform: "uppercase" as const,
    color: "var(--syn-nav-title)",
    padding: "1px 5px",
    border: "1px solid var(--syn-border-medium)",
    borderRadius: 3,
  },
  itemName: {
    fontSize: 12,
    color: "var(--syn-text-primary)",
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap" as const,
    minWidth: 0,
  },
  itemCtx: {
    fontSize: 10,
    color: "var(--syn-text-muted)",
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap" as const,
    minWidth: 0,
    maxWidth: 120,
  },
};
