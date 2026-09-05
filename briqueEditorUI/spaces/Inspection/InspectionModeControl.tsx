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

import type { InspectionMode } from "./contracts.js";

export type InspectionModeControlProps = {
  canOpenSource: boolean;
  mode: InspectionMode;
  onModeChange: (mode: InspectionMode) => void;
  onOpenSource: () => void;
};

const modes: Array<{ mode: InspectionMode; label: string }> = [
  { mode: "inspect", label: "Inspect" },
  { mode: "browse", label: "Browse" },
  { mode: "search", label: "Search" },
];

export function InspectionModeControl({
  canOpenSource,
  mode,
  onModeChange,
  onOpenSource,
}: InspectionModeControlProps) {
  return (
    <nav aria-label="Inspection mode" style={styles.root}>
      {modes.map(({ mode: candidate, label }) => {
        const active = candidate === mode;

        const modeButton = (
          <button
            aria-current={active ? "page" : undefined}
            key={candidate}
            onClick={() => onModeChange(candidate)}
            style={{
              ...styles.mode,
              ...(active ? styles.activeMode : undefined),
            }}
            type="button"
          >
            {label}
          </button>
        );

        if (candidate !== "inspect") return modeButton;

        return (
          <div key={candidate} style={styles.inspectMode}>
            {modeButton}
            <button
              aria-label="Open inspected source file in VS Code"
              disabled={!canOpenSource}
              onClick={onOpenSource}
              style={{
                ...styles.openSource,
                ...(!canOpenSource ? styles.disabled : undefined),
              }}
              title="Open inspected source file in VS Code"
              type="button"
            >
              ✎
            </button>
          </div>
        );
      })}
    </nav>
  );
}

const styles: Record<string, React.CSSProperties> = {
  root: {
    display: "grid",
    gridTemplateColumns: "repeat(3, minmax(0, 1fr))",
    flex: 1,
    alignSelf: "stretch",
    minWidth: 0,
    gap: "4px",
  },
  mode: {
    minWidth: 0,
    border: "1px solid var(--syn-control-border)",
    borderRadius: "4px",
    background: "var(--syn-control-bg)",
    color: "var(--syn-text-secondary)",
    cursor: "pointer",
    fontFamily: "inherit",
    fontSize: "11px",
  },
  inspectMode: {
    display: "grid",
    gridTemplateColumns: "minmax(0, 1fr) 22px",
    minWidth: 0,
    gap: "2px",
  },
  openSource: {
    minWidth: 0,
    border: "1px solid var(--syn-control-border)",
    borderRadius: "4px",
    padding: 0,
    background: "var(--syn-control-bg)",
    color: "var(--syn-text-secondary)",
    cursor: "pointer",
    fontFamily: "inherit",
    fontSize: "12px",
  },
  disabled: {
    cursor: "default",
    opacity: 0.3,
  },
  activeMode: {
    borderColor: "var(--syn-selected-border)",
    background: "var(--syn-selected-bg)",
    color: "var(--syn-text-primary)",
  },
};
