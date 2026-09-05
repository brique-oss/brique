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

import { useRef, useState } from "react";
import type { ProjectionKind } from "../Projection/contracts.js";
import type { TabId, TabState } from "./contracts.js";
import { McpWrapperButton } from "./McpWrapperButton.js";
import { ThemeConfigurationButton } from "../Theme/ThemeConfigurationButton.js";

const projections: Array<{ kind: ProjectionKind; label: string }> = [
  { kind: "structure", label: "Structure" },
  { kind: "semantic", label: "Semantic" },
  { kind: "flow", label: "Flow" },
  { kind: "trace", label: "Trace" },
];

export type TabBarProps = {
  tabs: TabState[];
  activeTabId: TabId;
  activeProjection: ProjectionKind;
  activeContext: string | undefined;
  showHistory: boolean;
  canGoBack: boolean;
  onActivateTab: (id: TabId) => void;
  onCloseTab: (id: TabId) => void;
  onNewTab: () => void;
  onActiveProjectionChange: (projection: ProjectionKind) => void;
  onReorderTabs: (fromIndex: number, toIndex: number) => void;
  onToggleHistory: () => void;
  onGoBack: () => void;
};

export function TabBar({
  tabs,
  activeTabId,
  activeProjection,
  activeContext,
  showHistory,
  canGoBack,
  onActivateTab,
  onCloseTab,
  onNewTab,
  onActiveProjectionChange,
  onReorderTabs,
  onToggleHistory,
  onGoBack,
}: TabBarProps) {
  const dragIndexRef = useRef<number | null>(null);
  const [dropTargetIndex, setDropTargetIndex] = useState<number | null>(null);

  function handleDragStart(e: React.DragEvent, index: number) {
    dragIndexRef.current = index;
    e.dataTransfer.effectAllowed = "move";
    // Transparent drag image to avoid browser default ghost
    e.dataTransfer.setDragImage(new Image(), 0, 0);
  }

  function handleDragOver(e: React.DragEvent, index: number) {
    e.preventDefault();
    e.dataTransfer.dropEffect = "move";
    if (dragIndexRef.current !== null && dragIndexRef.current !== index) {
      setDropTargetIndex(index);
    }
  }

  function handleDrop(e: React.DragEvent, index: number) {
    e.preventDefault();
    if (dragIndexRef.current !== null && dragIndexRef.current !== index) {
      onReorderTabs(dragIndexRef.current, index);
    }
    dragIndexRef.current = null;
    setDropTargetIndex(null);
  }

  function handleDragEnd() {
    dragIndexRef.current = null;
    setDropTargetIndex(null);
  }

  return (
    <div style={styles.root}>
      {/* Projection switch — left side */}
      <div style={styles.projections} role="tablist" aria-label="Projection selection">
        {projections.map(({ kind, label }) => {
          const active = kind === activeProjection;
          return (
            <button
              key={kind}
              type="button"
              role="tab"
              aria-selected={active}
              onClick={() => { if (!active) onActiveProjectionChange(kind); }}
              style={{ ...styles.projectionButton, ...(active ? styles.projectionActive : undefined) }}
            >
              {label}
            </button>
          );
        })}
      </div>

      {/* Divider */}
      <div style={styles.divider} />

      {/* Back button */}
      <button
        type="button"
        aria-label="Go back"
        disabled={!canGoBack}
        onClick={onGoBack}
        style={{ ...styles.projectionButton, ...(!canGoBack ? styles.projectionDisabled : undefined), fontSize: 16, fontWeight: 700 }}
        title="Go back"
      >
        ←
      </button>

      {/* History toggle */}
      <button
        type="button"
        aria-label="Toggle history"
        onClick={onToggleHistory}
        style={{ ...styles.projectionButton, ...(showHistory ? styles.projectionActive : undefined), fontSize: 16, fontWeight: 700 }}
        title="History"
      >
        ◷
      </button>

      {/* Divider */}
      <div style={styles.divider} />

      {/* Tab list — right of projections */}
      <div style={styles.tabs} role="tablist" aria-label="Open tabs">
        {tabs.map((tab, index) => {
          const active = tab.id === activeTabId;
          const label = tab.selectedElement?.elementName ?? "New Tab";
          const isDropTarget = dropTargetIndex === index && dragIndexRef.current !== index;
          return (
            <div
              key={tab.id}
              role="tab"
              aria-selected={active}
              draggable
              onDragStart={(e) => handleDragStart(e, index)}
              onDragOver={(e) => handleDragOver(e, index)}
              onDrop={(e) => handleDrop(e, index)}
              onDragEnd={handleDragEnd}
              style={{
                ...styles.tab,
                ...(active ? styles.tabActive : undefined),
                ...(isDropTarget ? styles.tabDropTarget : undefined),
              }}
            >
              <button
                type="button"
                style={styles.tabLabel}
                onClick={() => onActivateTab(tab.id)}
                title={label}
              >
                {label}
              </button>
              <button
                type="button"
                aria-label={`Close ${label}`}
                style={styles.tabClose}
                onClick={(e) => { e.stopPropagation(); onCloseTab(tab.id); }}
              >
                ×
              </button>
            </div>
          );
        })}

        {/* New tab button */}
        <button
          type="button"
          aria-label="New tab"
          style={styles.newTab}
          onClick={onNewTab}
        >
          +
        </button>
      </div>

      <div style={styles.rightControls}>
        <McpWrapperButton activeContext={activeContext} />
        <ThemeConfigurationButton />
      </div>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  root: {
    display: "flex",
    alignItems: "stretch",
    width: "100%",
    height: "100%",
    boxSizing: "border-box",
    overflow: "hidden",
  },
  projections: {
    display: "flex",
    alignItems: "center",
    gap: 4,
    padding: "6px 8px",
    flexShrink: 0,
  },
  projectionButton: {
    alignSelf: "stretch",
    border: "none",
    borderRadius: 4,
    padding: "0 10px",
    background: "transparent",
    color: "var(--syn-text-muted, rgba(232,234,237,0.6))",
    cursor: "pointer",
    fontFamily: "inherit",
    fontWeight: 500,
    fontSize: 11,
    whiteSpace: "nowrap",
  },
  projectionActive: {
    background: "var(--syn-nav-button-active-bg, rgba(255,255,255,0.1))",
    color: "var(--syn-nav-title, #e8eaed)",
  },
  projectionDisabled: {
    opacity: 0.25,
    cursor: "default",
  },
  divider: {
    width: 1,
    margin: "6px 6px",
    background: "var(--syn-nav-border, rgba(255,255,255,0.12))",
    flexShrink: 0,
  },
  tabs: {
    display: "flex",
    alignItems: "flex-end",
    gap: 1,
    padding: "4px 4px 0",
    flex: "1 1 0%",
    overflow: "hidden",
  },
  rightControls: {
    display: "flex",
    alignItems: "center",
    gap: 4,
    padding: "6px 8px",
    flexShrink: 0,
  },
  tab: {
    display: "flex",
    alignItems: "center",
    borderRadius: "6px 6px 0 0",
    border: "none",
    background: "transparent",
    overflow: "hidden",
    flexShrink: 0,
    maxWidth: 180,
    height: 28,
  },
  tabActive: {
    background: "var(--syn-nav-tab-active-bg, #35363a)",
    borderColor: "var(--syn-border-medium, rgba(255,255,255,0.18))",
    boxShadow: "0 -2px 0 var(--syn-nav-tab-active-border, #8ab4f8) inset",
  },
  tabDropTarget: {
    borderColor: "var(--syn-selected-border)",
    boxShadow: "-2px 0 0 var(--syn-selected-border)",
  },
  tabLabel: {
    flex: "1 1 0%",
    border: 0,
    background: "transparent",
    color: "var(--syn-text-secondary, rgba(232,234,237,0.75))",
    fontFamily: "inherit",
    fontWeight: 400,
    fontSize: 12,
    cursor: "pointer",
    padding: "0 8px",
    textAlign: "left",
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap",
    minWidth: 0,
  },
  tabClose: {
    border: 0,
    background: "transparent",
    color: "var(--syn-text-muted, rgba(232,234,237,0.4))",
    cursor: "pointer",
    fontFamily: "inherit",
    fontSize: 13,
    padding: "0 6px",
    flexShrink: 0,
    lineHeight: 1,
  },
  newTab: {
    border: "none",
    borderRadius: 4,
    background: "transparent",
    color: "var(--syn-text-muted, rgba(232,234,237,0.5))",
    cursor: "pointer",
    fontFamily: "inherit",
    fontSize: 16,
    padding: "0 10px",
    flexShrink: 0,
    alignSelf: "center",
    marginBottom: 4,
  },
};
