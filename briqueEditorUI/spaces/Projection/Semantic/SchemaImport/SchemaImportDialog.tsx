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
import type { ExecuteInput, ExecuteResult } from "../../../../Brique_Substrate/types/index.js";
import { extractWindowSpecsFromSchema, parseSchemaAddress, schemaGabaritFromFunctional } from "./schemaImport.js";
import type { SchemaWindowSpec } from "./schemaImport.js";

export type SchemaImportDialogProps = {
  mutate: (input: ExecuteInput) => Promise<ExecuteResult>;
  onApply: (specs: SchemaWindowSpec[]) => void;
  onClose: () => void;
};

export function SchemaImportDialog({ mutate, onApply, onClose }: SchemaImportDialogProps) {
  const [input, setInput] = useState("");
  const [busy, setBusy] = useState(false);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    inputRef.current?.focus();
  }, []);

  const isValidInput = parseSchemaAddress(input) !== null;

  const handleApply = async () => {
    const parsed = parseSchemaAddress(input);
    if (!parsed) return;
    setBusy(true);
    setErrorMessage(null);
    try {
      const result = await mutate({
        context: parsed.context,
        capability: "read.meaning",
        params: { input: [{ element_kind: "schema", element_name: parsed.elementName, sections: ["functional"] }] },
      });
      const response = result.response as { status?: string; payload?: { result?: unknown[] } } | undefined;
      if (response?.status !== "ok") {
        throw new Error("read.meaning failed");
      }
      const items = response?.payload?.result;
      if (!Array.isArray(items) || items.length === 0) {
        throw new Error("Schema not found.");
      }
      const item = items[0] as Record<string, unknown>;
      if (!item.ok) {
        throw new Error("Schema not found.");
      }
      const getRecord = (v: unknown): Record<string, unknown> | undefined =>
        v && typeof v === "object" && !Array.isArray(v) ? v as Record<string, unknown> : undefined;
      const descriptor =
        getRecord(item.descriptor) ?? getRecord(item.desc) ?? getRecord(item.meaning);
      const source = descriptor ?? item;
      const functional = getRecord(source.functional);
      const gabarit = schemaGabaritFromFunctional(functional);
      const extracted = extractWindowSpecsFromSchema(gabarit);
      if (extracted.length === 0) {
        throw new Error("No window paths found in schema fields.");
      }
      onApply(extracted);
      onClose();
    } catch (err) {
      setErrorMessage(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === "Escape") onClose();
    if (e.key === "Enter" && isValidInput && !busy) void handleApply();
  };

  return (
    <div
      style={styles.backdrop}
      onMouseDown={(e) => { if (e.target === e.currentTarget) onClose(); }}
    >
      <div style={styles.dialog} onKeyDown={handleKeyDown}>
        <div style={styles.title}>Import from schema</div>

        <input
          ref={inputRef}
          disabled={busy}
          onChange={(e) => { setInput(e.target.value); setErrorMessage(null); }}
          placeholder="ex: /root/music::music_matter"
          style={styles.input}
          type="text"
          value={input}
        />

        {errorMessage && (
          <div style={styles.error}>{errorMessage}</div>
        )}

        <div style={styles.actions}>
          <button disabled={busy} onClick={onClose} style={styles.cancelButton} type="button">
            Cancel
          </button>
          <button
            disabled={!isValidInput || busy}
            onClick={() => void handleApply()}
            style={{
              ...styles.confirmButton,
              ...(!isValidInput || busy ? styles.confirmButtonDisabled : {}),
            }}
            type="button"
          >
            {busy ? "Loading…" : "Apply"}
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
    width: 400,
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
  error: {
    color: "var(--syn-feedback-error)",
    fontSize: 12,
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
  confirmButtonDisabled: {
    border: "1px solid var(--syn-control-border)",
    background: "var(--syn-control-disabled-bg)",
    color: "var(--syn-control-disabled-text)",
    cursor: "not-allowed",
  },
};
