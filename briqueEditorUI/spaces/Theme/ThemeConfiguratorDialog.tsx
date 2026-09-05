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

import { useMemo, useState } from "react";
import {
  THEME_INTENSITIES,
  THEME_VARIATIONS,
  type ThemeIntensity,
  type ThemeIntent,
  type ThemeVariation,
} from "./contracts.js";
import { ThemePreview } from "./ThemePreview.js";

export type ThemeConfiguratorDialogProps = {
  initialIntent: ThemeIntent;
  saving: boolean;
  error: string | undefined;
  onCancel: () => void;
  onApply: (intent: ThemeIntent) => void;
};

export function ThemeConfiguratorDialog({
  initialIntent,
  saving,
  error,
  onCancel,
  onApply,
}: ThemeConfiguratorDialogProps) {
  const [baseColor, setBaseColor] = useState(initialIntent.baseColor);
  const [variation, setVariation] = useState<ThemeVariation>(initialIntent.variation);
  const [intensity, setIntensity] = useState<ThemeIntensity>(initialIntent.intensity);
  const draft = useMemo<ThemeIntent>(() => ({
    version: 1,
    baseColor,
    variation,
    intensity,
  }), [baseColor, intensity, variation]);

  return (
    <div style={styles.overlay} role="presentation">
      <div style={styles.dialog} role="dialog" aria-modal="true" aria-label="UI configuration">
        <div style={styles.header}>
          <div>
            <div style={styles.title}>UI Configuration</div>
            <div style={styles.subtitle}>Color theme for the Brique interface</div>
          </div>
          <button type="button" style={styles.closeButton} onClick={onCancel} disabled={saving}>×</button>
        </div>

        <div style={styles.content}>
          <div style={styles.controls}>
            <label style={styles.field}>
              <span style={styles.label}>Base color</span>
              <div style={styles.colorRow}>
                <input
                  type="color"
                  value={baseColor}
                  onChange={(event) => setBaseColor(event.target.value)}
                  style={styles.colorInput}
                  disabled={saving}
                />
                <input
                  type="text"
                  value={baseColor}
                  onChange={(event) => {
                    const value = event.target.value;
                    setBaseColor(value.startsWith("#") ? value : `#${value}`);
                  }}
                  style={styles.textInput}
                  disabled={saving}
                />
              </div>
            </label>

            <div style={styles.field}>
              <span style={styles.label}>Secondary colors mode</span>
              <div style={styles.segmented}>
                {THEME_VARIATIONS.map((item) => (
                  <button
                    key={item.value}
                    type="button"
                    onClick={() => setVariation(item.value)}
                    disabled={saving}
                    style={{
                      ...styles.segmentButton,
                      ...(variation === item.value ? styles.segmentActive : undefined),
                    }}
                  >
                    {item.label}
                  </button>
                ))}
              </div>
            </div>

            <div style={styles.field}>
              <span style={styles.label}>Intensity</span>
              <div style={styles.segmented}>
                {THEME_INTENSITIES.map((item) => (
                  <button
                    key={item.value}
                    type="button"
                    onClick={() => setIntensity(item.value)}
                    disabled={saving}
                    style={{
                      ...styles.segmentButton,
                      ...(intensity === item.value ? styles.segmentActive : undefined),
                    }}
                  >
                    {item.label}
                  </button>
                ))}
              </div>
            </div>
          </div>

          <div data-syn-theme-preview style={styles.previewShell}>
            <ThemePreview intent={draft} />
          </div>
        </div>

        {error && <div style={styles.error}>{error}</div>}

        <div style={styles.footer}>
          <button type="button" style={styles.secondaryButton} onClick={onCancel} disabled={saving}>Cancel</button>
          <button
            type="button"
            style={styles.primaryButton}
            onClick={() => onApply(draft)}
            disabled={saving || !/^#[0-9a-fA-F]{6}$/.test(baseColor)}
          >
            {saving ? "Saving…" : "OK"}
          </button>
        </div>
      </div>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  overlay: {
    position: "fixed",
    inset: 0,
    zIndex: 2000,
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    background: "rgba(0,0,0,0.52)",
    color: "var(--syn-text-primary, #e8eaed)",
    fontFamily: "monospace",
  },
  dialog: {
    width: 720,
    maxWidth: "calc(100vw - 40px)",
    borderRadius: 12,
    border: "1px solid var(--syn-border-medium, rgba(255,255,255,0.16))",
    background: "var(--syn-surface-overlay, #202124)",
    boxShadow: "0 24px 70px rgba(0,0,0,0.45)",
    overflow: "hidden",
  },
  header: {
    display: "flex",
    alignItems: "flex-start",
    justifyContent: "space-between",
    gap: 16,
    padding: "16px 18px",
    borderBottom: "1px solid var(--syn-border-soft, rgba(255,255,255,0.1))",
  },
  title: {
    fontSize: 14,
    fontWeight: 800,
  },
  subtitle: {
    marginTop: 4,
    fontSize: 11,
    color: "var(--syn-text-muted, rgba(232,234,237,0.55))",
  },
  closeButton: {
    border: 0,
    background: "transparent",
    color: "var(--syn-text-secondary, rgba(232,234,237,0.75))",
    cursor: "pointer",
    font: "inherit",
    fontSize: 18,
  },
  content: {
    display: "grid",
    gridTemplateColumns: "260px 1fr",
    gap: 16,
    padding: 18,
  },
  controls: {
    display: "flex",
    flexDirection: "column",
    gap: 16,
  },
  field: {
    display: "flex",
    flexDirection: "column",
    gap: 8,
  },
  label: {
    fontSize: 10,
    fontWeight: 700,
    textTransform: "uppercase",
    letterSpacing: "0.08em",
    color: "var(--syn-text-muted, rgba(232,234,237,0.55))",
  },
  colorRow: {
    display: "flex",
    gap: 8,
    alignItems: "center",
  },
  colorInput: {
    width: 54,
    height: 34,
    padding: 0,
    border: "1px solid var(--syn-border-medium, rgba(255,255,255,0.16))",
    borderRadius: 6,
    background: "transparent",
    cursor: "pointer",
  },
  textInput: {
    flex: 1,
    minWidth: 0,
    border: "1px solid var(--syn-border-medium, rgba(255,255,255,0.16))",
    borderRadius: 6,
    background: "var(--syn-surface-0, rgba(0,0,0,0.18))",
    color: "var(--syn-text-primary, #e8eaed)",
    font: "inherit",
    fontSize: 12,
    padding: "8px 10px",
  },
  segmented: {
    display: "flex",
    flexDirection: "column",
    gap: 6,
  },
  segmentButton: {
    border: "1px solid var(--syn-border-soft, rgba(255,255,255,0.1))",
    borderRadius: 6,
    background: "var(--syn-surface-0, rgba(0,0,0,0.18))",
    color: "var(--syn-text-secondary, rgba(232,234,237,0.75))",
    cursor: "pointer",
    font: "inherit",
    fontSize: 11,
    padding: "8px 10px",
    textAlign: "left",
  },
  segmentActive: {
    background: "var(--syn-selected-bg, rgba(138,180,248,0.18))",
    borderColor: "var(--syn-selected-border, rgba(138,180,248,0.6))",
    color: "var(--syn-text-primary, #e8eaed)",
  },
  previewShell: {
    minWidth: 0,
  },
  error: {
    margin: "0 18px",
    padding: "8px 10px",
    borderRadius: 6,
    background: "var(--syn-feedback-error-bg)",
    color: "var(--syn-feedback-error)",
    fontSize: 11,
  },
  footer: {
    display: "flex",
    justifyContent: "flex-end",
    gap: 8,
    padding: "14px 18px",
    borderTop: "1px solid var(--syn-border-soft, rgba(255,255,255,0.1))",
  },
  secondaryButton: {
    border: "1px solid var(--syn-border-medium, rgba(255,255,255,0.16))",
    borderRadius: 6,
    background: "transparent",
    color: "var(--syn-text-secondary, rgba(232,234,237,0.75))",
    cursor: "pointer",
    font: "inherit",
    fontSize: 12,
    padding: "7px 14px",
  },
  primaryButton: {
    border: "1px solid var(--syn-selected-border, rgba(138,180,248,0.6))",
    borderRadius: 6,
    background: "var(--syn-feedback-info, #8ab4f8)",
    color: "var(--syn-text-inverse, #071014)",
    cursor: "pointer",
    font: "inherit",
    fontWeight: 800,
    fontSize: 12,
    padding: "7px 16px",
  },
};
