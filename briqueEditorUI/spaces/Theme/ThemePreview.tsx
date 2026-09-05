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

import { generateThemeCss } from "./themeGenerator.js";
import type { ThemeIntent } from "./contracts.js";

export type ThemePreviewProps = {
  intent: ThemeIntent;
};

export function ThemePreview({ intent }: ThemePreviewProps) {
  return (
    <div style={styles.root}>
      <style>{scopePreviewCss(generateThemeCss(intent))}</style>
      <div style={styles.nav}>
        <span style={styles.navButton}>Structure</span>
        <span style={styles.navButtonActive}>Semantic</span>
        <span style={styles.navButton}>Flow</span>
        <span style={styles.navButton}>Trace</span>
      </div>
      <div style={styles.body}>
        <div style={styles.projection}>
          <div style={styles.panelTitle}>Projection</div>
          <div style={styles.projectionTabs}>
            <span style={styles.projectionTab}>Structure</span>
            <span style={styles.projectionTab}>Flow</span>
            <span style={styles.projectionTab}>Semantic</span>
            <span style={styles.projectionTab}>Trace</span>
          </div>
          <div style={styles.section}>
            <div style={styles.selected}>Selected item</div>
            <div style={styles.card}>Card</div>
            <div style={styles.cardMuted}>Section</div>
          </div>
        </div>
        <div style={styles.observation}>
          <div style={styles.panelTitle}>Observation</div>
          <div style={styles.inspect}>Inspection</div>
          <div style={styles.operational}>Operational State</div>
        </div>
      </div>
    </div>
  );
}

function scopePreviewCss(css: string): string {
  return css.replace(":root", "[data-syn-theme-preview]");
}

const styles: Record<string, React.CSSProperties> = {
  root: {
    display: "grid",
    gridTemplateRows: "34px 1fr",
    height: 240,
    border: "1px solid var(--syn-border-medium)",
    borderRadius: 8,
    overflow: "hidden",
    background: "var(--syn-app-bg)",
    color: "var(--syn-text-primary)",
  },
  nav: {
    display: "flex",
    alignItems: "center",
    gap: 6,
    padding: "6px 8px",
    background: "var(--syn-nav-bg)",
    borderBottom: "1px solid var(--syn-nav-border)",
  },
  navButton: {
    padding: "4px 8px",
    borderRadius: 4,
    color: "var(--syn-text-muted)",
    background: "transparent",
    fontSize: 10,
  },
  navButtonActive: {
    padding: "4px 8px",
    borderRadius: 4,
    color: "var(--syn-nav-title)",
    background: "var(--syn-nav-button-active-bg)",
    fontSize: 10,
  },
  body: {
    display: "grid",
    gridTemplateColumns: "1fr 150px",
    gap: 8,
    padding: 8,
  },
  projection: {
    padding: 8,
    borderRadius: 8,
    background: "var(--syn-projection-bg)",
    border: "1px solid var(--syn-projection-border)",
  },
  observation: {
    display: "flex",
    flexDirection: "column",
    gap: 8,
    padding: 8,
    borderRadius: 8,
    background: "var(--syn-observation-bg)",
    border: "1px solid var(--syn-observation-border)",
  },
  panelTitle: {
    marginBottom: 8,
    color: "var(--syn-text-secondary)",
    fontSize: 10,
    fontWeight: 700,
    textTransform: "uppercase",
    letterSpacing: "0.08em",
  },
  projectionTabs: {
    display: "grid",
    gridTemplateColumns: "repeat(4, 1fr)",
    gap: 5,
    marginBottom: 8,
  },
  projectionTab: {
    padding: "5px 4px",
    borderRadius: 5,
    background: "var(--syn-projection-section-bg)",
    color: "var(--syn-text-secondary)",
    fontSize: 9,
    textAlign: "center",
  },
  section: {
    display: "grid",
    gridTemplateColumns: "1fr 1fr",
    gap: 8,
    padding: 8,
    borderRadius: 8,
    background: "var(--syn-projection-panel)",
  },
  selected: {
    gridColumn: "span 2",
    padding: 10,
    borderRadius: 6,
    background: "var(--syn-selected-bg)",
    border: "1px solid var(--syn-selected-border)",
    color: "var(--syn-text-primary)",
    fontSize: 11,
  },
  card: {
    padding: 10,
    borderRadius: 6,
    background: "var(--syn-projection-card-bg)",
    border: "1px solid var(--syn-border-soft)",
    color: "var(--syn-text-secondary)",
    fontSize: 10,
  },
  cardMuted: {
    padding: 10,
    borderRadius: 6,
    background: "var(--syn-projection-section-bg)",
    border: "1px solid var(--syn-border-soft)",
    color: "var(--syn-text-muted)",
    fontSize: 10,
  },
  inspect: {
    padding: 10,
    borderRadius: 6,
    background: "var(--syn-inspection-bg)",
    border: "1px solid var(--syn-border-soft)",
    color: "var(--syn-text-secondary)",
    fontSize: 10,
  },
  operational: {
    padding: 10,
    borderRadius: 6,
    background: "var(--syn-operational-bg)",
    border: "1px solid var(--syn-border-soft)",
    color: "var(--syn-text-secondary)",
    fontSize: 10,
  },
};
