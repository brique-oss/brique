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

import type { ProjectionKind } from "../Projection/contracts.js";

export type ProjectionSwitchControlProps = {
  activeProjection: ProjectionKind;
  onActiveProjectionChange: (projection: ProjectionKind) => void;
};

const projections: Array<{ kind: ProjectionKind; label: string }> = [
  { kind: "structure", label: "Structure" },
  { kind: "semantic", label: "Semantic" },
  { kind: "flow", label: "Flow" },
  { kind: "trace", label: "Trace" },
];

export function ProjectionSwitchControl({
  activeProjection,
  onActiveProjectionChange,
}: ProjectionSwitchControlProps) {
  return (
    <div aria-label="Projection selection" role="tablist" style={styles.root}>
      {projections.map(({ kind, label }) => {
        const active = kind === activeProjection;
        return (
          <button
            aria-selected={active}
            key={kind}
            onClick={() => {
              if (!active) onActiveProjectionChange(kind);
            }}
            role="tab"
            style={{ ...styles.button, ...(active ? styles.active : undefined) }}
            type="button"
          >
            {label}
          </button>
        );
      })}
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  root: {
    display: "flex",
    alignSelf: "stretch",
    alignItems: "center",
    gap: "4px",
    padding: "6px 8px",
  },
  button: {
    alignSelf: "stretch",
    border: "1px solid var(--syn-control-border)",
    borderRadius: "4px",
    padding: "0 12px",
    background: "var(--syn-control-bg)",
    color: "var(--syn-text-secondary)",
    cursor: "pointer",
    fontFamily: "inherit",
    fontWeight: 700,
  },
  active: {
    borderColor: "var(--syn-selected-border)",
    background: "var(--syn-selected-bg)",
    color: "var(--syn-text-primary)",
  },
};
