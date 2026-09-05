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

import type { InspectionDisplayMode } from "./contracts.js";

export type InspectionDisplayControlProps = {
  displayMode: InspectionDisplayMode;
  onDisplayModeChange: (mode: InspectionDisplayMode) => void;
};

export function InspectionDisplayControl({
  displayMode,
  onDisplayModeChange,
}: InspectionDisplayControlProps) {
  const expanded = displayMode === "expanded";
  const label = expanded ? "Collapse inspection" : "Expand inspection";

  return (
    <button
      aria-label={label}
      onClick={() => onDisplayModeChange(expanded ? "collapsed" : "expanded")}
      style={styles.control}
      title={label}
      type="button"
    >
      {expanded ? "▶" : "◀"}
    </button>
  );
}

const styles: Record<string, React.CSSProperties> = {
  control: {
    width: "24px",
    height: "24px",
    flex: "0 0 24px",
    border: "1px solid var(--syn-control-border)",
    borderRadius: "4px",
    background: "var(--syn-control-bg)",
    color: "var(--syn-text-primary)",
    cursor: "pointer",
    fontSize: "10px",
  },
};
