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

import { useCallback, useEffect, useRef } from "react";
import { InspectionSpace } from "../Inspection/InspectionSpace.js";
import type { InspectionDisplayMode, InspectionMode, InspectionTarget } from "../Inspection/contracts.js";
import { OverlaySpaceProvider } from "../Overlay/OverlaySpace.js";
import { OperationalStateSpace } from "../OperationalState/OperationalStateSpace.js";
import { ProjectionSpace } from "../Projection/ProjectionSpace.js";
import type { ProjectionStructureSpaceHandle } from "../Projection/Structure/ProjectionStructureSpace.js";
import type { SemanticWindowConfig, SemanticWindowSelections } from "../Projection/Semantic/contracts.js";
import type { FlowCapacityTarget } from "../Projection/contracts.js";
import type { FlowBriqueRefCallbacks } from "../Projection/Flow/SectionFlow/FlowLayoutRenderer.js";
import type { TabId, TabState } from "./contracts.js";
import { HistoryView } from "./HistoryView.js";

export type TabInstanceProps = {
  tab: TabState;
  active: boolean;
  onPatch: (id: TabId, patch: Partial<TabState>) => void;
  onNewSeededTab: (element: InspectionTarget) => void;
  onSelectFromHistory: (element: InspectionTarget) => void;
};

export function TabInstance({ tab, active, onPatch, onNewSeededTab, onSelectFromHistory }: TabInstanceProps) {
  const patch = useCallback(
    (p: Partial<TabState>) => onPatch(tab.id, p),
    [onPatch, tab.id]
  );

  const structureRef = useRef<ProjectionStructureSpaceHandle>(null);
  const inspectionModeRef = useRef<InspectionMode>("inspect");
  const inspectBaseRef = useRef<SemanticWindowSelections>({});
  const browseFilterRef = useRef<SemanticWindowSelections>({});

  const {
    activeProjection,
    selectedElement,
    flowCapacityTarget,
    inspectionDisplayMode,
    windowSelections,
    inspectSelections,
    inspectSelectedKinds,
    inspectionMode,
    selectedKinds,
    windowList,
    semanticWorkspaceMode,
    contextFilterMode,
    initialStructureContext,
    history,
    showHistory,
  } = tab;

  const inspectionExpanded = inspectionDisplayMode === "expanded";
  // ProjectionSpace receives inspect-specific selections when inspection is in inspect mode
  const semanticSelections = inspectionMode === "inspect" ? inspectSelections : windowSelections;
  const semanticKinds = inspectionMode === "inspect" ? inspectSelectedKinds : selectedKinds;

  useEffect(() => {
    if (selectedElement?.elementKind === "context") {
      structureRef.current?.activateContext(selectedElement.elementName);
      patch({ activeProjection: "structure" });
    } else if (selectedElement?.elementKind === "capacity") {
      patch({
        activeProjection: "flow",
        flowCapacityTarget: {
          context: selectedElement.context,
          capacityName: selectedElement.elementName,
        },
      });
    }
  }, [selectedElement]); // eslint-disable-line react-hooks/exhaustive-deps

  const handleActivateContext = useCallback((contextPath: string) => {
    structureRef.current?.activateContext(contextPath);
    patch({ activeProjection: "structure", selectedElement: { context: contextPath, elementKind: "context", elementName: contextPath } });
  }, [patch]);

  const handleSelectedElementChange = useCallback((element: InspectionTarget | undefined) => {
    patch({ selectedElement: element });
  }, [patch]);

  const handleInspectionDisplayModeChange = useCallback((mode: InspectionDisplayMode) => {
    patch({ inspectionDisplayMode: mode });
  }, [patch]);

  const flowBriqueRefCallbacks = useRef<FlowBriqueRefCallbacks>({
    onInspectElement: (contextPath, elementKind, elementName) => {
      patch({ selectedElement: { context: contextPath, elementKind, elementName } });
    },
    onActivateContext: (contextPath) => {
      structureRef.current?.activateContext(contextPath);
      patch({ activeProjection: "structure", selectedElement: { context: contextPath, elementKind: "context", elementName: contextPath } });
    },
    onFlowProjectCapacity: (contextPath, capacityName) => {
      patch({
        flowCapacityTarget: { context: contextPath, capacityName },
        selectedElement: { context: contextPath, elementKind: "capacity", elementName: capacityName },
        activeProjection: "flow",
      });
    },
    onOpenInNewTab: (contextPath, elementKind, elementName) => {
      onNewSeededTab({ context: contextPath, elementKind, elementName });
    },
  });

  useEffect(() => {
    flowBriqueRefCallbacks.current = {
      onInspectElement: (contextPath, elementKind, elementName) => {
        patch({ selectedElement: { context: contextPath, elementKind, elementName } });
      },
      onActivateContext: (contextPath) => {
        structureRef.current?.activateContext(contextPath);
        patch({ activeProjection: "structure", selectedElement: { context: contextPath, elementKind: "context", elementName: contextPath } });
      },
      onFlowProjectCapacity: (contextPath, capacityName) => {
        patch({
          flowCapacityTarget: { context: contextPath, capacityName },
          selectedElement: { context: contextPath, elementKind: "capacity", elementName: capacityName },
          activeProjection: "flow",
        });
      },
      onOpenInNewTab: (contextPath, elementKind, elementName) => {
        onNewSeededTab({ context: contextPath, elementKind, elementName });
      },
    };
  }, [patch, onNewSeededTab]);

  const applyBrowseFilter = useCallback((browseFilter: SemanticWindowSelections) => {
    const browseIsEmpty = !Object.values(browseFilter).some((e) => e.selectedKeys.length > 0);
    const reconciled: SemanticWindowSelections = {};
    for (const w of tab.windowList) {
      if (browseIsEmpty) {
        if (w.defaultKeys.length > 0) {
          reconciled[w.id] = { selectedKeys: w.defaultKeys, combinator: w.defaultCombinator };
        }
      } else {
        const saved = browseFilter[w.id];
        if (saved && saved.selectedKeys.length > 0) reconciled[w.id] = saved;
      }
    }
    patch({ windowSelections: reconciled });
  }, [patch, tab.windowList]);

  const handleBrowseRestore = useCallback((browseFilter: SemanticWindowSelections) => {
    browseFilterRef.current = browseFilter;
    applyBrowseFilter(browseFilter);
  }, [applyBrowseFilter]);

  const handleInspectSeed = useCallback((seed: SemanticWindowSelections) => {
    inspectBaseRef.current = seed;
    patch({ inspectSelections: seed, inspectSelectedKinds: new Set() });
  }, [patch]);

  const handleReturnToUse = useCallback(() => {
    if (inspectionModeRef.current === "browse") {
      applyBrowseFilter(browseFilterRef.current);
    } else {
      // Edit → Use: apply defaultKeys for each window (browseFilter empty → defaultKeys)
      applyBrowseFilter({});
    }
  }, [applyBrowseFilter]);

  const handleInspectionModeChange = useCallback((m: InspectionMode) => {
    inspectionModeRef.current = m;
    patch({ inspectionMode: m });
    if (m === "browse") {
      applyBrowseFilter(browseFilterRef.current);
    }
  }, [applyBrowseFilter, patch]);

  const handleWindowListChange = useCallback((l: SemanticWindowConfig[]) => {
    patch({ windowList: l });
  }, [patch]);

  const handleWindowSelectionsChange = useCallback((s: SemanticWindowSelections) => {
    if (inspectionModeRef.current === "inspect") {
      patch({ inspectSelections: s });
    } else {
      patch({ windowSelections: s });
    }
  }, [patch]);

  const handleSelectedKindsChange = useCallback((k: Set<string>) => {
    if (inspectionModeRef.current === "inspect") {
      patch({ inspectSelectedKinds: k });
      return;
    }
    patch({ selectedKinds: k });
  }, [patch]);

  const handleContextFilterModeChange = useCallback((mode: "off" | "strict" | "subtree") => {
    patch({ contextFilterMode: mode });
  }, [patch]);

  return (
    <OverlaySpaceProvider>
      <div
        data-tab-id={tab.id}
        style={{ ...styles.root, display: active ? "grid" : "none" }}
      >
        <div
          style={{
            ...styles.workspace,
            gridTemplateColumns: inspectionExpanded ? "72fr 28fr" : "97fr 3fr",
          }}
        >
          <div style={styles.primary}>
            <div style={styles.projection}>
              <div style={{ position: "absolute", inset: 0, display: showHistory ? "none" : "block", zIndex: 0 }}>
                <ProjectionSpace
                  activeProjection={activeProjection}
                  selectedElement={selectedElement}
                  flowCapacityTarget={flowCapacityTarget as FlowCapacityTarget | undefined}
                  onSelectedElementChange={handleSelectedElementChange}
                  structureRef={structureRef}
                  flowBriqueRefCallbacks={flowBriqueRefCallbacks.current}
                  windowSelections={semanticSelections}
                  selectedKinds={semanticKinds}
                  contextFilterMode={contextFilterMode}
                  onWindowSelectionsChange={handleWindowSelectionsChange}
                  onSelectedKindsChange={handleSelectedKindsChange}
                  onContextFilterModeChange={handleContextFilterModeChange}
                  onWindowListChange={handleWindowListChange}
                  onCommit={() => {}}
                  onReturnToUse={handleReturnToUse}
                  onSemanticModeChange={(m) => patch({ semanticWorkspaceMode: m })}
                  semanticInspectMode={inspectionMode === "inspect"}
                  onOpenInNewTab={(context, elementKind, elementName) => onNewSeededTab({ context, elementKind, elementName })}
                  initialStructureContext={initialStructureContext}
                />
              </div>
              <div style={{ position: "absolute", inset: 0, display: showHistory ? "block" : "none", zIndex: 0 }}>
                <HistoryView
                  history={history}
                  onSelect={onSelectFromHistory}
                  onActivateContext={handleActivateContext}
                  onOpenInNewTab={onNewSeededTab}
                />
              </div>
            </div>
            <div style={styles.operationalState}>
              <OperationalStateSpace selectedContext={selectedElement?.context} />
            </div>
          </div>

          <div style={styles.inspection}>
            <InspectionSpace
              displayMode={inspectionDisplayMode}
              contextFilterMode={contextFilterMode}
              onDisplayModeChange={handleInspectionDisplayModeChange}
              target={selectedElement}
              windowSelections={semanticSelections}
              selectedKinds={semanticKinds}
              windowList={windowList}
              activeProjection={activeProjection}
              onInspectionTargetChange={handleSelectedElementChange}
              onBrowseRestore={handleBrowseRestore}
              onInspectSeed={handleInspectSeed}
              onInspectionModeChange={handleInspectionModeChange}
              semanticWorkspaceMode={semanticWorkspaceMode}
              onActivateContext={handleActivateContext}
              onOpenInNewTab={onNewSeededTab}
            />
          </div>
        </div>
      </div>
    </OverlaySpaceProvider>
  );
}

const styles: Record<string, React.CSSProperties> = {
  root: {
    position: "absolute",
    inset: 0,
    gridTemplateRows: "1fr",
    overflow: "hidden",
  },
  workspace: {
    display: "grid",
    minHeight: 0,
    height: "100%",
    transition: "grid-template-columns 0.2s ease",
  },
  primary: {
    display: "grid",
    gridTemplateRows: "1fr 20px",
    minWidth: 0,
    minHeight: 0,
    position: "relative",
  },
  projection: {
    position: "relative",
    minWidth: 0,
    minHeight: 0,
  },
  operationalState: {
    minHeight: 0,
  },
  inspection: {
    minWidth: 0,
    minHeight: 0,
    overflow: "hidden",
  },
};
