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

import type { ActiveContextActivationRequest, ContextNodeView } from "./contracts.js";

export type ContextNavigationControlsProps = {
  breadcrumb: ContextNodeView[];
  onActivateContext: (request: ActiveContextActivationRequest) => void;
  onOpenInNewTab?: (contextPath: string) => void;
};

export function ContextNavigationControls({
  breadcrumb,
  onActivateContext,
  onOpenInNewTab,
}: ContextNavigationControlsProps) {
  return (
    <div style={styles.controls}>
      {breadcrumb.map((segment, index) => {
        const isLast = index === breadcrumb.length - 1;
        return (
          <span key={segment.key} style={styles.crumbRow}>
            {index > 0 && <span style={styles.separator}>/</span>}
            <span
              style={{
                ...styles.crumb,
                ...(isLast ? styles.crumbCurrent : styles.crumbAncestor),
              }}
              onClick={isLast ? undefined : () => onActivateContext({ target: segment.key })}
              onAuxClick={(e) => { if (e.button === 1) { e.preventDefault(); onOpenInNewTab?.(segment.key); } }}
            >
              {segment.label}
            </span>
          </span>
        );
      })}
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  controls: {
    display: "flex",
    flexWrap: "wrap",
    alignItems: "center",
    gap: "2px",
    padding: "8px 12px",
    borderBottom: "1px solid var(--syn-border-medium)",
    fontSize: "12px",
    minHeight: "36px",
  },
  crumbRow: {
    display: "flex",
    alignItems: "center",
    gap: "2px",
  },
  separator: {
    opacity: 0.4,
    marginRight: "2px",
  },
  crumb: {
    padding: "2px 4px",
    borderRadius: "3px",
  },
  crumbAncestor: {
    cursor: "pointer",
    opacity: 0.7,
  },
  crumbCurrent: {
    cursor: "default",
    opacity: 1,
    fontWeight: 700,
  },
};
