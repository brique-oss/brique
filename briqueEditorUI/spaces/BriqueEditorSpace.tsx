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

import { useCallback, useRef, useState } from "react";
import { NavigationSpace } from "./Navigation/NavigationSpace.js";
import { TabInstance } from "./Navigation/TabInstance.js";
import type { TabId, TabState } from "./Navigation/contracts.js";
import { appendHistory, makeEmptyTab, makeSeededTab, nextTabId } from "./Navigation/contracts.js";
import type { InspectionTarget } from "./Inspection/contracts.js";

export function BriqueEditorSpace() {
  const [tabs, setTabs] = useState<TabState[]>(() => [makeEmptyTab(nextTabId())]);
  const [activeTabId, setActiveTabId] = useState<TabId>(() => tabs[0].id);

  const activeTab = tabs.find((t) => t.id === activeTabId) ?? tabs[0];

  // Flag: next selectedElement patch comes from history → skip re-log
  const fromHistoryRef = useRef(false);

  const patchTab = useCallback((id: TabId, patch: Partial<TabState>) => {
    setTabs((current) => current.map((t) => {
      if (t.id !== id) return t;
      const next = { ...t, ...patch };
      // Auto-log selectedElement changes unless they originate from history
      if (patch.selectedElement && patch.selectedElement !== t.selectedElement) {
        if (fromHistoryRef.current) {
          fromHistoryRef.current = false;
        } else {
          next.history = appendHistory(t.history, patch.selectedElement);
        }
      }
      return next;
    }));
  }, []);

  // Called by HistoryView — inspect only, no re-log
  const handleSelectFromHistory = useCallback((element: InspectionTarget) => {
    fromHistoryRef.current = true;
    patchTab(activeTabId, { selectedElement: element });
  }, [activeTabId, patchTab]);

  const handleNewTab = useCallback(() => {
    const tab = makeEmptyTab(nextTabId());
    setTabs((current) => [...current, tab]);
    setActiveTabId(tab.id);
  }, []);

  const handleCloseTab = useCallback((id: TabId) => {
    setTabs((current) => {
      if (current.length === 1) {
        const fresh = makeEmptyTab(nextTabId());
        setActiveTabId(fresh.id);
        return [fresh];
      }
      const next = current.filter((t) => t.id !== id);
      setActiveTabId((currentActive) => {
        if (currentActive !== id) return currentActive;
        const closedIndex = current.findIndex((t) => t.id === id);
        return (next[closedIndex] ?? next[closedIndex - 1] ?? next[0]).id;
      });
      return next;
    });
  }, []);

  const handleActivateTab = useCallback((id: TabId) => {
    setActiveTabId(id);
  }, []);

  const handleNewSeededTab = useCallback((element: InspectionTarget) => {
    const tab = makeSeededTab(nextTabId(), element);
    setTabs((current) => [...current, tab]);
    setActiveTabId(tab.id);
  }, []);

  const handleGoBack = useCallback(() => {
    const history = activeTab.history;
    if (history.length < 2) return;
    const previous = history[history.length - 2];
    fromHistoryRef.current = true;
    setTabs((current) => current.map((t) => {
      if (t.id !== activeTabId) return t;
      return { ...t, selectedElement: previous, history: t.history.slice(0, -1) };
    }));
  }, [activeTab.history, activeTabId]);

  const handleReorderTabs = useCallback((fromIndex: number, toIndex: number) => {
    setTabs((current) => {
      const next = [...current];
      const [moved] = next.splice(fromIndex, 1);
      next.splice(toIndex, 0, moved);
      return next;
    });
  }, []);

  return (
    <main data-space="brique-editor" style={styles.root}>
      <div style={styles.navigation}>
        <NavigationSpace
          activeProjection={activeTab.activeProjection}
          activeContext={activeTab.selectedElement?.context ?? activeTab.initialStructureContext}
          onActiveProjectionChange={(projection) => patchTab(activeTabId, { activeProjection: projection })}
          tabs={tabs}
          activeTabId={activeTabId}
          showHistory={activeTab.showHistory}
          canGoBack={activeTab.history.length > 1}
          onActivateTab={handleActivateTab}
          onNewTab={handleNewTab}
          onCloseTab={handleCloseTab}
          onReorderTabs={handleReorderTabs}
          onToggleHistory={() => patchTab(activeTabId, { showHistory: !activeTab.showHistory })}
          onGoBack={handleGoBack}
        />
      </div>

      <div style={styles.workspace}>
        {/* Normal tab instances — hidden when history is shown */}
        {tabs.map((tab) => (
          <TabInstance
            key={tab.id}
            tab={tab}
            active={tab.id === activeTabId}
            onPatch={patchTab}
            onNewSeededTab={handleNewSeededTab}
            onSelectFromHistory={handleSelectFromHistory}
          />
        ))}
      </div>
    </main>
  );
}

const styles: Record<string, React.CSSProperties> = {
  root: {
    position: "fixed",
    inset: 0,
    display: "grid",
    gridTemplateRows: "4fr 96fr",
    boxSizing: "border-box",
    overflow: "hidden",
  },
  navigation: {
    minHeight: 0,
  },
  workspace: {
    position: "relative",
    minHeight: 0,
  },
};
