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

import type { ActiveContextActivationRequest, ContextNodeView } from "./contracts.js";

export type ChildContextGridProps = {
  children: ContextNodeView[];
  onActivateContext: (request: ActiveContextActivationRequest) => void;
  onOpenInNewTab?: (contextPath: string) => void;
};

export function ChildContextGrid({ children, onActivateContext, onOpenInNewTab }: ChildContextGridProps) {
  if (children.length === 0) {
    return <div style={styles.empty}>No child contexts</div>;
  }

  return (
    <div style={styles.grid}>
      {children.map((child) => (
        <ChildBubble
          key={child.key}
          node={child}
          onActivateContext={onActivateContext}
          onOpenInNewTab={onOpenInNewTab}
        />
      ))}
    </div>
  );
}

type ChildBubbleProps = {
  node: ContextNodeView;
  onActivateContext: (request: ActiveContextActivationRequest) => void;
  onOpenInNewTab?: (contextPath: string) => void;
};

function ChildBubble({ node, onActivateContext, onOpenInNewTab }: ChildBubbleProps) {
  function handleDoubleClick() {
    onActivateContext({ target: node.key });
  }

  function handleWheel(e: React.WheelEvent) {
    if (e.deltaY < 0) {
      e.stopPropagation();
      onActivateContext({ target: node.key });
    }
  }

  return (
    <div
      style={styles.bubble}
      onDoubleClick={handleDoubleClick}
      onWheel={handleWheel}
      onAuxClick={(e) => { if (e.button === 1) { e.preventDefault(); onOpenInNewTab?.(node.key); } }}
      title={node.key}
    >
      <span style={styles.bubbleLabel}>{node.label}</span>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  grid: {
    display: "grid",
    gridTemplateColumns: "1fr 1fr",
    gridAutoRows: "minmax(48px, 1fr)",
    gap: "8px",
    padding: "8px",
    overflowY: "auto",
    flex: 1,
    minHeight: 0,
  },
  empty: {
    flex: 1,
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    opacity: 0.5,
    fontSize: "13px",
  },
  bubble: {
    display: "flex",
    flexDirection: "column",
    justifyContent: "center",
    alignItems: "center",
    padding: "10px 12px",
    border: "1px solid var(--syn-structure-node-border)",
    borderRadius: "6px",
    cursor: "pointer",
    background: "var(--syn-structure-node-bg)",
    userSelect: "none",
  },
  bubbleLabel: {
    fontSize: "13px",
    fontWeight: 700,
  },
};
