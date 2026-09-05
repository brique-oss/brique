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

import { useEffect, useMemo, useRef, useState } from "react";
import {
  Background,
  Handle,
  MarkerType,
  Position,
  ReactFlow,
  useViewport,
  type Edge,
  type Node,
  type NodeProps,
  type ReactFlowInstance,
} from "@xyflow/react";
import type { StructureNode } from "../../../../Brique_Substrate/capability/typed.js";
import {
  getStructureNodeContext,
  getStructureNodeLabel,
  type ActiveContext,
  type ActiveContextActivationRequest,
  type HierarchyStructure,
} from "./contracts.js";
import { usePreview } from "../../../Overlay/PreviewContext.js";

export type ExpandedHierarchyProps = {
  activeContext: ActiveContext;
  hierarchy: HierarchyStructure;
  onActivateContext: (request: ActiveContextActivationRequest) => void;
  onOpenInNewTab?: (contextPath: string) => void;
};

type HierarchyNodeData = {
  active: boolean;
  context: ActiveContext;
  hasExpandedOverflow: boolean;
  label: string;
  onCollapseOverflow: () => void;
  onOpenInNewTab?: (contextPath: string) => void;
};

type OverflowNodeData = {
  hiddenCount: number;
  parentContext: ActiveContext;
};

type ContextFlowNode = Node<HierarchyNodeData, "contextNode">;
type OverflowFlowNode = Node<OverflowNodeData, "overflowNode">;
type HierarchyFlowNode = ContextFlowNode | OverflowFlowNode;

const VISIBLE_CHILDREN_LIMIT = 3;

type HierarchyGraph = {
  activeNodeId: string | undefined;
  edges: Edge[];
  nodes: HierarchyFlowNode[];
};

const NODE_WIDTH = 144;
const NODE_HEIGHT = 64;
const COLUMN_GAP = 48;
const ROW_GAP = 72;

function HierarchyContextNode({ data }: NodeProps<ContextFlowNode>) {
  const { zoom } = useViewport();
  const [hovered, setHovered] = useState(false);
  const { requestPreview } = usePreview();
  const magnification = hovered
    ? Math.min(6, Math.max(1.6, 0.9 / zoom))
    : 1;

  return (
    <div style={styles.nodeFrame}>
      <Handle type="target" position={Position.Top} style={styles.handle} />
      <div
        onMouseEnter={(e) => {
          setHovered(true);
          if (e.ctrlKey) requestPreview({ elementKind: "context", elementName: data.context, context: data.context, pos: { x: e.clientX, y: e.clientY } });
        }}
        onMouseLeave={() => setHovered(false)}
        onAuxClick={(e) => { if (e.button === 1) { e.preventDefault(); e.stopPropagation(); data.onOpenInNewTab?.(data.context); } }}
        title={data.context}
        style={{
          ...styles.nodeBubble,
          background: data.active ? "var(--syn-structure-panel-bg)" : "var(--syn-structure-node-bg)",
          border: data.active ? "2px solid var(--syn-selected-border)" : "1px solid var(--syn-structure-node-border)",
          color: data.active ? "var(--syn-text-primary)" : "var(--syn-text-secondary)",
          boxShadow: hovered ? "0 12px 32px rgba(0, 0, 0, 0.45)" : "none",
          transform: `scale(${magnification})`,
        }}
      >
        {data.label}
      </div>
      {data.hasExpandedOverflow && (
        <button
          type="button"
          title="Collapse children"
          aria-label="Collapse children"
          onClick={(e) => {
            e.stopPropagation();
            data.onCollapseOverflow();
          }}
          onDoubleClick={(e) => e.stopPropagation()}
          style={styles.collapseButton}
        >
          −
        </button>
      )}
      <Handle type="source" position={Position.Bottom} style={styles.handle} />
    </div>
  );
}

function OverflowNode({
  data,
}: NodeProps<OverflowFlowNode>) {
  return (
    <div style={styles.nodeFrame}>
      <Handle type="target" position={Position.Top} style={styles.handle} />
      <div style={styles.overflowBubble} title={`${data.hiddenCount} more`}>
        +{data.hiddenCount}
      </div>
    </div>
  );
}

const NODE_TYPES = { contextNode: HierarchyContextNode, overflowNode: OverflowNode };

export function ExpandedHierarchy({
  activeContext,
  hierarchy,
  onActivateContext,
  onOpenInNewTab,
}: ExpandedHierarchyProps) {
  // A context with more than VISIBLE_CHILDREN_LIMIT children only shows its
  // first VISIBLE_CHILDREN_LIMIT children plus a synthetic "+N" overflow node
  // by default. `expandedOverflowContexts` tracks which such contexts the
  // user has clicked open to reveal the remaining children.
  const [expandedOverflowContexts, setExpandedOverflowContexts] = useState<
    Set<ActiveContext>
  >(() => new Set());

  // Toggling overflow for a context can shift columns anywhere along its
  // ancestor chain (a parent's column is the midpoint of its children's
  // columns). To keep the clicked context visually anchored under the
  // pointer instead of the whole view jumping, we remember which context
  // triggered the toggle and, once the new graph is computed, shift the
  // viewport by exactly the on-screen displacement that context underwent.
  const anchorRef = useRef<{ context: ActiveContext; flowX: number; flowY: number } | null>(
    null
  );

  const setOverflowExpanded = (
    context: ActiveContext,
    expanded: boolean,
    anchorNode: HierarchyFlowNode
  ) => {
    anchorRef.current = {
      context,
      flowX: anchorNode.position.x,
      flowY: anchorNode.position.y,
    };
    setExpandedOverflowContexts((current) => {
      const has = current.has(context);
      if (has === expanded) return current;
      const next = new Set(current);
      if (expanded) next.add(context);
      else next.delete(context);
      return next;
    });
  };

  const graph = useMemo(
    () =>
      createHierarchyGraph(
        hierarchy.root,
        activeContext,
        expandedOverflowContexts,
        setOverflowExpanded,
        onOpenInNewTab
      ),
    // eslint-disable-next-line react-hooks/exhaustive-deps -- setOverflowExpanded closes over stable setState identities only
    [activeContext, expandedOverflowContexts, hierarchy, onOpenInNewTab]
  );
  const [instance, setInstance] =
    useState<ReactFlowInstance<HierarchyFlowNode, Edge>>();

  useEffect(() => {
    if (!instance) return;

    const anchor = anchorRef.current;
    if (anchor) {
      anchorRef.current = null;
      const anchoredNode = graph.nodes.find(
        (node) => node.type === "contextNode" && node.data.context === anchor.context
      );
      if (anchoredNode) {
        const dx = anchoredNode.position.x - anchor.flowX;
        const dy = anchoredNode.position.y - anchor.flowY;
        if (dx !== 0 || dy !== 0) {
          const viewport = instance.getViewport();
          void instance.setViewport(
            {
              x: viewport.x - dx * viewport.zoom,
              y: viewport.y - dy * viewport.zoom,
              zoom: viewport.zoom,
            },
            { duration: 0 }
          );
        }
        return;
      }
    }

    if (!graph.activeNodeId) return;

    const activeNode = graph.nodes.find((node) => node.id === graph.activeNodeId);
    if (!activeNode) return;

    const zoom = instance.getZoom() || 0.8;
    void instance.setCenter(
      activeNode.position.x + NODE_WIDTH / 2,
      activeNode.position.y + NODE_HEIGHT / 2,
      { duration: 300, zoom }
    );
  }, [graph, instance]);

  return (
    <div aria-label="Complete context hierarchy" style={styles.body}>
      <ReactFlow
        defaultViewport={{ x: 0, y: 0, zoom: 0.45 }}
        edges={graph.edges}
        maxZoom={1.8}
        minZoom={0.1}
        nodes={graph.nodes}
        nodesConnectable={false}
        nodesDraggable={false}
        nodesFocusable={false}
        nodeTypes={NODE_TYPES}
        onInit={setInstance}
        onNodeClick={(_, node) => {
          if (node.type === "overflowNode") {
            const parentNode = graph.nodes.find(
              (n) =>
                n.type === "contextNode" &&
                n.data.context === node.data.parentContext
            );
            if (parentNode) {
              setOverflowExpanded(node.data.parentContext, true, parentNode);
            }
          }
        }}
        onNodeDoubleClick={(_, node) => {
          if (node.type === "contextNode") {
            onActivateContext({ target: node.data.context });
          }
        }}
        panOnDrag
        unselectable="on"
        zoomOnDoubleClick={false}
      >
        <Background color="var(--syn-border-soft)" gap={24} />
      </ReactFlow>
    </div>
  );
}

function createHierarchyGraph(
  root: StructureNode,
  activeContext: ActiveContext,
  expandedOverflowContexts: Set<ActiveContext>,
  setOverflowExpanded: (
    context: ActiveContext,
    expanded: boolean,
    anchorNode: HierarchyFlowNode
  ) => void,
  onOpenInNewTab?: (contextPath: string) => void
): HierarchyGraph {
  const nodes: HierarchyFlowNode[] = [];
  const edges: Edge[] = [];
  let nextLeafColumn = 0;
  let activeNodeId: string | undefined;

  function addEdge(sourceId: string, targetId: string): void {
    edges.push({
      id: `${sourceId}->${targetId}`,
      source: sourceId,
      target: targetId,
      markerEnd: {
        type: MarkerType.ArrowClosed,
        color: "var(--syn-text-primary)",
      },
      style: {
        stroke: "var(--syn-text-primary)",
        strokeWidth: 2,
      },
    });
  }

  function visit(node: StructureNode, depth: number, path: string): number {
    const context = getStructureNodeContext(node);
    const allChildren = node.children ?? [];
    const isOverflowing = allChildren.length > VISIBLE_CHILDREN_LIMIT;
    const overflowExpanded = expandedOverflowContexts.has(context);
    const visibleChildren =
      isOverflowing && !overflowExpanded
        ? allChildren.slice(0, VISIBLE_CHILDREN_LIMIT)
        : allChildren;
    const hiddenCount = allChildren.length - visibleChildren.length;

    const id = path;
    const childColumns = visibleChildren.map((child, index) =>
      visit(child, depth + 1, `${path}.${index}`)
    );

    let overflowColumn: number | undefined;
    if (hiddenCount > 0) {
      overflowColumn = nextLeafColumn++;
      const overflowId = `${id}.overflow`;
      nodes.push({
        id: overflowId,
        type: "overflowNode",
        className: "hierarchy-overflow-node",
        data: { hiddenCount, parentContext: context },
        position: {
          x: overflowColumn * (NODE_WIDTH + COLUMN_GAP),
          y: (depth + 1) * (NODE_HEIGHT + ROW_GAP),
        },
        sourcePosition: Position.Bottom,
        targetPosition: Position.Top,
        style: { width: NODE_WIDTH, height: NODE_HEIGHT },
      });
      addEdge(id, overflowId);
    }

    const allColumns =
      overflowColumn === undefined
        ? childColumns
        : [...childColumns, overflowColumn];
    const column =
      allColumns.length === 0
        ? nextLeafColumn++
        : (allColumns[0] + allColumns[allColumns.length - 1]) / 2;
    const active = context === activeContext;

    if (active) activeNodeId = id;

    const contextNode: ContextFlowNode = {
      id,
      type: "contextNode",
      className: "hierarchy-context-node",
      data: {
        active,
        context,
        hasExpandedOverflow: isOverflowing && overflowExpanded,
        label: getStructureNodeLabel(node),
        onCollapseOverflow: () => {},
        onOpenInNewTab,
      },
      position: {
        x: column * (NODE_WIDTH + COLUMN_GAP),
        y: depth * (NODE_HEIGHT + ROW_GAP),
      },
      sourcePosition: Position.Bottom,
      targetPosition: Position.Top,
      style: {
        width: NODE_WIDTH,
        height: NODE_HEIGHT,
      },
    };
    contextNode.data.onCollapseOverflow = () =>
      setOverflowExpanded(context, false, contextNode);
    nodes.push(contextNode);

    visibleChildren.forEach((_, index) => addEdge(id, `${path}.${index}`));

    return column;
  }

  visit(root, 0, "root");
  return { activeNodeId, edges, nodes };
}

const styles: Record<string, React.CSSProperties> = {
  body: {
    minHeight: 0,
    flex: 1,
    background: "var(--syn-structure-bg)",
  },
  nodeFrame: {
    position: "relative",
    width: "100%",
    height: "100%",
  },
  nodeBubble: {
    position: "relative",
    zIndex: 1,
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    width: "100%",
    height: "100%",
    boxSizing: "border-box",
    borderRadius: "18px",
    padding: "8px 12px",
    overflow: "hidden",
    textAlign: "center",
    fontFamily: "monospace",
    fontSize: "12px",
    fontWeight: 700,
    lineHeight: 1.25,
    transformOrigin: "center",
    transition: "transform 140ms ease-out, box-shadow 140ms ease-out",
    cursor: "pointer",
  },
  handle: {
    width: "6px",
    height: "6px",
    border: 0,
    background: "var(--syn-text-primary)",
  },
  collapseButton: {
    position: "absolute",
    top: "-8px",
    right: "-8px",
    zIndex: 2,
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    width: "20px",
    height: "20px",
    padding: 0,
    borderRadius: "10px",
    border: "1px solid var(--syn-structure-node-border)",
    background: "var(--syn-structure-panel-bg)",
    color: "var(--syn-text-primary)",
    fontFamily: "monospace",
    fontSize: "13px",
    fontWeight: 700,
    lineHeight: 1,
    cursor: "pointer",
  },
  overflowBubble: {
    position: "relative",
    zIndex: 1,
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    width: "100%",
    height: "100%",
    boxSizing: "border-box",
    borderRadius: "18px",
    padding: "8px 12px",
    border: "1px dashed var(--syn-structure-node-border)",
    color: "var(--syn-text-secondary)",
    fontFamily: "monospace",
    fontSize: "12px",
    fontWeight: 700,
    cursor: "pointer",
  },
};

const globalStyle = document.createElement("style");
globalStyle.textContent = `
  .react-flow__attribution { display: none !important; }
  .react-flow__node-hierarchy-context-node:hover { z-index: 1000 !important; }
`;
document.head.appendChild(globalStyle);
