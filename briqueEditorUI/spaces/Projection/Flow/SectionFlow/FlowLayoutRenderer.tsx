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

import React from "react";
import type {
  FlowDataObject,
  FlowDataValue,
  FlowNode,
} from "../../../../Brique_Substrate/projection/Flow/flowProjectionModel.js";
import {
  flowDataGet,
  isFlowDataArray,
  isFlowDataObject,
} from "../../../../Brique_Substrate/projection/Flow/flowData.js";
import { useOpen } from "../../../Overlay/OpenContext.js";
import { usePreview } from "../../../Overlay/PreviewContext.js";
import type { FlowCapacityTarget } from "../../contracts.js";
import type { FlowExpansion } from "./contracts.js";
import type { FlowLayoutBox } from "./flowLayoutModel.js";
import {
  BRANCH_LABEL_HEIGHT,
  CONTRACT_MAX_WIDTH,
  FRAME_PADDING_BOTTOM,
  FRAME_PADDING_TOP,
  FRAME_PADDING_X,
  GAP_X,
  GAP_Y,
} from "./flowLayoutModel.js";

export type FlowBriqueRefCallbacks = {
  onInspectElement: (contextPath: string, elementKind: string, elementName: string) => void;
  onActivateContext: (contextPath: string) => void;
  onFlowProjectCapacity?: (contextPath: string, capacityName: string) => void;
  onOpenInNewTab?: (contextPath: string, elementKind: string, elementName: string) => void;
};

export type FlowLayoutRendererProps = {
  layout: FlowLayoutBox;
  sectionExpansions: Record<string, FlowExpansion>;
  onFlowCapacityTargetChange: (target: FlowCapacityTarget) => void;
  onSectionExpansionChange: (nodeId: string, expansion: FlowExpansion) => void;
  briqueRefCallbacks?: FlowBriqueRefCallbacks;
  onRefresh?: () => void;
};

export function FlowLayoutRenderer({
  layout,
  sectionExpansions,
  onFlowCapacityTargetChange,
  onSectionExpansionChange,
  briqueRefCallbacks,
  onRefresh,
}: FlowLayoutRendererProps) {
  return (
    <BoxView
      box={layout}
      sectionExpansions={sectionExpansions}
      onFlowCapacityTargetChange={onFlowCapacityTargetChange}
      onSectionExpansionChange={onSectionExpansionChange}
      briqueRefCallbacks={briqueRefCallbacks}
      onRefresh={onRefresh}
    />
  );
}

type Callbacks = {
  sectionExpansions: Record<string, FlowExpansion>;
  onFlowCapacityTargetChange: (target: FlowCapacityTarget) => void;
  onSectionExpansionChange: (nodeId: string, expansion: FlowExpansion) => void;
  briqueRefCallbacks?: FlowBriqueRefCallbacks;
  branchColor?: string;
  onRefresh?: () => void;
};

function BoxView({ box, ...callbacks }: { box: FlowLayoutBox } & Callbacks) {
  // Capacity — invisible root, children centered in a column
  if (box.kind === "capacity") {
    return (
      <div style={styles.capacity}>
        {box.children.map((child, i) => (
          <BoxView key={child.id} box={child} {...callbacks} onRefresh={i === 0 ? callbacks.onRefresh : undefined} />
        ))}
      </div>
    );
  }

  // Sequence — vertical frame, no label, arrows between children touch the blocks
  if (box.kind === "sequence") {
    return (
      <div style={{ ...styles.frame, ...frameStyle("sequence"), display: "flex", flexDirection: "column", alignItems: "center" }}>
        {box.children.map((child, i) => (
          <React.Fragment key={child.id}>
            {i > 0 && <div style={styles.sequenceArrow}>↓</div>}
            <BoxView box={child} {...callbacks} />
          </React.Fragment>
        ))}
      </div>
    );
  }

  // Section — border + header + optional children
  if (box.kind === "section") {
    return (
      <div style={{ ...styles.frame, ...frameStyle("section"), ...styles.verticalFrame }}>
        <SectionHeader box={box} onSectionExpansionChange={callbacks.onSectionExpansionChange} onRefresh={callbacks.onRefresh} />
        {box.children.map((child) => (
          <BoxView key={child.id} box={child} {...callbacks} onRefresh={undefined} />
        ))}
      </div>
    );
  }

  // Resolution — collapsible vertical frame
  if (box.kind === "resolution") {
    const expanded = (callbacks.sectionExpansions[box.id] ?? "oneLevel") !== "collapsed";
    return (
      <div style={{ ...styles.frame, ...frameStyle(box.kind), ...styles.verticalFrame }}>
        <CollapsibleLabel label={box.label ?? box.kind} expanded={expanded} boxId={box.id} onSectionExpansionChange={callbacks.onSectionExpansionChange} />
        {expanded && box.children.map((child) => (
          <BoxView key={child.id} box={child} {...callbacks} />
        ))}
      </div>
    );
  }

  // For each — collapsible, no branch label for body
  if (box.kind === "for_each") {
    const expanded = (callbacks.sectionExpansions[box.id] ?? "oneLevel") !== "collapsed";
    return (
      <div style={{ ...styles.frame, ...frameStyle(box.kind), ...styles.verticalFrame }}>
        <CollapsibleLabel label={box.label ?? box.kind} expanded={expanded} boxId={box.id} onSectionExpansionChange={callbacks.onSectionExpansionChange} />
        {expanded && box.children.map((child) => (
          <BoxView key={child.id} box={child} {...callbacks} branchColor={undefined} />
        ))}
      </div>
    );
  }

  // Branch — inherits color from parent (if/switch/parallel)
  if (box.kind === "branch") {
    const expanded = (callbacks.sectionExpansions[box.id] ?? "oneLevel") !== "collapsed";
    const color = callbacks.branchColor;
    const branchStyle: React.CSSProperties = color
      ? { border: `1px solid ${color}40`, background: "rgba(255,255,255,0.02)", color }
      : frameStyle("branch");
    return (
      <div style={{ ...styles.frame, ...branchStyle, ...styles.verticalFrame }}>
        <CollapsibleLabel label={box.label ?? "branch"} expanded={expanded} boxId={box.id} onSectionExpansionChange={callbacks.onSectionExpansionChange} color={color} />
        {expanded && box.children.map((child) => (
          <BoxView key={child.id} box={child} {...callbacks} branchColor={undefined} />
        ))}
      </div>
    );
  }

  // Parallel / if / switch — collapsible, horizontal branches
  if (box.kind === "parallel" || box.kind === "if" || box.kind === "switch") {
    const expanded = (callbacks.sectionExpansions[box.id] ?? "oneLevel") !== "collapsed";
    const tokens = frameTokens(box.kind);
    return (
      <div style={{ ...styles.frame, ...frameStyle(box.kind), display: "flex", flexDirection: "column", alignItems: "center" }}>
        <CollapsibleLabel label={box.label ?? box.kind} expanded={expanded} boxId={box.id} onSectionExpansionChange={callbacks.onSectionExpansionChange} />
        {expanded && (
          <div style={styles.horizontalChildren}>
            {box.children.map((child) => (
              <BoxView key={child.id} box={child} {...callbacks} branchColor={tokens.text} />
            ))}
          </div>
        )}
      </div>
    );
  }

  // Action
  if (box.kind === "action") {
    return (
      <div style={{ ...styles.leaf, ...leafStyle("action") }}>
        <ActionContent
          node={box.node}
          onFlowCapacityTargetChange={callbacks.onFlowCapacityTargetChange}
          briqueRefCallbacks={callbacks.briqueRefCallbacks}
        />
      </div>
    );
  }

  // Contract
  if (box.kind === "contract") {
    return (
      <div style={{ ...styles.leaf, ...leafStyle("contract") }}>
        <ContractContent
          node={box.node}
          briqueRefCallbacks={callbacks.briqueRefCallbacks}
        />
      </div>
    );
  }

  // Reference / empty
  return (
    <div style={{ ...styles.leaf, ...leafStyle("action") }}>
      <span style={styles.keyword}>{box.label ?? box.kind}</span>
    </div>
  );
}

function CollapsibleLabel({
  label,
  expanded,
  boxId,
  onSectionExpansionChange,
  color,
}: {
  label: string;
  expanded: boolean;
  boxId: string;
  onSectionExpansionChange: (nodeId: string, expansion: FlowExpansion) => void;
  color?: string;
}) {
  const labelStyle: React.CSSProperties = color ? { ...styles.frameLabel, color } : styles.frameLabel;
  return (
    <div style={{ display: "flex", alignItems: "center", gap: 4, color: color ?? "inherit" }}>
      <button
        onClick={() => onSectionExpansionChange(boxId, expanded ? "collapsed" : "oneLevel")}
        style={styles.sectionIconButton}
        type="button"
        title={expanded ? "Collapse" : "Expand"}
      >
        {expanded ? "▼" : "▶"}
      </button>
      <span style={labelStyle}>{label}</span>
    </div>
  );
}

function SectionHeader({
  box,
  onSectionExpansionChange,
  onRefresh,
}: {
  box: FlowLayoutBox;
  onSectionExpansionChange: (nodeId: string, expansion: FlowExpansion) => void;
  onRefresh?: () => void;
}) {
  const section = readObject(box.node?.data.entries[0]?.value);
  const transformation = readObject(flowDataGet(section, "transformation_contract"));
  const role = readString(flowDataGet(section, "role"));
  const morphing = readString(flowDataGet(transformation, "morphing"));
  const expanded = (box.expansion ?? "collapsed") !== "collapsed";

  return (
    <div style={styles.sectionContent}>
      <div style={styles.sectionHeader}>
        <button
          aria-label={expanded ? `Collapse ${box.label}` : `Expand ${box.label}`}
          onClick={() => {
            if (box.node) onSectionExpansionChange(box.node.id, expanded ? "collapsed" : "oneLevel");
          }}
          style={styles.sectionIconButton}
          title={expanded ? "Collapse" : "Expand"}
          type="button"
        >
          {expanded ? "▼" : "▶"}
        </button>
        <div style={styles.sectionName}>{box.label ?? "section"}</div>
        {onRefresh && (
          <button
            onClick={onRefresh}
            style={styles.sectionRefreshButton}
            title="Reload flow"
            type="button"
          >
            ↻
          </button>
        )}
      </div>
      {!expanded && role && <div style={styles.sectionRole}>{role}</div>}
      {!expanded && morphing && (
        <div style={styles.sectionMorphing}>
          <span style={styles.sectionMetaLabel}>Morphing</span>
          {morphing}
        </div>
      )}
    </div>
  );
}

function ActionContent({
  node,
  onFlowCapacityTargetChange,
  briqueRefCallbacks,
}: {
  node: FlowNode | undefined;
  onFlowCapacityTargetChange: (target: FlowCapacityTarget) => void;
  briqueRefCallbacks?: FlowBriqueRefCallbacks;
}) {
  // output node
  if (node?.subtype === "output") {
    const outputBlock = readObject(flowDataGet(node.data, "output"));
    return (
      <div data-flow-leaf style={styles.leafButton}>
        <div style={{ display: "flex", alignItems: "center", gap: 6, flexWrap: "wrap" }}>
          <span style={{ ...styles.keyword, color: "#86efac", fontSize: 10, fontWeight: 600 }}>output</span>
        </div>
        {outputBlock && (
          <div style={styles.withBlock}>
            <div style={styles.withRefs}>
              {outputBlock.entries.map(({ key, value: v }) => {
                const val = typeof v === "string" ? v : null;
                return (
                  <div key={key} style={{ display: "flex", alignItems: "center", gap: 4 }}>
                    <span style={{ fontSize: 9, color: "#64748b", fontWeight: 700 }}>{key}</span>
                    <span style={{ fontSize: 9, color: "#64748b" }}>←</span>
                    {val
                      ? <span style={styles.asChip}>{val}</span>
                      : <span style={{ fontSize: 9, color: "#94a3b8" }}>—</span>
                    }
                  </div>
                );
              })}
            </div>
          </div>
        )}
      </div>
    );
  }

  const invoke = node?.subtype === "invoke"
    ? readObject(flowDataGet(node.data, "invoke"))
    : undefined;
  const reference = readString(flowDataGet(invoke, "@capacity"));
  const capacityRef: BriqueRef | null = reference
    ? { kind: "capacity", value: reference }
    : null;

  // For non-invoke actions, keep the old button behaviour
  if (!invoke) {
    return (
      <div data-flow-leaf style={styles.leafButton}>
        <span style={styles.keyword}>{node ? elementTitle(node) : "action"}</span>
        {node && <ElementDetails node={node} />}
      </div>
    );
  }

  const asAlias = readString(flowDataGet(invoke, "as"));
  const withBlock = readObject(flowDataGet(invoke, "with"));
  const withRefs: BriqueRef[] = [];
  if (withBlock) {
    for (const { key, value: v } of withBlock.entries) {
      // Direct string refs: @schema, @structure, @document, @capacity as slot
      const directRef = detectBriqueRef(key, v, undefined);
      if (directRef) { withRefs.push(directRef); continue; }
      // Named-map refs: @matter { name: "/path" }, @capacity { name: "/path" }
      const obj = readObject(v);
      if (obj && (key === "@matter" || key === "@schema" || key === "@structure" || key === "@document" || key === "@capacity")) {
        const kind = key.slice(1) as BriqueRefKind;
        for (const { value: inner } of obj.entries) {
          const s = typeof inner === "string" ? inner : undefined;
          if (s && s.startsWith("/")) withRefs.push({ kind, value: s });
        }
      }
    }
  }

  const callbacks = briqueRefCallbacks ?? {
    onInspectElement: () => undefined,
    onActivateContext: () => {
      const target = capacityTargetFromReference(reference!);
      if (target) onFlowCapacityTargetChange(target);
    },
  };

  return (
    <div data-flow-leaf style={styles.leafButton}>
      <div style={{ display: "flex", alignItems: "center", gap: 6, flexWrap: "wrap" }}>
        <span style={{ ...styles.keyword, color: "#64748b", fontSize: 10, fontWeight: 600 }}>invoke</span>
        {capacityRef ? (
          <BriqueToken ref={capacityRef} briqueRefCallbacks={callbacks} />
        ) : (
          <span style={{ ...styles.keyword, opacity: 0.4 }}>—</span>
        )}
        {asAlias && (
          <>
            <span style={{ fontSize: 9, color: "#64748b", fontWeight: 700 }}>as</span>
            <span style={styles.asChip}>{asAlias}</span>
          </>
        )}
      </div>
      {withRefs.length > 0 && (
        <div style={styles.withBlock}>
          <span style={styles.withLabel}>with</span>
          <div style={styles.withRefs}>
            {withRefs.map((r, i) => (
              <BriqueToken key={i} ref={r} briqueRefCallbacks={callbacks} />
            ))}
          </div>
        </div>
      )}
      {node && <ElementDetails node={node} />}
    </div>
  );
}

function ContractContent({
  node,
  briqueRefCallbacks,
}: {
  node: FlowNode | undefined;
  briqueRefCallbacks?: FlowBriqueRefCallbacks;
}) {
  const sectionValue = readObject(node?.data.entries[0]?.value);

  return (
    <div style={{ width: CONTRACT_MAX_WIDTH, padding: "10px 14px", boxSizing: "border-box" }}>
      <div style={{ ...styles.sectionHeader, marginBottom: sectionValue ? 10 : 0 }}>
        <span style={{ fontSize: 11, color: "#7dd3fc", fontWeight: 500 }}>contract</span>
      </div>
      {sectionValue && (
        <div style={{ display: "grid", gap: 6 }}>
          {sectionValue.entries
            .filter(({ key }) => key !== "resolution")
            .map(({ key, value }) => (
              <DslEntry key={key} entryKey={key} value={value} depth={0} briqueRefCallbacks={briqueRefCallbacks} />
            ))}
        </div>
      )}
    </div>
  );
}

const DSL_DEPTH_STYLES: React.CSSProperties[] = [
  // depth 0 — inputs, outputs, effects
  { fontSize: 13, fontWeight: 900, letterSpacing: "0.05em", textTransform: "uppercase", color: "#fde68a", lineHeight: 1.4 },
  // depth 1 — params, payload, workflow_contexts
  { fontSize: 11, fontWeight: 700, color: "#fbbf24", lineHeight: 1.4 },
  // depth 2 — publish, result, observations
  { fontSize: 10, fontWeight: 500, color: "#fed7aa", lineHeight: 1.4 },
  // depth 3+ — type, required, schema...
  { fontSize: 9, fontWeight: 400, color: "#fef3c7", opacity: 0.75, lineHeight: 1.4 },
];

function dslKeyStyle(depth: number): React.CSSProperties {
  return DSL_DEPTH_STYLES[Math.min(depth, DSL_DEPTH_STYLES.length - 1)];
}

function DslEntry({
  entryKey,
  value,
  depth,
  ancestorKey,
  briqueRefCallbacks,
}: {
  entryKey: string;
  value: FlowDataValue;
  depth: number;
  ancestorKey?: string;
  briqueRefCallbacks?: FlowBriqueRefCallbacks;
}) {
  const indent = depth * 14;
  const keyStyle = dslKeyStyle(depth);
  const obj = readObject(value);

  // The ancestor key for children: if this key starts with @, propagate it
  const childAncestorKey = entryKey.startsWith("@") ? entryKey : ancestorKey;

  if (obj) {
    return (
      <div style={{ display: "grid", gap: depth === 0 ? 5 : 3, marginTop: depth === 0 ? 4 : 0 }}>
        <div style={{ paddingLeft: indent }}>
          <span style={keyStyle}>{entryKey}</span>
        </div>
        {obj.entries.map(({ key, value: v }) => (
          <DslEntry key={key} entryKey={key} value={v} depth={depth + 1} ancestorKey={childAncestorKey} briqueRefCallbacks={briqueRefCallbacks} />
        ))}
      </div>
    );
  }

  if (isFlowDataArray(value)) {
    return (
      <div style={{ display: "grid", gap: 3, marginTop: depth === 0 ? 4 : 0 }}>
        <div style={{ paddingLeft: indent }}>
          <span style={keyStyle}>{entryKey}</span>
        </div>
        {value.items.map((item, i) => (
          <div key={i} style={{ paddingLeft: indent + 14, fontSize: 9, lineHeight: 1.4, color: "#fff7ed", wordBreak: "break-word" }}>
            {typeof item === "string"
              ? (() => {
                  const ref = detectBriqueRef(entryKey, item, childAncestorKey);
                  return ref
                    ? <BriqueToken ref={ref} briqueRefCallbacks={briqueRefCallbacks} />
                    : <span>{item}</span>;
                })()
              : formatValue(item)}
          </div>
        ))}
      </div>
    );
  }

  // Scalar — key + value on same line
  const briqueRef = detectBriqueRef(entryKey, value, ancestorKey);
  return (
    <div style={{ paddingLeft: indent, display: "flex", gap: 8, alignItems: "baseline", flexWrap: "wrap", marginTop: depth === 0 ? 4 : 0 }}>
      <span style={keyStyle}>{entryKey}</span>
      {briqueRef
        ? <BriqueToken ref={briqueRef} briqueRefCallbacks={briqueRefCallbacks} />
        : <span style={{ ...detailValueStyle(value), fontSize: 9, lineHeight: 1.4, wordBreak: "break-word", opacity: 0.9 }}>
            {formatValue(value)}
          </span>
      }
    </div>
  );
}

// ---------------------------------------------------------------------------
// BriqueToken — styled chip for Brique references
// ---------------------------------------------------------------------------

const BRIQUE_TOKEN_COLORS: Record<BriqueRefKind, { bg: string; border: string; color: string }> = {
  capacity:         { bg: "rgba(147,197,253,0.12)", border: "rgba(147,197,253,0.5)", color: "#93c5fd" },
  matter:           { bg: "rgba(167,139,250,0.12)", border: "rgba(167,139,250,0.5)", color: "#c4b5fd" },
  schema:           { bg: "rgba(52,211,153,0.12)",  border: "rgba(52,211,153,0.5)",  color: "#6ee7b7" },
  structure:        { bg: "rgba(251,146,60,0.12)",  border: "rgba(251,146,60,0.5)",  color: "#fb923c" },
  document:         { bg: "rgba(248,113,113,0.12)", border: "rgba(248,113,113,0.5)", color: "#f87171" },
  "runtime-input":  { bg: "rgba(251,191,36,0.08)",  border: "rgba(251,191,36,0.35)",  color: "#fcd34d" },
  "runtime-alias":  { bg: "rgba(196,181,253,0.08)", border: "rgba(196,181,253,0.35)", color: "#c4b5fd" },
};

function BriqueToken({
  ref: briqueRef,
  briqueRefCallbacks,
}: {
  ref: BriqueRef;
  briqueRefCallbacks?: FlowBriqueRefCallbacks;
}) {
  const { requestPreview } = usePreview();
  const { requestOpen } = useOpen();
  const colors = BRIQUE_TOKEN_COLORS[briqueRef.kind];
  const navigable = briqueRef.kind !== "runtime-input" && briqueRef.kind !== "runtime-alias";
  const interactive = navigable && Boolean(briqueRefCallbacks);
  const lastPreviewRef = React.useRef<string>("");

  function handleClick(e: React.MouseEvent) {
    if (!interactive || !briqueRefCallbacks) return;
    e.stopPropagation();
    const parsed = parseRefContextAndName(briqueRef);
    if (!parsed.context || !parsed.name) return;
    const { context, name } = parsed;
    const elementKind = refKindToElementKind(briqueRef.kind);

    if (e.metaKey) {
      briqueRefCallbacks.onActivateContext(context);
      return;
    }
    if (e.shiftKey && (elementKind === "matter" || elementKind === "document")) {
      requestOpen({ elementKind, elementName: name, context });
      return;
    }
    // Click on capacity: show in Flow Projection if handler available, else Inspect
    if (elementKind === "capacity") {
      if (briqueRefCallbacks.onFlowProjectCapacity) {
        briqueRefCallbacks.onFlowProjectCapacity(context, name);
      } else {
        briqueRefCallbacks.onInspectElement(context, elementKind, name);
      }
      return;
    }
    briqueRefCallbacks.onInspectElement(context, elementKind, name);
  }

  // Ctrl+hover — preview overlay (mirrors BrowseView/SearchView behaviour)
  // Both mouseenter and mousemove so Ctrl held before entering still triggers.
  // Deduplicate only while the mouse is over the same token (reset on mouseleave).
  function handleCtrlHover(e: React.MouseEvent) {
    if (!interactive || !briqueRefCallbacks || !e.ctrlKey) return;
    const parsed = parseRefContextAndName(briqueRef);
    if (!parsed.context || !parsed.name) return;
    const dedupeKey = `${parsed.context}|${parsed.name}`;
    // Allow re-trigger on mouseenter even if same target (preview may have been dismissed)
    if (e.type === "mousemove" && lastPreviewRef.current === dedupeKey) return;
    lastPreviewRef.current = dedupeKey;
    const elementKind = refKindToElementKind(briqueRef.kind);
    requestPreview({ elementKind, elementName: parsed.name, context: parsed.context, pos: { x: e.clientX, y: e.clientY } });
  }

  function handleAuxClick(e: React.MouseEvent) {
    if (!interactive || !briqueRefCallbacks || e.button !== 1) return;
    e.preventDefault();
    e.stopPropagation();
    const parsed = parseRefContextAndName(briqueRef);
    if (!parsed.context || !parsed.name) return;
    const elementKind = refKindToElementKind(briqueRef.kind);
    briqueRefCallbacks.onOpenInNewTab?.(parsed.context, elementKind, parsed.name);
  }

  // Stop pointer propagation so the viewport pan doesn't start on token click
  function handlePointerDown(e: React.PointerEvent) {
    if (interactive) e.stopPropagation();
  }

  return (
    <span
      data-brique-ref={interactive ? briqueRef.kind : undefined}
      title={`${briqueRef.kind}: ${briqueRef.value}`}
      onClick={interactive ? handleClick : undefined}
      onAuxClick={interactive ? handleAuxClick : undefined}
      onMouseEnter={interactive ? handleCtrlHover : undefined}
      onMouseMove={interactive ? handleCtrlHover : undefined}
      onMouseLeave={interactive ? () => { lastPreviewRef.current = ""; } : undefined}
      onPointerDown={interactive ? handlePointerDown : undefined}
      style={{
        display: "inline-block",
        padding: "1px 6px",
        borderRadius: 4,
        border: `1px solid ${colors.border}`,
        background: colors.bg,
        color: colors.color,
        fontSize: 9,
        fontFamily: "monospace",
        fontWeight: 600,
        lineHeight: 1.5,
        letterSpacing: "0.02em",
        whiteSpace: "nowrap",
        cursor: interactive ? "pointer" : "default",
        userSelect: "none",
      }}
    >
      {briqueRef.value}
    </span>
  );
}

function parseRefContextAndName(ref: BriqueRef): { context: string; name: string } | { context: undefined; name: undefined } {
  const path = ref.value.replace(/^\/+/, "");
  const parts = path.split("/").filter(Boolean);
  if (parts.length < 2) return { context: undefined, name: undefined };
  const name = parts.pop()!;
  // For typed element refs the path is /ctx/<namespace>/name — strip the namespace segment
  // so the context points to the actual Brique context directory, not the sub-folder.
  const namespaceByKind: Partial<Record<BriqueRefKind, string>> = {
    schema: "schema",
    structure: "structure",
    document: "document",
  };
  const expectedNs = namespaceByKind[ref.kind];
  if (expectedNs && parts[parts.length - 1] === expectedNs) {
    parts.pop();
  }
  if (parts.length === 0) return { context: undefined, name: undefined };
  return { context: "/" + parts.join("/"), name };
}

function refKindToElementKind(kind: BriqueRefKind): string {
  switch (kind) {
    case "capacity":  return "capacity";
    case "matter":    return "matter";
    case "schema":    return "schema";
    case "structure": return "structure";
    case "document":  return "document";
    default:          return kind;
  }
}

function ElementDetails({ node }: { node: FlowNode }) {
  const value = readObject(node.data.entries[0]?.value);
  if (!value) return null;
  const entries = value.entries.filter(({ key }) => !key.startsWith(">") && key !== "@capacity" && key !== "with" && key !== "as");
  if (entries.length === 0) return null;
  return (
    <span style={styles.details}>
      {entries.map(({ key, value: item }) => (
        <span key={key}>
          <span style={styles.qualifier}>{key}</span>{" "}
          <span style={detailValueStyle(item)}>{formatValue(item)}</span>
        </span>
      ))}
    </span>
  );
}

function elementTitle(node: FlowNode): string {
  if (node.subtype === "invoke") {
    // Label handled directly in ActionContent — just return "invoke" as fallback
    return "invoke";
  }
  if (node.subtype === "if") return `if ${conditionLabel(node.data)}`;
  if (node.subtype === "switch") return `switch ${conditionLabel(node.data)}`;
  if (node.subtype === "for_each") return `for each ${conditionLabel(node.data)}`;
  if (node.subtype === "section_reference") return node.data.entries[0]?.key ?? "section";
  return node.subtype ?? node.type;
}

function conditionLabel(data: FlowDataObject): string {
  const value = readObject(data.entries[0]?.value);
  const condition = flowDataGet(value, "when") ?? flowDataGet(value, "on") ?? flowDataGet(value, "items");
  return formatValue(condition ?? value ?? "");
}

function capacityTargetFromReference(reference: string): FlowCapacityTarget | undefined {
  const parts = reference.replace(/^\/+/, "").split("/").filter(Boolean);
  const capacityName = parts.pop();
  if (!capacityName || parts.length === 0) return undefined;
  return { context: "/" + parts.join("/"), capacityName };
}

// ---------------------------------------------------------------------------
// Brique reference detection
// ---------------------------------------------------------------------------

export type BriqueRefKind =
  | "capacity"       // @capacity key or absolute /ctx/cap value under @capacity
  | "matter"         // value under @matter slot (absolute path)
  | "schema"         // value of a "schema" key (absolute path)
  | "structure"      // @structure key (absolute path)
  | "document"       // @document key (absolute path)
  | "runtime-input"  // $input.* — runtime ref, styled only
  | "runtime-alias"; // $alias.* — runtime ref, styled only

export type BriqueRef = {
  kind: BriqueRefKind;
  value: string;
};

/**
 * Detect whether a scalar value in a DSL entry is a Brique reference.
 * @param entryKey   the JSON key of this entry
 * @param value      the scalar string value
 * @param ancestorKey the nearest ancestor key that carries semantic type info
 *                   (e.g. "@matter" when iterating inside an @matter object)
 */
export function detectBriqueRef(
  entryKey: string,
  value: unknown,
  ancestorKey?: string
): BriqueRef | null {
  if (typeof value !== "string" || value === "") return null;

  // Runtime refs — always detected by value prefix
  if (value.startsWith("$input.") || value === "$input") {
    return { kind: "runtime-input", value };
  }
  if (value.startsWith("$")) {
    return { kind: "runtime-alias", value };
  }

  // @capacity key
  if (entryKey === "@capacity") {
    return { kind: "capacity", value };
  }

  // schema / @schema key — value is an absolute path to a schema
  if ((entryKey === "schema" || entryKey === "@schema") && value.startsWith("/")) {
    return { kind: "schema", value };
  }

  // @structure key — value is an absolute path to a structure
  if (entryKey === "@structure" && value.startsWith("/")) {
    return { kind: "structure", value };
  }

  // @document key — value is an absolute path to a document
  if (entryKey === "@document" && value.startsWith("/")) {
    return { kind: "document", value };
  }

  // matter_ref key — explicit matter ref in outputs/@matter slots
  if (entryKey === "matter_ref" && value.startsWith("/")) {
    return { kind: "matter", value };
  }

  // Values nested under an @matter ancestor
  if (ancestorKey === "@matter" && value.startsWith("/")) {
    return { kind: "matter", value };
  }

  return null;
}

function formatValue(value: FlowDataValue): string {
  if (typeof value === "string") return value;
  if (typeof value === "number" || typeof value === "boolean") return String(value);
  if (value === null) return "null";
  if (isFlowDataArray(value)) return value.items.map(formatValue).join(", ");
  if (isFlowDataObject(value)) {
    return value.entries.map(({ key, value: v }) => `${key} ${formatValue(v)}`).join(" ");
  }
  return "";
}

function detailValueStyle(value: FlowDataValue): React.CSSProperties {
  const text = formatValue(value);
  return {
    color: text.includes("$") ? "#c4b5fd" : text.includes("@") ? "#93c5fd" : "#fff7ed",
  };
}

function readObject(value: FlowDataValue | undefined): FlowDataObject | undefined {
  return isFlowDataObject(value) ? value : undefined;
}

function readString(value: FlowDataValue | undefined): string | undefined {
  return typeof value === "string" ? value : undefined;
}

function frameStyle(kind: FlowLayoutBox["kind"]): React.CSSProperties {
  const { border, bg, text } = frameTokens(kind);
  return { background: bg, border: `1px solid ${border}`, color: text };
}

function leafStyle(kind: FlowLayoutBox["kind"]): React.CSSProperties {
  if (kind === "contract") {
    return { background: "#0d1f2d", border: "1px solid rgba(56,189,248,0.4)", color: "#7dd3fc" };
  }
  return { background: "#111827", border: "1px solid rgba(99,130,180,0.5)", color: "#e2e8f0" };
}

function frameTokens(kind: FlowLayoutBox["kind"]): { border: string; bg: string; text: string } {
  switch (kind) {
    case "section":
      return { border: "rgba(250,204,21,0.55)",  bg: "rgba(250,204,21,0.05)", text: "#fde68a" };
    case "resolution":
      return { border: "rgba(251,191,36,0.4)",   bg: "rgba(251,191,36,0.04)", text: "#fcd34d" };
    case "sequence":
      return { border: "rgba(100,116,139,0.25)", bg: "rgba(15,17,23,0.0)",    text: "#94a3b8" };
    case "parallel":
      return { border: "rgba(34,197,94,0.8)",    bg: "rgba(34,197,94,0.06)",  text: "#86efac" };
    case "if":
      return { border: "rgba(96,165,250,0.8)",   bg: "rgba(96,165,250,0.06)", text: "#93c5fd" };
    case "switch":
      return { border: "rgba(168,85,247,0.8)",   bg: "rgba(168,85,247,0.06)", text: "#d8b4fe" };
    case "for_each":
      return { border: "rgba(244,114,182,0.8)",  bg: "rgba(244,114,182,0.06)",text: "#f9a8d4" };
    case "branch":
      return { border: "rgba(100,116,139,0.25)", bg: "transparent",           text: "#94a3b8" };
    default:
      return { border: "rgba(100,116,139,0.2)",  bg: "transparent",           text: "#94a3b8" };
  }
}

const styles: Record<string, React.CSSProperties> = {
  // Capacity: invisible root, children stacked and centered
  capacity: {
    display: "flex",
    flexDirection: "column",
    alignItems: "center",
    gap: 10,
    padding: 10,
  },
  // All frame boxes: fit-content width, hug children
  frame: {
    boxSizing: "border-box",
    width: "fit-content",
    borderRadius: 6,
    fontFamily: "monospace",
  },
  // Vertical frames: children stacked and centered
  verticalFrame: {
    display: "flex",
    flexDirection: "column",
    alignItems: "center",
    gap: 10,
    padding: "0 4px 4px",
  },
  // Horizontal children row (parallel / if / switch)
  horizontalChildren: {
    display: "flex",
    flexDirection: "row",
    alignItems: "flex-start",
    gap: 10,
    padding: "0 4px 4px",
  },
  // Leaf boxes: fit-content, will be centered by parent's align-items
  leaf: {
    boxSizing: "border-box",
    width: "fit-content",
    borderRadius: 8,
    fontFamily: "monospace",
  },
  frameLabel: {
    display: "inline-block",
    padding: "2px 6px",
    fontSize: 9,
    fontWeight: 700,
    letterSpacing: "0.1em",
    textTransform: "uppercase",
  },
  sequenceArrow: {
    fontSize: 22,
    color: "rgba(251,191,36,0.6)",
    lineHeight: 1,
    userSelect: "none",
    margin: 0,
    padding: 0,
  },
  sectionContent: {
    display: "grid",
    gap: 6,
    padding: "10px 14px",
    color: "#fde68a",
    width: "fit-content",
  },
  sectionHeader: {
    display: "flex",
    alignItems: "center",
    gap: 6,
  },
  sectionIconButton: {
    flex: "0 0 auto",
    width: 20,
    height: 20,
    padding: 0,
    border: 0,
    background: "transparent",
    color: "inherit",
    cursor: "pointer",
    fontFamily: "inherit",
    fontSize: 10,
  },
  sectionName: {
    fontSize: 13,
    fontWeight: 900,
    lineHeight: 1.4,
    letterSpacing: "0.06em",
    textTransform: "uppercase",
    whiteSpace: "nowrap",
    flex: "1 1 auto",
  },
  sectionRefreshButton: {
    flex: "0 0 auto",
    width: 20,
    height: 20,
    padding: 0,
    border: 0,
    background: "transparent",
    color: "inherit",
    cursor: "pointer",
    fontFamily: "inherit",
    fontSize: 13,
    opacity: 0.6,
  },
  sectionRole: {
    color: "#fff7ed",
    fontSize: 10,
    lineHeight: 1.35,
    opacity: 0.88,
    whiteSpace: "nowrap",
  },
  sectionMorphing: {
    display: "grid",
    gap: 2,
    color: "#fed7aa",
    fontSize: 9,
    lineHeight: 1.3,
    opacity: 0.78,
    whiteSpace: "nowrap",
  },
  sectionMetaLabel: {
    color: "#facc15",
    fontSize: 7,
    fontWeight: 900,
    letterSpacing: "0.12em",
    textTransform: "uppercase",
  },
  leafButton: {
    display: "grid",
    gap: 7,
    padding: "10px 12px",
    border: 0,
    background: "transparent",
    color: "#ffffff",
    fontFamily: "inherit",
    textAlign: "left",
    whiteSpace: "nowrap",
  },
  asChip: {
    display: "inline-block",
    padding: "1px 6px",
    borderRadius: 4,
    border: "1px solid rgba(196,181,253,0.35)",
    background: "rgba(196,181,253,0.08)",
    color: "#c4b5fd",
    fontSize: 9,
    fontFamily: "monospace",
    fontWeight: 600,
    lineHeight: 1.5,
    whiteSpace: "nowrap" as const,
  },
  withBlock: {
    display: "grid",
    gap: 4,
    paddingLeft: 8,
    borderLeft: "2px solid rgba(100,116,139,0.35)",
  },
  withLabel: {
    fontSize: 8,
    fontWeight: 700,
    letterSpacing: "0.1em",
    textTransform: "uppercase" as const,
    color: "#64748b",
  },
  withRefs: {
    display: "flex",
    flexWrap: "wrap" as const,
    gap: 4,
  },
  keyword: {
    color: "#93c5fd",
    fontSize: 11,
    fontWeight: 800,
    lineHeight: 1.4,
    whiteSpace: "nowrap",
  },
  details: {
    display: "grid",
    gap: 4,
    fontSize: 9,
    lineHeight: 1.4,
    whiteSpace: "nowrap",
  },
  qualifier: {
    color: "#fbbf24",
    fontWeight: 700,
  },
};
