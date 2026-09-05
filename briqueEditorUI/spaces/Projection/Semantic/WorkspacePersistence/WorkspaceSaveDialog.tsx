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
import type { SavedWorkspaceEntry } from "../contracts.js";

export type WorkspaceSaveDialogProps = {
  existing: SavedWorkspaceEntry[];
  busy: boolean;
  onConfirm: (name: string) => void;
  onCancel: () => void;
};

export function WorkspaceSaveDialog({ existing, busy, onConfirm, onCancel }: WorkspaceSaveDialogProps) {
  const [name, setName] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    inputRef.current?.focus();
  }, []);

  const handleConfirm = () => {
    const trimmed = name.trim();
    if (!trimmed) return;
    onConfirm(trimmed);
  };

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === "Enter") handleConfirm();
    if (e.key === "Escape") onCancel();
  };

  return (
    <div style={styles.backdrop} onMouseDown={(e) => { if (e.target === e.currentTarget) onCancel(); }}>
      <div style={styles.dialog}>
        <div style={styles.title}>Save workspace</div>
        <input
          ref={inputRef}
          disabled={busy}
          onKeyDown={handleKeyDown}
          onChange={(e) => setName(e.target.value)}
          placeholder="Configuration name…"
          style={styles.input}
          type="text"
          value={name}
        />
        {existing.length > 0 && (
          <div style={styles.section}>
            <div style={styles.sectionLabel}>Existing configurations</div>
            <select
              disabled={busy}
              onChange={(e) => setName(e.target.value)}
              style={styles.select}
              value={name || ""}
            >
              <option value="">— select —</option>
              {existing.map((entry) => (
                <option key={entry.matterId} value={entry.name}>
                  {entry.name}
                </option>
              ))}
            </select>
          </div>
        )}
        <div style={styles.actions}>
          <button disabled={busy} onClick={onCancel} style={styles.cancelButton} type="button">
            Cancel
          </button>
          <button disabled={busy || !name.trim()} onClick={handleConfirm} style={styles.confirmButton} type="button">
            {busy ? "Saving…" : "Save"}
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
  input: {
    padding: "8px 10px",
    border: "1px solid var(--syn-control-border)",
    borderRadius: 6,
    background: "var(--syn-control-bg)",
    color: "var(--syn-text-primary)",
    fontSize: 13,
    fontFamily: "monospace",
    outline: "none",
  },
  section: {
    display: "grid",
    gap: 6,
  },
  sectionLabel: {
    fontSize: 10,
    fontWeight: 900,
    letterSpacing: "0.1em",
    textTransform: "uppercase",
    color: "var(--syn-text-muted)",
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
