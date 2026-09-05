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

import { useCallback, useMemo, useRef, useState } from "react";
import type { SemanticVocabularySnapshot, VocabularyPath } from "../contracts.js";

export type SemanticVocabularyPathWindowProps = {
  snapshot: SemanticVocabularySnapshot;
  path: VocabularyPath;
  mode: "edit" | "use";
  selectedKeys: string[];
  combinator: "AND" | "OR";
  pos: { x: number; y: number };
  size: { width: number; height: number };
  canvasOffset: { x: number; y: number };
  scale: number;
  onPosChange: (pos: { x: number; y: number }) => void;
  onSizeChange: (size: { width: number; height: number }) => void;
  onPathChange: (path: VocabularyPath) => void;
  onSelectionChange: (selectedKeys: string[], combinator: "AND" | "OR") => void;
  onVocabularyValueAdd: (path: VocabularyPath, value: string) => Promise<void>;
  onVocabularyValuesRemove: (path: VocabularyPath, values: string[]) => Promise<void>;
  onClose: () => void;
};

type VocabularyGridEntry = {
  key: string;
  path: VocabularyPath;
  kind: "branch" | "value";
};

const MIN_WIDTH = 260;
const MIN_HEIGHT = 240;
const PATH_BAR_HEIGHT = 38;
const HANDLE = 6;
const CORNER = 10;

export function SemanticVocabularyPathWindow({
  snapshot,
  path,
  mode,
  selectedKeys: selectedKeysProp,
  combinator,
  pos,
  size,
  canvasOffset,
  scale,
  onPosChange,
  onSizeChange,
  onPathChange,
  onSelectionChange,
  onVocabularyValueAdd,
  onVocabularyValuesRemove,
  onClose,
}: SemanticVocabularyPathWindowProps) {
  const selectedKeys = new Set(selectedKeysProp);
  const [addOpen, setAddOpen] = useState(false);
  const [draftValue, setDraftValue] = useState("");
  const [busy, setBusy] = useState(false);

  const activeValue = useMemo(
    () => getPathValue(snapshot.vocabulary, path),
    [path, snapshot]
  );
  const entries = useMemo(() => buildEntries(activeValue, path), [path, activeValue]);
  const valueLevel = Array.isArray(activeValue);

  const toggleKey = useCallback(
    (key: string) => {
      const next = new Set(selectedKeys);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      onSelectionChange([...next], combinator);
    },
    [selectedKeys, combinator, onSelectionChange]
  );

  const toggleCombinator = useCallback(() => {
    const next = combinator === "AND" ? "OR" : "AND";
    onSelectionChange([...selectedKeys], next);
  }, [selectedKeys, combinator, onSelectionChange]);

  const addValue = useCallback(async () => {
    const value = draftValue.trim();
    if (!value || busy) return;
    setBusy(true);
    try {
      await onVocabularyValueAdd(path, value);
      setDraftValue("");
      setAddOpen(false);
    } finally {
      setBusy(false);
    }
  }, [busy, draftValue, onVocabularyValueAdd, path]);

  const removeSelectedValues = useCallback(async () => {
    if (selectedKeysProp.length === 0 || busy) return;
    setBusy(true);
    try {
      await onVocabularyValuesRemove(path, selectedKeysProp);
    } finally {
      setBusy(false);
    }
  }, [busy, onVocabularyValuesRemove, path, selectedKeysProp]);

  const dragRef = useRef<{ mx: number; my: number; ox: number; oy: number; scale: number } | null>(null);
  const onHeaderMouseDown = useCallback(
    (event: React.MouseEvent) => {
      if (mode === "use") return;
      if ((event.target as HTMLElement).closest("button")) return;
      event.preventDefault();
      dragRef.current = { mx: event.clientX, my: event.clientY, ox: pos.x, oy: pos.y, scale };

      function onMouseMove(e: MouseEvent) {
        if (!dragRef.current) return;
        const s = dragRef.current.scale;
        onPosChange({
          x: dragRef.current.ox + (e.clientX - dragRef.current.mx) / s,
          y: dragRef.current.oy + (e.clientY - dragRef.current.my) / s,
        });
      }
      function onMouseUp() {
        dragRef.current = null;
        window.removeEventListener("mousemove", onMouseMove);
        window.removeEventListener("mouseup", onMouseUp);
      }
      window.addEventListener("mousemove", onMouseMove);
      window.addEventListener("mouseup", onMouseUp);
    },
    [pos.x, pos.y, scale, onPosChange]
  );

  const resizeRef = useRef<{
    mx: number; my: number; ow: number; oh: number; ox: number; oy: number; dir: string; scale: number;
  } | null>(null);

  const onResizeMouseDown = useCallback(
    (event: React.MouseEvent, dir: string) => {
      if (mode === "use") return;
      event.preventDefault();
      event.stopPropagation();
      resizeRef.current = {
        mx: event.clientX, my: event.clientY,
        ow: size.width, oh: size.height,
        ox: pos.x, oy: pos.y,
        dir, scale,
      };

      function onMouseMove(e: MouseEvent) {
        if (!resizeRef.current) return;
        const s = resizeRef.current.scale;
        const dx = (e.clientX - resizeRef.current.mx) / s;
        const dy = (e.clientY - resizeRef.current.my) / s;
        const { ow, oh, ox, oy, dir: d } = resizeRef.current;

        let nw = ow, nh = oh, nx = ox, ny = oy;
        if (d.includes("e")) nw = Math.max(MIN_WIDTH / s, ow + dx);
        if (d.includes("s")) nh = Math.max(MIN_HEIGHT / s, oh + dy);
        if (d.includes("w")) { nw = Math.max(MIN_WIDTH / s, ow - dx); nx = ox + (ow - nw); }
        if (d.includes("n")) { nh = Math.max(MIN_HEIGHT / s, oh - dy); ny = oy + (oh - nh); }

        onSizeChange({ width: nw, height: nh });
        onPosChange({ x: nx, y: ny });
      }
      function onMouseUp() {
        resizeRef.current = null;
        window.removeEventListener("mousemove", onMouseMove);
        window.removeEventListener("mouseup", onMouseUp);
      }
      window.addEventListener("mousemove", onMouseMove);
      window.addEventListener("mouseup", onMouseUp);
    },
    [pos.x, pos.y, size.width, size.height, scale, onPosChange, onSizeChange]
  );

  const screenX = pos.x * scale + canvasOffset.x;
  const screenY = pos.y * scale + canvasOffset.y;
  const screenW = size.width * scale;
  const screenH = size.height * scale;

  return (
    <div
      data-semantic-window
      style={{ ...styles.window, left: screenX, top: screenY, width: screenW, height: screenH }}
    >
      <div style={{
        position: "absolute",
        inset: 0,
        transformOrigin: "top left",
        transform: `scale(${scale})`,
        width: size.width,
        height: size.height,
        display: "grid",
        gridTemplateRows: `${PATH_BAR_HEIGHT}px 1fr`,
        overflow: "hidden",
      }}>
        <div
          style={{ ...styles.pathBar, cursor: mode === "use" ? "default" : "grab" }}
          onMouseDown={onHeaderMouseDown}
        >
          <div style={styles.breadcrumbs} aria-label="Current semantic path">
            {mode === "edit" ? (
              <button onClick={() => onPathChange([])} style={styles.crumb} type="button">root</button>
            ) : (
              <span style={{ ...styles.crumb, cursor: "default" }}>root</span>
            )}
            {path.map((segment, index) => (
              <span key={`${segment}-${index}`} style={styles.crumbWrap}>
                <span style={styles.dot}>.</span>
                {mode === "edit" ? (
                  <button onClick={() => onPathChange(path.slice(0, index + 1))} style={styles.crumb} type="button">
                    {segment}
                  </button>
                ) : (
                  <span style={{ ...styles.crumb, cursor: "default" }}>{segment}</span>
                )}
              </span>
            ))}
          </div>
          {(mode === "use" || mode === "edit") && (
            <button onClick={toggleCombinator} style={styles.combinatorButton} type="button">
              {combinator}
            </button>
          )}
          {valueLevel && mode === "edit" && <span style={styles.valueBadge}>Values</span>}
          {valueLevel && mode === "edit" && (
            <div style={styles.valueActions}>
              <button
                aria-label="Add vocabulary value"
                disabled={busy}
                onClick={() => setAddOpen(true)}
                style={styles.iconButton}
                title="Add value"
                type="button"
              >
                +
              </button>
              <button
                aria-label="Remove selected vocabulary values"
                disabled={busy || selectedKeysProp.length === 0}
                onClick={() => void removeSelectedValues()}
                style={styles.iconButton}
                title="Remove selected values"
                type="button"
              >
                -
              </button>
            </div>
          )}
          <button aria-label="Close" onClick={onClose} style={styles.iconButton} type="button">
            x
          </button>
        </div>

        <div style={styles.body}>
          <div style={styles.entryGrid}>
            {entries.map((entry, index) => {
              if (mode === "use") {
                const isSelected = selectedKeys.has(entry.key);
                return (
                  <button
                    key={`${path.join(".")}:${entry.key}:${index}`}
                    onClick={() => toggleKey(entry.key)}
                    style={isSelected ? styles.entryButtonSelected : styles.entryButtonUse}
                    type="button"
                  >
                    {entry.key}
                  </button>
                );
              }
              return entry.kind === "branch" ? (
                <button
                  key={entry.path.join(".")}
                  onClick={() => onPathChange(entry.path)}
                  style={styles.entryButton}
                  type="button"
                >
                  {entry.key}
                </button>
              ) : (
                <button
                  key={`${path.join(".")}:${entry.key}:${index}`}
                  onClick={() => toggleKey(entry.key)}
                  style={selectedKeys.has(entry.key)
                    ? { ...styles.entryButtonUse, ...styles.entryButtonSelected }
                    : { ...styles.entryButton, ...styles.valueEntry }}
                  type="button"
                >
                  {entry.key}
                </button>
              );
            })}
            {entries.length === 0 && <div style={styles.empty} />}
          </div>
        </div>
      </div>

      {addOpen && (
        <div style={styles.dialog} role="dialog" aria-label="Add vocabulary value">
          <input
            autoFocus
            disabled={busy}
            onChange={(event) => setDraftValue(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "Enter") void addValue();
              if (event.key === "Escape") setAddOpen(false);
            }}
            placeholder="value"
            style={styles.dialogInput}
            value={draftValue}
          />
          <div style={styles.dialogActions}>
            <button disabled={busy} onClick={() => setAddOpen(false)} style={styles.dialogButton} type="button">Cancel</button>
            <button disabled={busy || draftValue.trim() === ""} onClick={() => void addValue()} style={styles.dialogButtonPrimary} type="button">Add</button>
          </div>
        </div>
      )}

      {mode === "edit" && (
        <>
          <div style={{ ...styles.resizeHandle, ...styles.resizeE }} onMouseDown={(e) => onResizeMouseDown(e, "e")} />
          <div style={{ ...styles.resizeHandle, ...styles.resizeS }} onMouseDown={(e) => onResizeMouseDown(e, "s")} />
          <div style={{ ...styles.resizeHandle, ...styles.resizeW }} onMouseDown={(e) => onResizeMouseDown(e, "w")} />
          <div style={{ ...styles.resizeHandle, ...styles.resizeN }} onMouseDown={(e) => onResizeMouseDown(e, "n")} />
          <div style={{ ...styles.resizeHandle, ...styles.resizeSE }} onMouseDown={(e) => onResizeMouseDown(e, "se")} />
          <div style={{ ...styles.resizeHandle, ...styles.resizeSW }} onMouseDown={(e) => onResizeMouseDown(e, "sw")} />
          <div style={{ ...styles.resizeHandle, ...styles.resizeNE }} onMouseDown={(e) => onResizeMouseDown(e, "ne")} />
          <div style={{ ...styles.resizeHandle, ...styles.resizeNW }} onMouseDown={(e) => onResizeMouseDown(e, "nw")} />
        </>
      )}
    </div>
  );
}

function getPathValue(root: Record<string, unknown>, path: VocabularyPath): unknown {
  let cursor: unknown = root;
  for (const segment of path) {
    if (!isRecord(cursor)) return undefined;
    cursor = cursor[segment];
  }
  return cursor;
}

function buildEntries(value: unknown, basePath: VocabularyPath): VocabularyGridEntry[] {
  if (Array.isArray(value)) {
    return value
      .filter((item) => !isRecord(item) && !Array.isArray(item))
      .map((item) => String(item))
      .filter((item) => item.trim() !== "")
      .sort((a, b) => a.localeCompare(b))
      .map((key) => ({ key, path: basePath, kind: "value" as const }));
  }
  if (!isRecord(value)) return [];
  return Object.entries(value)
    .map(([key, child]) => ({
      key,
      path: [...basePath, key],
      kind: (isRecord(child) || Array.isArray(child)) ? "branch" as const : "value" as const,
    }))
    .sort((a, b) => {
      if (a.kind !== b.kind) return a.kind === "branch" ? -1 : 1;
      return a.key.localeCompare(b.key);
    });
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

const styles: Record<string, React.CSSProperties> = {
  window: {
    position: "absolute",
    zIndex: 100,
    overflow: "hidden",
    border: "1px solid var(--syn-semantic-window-border)",
    borderRadius: "8px",
    background: "var(--syn-semantic-window-bg)",
    color: "var(--syn-text-primary)",
    boxShadow: "0 22px 70px rgba(0,0,0,0.42)",
  },
  iconButton: {
    flexShrink: 0,
    width: 24,
    height: 24,
    display: "grid",
    placeItems: "center",
    border: "1px solid var(--syn-control-border)",
    borderRadius: 4,
    background: "var(--syn-control-bg)",
    color: "var(--syn-text-secondary)",
    cursor: "pointer",
  },
  pathBar: {
    display: "flex",
    alignItems: "center",
    gap: 8,
    padding: "7px 8px 7px 12px",
    borderBottom: "1px solid var(--syn-border-soft)",
    background: "var(--syn-semantic-panel-bg)",
    cursor: "grab",
    userSelect: "none",
  },
  breadcrumbs: {
    flex: "1 1 0%",
    minWidth: 0,
    display: "flex",
    alignItems: "center",
    gap: 0,
    overflowX: "auto",
  },
  crumbWrap: {
    display: "inline-flex",
    alignItems: "center",
    minWidth: 0,
  },
  dot: {
    color: "var(--syn-nav-title)",
    fontSize: 13,
    fontWeight: 900,
  },
  crumb: {
    flex: "0 0 auto",
    maxWidth: 150,
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap",
    border: "none",
    padding: 0,
    background: "transparent",
    color: "var(--syn-text-secondary)",
    cursor: "pointer",
    fontSize: 13,
    fontWeight: 900,
  },
  valueBadge: {
    flexShrink: 0,
    padding: "3px 7px",
    border: "1px solid var(--syn-border-medium)",
    borderRadius: 4,
    color: "var(--syn-nav-title)",
    background: "var(--syn-control-bg)",
    fontSize: 10,
    fontWeight: 900,
    textTransform: "uppercase",
    letterSpacing: "0.08em",
  },
  valueActions: {
    flexShrink: 0,
    display: "flex",
    alignItems: "center",
    gap: 4,
  },
  body: {
    minHeight: 0,
    overflow: "auto",
    padding: "0 12px 12px",
  },
  entryGrid: {
    display: "grid",
    gridTemplateColumns: "repeat(auto-fill, minmax(104px, 1fr))",
    gap: 8,
  },
  entryButton: {
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    minHeight: 56,
    minWidth: 0,
    padding: "8px 10px",
    border: "1px solid var(--syn-semantic-window-border)",
    borderRadius: 8,
    background: "var(--syn-semantic-panel-bg)",
    color: "var(--syn-text-primary)",
    cursor: "pointer",
    textAlign: "center",
    fontSize: 13,
    fontWeight: 900,
    overflow: "hidden",
    textOverflow: "ellipsis",
  },
  valueEntry: {
    cursor: "default",
  },
  entryButtonUse: {
    display: "grid",
    placeItems: "center",
    minHeight: 56,
    minWidth: 0,
    padding: "8px 10px",
    border: "1px solid var(--syn-semantic-window-border)",
    borderRadius: 8,
    background: "var(--syn-semantic-panel-bg)",
    color: "var(--syn-text-muted)",
    cursor: "pointer",
    textAlign: "center" as const,
    fontSize: 13,
    fontWeight: 900,
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap" as const,
  },
  entryButtonSelected: {
    display: "grid",
    placeItems: "center",
    minHeight: 56,
    minWidth: 0,
    padding: "8px 10px",
    border: "2px solid var(--syn-selected-border)",
    borderRadius: 8,
    background: "var(--syn-selected-bg)",
    color: "var(--syn-text-primary)",
    cursor: "pointer",
    textAlign: "center" as const,
    fontSize: 13,
    fontWeight: 900,
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap" as const,
    boxShadow: "0 0 0 1px var(--syn-selected-border), inset 0 0 12px var(--syn-selected-bg)",
  },
  combinatorButton: {
    flexShrink: 0,
    padding: "3px 8px",
    border: "1px solid var(--syn-selected-border)",
    borderRadius: 4,
    background: "var(--syn-selected-bg)",
    color: "var(--syn-nav-title)",
    fontSize: 10,
    fontWeight: 900,
    letterSpacing: "0.08em",
    textTransform: "uppercase" as const,
    cursor: "pointer",
  },
  empty: {
    gridColumn: "1 / -1",
    minHeight: 56,
  },
  dialog: {
    position: "absolute",
    zIndex: 220,
    top: 48,
    right: 12,
    width: 230,
    display: "grid",
    gap: 8,
    padding: 10,
    border: "1px solid var(--syn-border-medium)",
    borderRadius: 6,
    background: "var(--syn-surface-overlay)",
    boxShadow: "0 18px 48px rgba(0,0,0,0.35)",
  },
  dialogInput: {
    minWidth: 0,
    height: 30,
    padding: "0 8px",
    border: "1px solid var(--syn-control-border)",
    borderRadius: 4,
    background: "var(--syn-control-bg)",
    color: "var(--syn-text-primary)",
    fontFamily: "monospace",
    fontSize: 12,
    outline: "none",
  },
  dialogActions: {
    display: "flex",
    justifyContent: "flex-end",
    gap: 6,
  },
  dialogButton: {
    height: 26,
    padding: "0 8px",
    border: "1px solid var(--syn-control-border)",
    borderRadius: 4,
    background: "transparent",
    color: "var(--syn-text-secondary)",
    cursor: "pointer",
    fontFamily: "monospace",
    fontWeight: 900,
  },
  dialogButtonPrimary: {
    height: 26,
    padding: "0 10px",
    border: "1px solid var(--syn-selected-border)",
    borderRadius: 4,
    background: "var(--syn-control-active-bg)",
    color: "var(--syn-text-primary)",
    cursor: "pointer",
    fontFamily: "monospace",
    fontWeight: 900,
  },
  resizeHandle: {
    position: "absolute",
    zIndex: 10,
  },
  resizeE:  { right: 0,  top: CORNER,  bottom: CORNER, width: HANDLE,  cursor: "ew-resize" },
  resizeW:  { left: 0,   top: CORNER,  bottom: CORNER, width: HANDLE,  cursor: "ew-resize" },
  resizeS:  { bottom: 0, left: CORNER, right: CORNER,  height: HANDLE, cursor: "ns-resize" },
  resizeN:  { top: 0,    left: CORNER, right: CORNER,  height: HANDLE, cursor: "ns-resize" },
  resizeSE: { right: 0,  bottom: 0, width: CORNER, height: CORNER, cursor: "se-resize" },
  resizeSW: { left: 0,   bottom: 0, width: CORNER, height: CORNER, cursor: "sw-resize" },
  resizeNE: { right: 0,  top: 0,    width: CORNER, height: CORNER, cursor: "ne-resize" },
  resizeNW: { left: 0,   top: 0,    width: CORNER, height: CORNER, cursor: "nw-resize" },
};
