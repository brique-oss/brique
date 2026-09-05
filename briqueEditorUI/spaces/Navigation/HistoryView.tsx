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

import { usePreview } from "../Overlay/PreviewContext.js";
import { useOpen } from "../Overlay/OpenContext.js";
import type { InspectionTarget } from "../Inspection/contracts.js";

export type HistoryViewProps = {
  history: InspectionTarget[];
  onSelect: (target: InspectionTarget) => void;
  onActivateContext: (contextPath: string) => void;
  onOpenInNewTab: (target: InspectionTarget) => void;
};

export function HistoryView({ history, onSelect, onActivateContext, onOpenInNewTab }: HistoryViewProps) {
  if (history.length === 0) {
    return (
      <div style={styles.root}>
        <div style={styles.empty}>No history yet.</div>
      </div>
    );
  }

  // Most recent first
  const reversed = [...history].reverse();

  return (
    <div style={styles.root}>
      <div style={styles.grid}>
        {reversed.map((entry, i) => (
          <HistoryBubble
            key={`${entry.context}:${entry.elementKind}:${entry.elementName}:${i}`}
            target={entry}
            onSelect={onSelect}
            onActivateContext={onActivateContext}
            onOpenInNewTab={onOpenInNewTab}
          />
        ))}
      </div>
    </div>
  );
}

function HistoryBubble({
  target,
  onSelect,
  onActivateContext,
  onOpenInNewTab,
}: {
  target: InspectionTarget;
  onSelect: (t: InspectionTarget) => void;
  onActivateContext: (ctx: string) => void;
  onOpenInNewTab: (t: InspectionTarget) => void;
}) {
  const { requestPreview } = usePreview();
  const { requestOpen } = useOpen();

  return (
    <div
      style={styles.bubble}
      onClick={(e) => {
        if (e.metaKey) { onActivateContext(target.context); return; }
        if (e.shiftKey && (target.elementKind === "document" || target.elementKind === "matter")) {
          requestOpen({ elementKind: target.elementKind, elementName: target.elementName, context: target.context });
          return;
        }
        onSelect(target);
      }}
      onAuxClick={(e) => {
        if (e.button === 1) { e.preventDefault(); onOpenInNewTab(target); }
      }}
      onMouseEnter={(e) => {
        if (e.ctrlKey) requestPreview({ elementKind: target.elementKind, elementName: target.elementName, context: target.context, pos: { x: e.clientX, y: e.clientY } });
      }}
      onMouseMove={(e) => {
        if (e.ctrlKey) requestPreview({ elementKind: target.elementKind, elementName: target.elementName, context: target.context, pos: { x: e.clientX, y: e.clientY } });
      }}
      title={`${target.elementKind} · ${target.elementName}\n${target.context}`}
    >
      <span style={styles.bubbleKind}>{target.elementKind}</span>
      <span style={styles.bubbleName}>{target.elementName}</span>
      <span style={styles.bubbleContext}>{target.context}</span>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  root: {
    position: "absolute",
    inset: 0,
    display: "flex",
    flexDirection: "column",
    overflow: "hidden",
    background: "var(--syn-nav-bg)",
    color: "var(--syn-text-primary)",
    fontFamily: "monospace",
  },
  empty: {
    flex: 1,
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    opacity: 0.4,
    fontSize: 13,
  },
  grid: {
    display: "flex",
    flexWrap: "wrap",
    alignItems: "center",
    gap: 8,
    padding: 16,
    alignContent: "start",
    overflowY: "auto",
    flex: 1,
  },
  bubble: {
    display: "flex",
    flexDirection: "column",
    gap: 2,
    padding: "8px 12px",
    border: "1px solid var(--syn-nav-border)",
    background: "var(--syn-nav-panel-bg)",
    borderRadius: 8,
    cursor: "pointer",
    userSelect: "none",
    minWidth: 120,
    maxWidth: 220,
    transition: "filter 0.1s",
  },
  bubbleKind: {
    fontSize: 9,
    fontWeight: 700,
    textTransform: "uppercase",
    letterSpacing: "0.1em",
    opacity: 0.9,
    color: "var(--syn-nav-title)",
  },
  bubbleName: {
    fontSize: 13,
    fontWeight: 700,
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap",
    color: "var(--syn-text-primary)",
  },
  bubbleContext: {
    fontSize: 9,
    opacity: 0.5,
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap",
  },
};
