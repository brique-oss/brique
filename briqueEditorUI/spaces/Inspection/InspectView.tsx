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

import { useEffect, useState } from "react";
import type { CurrentSemanticFilter, SemanticWindowSelections } from "../Projection/Semantic/contracts.js";
import type {
  InspectionReadState,
  ReflexiveMeaningSections,
} from "./contracts.js";
import { usePreview } from "../Overlay/PreviewContext.js";
import { useOpen } from "../Overlay/OpenContext.js";

export type InspectViewProps = {
  readState: InspectionReadState;
  semanticFilter: CurrentSemanticFilter | null;
  inspectBase: SemanticWindowSelections;
  onRefresh: () => void;
  onActivateContext: (contextPath: string) => void;
  onOpenInNewTab?: (target: { context: string; elementKind: string; elementName: string }) => void;
};

export function InspectView({ readState, semanticFilter, inspectBase, onRefresh, onActivateContext, onOpenInNewTab }: InspectViewProps) {
  const { capability, error, loading, result, target } = readState;
  const { requestPreview } = usePreview();
  const { requestOpen } = useOpen();
  const [openedSections, setOpenedSections] = useState<Set<SectionKey>>(
    () => new Set(sections.map(({ key }) => key))
  );
  const [openedFirstKey, setOpenedFirstKey] = useState<FirstKeyPath>();
  const [openedDeepPaths, setOpenedDeepPaths] = useState<Set<string>>(
    () => new Set()
  );

  useEffect(() => {
    setOpenedDeepPaths(new Set());
  }, [target?.context, target?.elementKind, target?.elementName]);

  if (!target) {
    return <div style={styles.status}>Select an element to inspect.</div>;
  }

  if (loading) {
    return (
      <div style={styles.status}>
        {capability}({target.elementKind}: {target.elementName})
      </div>
    );
  }

  if (error) {
    return (
      <div role="alert" style={styles.status}>
        {error}
      </div>
    );
  }

  if (!result) {
    return <div style={styles.status}>No semantic information available.</div>;
  }

  const address = target.elementKind === "context"
    ? target.context
    : `${target.context}::${target.elementName}`;

  return (
    <div aria-label="Selected element semantic inspection" style={styles.root}>
      <div style={styles.identityHeader}>
        <span
          style={{ ...styles.identityAddress, cursor: "pointer" }}
          onClick={(e) => {
            if (e.metaKey) { onActivateContext(target.context); return; }
            if (e.shiftKey && (target.elementKind === "document" || target.elementKind === "matter")) {
              requestOpen({ elementKind: target.elementKind, elementName: target.elementName, context: target.context });
              return;
            }
            onActivateContext(target.context);
          }}
          onAuxClick={(e) => {
            if (e.button === 1) { e.preventDefault(); onOpenInNewTab?.(target); }
          }}
          onMouseEnter={(e) => {
            if (e.ctrlKey) requestPreview({ elementKind: target.elementKind, elementName: target.elementName, context: target.context, pos: { x: e.clientX, y: e.clientY } });
          }}
          onMouseMove={(e) => {
            if (e.ctrlKey) requestPreview({ elementKind: target.elementKind, elementName: target.elementName, context: target.context, pos: { x: e.clientX, y: e.clientY } });
          }}
          title={address}
        >{address}</span>
        <div style={styles.identityActions}>
          <span style={styles.identityKind}>{target.elementKind}</span>
          <button
            aria-label="Refresh"
            onClick={onRefresh}
            style={styles.refreshButton}
            title="Refresh"
            type="button"
          >
            ↻
          </button>
        </div>
      </div>

      {semanticFilter && semanticFilter.windows.length > 0 && (
        <QualificationSection
          inspectBase={inspectBase}
          semanticFilter={semanticFilter}
        />
      )}

      {sections.map(({ key, label }) => (
        <div key={key} style={styles.section}>
          <div style={styles.sectionHeader}>
            <button
              aria-expanded={openedSections.has(key)}
              aria-label={`${openedSections.has(key) ? "Collapse" : "Expand"} ${label}`}
              onClick={() => {
                setOpenedSections((current) =>
                  updateSet(current, key, !current.has(key))
                );
              }}
              style={styles.iconAction}
              type="button"
            >
              {openedSections.has(key) ? "▼" : "▶"}
            </button>
            <span>{label}</span>
          </div>
          {openedSections.has(key) && (
            <div style={styles.sectionContent}>
              <SemanticValue
                openedDeepPaths={openedDeepPaths}
                openedFirstKey={openedFirstKey}
                path={key}
                section={key}
                setOpenedDeepPaths={setOpenedDeepPaths}
                setOpenedFirstKey={setOpenedFirstKey}
                value={result[key]}
              />
            </div>
          )}
        </div>
      ))}

    </div>
  );
}

function QualificationSection({
  inspectBase,
  semanticFilter,
}: {
  inspectBase: SemanticWindowSelections;
  semanticFilter: CurrentSemanticFilter;
}) {
  const liveByWindowId = new Map(semanticFilter.windows.map((w) => [w.windowId, w]));
  const allWindowIds = new Set([
    ...semanticFilter.windows.map((w) => w.windowId),
    ...Object.keys(inspectBase),
  ]);

  const rows = Array.from(allWindowIds)
    .map((windowId) => {
      const live = liveByWindowId.get(windowId);
      const activePath = live?.activePath ?? windowId.split(".");
      if (activePath.length === 0) return null;
      const valueLevel = live?.valueLevel ?? false;
      const baseKeys = inspectBase[windowId]?.selectedKeys ?? [];
      const liveKeys = live?.selectedKeys ?? [];

      const valueMatched = liveKeys.filter((k) => baseKeys.includes(k));
      const valueWillSet = liveKeys.filter((k) => !baseKeys.includes(k));
      const valueWillRemove = baseKeys.filter((k) => !liveKeys.includes(k));

      return { windowId, activePath, valueLevel, valueMatched, valueWillSet, valueWillRemove };
    })
    .filter((row): row is NonNullable<typeof row> => row !== null)
    .filter(({ valueWillSet, valueWillRemove }) => valueWillSet.length > 0 || valueWillRemove.length > 0);

  if (rows.length === 0) return null;
  const hasEditableDelta = rows.some((row) => row.valueLevel);
  const hasReadOnlyDelta = rows.some((row) => !row.valueLevel);
  const hint = hasEditableDelta
    ? hasReadOnlyDelta
      ? "⌘ Enter applies values only"
      : "⌘ Enter to apply"
    : "No patch will be sent";

  return (
    <div style={qualStyles.section}>
      <div style={qualStyles.sectionHeader}>
        <span style={qualStyles.label}>QualificationChange</span>
        <span style={qualStyles.hint}>{hint}</span>
      </div>
      <div style={qualStyles.body}>
        {hasReadOnlyDelta && (
          <div role="status" style={qualStyles.warning}>
            Only terminal vocabulary values can be edited here. Category selections are read-only and ignored by patch.
          </div>
        )}
        {rows.map(({ windowId, activePath, valueLevel, valueMatched, valueWillSet, valueWillRemove }) => (
          <div key={windowId} style={qualStyles.row}>
            <span style={qualStyles.path}>{activePath.join(".")}</span>
            {valueLevel ? (
              <div style={qualStyles.values}>
                {valueMatched.map((key) => (
                  <span key={key} style={qualStyles.valueMatched}>{key}</span>
                ))}
                {valueWillSet.map((key) => (
                  <span key={key} style={qualStyles.valueWillSet}>{key}</span>
                ))}
                {valueWillRemove.map((key) => (
                  <span key={key} style={qualStyles.valueWillRemove}>{key}</span>
                ))}
              </div>
            ) : (
              <span style={qualStyles.readOnly}>category selection — not a terminal value</span>
            )}
          </div>
        ))}
      </div>
    </div>
  );
}

const sections = [
  { key: "brique", label: "Brique" },
  { key: "objective", label: "Objective" },
  { key: "subjective", label: "Subjective" },
  { key: "functional", label: "Functional" },
] as const;

type SectionKey = keyof ReflexiveMeaningSections;
type FirstKeyPath = { section: SectionKey; key: string } | undefined;
type SemanticValueProps = {
  openedDeepPaths: Set<string>;
  openedFirstKey: FirstKeyPath;
  path: string;
  section: SectionKey;
  setOpenedDeepPaths: React.Dispatch<React.SetStateAction<Set<string>>>;
  setOpenedFirstKey: React.Dispatch<React.SetStateAction<FirstKeyPath>>;
  value: unknown;
};

function SemanticValue(props: SemanticValueProps) {
  const { path, value } = props;

  if (value === undefined) {
    return <div style={styles.empty}>No information</div>;
  }

  if (Array.isArray(value)) {
    if (value.length === 0) return <div style={styles.empty}>Empty list</div>;

    return (
      <div style={styles.group}>
        {value.map((item, index) => (
          <KeyValue
            {...props}
            key={index}
            name={`${index + 1}`}
            path={`${path}.${index}`}
            value={item}
          />
        ))}
      </div>
    );
  }

  if (isRecord(value)) {
    const entries = Object.entries(value);
    if (entries.length === 0) return <div style={styles.empty}>No information</div>;

    return (
      <div style={styles.group}>
        {entries.map(([key, item]) => (
          <KeyValue
            {...props}
            key={key}
            name={key}
            path={`${path}.${key}`}
            value={item}
          />
        ))}
      </div>
    );
  }

  return <span style={styles.value}>{formatPrimitive(value)}</span>;
}

function KeyValue({
  name,
  openedDeepPaths,
  openedFirstKey,
  path,
  section,
  setOpenedDeepPaths,
  setOpenedFirstKey,
  value,
}: SemanticValueProps & { name: string }) {
  const nested = Array.isArray(value) || isRecord(value);

  if (nested) {
    const firstKey = path.split(".").length === 2;
    const open = firstKey
      ? openedFirstKey?.section === section && openedFirstKey.key === name
      : openedDeepPaths.has(path);
    const nestedPaths = collectNestedPathSet(value, path);
    const hasDeeperTree = nestedPaths.size > 0;
    const expandedAll = hasDeeperTree &&
      Array.from(nestedPaths).every((nestedPath) => openedDeepPaths.has(nestedPath));

    return (
      <div style={styles.nestedEntry}>
        <div style={styles.nestedHeader}>
          <button
            aria-expanded={open}
            aria-label={`${open ? "Collapse" : "Expand one level"} ${name}`}
            onClick={() => setKeyOpen({
              firstKey,
              name,
              open: !open,
              path,
              section,
              setOpenedDeepPaths,
              setOpenedFirstKey,
            })}
            style={styles.iconAction}
            title={open ? "Collapse" : "Expand one level"}
            type="button"
          >
            {open ? "▼" : "▶"}
          </button>
          {hasDeeperTree && (
            <button
              aria-label={`${expandedAll ? "Collapse to one level" : "Expand all"} ${name}`}
              onClick={() => {
                const params = {
                  firstKey,
                  name,
                  path,
                  section,
                  setOpenedDeepPaths,
                  setOpenedFirstKey,
                };
                if (expandedAll) openOneLevel(params);
                else openAll({ ...params, value });
              }}
              style={styles.iconAction}
              title={expandedAll ? "Collapse to one level" : "Expand all"}
              type="button"
            >
              ⇊
            </button>
          )}
          <span style={styles.nestedLabel}>
            {name}
          </span>
        </div>
        {open && (
          <div style={styles.nestedContent}>
            <SemanticValue
              openedDeepPaths={openedDeepPaths}
              openedFirstKey={openedFirstKey}
              path={path}
              section={section}
              setOpenedDeepPaths={setOpenedDeepPaths}
              setOpenedFirstKey={setOpenedFirstKey}
              value={value}
            />
          </div>
        )}
      </div>
    );
  }

  return (
    <div style={styles.entry}>
      <span style={styles.key}>{name}:</span>{" "}
      <span style={styles.value}>{formatPrimitive(value)}</span>
    </div>
  );
}

type OpenActionParams = {
  firstKey: boolean;
  name: string;
  path: string;
  section: SectionKey;
  setOpenedDeepPaths: React.Dispatch<React.SetStateAction<Set<string>>>;
  setOpenedFirstKey: React.Dispatch<React.SetStateAction<FirstKeyPath>>;
};

function setKeyOpen(params: OpenActionParams & { open: boolean }) {
  const {
    firstKey,
    name,
    open,
    path,
    section,
    setOpenedDeepPaths,
    setOpenedFirstKey,
  } = params;

  if (firstKey) {
    setOpenedFirstKey(open ? { section, key: name } : undefined);
  } else {
    setOpenedDeepPaths((current) => updateSet(current, path, open));
  }
}

function openOneLevel(params: OpenActionParams) {
  const {
    firstKey,
    name,
    path,
    section,
    setOpenedDeepPaths,
    setOpenedFirstKey,
  } = params;

  if (firstKey) setOpenedFirstKey({ section, key: name });
  else setOpenedDeepPaths((current) => updateSet(current, path, true));

  setOpenedDeepPaths((current) => {
    const next = new Set(current);
    for (const openedPath of next) {
      if (openedPath.startsWith(`${path}.`)) next.delete(openedPath);
    }
    return next;
  });
}

function openAll(params: OpenActionParams & { value: unknown }) {
  openOneLevel(params);
  params.setOpenedDeepPaths((current) => {
    const next = new Set(current);
    collectNestedPaths(params.value, params.path, next);
    return next;
  });
}

function collectNestedPaths(value: unknown, path: string, paths: Set<string>) {
  const entries = Array.isArray(value)
    ? value.map((item, index) => [`${index}`, item] as const)
    : isRecord(value)
      ? Object.entries(value)
      : [];

  for (const [key, item] of entries) {
    if (!Array.isArray(item) && !isRecord(item)) continue;
    const childPath = `${path}.${key}`;
    paths.add(childPath);
    collectNestedPaths(item, childPath, paths);
  }
}

function collectNestedPathSet(value: unknown, path: string): Set<string> {
  const nestedPaths = new Set<string>();
  collectNestedPaths(value, path, nestedPaths);
  return nestedPaths;
}

function updateSet<T>(current: Set<T>, value: T, included: boolean): Set<T> {
  const next = new Set(current);
  if (included) next.add(value);
  else next.delete(value);
  return next;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function formatPrimitive(value: unknown): string {
  if (value === null) return "None";
  if (typeof value === "boolean") return value ? "Yes" : "No";
  return String(value);
}

const qualStyles: Record<string, React.CSSProperties> = {
  readOnly: {
    fontSize: 10,
    color: "var(--syn-text-disabled)",
    fontStyle: "italic",
  },
  section: {
    margin: "0 0 8px",
    border: "1px solid var(--syn-inspection-card-bg)",
    borderRadius: 6,
    background: "var(--syn-inspection-card-bg)",
    overflow: "hidden",
  },
  sectionHeader: {
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between",
    padding: "8px 12px",
    fontWeight: 700,
    userSelect: "none",
  },
  label: {
    fontSize: 12,
  },
  hint: {
    fontSize: 9,
    color: "var(--syn-text-muted)",
    fontStyle: "italic",
    fontWeight: 400,
  },
  body: {
    padding: "0 12px 10px",
    display: "flex",
    flexDirection: "column",
    gap: 6,
  },
  warning: {
    padding: "6px 8px",
    border: "1px solid var(--syn-feedback-warning)",
    borderRadius: 4,
    background: "var(--syn-feedback-warning-bg)",
    color: "var(--syn-feedback-warning)",
    fontSize: 10,
    lineHeight: 1.35,
  },
  row: {
    display: "flex",
    flexDirection: "column",
    gap: 4,
  },
  path: {
    fontSize: 10,
    color: "var(--syn-text-muted)",
    fontFamily: "monospace",
    letterSpacing: "0.04em",
  },
  values: {
    display: "flex",
    flexWrap: "wrap",
    gap: 4,
  },
  // already present in meaning AND in filter — no change
  valueMatched: {
    fontSize: 10,
    padding: "2px 7px",
    borderRadius: 3,
    background: "var(--syn-selected-bg)",
    color: "var(--syn-text-primary)",
    fontWeight: 700,
    border: "1px solid var(--syn-selected-border)",
  },
  // in filter but not yet in meaning — will be added
  valueWillSet: {
    fontSize: 10,
    padding: "2px 7px",
    borderRadius: 3,
    background: "transparent",
    color: "var(--syn-selected-border)",
    fontWeight: 600,
    border: "1px dashed var(--syn-selected-border)",
  },
  // in meaning but not in filter — will be removed
  valueWillRemove: {
    fontSize: 10,
    padding: "2px 7px",
    borderRadius: 3,
    background: "var(--syn-feedback-error-bg)",
    color: "var(--syn-feedback-error)",
    border: "1px solid var(--syn-feedback-error)",
    textDecoration: "line-through",
  },
};

const styles: Record<string, React.CSSProperties> = {
  root: {
    flex: 1,
    minWidth: 0,
    minHeight: 0,
    padding: "8px",
    boxSizing: "border-box",
    overflow: "auto",
  },
  identityHeader: {
    display: "flex",
    alignItems: "center",
    gap: 6,
    padding: "6px 8px 6px 10px",
    marginBottom: 6,
    borderBottom: "1px solid var(--syn-border-soft)",
    fontFamily: "monospace",
    fontSize: 11,
  },
  identityAddress: {
    flex: 1,
    minWidth: 0,
    color: "var(--syn-text-primary)",
    fontSize: 10,
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap",
  },
  identityActions: {
    flexShrink: 0,
    display: "flex",
    alignItems: "center",
    gap: 5,
  },
  identityKind: {
    flexShrink: 0,
    color: "var(--syn-nav-title)",
    fontWeight: 700,
    fontSize: 9,
    textTransform: "uppercase",
    letterSpacing: "0.06em",
    padding: "1px 5px",
    border: "1px solid var(--syn-border-medium)",
    borderRadius: 3,
  },
  refreshButton: {
    flexShrink: 0,
    background: "transparent",
    border: "none",
    color: "var(--syn-text-muted)",
    cursor: "pointer",
    fontSize: 14,
    lineHeight: 1,
    padding: "0 2px",
    appearance: "none",
  },
  status: {
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    flex: 1,
    padding: "16px",
    textAlign: "center",
    opacity: 0.7,
  },
  section: {
    marginBottom: "8px",
    border: "1px solid var(--syn-border-medium)",
    borderRadius: "6px",
    background: "var(--syn-inspection-card-bg)",
    overflow: "hidden",
  },
  sectionHeader: {
    display: "flex",
    alignItems: "center",
    gap: "4px",
    padding: "10px 12px",
    fontWeight: 700,
    userSelect: "none",
  },
  sectionContent: {
    padding: "4px 12px 12px",
  },
  group: {
    display: "block",
  },
  entry: {
    padding: "3px 0",
    lineHeight: 1.5,
  },
  key: {
    color: "var(--syn-text-secondary)",
    overflowWrap: "anywhere",
  },
  value: {
    color: "var(--syn-text-primary)",
    whiteSpace: "pre-wrap",
    overflowWrap: "anywhere",
  },
  nestedEntry: {
    padding: "3px 0",
  },
  nestedHeader: {
    display: "flex",
    alignItems: "center",
    gap: "3px",
    minWidth: 0,
  },
  nestedLabel: {
    minWidth: 0,
    color: "var(--syn-text-secondary)",
    lineHeight: 1.5,
    overflowWrap: "anywhere",
  },
  iconAction: {
    width: "auto",
    height: "auto",
    flex: "0 0 auto",
    border: 0,
    padding: 0,
    background: "transparent",
    color: "var(--syn-text-secondary)",
    cursor: "pointer",
    font: "inherit",
    lineHeight: 1,
    appearance: "none",
  },
  nestedContent: {
    marginLeft: "10px",
    paddingLeft: "10px",
    borderLeft: "1px solid var(--syn-border-soft)",
  },
  empty: {
    color: "var(--syn-text-muted)",
    fontStyle: "italic",
  },
};
