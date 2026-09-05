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

import type {
  ActiveContextStructure,
  ElementGroup,
  ElementItem,
  ElementSelectionRequest,
  TargetOpeningRequest,
} from "./contracts.js";
import { extractElementGroups } from "./contracts.js";
import { usePreview } from "../../../Overlay/PreviewContext.js";
import { useOpen } from "../../../Overlay/OpenContext.js";

const COMPACT_LIMIT = 3;
const ITEM_HEIGHT = 28;
const LIST_MAX_ITEMS = 10;
const DOC_COL_MAX_ITEMS = 5;

export type ElementGroupsProps = {
  activeContextStructure: ActiveContextStructure | undefined;
  activeContext: string;
  selectedElement: ElementItem | undefined;
  expanded: boolean;
  onSelectElement: (request: ElementSelectionRequest) => void;
  onOpenElement: (request: TargetOpeningRequest) => void;
  onOpenInNewTab?: (context: string, elementKind: string, elementName: string) => void;
};

export function ElementGroups({
  activeContextStructure,
  activeContext,
  selectedElement,
  expanded,
  onSelectElement,
  onOpenElement,
  onOpenInNewTab,
}: ElementGroupsProps) {
  const { requestPreview } = usePreview();
  const { requestOpen } = useOpen();
  const groups = activeContextStructure
    ? extractElementGroups(activeContextStructure.root)
    : [];

  if (groups.length === 0) {
    return (
      <div style={styles.empty}>
        {activeContextStructure ? "No elements in this context." : ""}
      </div>
    );
  }

  if (!expanded) {
    return (
      <div style={styles.compactBody}>
        {groups.map((group) => (
          <div key={group.kind} style={styles.compactSection}>
            <div style={styles.compactHeader}>{group.label}</div>
            {group.items.slice(0, COMPACT_LIMIT).map((item) => {
              const isSelected = selectedElement?.key === item.key;
              return (
                <div
                  key={item.key}
                  style={{
                    ...styles.compactItem,
                    ...(isSelected ? styles.compactItemSelected : {}),
                  }}
                  onClick={(e) => {
                    if (e.shiftKey && (item.kind === "document" || item.kind === "matter")) requestOpen({ elementKind: item.kind, elementName: item.name, context: activeContext });
                    else onSelectElement({ element: item });
                  }}
                  onAuxClick={(e) => {
                    if (e.button === 1) { e.preventDefault(); onOpenInNewTab?.(activeContext, item.kind, item.name); }
                  }}
                  onDoubleClick={() => onOpenElement({ element: item })}
                  onMouseEnter={(e) => {
                    if (e.ctrlKey) requestPreview({ elementKind: item.kind, elementName: item.name, context: activeContext, pos: { x: e.clientX, y: e.clientY } });
                  }}
                  title={item.key}
                >
                  {item.name}
                </div>
              );
            })}
            {group.items.length > COMPACT_LIMIT && (
              <div style={styles.compactMore}>…</div>
            )}
          </div>
        ))}
      </div>
    );
  }

  const docGroup = groups.find((g) => g.kind === "document");
  const otherGroups = groups.filter((g) => g.kind !== "document");

  return (
    <div style={styles.body}>
      {docGroup && (
        <div style={styles.documentSection}>
          <DocumentBrick
            group={docGroup}
            activeContext={activeContext}
            selectedElement={selectedElement}
            onSelectElement={onSelectElement}
            onOpenElement={onOpenElement}
            onPreviewRequest={requestPreview}
            onOpenRequest={requestOpen}
            onOpenInNewTab={onOpenInNewTab}
          />
        </div>
      )}
      {otherGroups.length > 0 && (
        <div style={styles.brickGrid}>
          {otherGroups.map((group) => (
            <CategoryBrick
              key={group.kind}
              group={group}
              activeContext={activeContext}
              selectedElement={selectedElement}
              onSelectElement={onSelectElement}
              onOpenElement={onOpenElement}
              onPreviewRequest={requestPreview}
              onOpenRequest={requestOpen}
              onOpenInNewTab={onOpenInNewTab}
            />
          ))}
        </div>
      )}
    </div>
  );
}

type CategoryBrickProps = {
  group: ElementGroup;
  activeContext: string;
  selectedElement: ElementItem | undefined;
  onSelectElement: (request: ElementSelectionRequest) => void;
  onOpenElement: (request: TargetOpeningRequest) => void;
  onPreviewRequest: (target: { elementKind: string; elementName: string; context: string; pos: { x: number; y: number } }) => void;
  onOpenRequest: (target: { elementKind: string; elementName: string; context: string }) => void;
  onOpenInNewTab?: (context: string, elementKind: string, elementName: string) => void;
};

function DocumentBrick({
  group,
  activeContext,
  selectedElement,
  onSelectElement,
  onOpenElement,
  onPreviewRequest,
  onOpenRequest,
  onOpenInNewTab,
}: CategoryBrickProps) {
  const col1 = group.items.slice(0, DOC_COL_MAX_ITEMS);
  const col2 = group.items.slice(DOC_COL_MAX_ITEMS, DOC_COL_MAX_ITEMS * 2);

  const renderItem = (item: ElementItem) => {
    const isSelected = selectedElement?.key === item.key;
    return (
      <div
        key={item.key}
        style={{ ...styles.documentItem, ...(isSelected ? styles.categoryItemSelected : {}) }}
        onClick={(e) => {
          if (e.shiftKey) onOpenRequest({ elementKind: item.kind, elementName: item.name, context: activeContext });
          else onSelectElement({ element: item });
        }}
        onAuxClick={(e) => {
          if (e.button === 1) { e.preventDefault(); onOpenInNewTab?.(activeContext, item.kind, item.name); }
        }}
        onDoubleClick={() => onOpenElement({ element: item })}
        onMouseEnter={(e) => {
          if (e.ctrlKey) onPreviewRequest({ elementKind: item.kind, elementName: item.name, context: activeContext, pos: { x: e.clientX, y: e.clientY } });
        }}
        title={item.key}
      >
        {item.name}
      </div>
    );
  };

  return (
    <div style={styles.documentBrick}>
      <div style={styles.categoryHeader}>
        <span style={styles.categoryLabel}>{group.label}</span>
        <span style={styles.categoryCount}>{group.items.length}</span>
      </div>
      <div style={styles.documentGrid}>
        <div style={{ display: "block", flex: 1, maxHeight: DOC_COL_MAX_ITEMS * ITEM_HEIGHT, overflowY: "auto", padding: "4px 0" }}>
          {col1.map(renderItem)}
        </div>
        <div style={{ display: "block", flex: 1, maxHeight: DOC_COL_MAX_ITEMS * ITEM_HEIGHT, overflowY: "auto", padding: "4px 0", borderLeft: "1px solid var(--syn-border-soft)" }}>
          {col2.map(renderItem)}
        </div>
      </div>
    </div>
  );
}

function CategoryBrick({
  group,
  activeContext,
  selectedElement,
  onSelectElement,
  onOpenElement,
  onPreviewRequest,
  onOpenRequest,
  onOpenInNewTab,
}: CategoryBrickProps) {
  return (
    <div style={styles.categoryBrick}>
      <div style={styles.categoryHeader}>
        <span style={styles.categoryLabel}>{group.label}</span>
        <span style={styles.categoryCount}>{group.items.length}</span>
      </div>
      <div style={styles.categoryItems}>
        {group.items.map((item) => {
          const isSelected = selectedElement?.key === item.key;
          return (
            <div
              key={item.key}
              style={{
                ...styles.categoryItem,
                ...(isSelected ? styles.categoryItemSelected : {}),
              }}
              onClick={(e) => {
                if (e.shiftKey && (item.kind === "document" || item.kind === "matter")) onOpenRequest({ elementKind: item.kind, elementName: item.name, context: activeContext });
                else onSelectElement({ element: item });
              }}
              onAuxClick={(e) => {
                if (e.button === 1) { e.preventDefault(); onOpenInNewTab?.(activeContext, item.kind, item.name); }
              }}
              onDoubleClick={() => onOpenElement({ element: item })}
              onMouseEnter={(e) => {
                if (e.ctrlKey) onPreviewRequest({ elementKind: item.kind, elementName: item.name, context: activeContext, pos: { x: e.clientX, y: e.clientY } });
              }}
              title={item.key}
            >
              {item.name}
            </div>
          );
        })}
      </div>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  body: {
    display: "flex",
    flexDirection: "column",
    flex: 1,
    overflowY: "auto",
    minHeight: 0,
  },
  documentSection: {
    padding: "10px 10px 0",
  },
  documentBrick: {
    display: "flex",
    flexDirection: "column",
    border: "1px solid var(--syn-structure-node-border)",
    borderRadius: "6px",
    background: "var(--syn-structure-node-bg)",
  },
  documentGrid: {
    display: "flex",
    flexDirection: "row",
  },
  documentCol: {
    flex: 1,
    padding: "4px 0",
  },
  documentItem: {
    padding: "4px 10px",
    fontSize: "12px",
    cursor: "pointer",
    userSelect: "none" as const,
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap" as const,
    borderBottom: "1px solid var(--syn-border-soft)",
  },
  brickGrid: {
    display: "grid",
    gridTemplateColumns: "1fr 1fr",
    gap: "8px",
    padding: "10px",
    alignContent: "start",
  },
  categoryBrick: {
    display: "flex",
    flexDirection: "column",
    border: "1px solid var(--syn-structure-node-border)",
    borderRadius: "6px",
    background: "var(--syn-structure-node-bg)",
    alignSelf: "start",
  },
  categoryHeader: {
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between",
    padding: "5px 8px",
    borderBottom: "1px solid var(--syn-border-soft)",
    background: "var(--syn-structure-panel-bg)",
  },
  categoryLabel: {
    fontSize: "10px",
    fontWeight: 700,
    textTransform: "uppercase",
    letterSpacing: "0.06em",
    opacity: 0.7,
  },
  categoryCount: {
    fontSize: "10px",
    opacity: 0.4,
  },
  categoryItems: {
    display: "flex",
    flexDirection: "column",
    padding: "4px 0",
  },
  categoryItem: {
    padding: "3px 8px",
    fontSize: "12px",
    cursor: "pointer",
    userSelect: "none",
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap",
  },
  categoryItemSelected: {
    background: "var(--syn-selected-bg)",
    fontWeight: 600,
  },
  compactBody: {
    display: "flex",
    flexDirection: "column",
    flex: 1,
    overflowY: "auto",
    minHeight: 0,
  },
  compactSection: {
    display: "flex",
    flexDirection: "column",
  },
  compactHeader: {
    fontSize: "10px",
    fontWeight: 700,
    textTransform: "uppercase",
    letterSpacing: "0.06em",
    opacity: 0.5,
    padding: "6px 12px 3px",
  },
  compactItem: {
    padding: "4px 12px",
    fontSize: "12px",
    cursor: "pointer",
    userSelect: "none",
    borderBottom: "1px solid var(--syn-border-soft)",
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap",
  },
  compactItemSelected: {
    background: "var(--syn-selected-bg)",
    fontWeight: 600,
  },
  compactMore: {
    padding: "2px 12px 6px",
    fontSize: "12px",
    opacity: 0.4,
  },
  empty: {
    flex: 1,
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    opacity: 0.4,
    fontSize: "13px",
  },
};
