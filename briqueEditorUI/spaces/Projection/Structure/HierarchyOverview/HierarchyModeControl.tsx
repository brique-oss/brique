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

import type { HierarchyDisplayMode } from "./contracts.js";

export type HierarchyModeControlProps = {
  mode: HierarchyDisplayMode;
  onModeChange: (mode: HierarchyDisplayMode) => void;
};

export function HierarchyModeControl({
  mode,
  onModeChange,
}: HierarchyModeControlProps) {
  return (
    <div aria-label="Hierarchy representation" style={styles.control}>
      <button
        aria-pressed={mode === "expanded"}
        onClick={() => onModeChange("expanded")}
        style={styles.button}
        type="button"
      >
        Hierarchy
      </button>
      <button
        aria-pressed={mode === "collapsed"}
        onClick={() => onModeChange("collapsed")}
        style={styles.button}
        type="button"
      >
        Lineage
      </button>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  control: {
    display: "flex",
    gap: "6px",
    padding: "12px",
    borderBottom: "1px solid var(--syn-border-medium)",
  },
  button: {
    minWidth: 0,
  },
};
