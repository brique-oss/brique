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

export type OperationalStateHeaderProps = {
  expanded: boolean;
  onToggle: () => void;
};

export function OperationalStateHeader({ expanded, onToggle }: OperationalStateHeaderProps) {
  return (
    <div style={styles.root}>
      <span style={styles.title}>Operational State</span>
      <button
        onClick={onToggle}
        style={styles.toggle}
        title={expanded ? "Collapse" : "Expand"}
        type="button"
      >
        {expanded ? "▼" : "▲"}
      </button>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  root: {
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between",
    height: 28,
    padding: "0 10px",
    boxSizing: "border-box",
    flexShrink: 0,
    background: "var(--syn-operational-panel-bg)",
    borderTop: "1px solid var(--syn-border-soft)",
  },
  title: {
    fontSize: 10,
    fontWeight: 700,
    textTransform: "uppercase",
    letterSpacing: "0.08em",
    opacity: 0.7,
  },
  toggle: {
    border: 0,
    background: "transparent",
    color: "inherit",
    cursor: "pointer",
    fontFamily: "inherit",
    fontSize: 12,
    padding: "0 4px",
    opacity: 0.7,
  },
};
