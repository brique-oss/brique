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

import type { ProjectionKind } from "../Projection/contracts.js";
import type { TabId, TabState } from "./contracts.js";
import { TabBar } from "./TabBar.js";

export type NavigationSpaceProps = {
  activeProjection: ProjectionKind;
  activeContext: string | undefined;
  onActiveProjectionChange: (projection: ProjectionKind) => void;
  tabs: TabState[];
  activeTabId: TabId;
  showHistory: boolean;
  canGoBack: boolean;
  onActivateTab: (id: TabId) => void;
  onNewTab: () => void;
  onCloseTab: (id: TabId) => void;
  onReorderTabs: (fromIndex: number, toIndex: number) => void;
  onToggleHistory: () => void;
  onGoBack: () => void;
};

export function NavigationSpace({
  activeProjection,
  activeContext,
  onActiveProjectionChange,
  tabs,
  activeTabId,
  showHistory,
  canGoBack,
  onActivateTab,
  onNewTab,
  onCloseTab,
  onReorderTabs,
  onToggleHistory,
  onGoBack,
}: NavigationSpaceProps) {
  return (
    <nav aria-label="Brique Editor navigation" data-space="navigation" style={styles.root}>
      <TabBar
        tabs={tabs}
        activeTabId={activeTabId}
        activeProjection={activeProjection}
        activeContext={activeContext}
        showHistory={showHistory}
        canGoBack={canGoBack}
        onActivateTab={onActivateTab}
        onCloseTab={onCloseTab}
        onNewTab={onNewTab}
        onActiveProjectionChange={onActiveProjectionChange}
        onReorderTabs={onReorderTabs}
        onToggleHistory={onToggleHistory}
        onGoBack={onGoBack}
      />
    </nav>
  );
}

const styles: Record<string, React.CSSProperties> = {
  root: {
    display: "flex",
    alignItems: "center",
    justifyContent: "flex-start",
    width: "100%",
    height: "100%",
    boxSizing: "border-box",
    background: "var(--syn-nav-bg, #202124)",
    color: "var(--syn-text-primary, #e8eaed)",
    fontFamily: "monospace",
    fontWeight: 700,
  },
};
