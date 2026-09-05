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

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useBriqueSubstrate } from "../../../../React_Substrate_Adapter/context.js";
import { createCapabilityClient } from "../../../../Brique_Substrate/capability/index.js";
import type { ActiveContext, ActiveContextActivationRequest, HierarchyDisplayedResponseState } from "../HierarchyOverview/contracts.js";
import type { ActiveContextStructure, ElementItem, ElementSelectionRequest, TargetOpeningRequest } from "./contracts.js";
import { ContextRelations } from "./ContextRelations.js";
import { ContextSummary } from "./ContextSummary.js";
import { ElementGroups } from "./ElementGroups.js";
import { NativeEntrypoints } from "./NativeEntrypoints.js";

export type StructureFocusViewProps = {
  activeContext: ActiveContext;
  hierarchyState: HierarchyDisplayedResponseState;
  focusSpaceExpanded: boolean;
  onActivateContext: (request: ActiveContextActivationRequest) => void;
  onInspectContext: (contextPath: string) => void;
  onRefreshHierarchy: () => void;
  structureRefreshToken: number;
  onSelectedElementChange: (element: ElementItem | undefined) => void;
  onOpenInNewTab?: (context: string, elementKind: string, elementName: string) => void;
};

export function StructureFocusView({
  activeContext,
  hierarchyState,
  focusSpaceExpanded,
  onActivateContext,
  onInspectContext,
  onRefreshHierarchy,
  structureRefreshToken,
  onSelectedElementChange,
  onOpenInNewTab,
}: StructureFocusViewProps) {
  const substrate = useBriqueSubstrate();
  const capabilityClient = useMemo(() => createCapabilityClient(substrate), [substrate]);

  const [activeContextStructure, setActiveContextStructure] = useState<ActiveContextStructure | undefined>(undefined);
  const [structureLoading, setStructureLoading] = useState(false);
  const [selectedElement, setSelectedElement] = useState<ElementItem | undefined>(undefined);
  const requestSeqRef = useRef(0);

  const readActiveContextStructure = useCallback(
    async (context: ActiveContext) => {
      const seq = ++requestSeqRef.current;
      setStructureLoading(true);
      try {
        const result = await capabilityClient.read.structure(context, { depth: 1 });
        if (seq !== requestSeqRef.current) return;
        if (result.ok && result.payload) {
          setActiveContextStructure(result.payload);
        } else {
          setActiveContextStructure(undefined);
        }
      } catch {
        if (seq !== requestSeqRef.current) return;
        setActiveContextStructure(undefined);
      } finally {
        if (seq === requestSeqRef.current) setStructureLoading(false);
      }
    },
    [capabilityClient]
  );

  useEffect(() => {
    void readActiveContextStructure(activeContext);
  }, [activeContext, structureRefreshToken, readActiveContextStructure]);

  useEffect(() => {
    setSelectedElement(undefined);
    onSelectedElementChange(undefined);
  }, [activeContext, onSelectedElementChange]);

  const handleSelectElement = useCallback(
    ({ element }: ElementSelectionRequest) => {
      setSelectedElement((current) => {
        const next = current?.key === element.key ? undefined : element;
        onSelectedElementChange(next);
        return next;
      });
    },
    [onSelectedElementChange]
  );

  const handleOpenElement = useCallback(
    (_request: TargetOpeningRequest) => {
      // TargetOpeningRequest forwarding — not yet wired to parent
    },
    []
  );

  return (
    <section aria-label="Structure focus view" style={styles.root}>
      <ContextSummary
        activeContext={activeContext}
        onSelectDescriptor={() => onInspectContext(activeContext)}
        onOpenInNewTab={onOpenInNewTab ? (ctx) => onOpenInNewTab(ctx, "context", ctx) : undefined}
      />
      {structureLoading && (
        <div style={styles.debug}>
          read.structure("{activeContext}", {"{"}depth: 1{"}"})
        </div>
      )}
      {focusSpaceExpanded && (
        <ContextRelations
          activeContext={activeContext}
          hierarchyState={hierarchyState}
          onActivateContext={onActivateContext}
          onOpenInNewTab={onOpenInNewTab ? (ctx) => onOpenInNewTab(ctx, "context", ctx) : undefined}
        />
      )}
      <ElementGroups
        activeContextStructure={activeContextStructure}
        activeContext={activeContext}
        selectedElement={selectedElement}
        expanded={focusSpaceExpanded}
        onSelectElement={handleSelectElement}
        onOpenElement={handleOpenElement}
        onOpenInNewTab={onOpenInNewTab}
      />
      <NativeEntrypoints
        activeContext={activeContext}
        activeContextStructure={activeContextStructure}
      />
    </section>
  );
}

const styles: Record<string, React.CSSProperties> = {
  debug: {
    padding: "4px 12px",
    fontSize: "10px",
    fontFamily: "monospace",
    opacity: 0.5,
    background: "var(--syn-structure-panel-bg)",
  },
  root: {
    display: "flex",
    flexDirection: "column",
    width: "100%",
    height: "100%",
    minWidth: 0,
    minHeight: 0,
    overflow: "hidden",
    background: "var(--syn-structure-panel-bg)",
    borderLeft: "1px solid var(--syn-border-soft)",
  },
};
