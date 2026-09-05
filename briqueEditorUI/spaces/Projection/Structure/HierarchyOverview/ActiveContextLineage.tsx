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

import { useEffect, useRef } from "react";
import {
  findContextLineage,
  getStructureNodeContext,
  getStructureNodeLabel,
  type ActiveContext,
  type ActiveContextActivationRequest,
  type HierarchyStructure,
} from "./contracts.js";

export type ActiveContextLineageProps = {
  activeContext: ActiveContext;
  hierarchy: HierarchyStructure;
  onActivateContext: (request: ActiveContextActivationRequest) => void;
  onOpenInNewTab?: (contextPath: string) => void;
};

export function ActiveContextLineage({
  activeContext,
  hierarchy,
  onActivateContext,
  onOpenInNewTab,
}: ActiveContextLineageProps) {
  const activeRef = useRef<HTMLButtonElement>(null);
  const lineage = findContextLineage(hierarchy.root, activeContext) ?? [];

  useEffect(() => {
    activeRef.current?.scrollIntoView({ block: "center", inline: "nearest" });
  }, [activeContext, hierarchy]);

  return (
    <div aria-label="Active context lineage" style={styles.body}>
      {lineage.map((node, index) => {
        const context = getStructureNodeContext(node);
        const active = context === activeContext;

        return (
          <button
            aria-current={active ? "true" : undefined}
            key={`${context}:${index}`}
            onDoubleClick={() => onActivateContext({ target: context })}
            onAuxClick={(e) => { if (e.button === 1) { e.preventDefault(); onOpenInNewTab?.(context); } }}
            ref={active ? activeRef : undefined}
            style={{
              ...styles.context,
              ...styles[active ? "activeContext" : "inactiveContext"],
            }}
            type="button"
          >
            {getStructureNodeLabel(node)}
          </button>
        );
      })}
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  body: {
    display: "flex",
    flexDirection: "column",
    minHeight: 0,
    flex: 1,
    overflow: "auto",
    padding: "8px",
    gap: "6px",
  },
  context: {
    border: 0,
    padding: "7px 8px",
    textAlign: "left",
    color: "inherit",
    font: "inherit",
  },
  activeContext: {
    background: "var(--syn-selected-border)",
  },
  inactiveContext: {
    background: "var(--syn-structure-panel-bg)",
  },
};
