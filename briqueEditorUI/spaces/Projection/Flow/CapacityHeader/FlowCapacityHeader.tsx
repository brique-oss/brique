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

import type { FlowCapacitySummary } from "../contracts.js";

export type FlowCapacityHeaderProps = {
  summary: FlowCapacitySummary | undefined;
  loading: boolean;
  onRefresh: () => void;
};

export function FlowCapacityHeader({
  summary,
  loading,
  onRefresh,
}: FlowCapacityHeaderProps) {
  return (
    <header
      aria-label="Flow capacity header"
      data-component="flow-capacity-header"
      style={styles.root}
    >
      
      {summary && <div style={styles.context}>{summary.context}</div>}
      {summary?.objective && <div style={styles.objective}>{summary.objective}</div>}
      <button
        disabled={!summary || loading}
        onClick={onRefresh}
        style={styles.refresh}
        title="Refresh flow projection"
        type="button"
      >
        {loading ? "Loading..." : "Refresh"}
      </button>
    </header>
  );
}

const styles: Record<string, React.CSSProperties> = {
  root: {
    flex: "0 0 auto",
    display: "flex",
    alignItems: "center",
    gap: "20px",
    minHeight: "36px",
    padding: "4px 14px",
    boxSizing: "border-box",
    borderBottom: "1px solid var(--syn-projection-toolbar-border)",
    background: "var(--syn-projection-toolbar-bg)",
  },
  identity: {
    display: "flex",
    flexDirection: "column",
    minWidth: 0,
  },
  eyebrow: {
    fontSize: "9px",
    fontWeight: 700,
    letterSpacing: "0.12em",
    opacity: 0.55,
    textTransform: "uppercase",
  },
  title: {
    fontSize: "17px",
    fontWeight: 700,
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap",
  },
  context: {
    fontSize: "10px",
    opacity: 0.65,
  },
  objective: {
    flex: "1 1 auto",
    minWidth: 0,
    fontSize: "11px",
    lineHeight: 1.35,
    opacity: 0.78,
  },
  refresh: {
    flex: "0 0 auto",
    border: "1px solid var(--syn-control-border)",
    borderRadius: "4px",
    padding: "5px 9px",
    background: "var(--syn-control-bg)",
    color: "var(--syn-text-primary)",
    cursor: "pointer",
    fontFamily: "inherit",
    fontSize: "11px",
  },
};
