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

import { useCallback, useEffect, useImperativeHandle, useMemo, useRef, useState } from "react";
import { useBriqueSubstrate } from "../../../React_Substrate_Adapter/context.js";
import { createCapabilityClient } from "../../../Brique_Substrate/capability/index.js";
import type { InspectionTarget } from "../../Inspection/contracts.js";
import { ContextMapFloatingWindow } from "./ContextMap/ContextMapFloatingWindow.js";
import { ContextActionsMenu } from "./FocusView/ContextActionsMenu.js";
import type { ElementItem } from "./FocusView/contracts.js";
import { StructureFocusView } from "./FocusView/StructureFocusView.js";
import { StructureHierarchyOverview } from "./HierarchyOverview/StructureHierarchyOverview.js";
import {
  hierarchyContainsContext,
  type ActiveContext,
  type ActiveContextActivationRequest,
  type HierarchyDisplayedResponseState,
} from "./HierarchyOverview/contracts.js";

type FocusedSpace = "navigation" | "focus";

export type ProjectionStructureSpaceHandle = {
  activateContext: (contextPath: string) => void;
};

export type ProjectionStructureSpaceProps = {
  externalSelectedElement: InspectionTarget | undefined;
  imperativeRef?: React.Ref<ProjectionStructureSpaceHandle>;
  onSelectedElementChange: (target: InspectionTarget | undefined) => void;
  onOpenInNewTab?: (context: string, elementKind: string, elementName: string) => void;
  initialContext?: string;
};

export function ProjectionStructureSpace({
  externalSelectedElement,
  imperativeRef,
  onSelectedElementChange,
  onOpenInNewTab,
  initialContext,
}: ProjectionStructureSpaceProps) {
  const substrate = useBriqueSubstrate();
  const capabilityClient = useMemo(() => createCapabilityClient(substrate), [substrate]);
  const [activeContext, setActiveContext] = useState<ActiveContext>(initialContext ?? "/root");
  const [hierarchyState, setHierarchyState] =
    useState<HierarchyDisplayedResponseState>({
      hierarchy: undefined,
      loading: true,
      error: undefined,
    });
  const [focusedSpace, setFocusedSpace] = useState<FocusedSpace>("navigation");
  const [contextMapOpen, setContextMapOpen] = useState(false);
  const [selectedElement, setSelectedElement] = useState<ElementItem | undefined>(undefined);
  const [structureRefreshToken, setStructureRefreshToken] = useState(0);
  const refreshStructure = useCallback(() => setStructureRefreshToken((v) => v + 1), []);
  const selectElement = useCallback(
    (element: ElementItem | undefined) => {
      setSelectedElement(element);
      if (!element || element.kind === "context") return;
      onSelectedElementChange({
        context: activeContext,
        elementKind: element.kind,
        elementName: element.name,
        sourcePath: element.key,
      });
    },
    [activeContext, onSelectedElementChange]
  );

  const inspectContext = useCallback(
    (contextPath: string) => {
      onSelectedElementChange({
        context: contextPath,
        elementKind: "context",
        elementName: contextPath,
      });
    },
    [onSelectedElementChange]
  );


  const readHierarchy = useCallback(async () => {
    setHierarchyState((current) => ({ ...current, loading: true, error: undefined }));
    try {
      const result = await capabilityClient.read.structure("/root", { depth: 0 });
      if (!result.ok || !result.payload) {
        setHierarchyState((current) => ({
          ...current,
          loading: false,
          error: result.error?.message ?? "Unable to load hierarchy.",
        }));
        return;
      }
      const hierarchy = result.payload;
      setActiveContext((current) =>
        hierarchyContainsContext(hierarchy, current) ? current : "/root"
      );
      setHierarchyState({ hierarchy, loading: false, error: undefined });
    } catch (error) {
      setHierarchyState((current) => ({
        ...current,
        loading: false,
        error: error instanceof Error ? error.message : "Unable to load hierarchy.",
      }));
    }
  }, [capabilityClient]);

  useEffect(() => {
    void readHierarchy();
  }, [readHierarchy]);

  const activateContext = useCallback(
    ({ target }: ActiveContextActivationRequest) => {
      setActiveContext((current) => (current === target ? current : target));
    },
    []
  );

  const mountedRef = useRef(false);
  useEffect(() => {
    if (!mountedRef.current) { mountedRef.current = true; return; }
    onSelectedElementChange({ context: activeContext, elementKind: "context", elementName: activeContext });
  }, [activeContext]); // eslint-disable-line react-hooks/exhaustive-deps

  useImperativeHandle(
    imperativeRef,
    () => ({
      activateContext: (contextPath: string) => setActiveContext(contextPath),
    }),
    []
  );

  const focusNavigation = useCallback(() => setFocusedSpace("navigation"), []);
  const focusFocus = useCallback(() => setFocusedSpace("focus"), []);
  const toggleContextMap = useCallback(() => setContextMapOpen((v) => !v), []);
  const refreshAll = useCallback(() => {
    void readHierarchy();
    refreshStructure();
  }, [readHierarchy, refreshStructure]);

  const focusWidth = focusedSpace === "focus" ? 504 : 260;
  const focusSpaceExpanded = focusedSpace === "focus";

  return (
    <section
      aria-label="Brique Editor structure projection"
      data-space="projection-structure"
      style={styles.root}
    >
      {/* Navigation Space */}
      <div style={{ ...styles.space, flex: 1 }}>
        <StructureHierarchyOverview
          activeContext={activeContext}
          hierarchyState={hierarchyState}
          onActivateContext={activateContext}
          onRefresh={readHierarchy}
          onOpenInNewTab={onOpenInNewTab ? (ctx) => onOpenInNewTab(ctx, "context", ctx) : undefined}
        />
        <button
          style={styles.contextMapToggle}
          onClick={toggleContextMap}
          title={contextMapOpen ? "Close context map" : "Open context map"}
        >
          {contextMapOpen ? "✕" : "⊞"}
        </button>
        <button
          style={styles.refreshToggle}
          onClick={refreshAll}
          title="Refresh hierarchy and focus view"
        >
          ↻
        </button>
        {focusedSpace === "focus" && (
          <button
            style={styles.expandButtonRight}
            onClick={focusNavigation}
            title="Expand navigation"
          >
            ▶
          </button>
        )}
      </div>

      {/* Focus Space */}
      <div style={{ ...styles.space, width: focusWidth, flexShrink: 0, transition: "width 0.2s ease" }}>
        <StructureFocusView
          activeContext={activeContext}
          hierarchyState={hierarchyState}
          focusSpaceExpanded={focusSpaceExpanded}
          onActivateContext={activateContext}
          onInspectContext={inspectContext}
          onRefreshHierarchy={readHierarchy}
          structureRefreshToken={structureRefreshToken}
          onSelectedElementChange={selectElement}
          onOpenInNewTab={onOpenInNewTab}
        />
        <div style={styles.focusToolbar}>
          {focusedSpace === "navigation" && (
            <button style={styles.expandButton} onClick={focusFocus} title="Expand focus">
              ◀
            </button>
          )}
          <ContextActionsMenu
            activeContext={activeContext}
            selectedElement={selectedElement}
            onActivateContext={activateContext}
            onRefresh={readHierarchy}
            onRefreshStructure={refreshStructure}
          />
        </div>
      </div>

      {/* Context Map floating window — above both spaces */}
      {contextMapOpen && (
        <ContextMapFloatingWindow
          activeContext={activeContext}
          hierarchyState={hierarchyState}
          onActivateContext={activateContext}
          onClose={toggleContextMap}
          onOpenInNewTab={onOpenInNewTab ? (ctx) => onOpenInNewTab(ctx, "context", ctx) : undefined}
        />
      )}
    </section>
  );
}

const styles: Record<string, React.CSSProperties> = {
  root: {
    position: "relative",
    display: "flex",
    alignItems: "stretch",
    width: "100%",
    height: "100%",
    boxSizing: "border-box",
    background: "var(--syn-structure-bg)",
    color: "var(--syn-text-primary)",
    fontFamily: "monospace",
    fontWeight: 700,
  },
  space: {
    position: "relative",
    display: "flex",
    flexDirection: "column",
    height: "100%",
    minWidth: 0,
    transition: "width 0.2s ease",
    overflow: "hidden",
  },
  contextMapToggle: {
    position: "absolute",
    top: "8px",
    left: "8px",
    width: "28px",
    height: "28px",
    border: "1px solid var(--syn-control-border)",
    borderRadius: "4px",
    background: "var(--syn-control-bg)",
    color: "var(--syn-text-primary)",
    cursor: "pointer",
    fontSize: "14px",
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    zIndex: 11,
  },
  refreshToggle: {
    position: "absolute",
    top: "8px",
    left: "40px",
    width: "28px",
    height: "28px",
    border: "1px solid var(--syn-control-border)",
    borderRadius: "4px",
    background: "var(--syn-control-bg)",
    color: "var(--syn-text-primary)",
    cursor: "pointer",
    fontSize: "14px",
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    zIndex: 11,
  },
  focusToolbar: {
    position: "absolute",
    top: "8px",
    left: "8px",
    display: "flex",
    alignItems: "center",
    gap: "4px",
    zIndex: 11,
  },
  expandButton: {
    position: "relative",
    top: "auto",
    left: "auto",
    width: "24px",
    height: "24px",
    border: "1px solid var(--syn-control-border)",
    borderRadius: "4px",
    background: "var(--syn-control-bg)",
    color: "var(--syn-text-primary)",
    cursor: "pointer",
    fontSize: "10px",
    zIndex: 11,
  },
  expandButtonRight: {
    position: "absolute",
    top: "8px",
    right: "8px",
    width: "24px",
    height: "24px",
    border: "1px solid var(--syn-control-border)",
    borderRadius: "4px",
    background: "var(--syn-control-bg)",
    color: "var(--syn-text-primary)",
    cursor: "pointer",
    fontSize: "10px",
    zIndex: 11,
  },
};
