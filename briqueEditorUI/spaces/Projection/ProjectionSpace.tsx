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

import type { Ref } from "react";
import { ProjectionFlowSpace } from "./Flow/ProjectionFlowSpace.js";
import type { FlowBriqueRefCallbacks } from "./Flow/SectionFlow/FlowLayoutRenderer.js";
import { ProjectionSemanticSpace } from "./Semantic/ProjectionSemanticSpace.js";
import { ProjectionStructureSpace, type ProjectionStructureSpaceHandle } from "./Structure/ProjectionStructureSpace.js";
import { ProjectionTraceSpace } from "./Trace/ProjectionTraceSpace.js";
import type { InspectionTarget } from "../Inspection/contracts.js";
import type { SemanticWindowConfig, SemanticWindowSelections } from "./Semantic/contracts.js";
import type { ProjectionKind, FlowCapacityTarget } from "./contracts.js";

export type ProjectionSpaceProps = {
  activeProjection: ProjectionKind;
  selectedElement: InspectionTarget | undefined;
  flowCapacityTarget?: FlowCapacityTarget;
  onSelectedElementChange: (target: InspectionTarget | undefined) => void;
  structureRef?: Ref<ProjectionStructureSpaceHandle>;
  flowBriqueRefCallbacks?: FlowBriqueRefCallbacks;
  windowSelections: SemanticWindowSelections;
  selectedKinds: Set<string>;
  contextFilterMode: "off" | "strict" | "subtree";
  onWindowSelectionsChange: (selections: SemanticWindowSelections) => void;
  onSelectedKindsChange: (kinds: Set<string>) => void;
  onContextFilterModeChange: (mode: "off" | "strict" | "subtree") => void;
  onWindowListChange: (windows: SemanticWindowConfig[]) => void;
  onCommit: () => void;
  onReturnToUse: () => void;
  onSemanticModeChange: (mode: "edit" | "use") => void;
  onOpenInNewTab?: (context: string, elementKind: string, elementName: string) => void;
  initialStructureContext?: string;
  semanticInspectMode?: boolean;
};

export function ProjectionSpace({
  activeProjection,
  selectedElement,
  flowCapacityTarget,
  onSelectedElementChange,
  structureRef,
  flowBriqueRefCallbacks,
  windowSelections,
  selectedKinds,
  contextFilterMode,
  onWindowSelectionsChange,
  onSelectedKindsChange,
  onContextFilterModeChange,
  onWindowListChange,
  onCommit,
  onReturnToUse,
  onSemanticModeChange,
  onOpenInNewTab,
  initialStructureContext,
  semanticInspectMode,
}: ProjectionSpaceProps) {
  const flowTarget = flowCapacityTarget;

  return (
    <section aria-label="Brique Editor projection" data-space="projection" style={styles.root}>
      <ProjectionLayer active={activeProjection === "structure"}>
        <ProjectionStructureSpace
          externalSelectedElement={selectedElement}
          imperativeRef={structureRef}
          onSelectedElementChange={onSelectedElementChange}
          onOpenInNewTab={onOpenInNewTab}
          initialContext={initialStructureContext}
        />
      </ProjectionLayer>
      <ProjectionLayer active={activeProjection === "semantic"}>
        <ProjectionSemanticSpace
          windowSelections={windowSelections}
          selectedKinds={selectedKinds}
          contextFilterMode={contextFilterMode}
          activeContext={selectedElement?.context}
          onWindowSelectionsChange={onWindowSelectionsChange}
          onSelectedKindsChange={onSelectedKindsChange}
          onContextFilterModeChange={onContextFilterModeChange}
          onWindowListChange={onWindowListChange}
          onCommit={onCommit}
          onReturnToUse={onReturnToUse}
          onModeChange={onSemanticModeChange}
          inspectMode={semanticInspectMode ?? false}
        />
      </ProjectionLayer>
      <ProjectionLayer active={activeProjection === "flow"}>
        <ProjectionFlowSpace
          target={flowTarget}
          onFlowCapacityTargetChange={(t) =>
            onSelectedElementChange({
              context: t.context,
              elementKind: "capacity",
              elementName: t.capacityName,
            })
          }
          briqueRefCallbacks={flowBriqueRefCallbacks}
        />
      </ProjectionLayer>
      <ProjectionLayer active={activeProjection === "trace"}>
        <ProjectionTraceSpace />
      </ProjectionLayer>
    </section>
  );
}

function ProjectionLayer({
  active,
  children,
}: {
  active: boolean;
  children: React.ReactNode;
}) {
  return (
    <div
      aria-hidden={!active}
      style={{
        ...styles.layer,
        opacity: active ? 1 : 0,
        pointerEvents: active ? "auto" : "none",
        zIndex: active ? 1 : 0,
      }}
    >
      {children}
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  root: {
    position: "relative",
    width: "100%",
    height: "100%",
    boxSizing: "border-box",
    overflow: "hidden",
  },
  layer: {
    position: "absolute",
    inset: 0,
    minWidth: 0,
    minHeight: 0,
    overflow: "hidden",
    contain: "strict",
  },
};
