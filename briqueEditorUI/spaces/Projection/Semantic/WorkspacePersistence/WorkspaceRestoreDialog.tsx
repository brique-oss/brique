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

import { useState } from "react";
import type { SavedWorkspaceEntry } from "../contracts.js";

export type WorkspaceRestoreDialogProps = {
  entries: SavedWorkspaceEntry[];
  busy: boolean;
  onConfirm: (entry: SavedWorkspaceEntry) => void;
  onCancel: () => void;
};

export function WorkspaceRestoreDialog({ entries, busy, onConfirm, onCancel }: WorkspaceRestoreDialogProps) {
  const [selected, setSelected] = useState<SavedWorkspaceEntry | null>(entries[0] ?? null);

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === "Escape") onCancel();
    if (e.key === "Enter" && selected) onConfirm(selected);
  };

  return (
    <div style={styles.backdrop} onMouseDown={(e) => { if (e.target === e.currentTarget) onCancel(); }}>
      <div style={styles.dialog} onKeyDown={handleKeyDown}>
        <div style={styles.title}>Restore workspace</div>
        {entries.length === 0 ? (
          <div style={styles.empty}>No saved configurations.</div>
        ) : (
          <select
            disabled={busy}
            onChange={(e) => {
              const entry = entries.find((en) => en.matterId === e.target.value) ?? null;
              setSelected(entry);
            }}
            style={styles.select}
            value={selected?.matterId ?? ""}
          >
            {entries.map((entry) => (
              <option key={entry.matterId} value={entry.matterId}>
                {entry.name}
              </option>
            ))}
          </select>
        )}
        <div style={styles.actions}>
          <button disabled={busy} onClick={onCancel} style={styles.cancelButton} type="button">
            Cancel
          </button>
          <button
            disabled={busy || !selected || entries.length === 0}
            onClick={() => selected && onConfirm(selected)}
            style={styles.confirmButton}
            type="button"
          >
            {busy ? "Restoring…" : "Restore"}
          </button>
        </div>
      </div>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  backdrop: {
    position: "absolute",
    inset: 0,
    zIndex: 500,
    display: "grid",
    placeItems: "center",
    background: "rgba(0,0,0,0.45)",
  },
  dialog: {
    display: "grid",
    gap: 12,
    width: 320,
    padding: 20,
    border: "1px solid var(--syn-border-medium)",
    borderRadius: 10,
    background: "var(--syn-surface-overlay)",
    boxShadow: "0 24px 80px rgba(0,0,0,0.55)",
    color: "var(--syn-text-primary)",
    fontFamily: "monospace",
  },
  title: {
    fontSize: 12,
    fontWeight: 900,
    letterSpacing: "0.1em",
    textTransform: "uppercase",
    color: "var(--syn-nav-title)",
  },
  empty: {
    color: "var(--syn-text-muted)",
    fontSize: 12,
    fontStyle: "italic",
  },
  select: {
    width: "100%",
    border: "1px solid var(--syn-control-border)",
    borderRadius: 5,
    background: "var(--syn-control-bg)",
    color: "var(--syn-text-secondary)",
    fontSize: 12,
    fontFamily: "monospace",
    padding: "4px 6px",
    outline: "none",
  },
  actions: {
    display: "flex",
    gap: 8,
    justifyContent: "flex-end",
  },
  cancelButton: {
    padding: "6px 14px",
    border: "1px solid var(--syn-control-border)",
    borderRadius: 5,
    background: "var(--syn-control-bg)",
    color: "var(--syn-text-secondary)",
    fontSize: 12,
    fontFamily: "monospace",
    cursor: "pointer",
  },
  confirmButton: {
    padding: "6px 14px",
    border: "1px solid var(--syn-selected-border)",
    borderRadius: 5,
    background: "var(--syn-control-active-bg)",
    color: "var(--syn-text-primary)",
    fontSize: 12,
    fontFamily: "monospace",
    fontWeight: 900,
    cursor: "pointer",
  },
};
