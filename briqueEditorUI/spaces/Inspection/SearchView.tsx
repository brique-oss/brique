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

import { useRef, useState } from "react";
import { useBriqueSubstrate } from "../../React_Substrate_Adapter/context.js";
import { createCapabilityClient } from "../../Brique_Substrate/capability/index.js";
import type { InspectionTarget, SemanticBrowseItem } from "./contracts.js";
import { usePreview } from "../Overlay/PreviewContext.js";
import { useOpen } from "../Overlay/OpenContext.js";

const ROOT_CONTEXT = "/root";

function deriveSourcePath(elementType: string, name: string): string | undefined {
  if (elementType === "matter") return `matter/${name}`;
  if (elementType === "capacity") return `capacity/${name}`;
  if (elementType === "structure") return `structure/${name}`;
  if (elementType === "schema") return `schema/${name}`;
  if (elementType === "document") return `document/${name}`;
  return undefined;
}

type SearchState =
  | { kind: "idle" }
  | { kind: "loading" }
  | { kind: "ready"; items: SemanticBrowseItem[] }
  | { kind: "error"; message: string };

export type SearchViewProps = {
  onTargetSelect: (target: InspectionTarget) => void;
  onActivateContext: (contextPath: string) => void;
  text: string;
  onTextChange: (text: string) => void;
  searchState: SearchState;
  onSearchStateChange: (state: SearchState) => void;
  onOpenInNewTab?: (target: InspectionTarget) => void;
};

export { SearchState };

export function SearchView({ onTargetSelect, onActivateContext, text, onTextChange, searchState, onSearchStateChange, onOpenInNewTab }: SearchViewProps) {
  const substrate = useRef(useBriqueSubstrate());
  const capabilityClient = useRef(createCapabilityClient(substrate.current));
  const querySeq = useRef(0);

  const runSearch = (query: string) => {
    if (!query.trim()) {
      onSearchStateChange({ kind: "idle" });
      return;
    }

    const seq = ++querySeq.current;
    onSearchStateChange({ kind: "loading" });

    void capabilityClient.current.meaning
      .query(ROOT_CONTEXT, {
        filters: [{ path: "objective.name", op: "LIKE", value: `${query.trim()}%` }],
        limit: 200,
        offset: 0,
      })
      .then(
        (r) => {
          if (seq !== querySeq.current) return;
          if (!r.ok) {
            onSearchStateChange({ kind: "error", message: r.error?.message ?? "Query failed." });
            return;
          }
          const rows = (r.payload as { result?: unknown[] } | undefined)?.result ?? [];
          const items = rows as SemanticBrowseItem[];
          onSearchStateChange({ kind: "ready", items });
        },
        (err: unknown) => {
          if (seq !== querySeq.current) return;
          onSearchStateChange({
            kind: "error",
            message: err instanceof Error ? err.message : "Query failed.",
          });
        }
      );
  };

  return (
    <section aria-label="Search Brique elements" style={styles.root}>
      <div style={styles.inputRow}>
        <input
          aria-label="Search by name"
          autoComplete="off"
          placeholder="Search by name…"
          spellCheck={false}
          style={styles.input}
          type="text"
          value={text}
          onChange={(e) => onTextChange(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") runSearch(text);
          }}
        />
      </div>

      {searchState.kind === "idle" && (
        <p style={styles.status}>Type a name and press Enter to search.</p>
      )}
      {searchState.kind === "loading" && (
        <p style={styles.status}>Searching…</p>
      )}
      {searchState.kind === "error" && (
        <p style={{ ...styles.status, ...styles.statusError }}>{searchState.message}</p>
      )}
      {searchState.kind === "ready" && searchState.items.length === 0 && (
        <p style={styles.status}>No results.</p>
      )}
      {searchState.kind === "ready" && searchState.items.length > 0 && (
        <ul style={styles.list}>
          {searchState.items.map((item) => (
            <SearchItem key={item.element_id} item={item} onSelect={onTargetSelect} onActivateContext={onActivateContext} onOpenInNewTab={onOpenInNewTab} />
          ))}
        </ul>
      )}
    </section>
  );
}

function SearchItem({
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

const styles: Record<string, React.CSSProperties> = {
  root: {
    display: "flex",
    flexDirection: "column",
    flex: 1,
    minWidth: 0,
    minHeight: 0,
    overflow: "hidden",
  },
  inputRow: {
    padding: "10px 12px 8px",
    flexShrink: 0,
  },
  input: {
    width: "100%",
    boxSizing: "border-box" as const,
    background: "var(--syn-control-bg)",
    border: "1px solid var(--syn-control-border)",
    borderRadius: 5,
    color: "var(--syn-text-primary)",
    fontFamily: "monospace",
    fontSize: 12,
    padding: "6px 10px",
    outline: "none",
  },
  status: {
    padding: "12px 14px",
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
