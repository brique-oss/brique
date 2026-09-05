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

import type { ActiveContext, ActiveContextActivationRequest, HierarchyDisplayedResponseState } from "../HierarchyOverview/contracts.js";
import { findNodeByContext, getParentContext, getStructureNodeContext, getStructureNodeLabel } from "../ContextMap/contracts.js";

export type ContextRelationsProps = {
  activeContext: ActiveContext;
  hierarchyState: HierarchyDisplayedResponseState;
  onActivateContext: (request: ActiveContextActivationRequest) => void;
  onOpenInNewTab?: (contextPath: string) => void;
};

export function ContextRelations({
  activeContext,
  hierarchyState,
  onActivateContext,
  onOpenInNewTab,
}: ContextRelationsProps) {
  const { hierarchy } = hierarchyState;

  const parentKey = hierarchy
    ? getParentContext(hierarchy.root, activeContext)
    : undefined;

  const activeNode = hierarchy
    ? findNodeByContext(hierarchy.root, activeContext)
    : undefined;

  const children = activeNode?.children ?? [];

  return (
    <div style={styles.section}>
      {parentKey !== undefined && (
        <div
          style={styles.contextBubble}
          onDoubleClick={() => onActivateContext({ target: parentKey })}
          onAuxClick={(e) => { if (e.button === 1) { e.preventDefault(); onOpenInNewTab?.(parentKey); } }}
          title={parentKey}
        >
          <span style={styles.bubbleLabel}>
            {parentKey.split("/").filter(Boolean).slice(-1)[0] ?? parentKey}
          </span>
        </div>
      )}

      {children.length > 0 && (
        <div style={styles.children}>
          {children.map((child) => {
            const key = getStructureNodeContext(child);
            const label = getStructureNodeLabel(child);
            return (
              <div
                key={key}
                style={styles.contextBubble}
                onDoubleClick={() => onActivateContext({ target: key })}
                onAuxClick={(e) => { if (e.button === 1) { e.preventDefault(); onOpenInNewTab?.(key); } }}
                title={key}
              >
                <span style={styles.bubbleLabel}>{label}</span>
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  section: {
    display: "flex",
    flexDirection: "column",
    gap: "8px",
    padding: "12px",
    borderBottom: "1px solid var(--syn-border-soft)",
  },
  contextBubble: {
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    padding: "6px 10px",
    border: "1px solid var(--syn-structure-node-border)",
    borderRadius: "6px",
    cursor: "pointer",
    userSelect: "none",
    background: "var(--syn-structure-node-bg)",
  },
  activeContext: {
    display: "flex",
    flexDirection: "column",
    gap: "2px",
    padding: "6px 10px",
    border: "1px solid var(--syn-selected-border)",
    borderRadius: "6px",
    background: "var(--syn-structure-panel-bg)",
  },
  bubbleRole: {
    fontSize: "10px",
    opacity: 0.5,
    textTransform: "uppercase",
    letterSpacing: "0.05em",
  },
  bubbleLabel: {
    fontSize: "13px",
    fontWeight: 600,
  },
  activeName: {
    fontSize: "13px",
    fontWeight: 700,
  },
  children: {
    display: "flex",
    flexWrap: "wrap",
    gap: "6px",
  },
};
