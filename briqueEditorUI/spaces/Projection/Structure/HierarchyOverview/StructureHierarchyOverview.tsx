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

import { ExpandedHierarchy } from "./ExpandedHierarchy.js";
import type {
  ActiveContext,
  ActiveContextActivationRequest,
  HierarchyDisplayedResponseState,
  RefreshRequest,
} from "./contracts.js";

export type StructureHierarchyOverviewProps = {
  activeContext: ActiveContext;
  hierarchyState: HierarchyDisplayedResponseState;
  onActivateContext: (request: ActiveContextActivationRequest) => void;
  onRefresh: (request: RefreshRequest) => void;
  onOpenInNewTab?: (contextPath: string) => void;
};

export function StructureHierarchyOverview({
  activeContext,
  hierarchyState,
  onActivateContext,
  onRefresh,
  onOpenInNewTab,
}: StructureHierarchyOverviewProps) {
  const { error, hierarchy, loading } = hierarchyState;

  return (
    <section
      aria-label="Structure hierarchy overview"
      style={styles.root}
    >
      {loading && <div style={styles.status}>Loading hierarchy...</div>}
      {error && (
        <div role="alert" style={styles.status}>
          <span>{error}</span>
          <button onClick={() => onRefresh()} type="button">
            Retry
          </button>
        </div>
      )}
      {!hierarchy && !loading && !error && (
        <div style={styles.status}>No hierarchy available.</div>
      )}
      {hierarchy && (
        <ExpandedHierarchy
          activeContext={activeContext}
          hierarchy={hierarchy}
          onActivateContext={onActivateContext}
          onOpenInNewTab={onOpenInNewTab}
        />

      )}
    </section>
  );
}

const styles: Record<string, React.CSSProperties> = {
  root: {
    display: "flex",
    flexDirection: "column",
    flex: "1 1 0%",
    minWidth: 0,
    minHeight: 0,
    overflow: "hidden",
    background: "var(--syn-structure-bg)",
  },
  status: {
    display: "flex",
    gap: "8px",
    alignItems: "center",
    padding: "8px 12px",
  },
};
