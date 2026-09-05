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

import { useCallback } from "react";
import type {
  ActiveContext,
  ActiveContextActivationRequest,
  HierarchyDisplayedResponseState,
} from "./contracts.js";
import {
  buildContextView,
  findNodeByContext,
  getParentContext,
  parseBreadcrumb,
} from "./contracts.js";
import { ChildContextGrid } from "./ChildContextGrid.js";
import { ContextNavigationControls } from "./ContextNavigationControls.js";
import { CurrentContextShell } from "./CurrentContextShell.js";

export type StructureContextMapProps = {
  activeContext: ActiveContext;
  hierarchyState: HierarchyDisplayedResponseState;
  onActivateContext: (request: ActiveContextActivationRequest) => void;
  onOpenInNewTab?: (contextPath: string) => void;
};

export function StructureContextMap({
  activeContext,
  hierarchyState,
  onActivateContext,
  onOpenInNewTab,
}: StructureContextMapProps) {
  const { hierarchy, loading, error } = hierarchyState;

  const breadcrumb = hierarchy
    ? parseBreadcrumb(hierarchy.root, activeContext)
    : [];

  const activeNode = hierarchy
    ? findNodeByContext(hierarchy.root, activeContext)
    : undefined;

  const childViews = activeNode
    ? (activeNode.children ?? []).map((c) => buildContextView(c, 0))
    : [];

  const handleWheel = useCallback(
    (e: React.WheelEvent) => {
      if (!hierarchy) return;
      if (e.deltaY > 0) {
        const parent = getParentContext(hierarchy.root, activeContext);
        if (parent !== undefined) {
          onActivateContext({ target: parent });
        }
      }
    },
    [hierarchy, activeContext, onActivateContext]
  );

  return (
    <section
      aria-label="Structure context map"
      style={styles.root}
      onWheel={handleWheel}
    >
      <ContextNavigationControls
        breadcrumb={breadcrumb}
        onActivateContext={onActivateContext}
        onOpenInNewTab={onOpenInNewTab}
      />
      <CurrentContextShell
        activeContext={activeContext}
        loading={loading}
        error={error}
      >
        <ChildContextGrid
          children={childViews}
          onActivateContext={onActivateContext}
          onOpenInNewTab={onOpenInNewTab}
        />
      </CurrentContextShell>
    </section>
  );
}

const styles: Record<string, React.CSSProperties> = {
  root: {
    display: "flex",
    flexDirection: "column",
    width: "100%",
    height: "100%",
    minWidth: 0,
    minHeight: 0,
    overflow: "hidden",
    background: "var(--syn-structure-bg)",
  },
};
