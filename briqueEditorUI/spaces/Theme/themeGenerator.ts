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

import type { ThemeIntensity, ThemeIntent, ThemeVariation } from "./contracts.js";

type Hsl = { h: number; s: number; l: number };

const intensityConfig: Record<ThemeIntensity, {
  saturationFactor: number;
  contrastStep: number;
  borderOpacity: number;
  activeOpacity: number;
}> = {
  discreet: {
    saturationFactor: 0.65,
    contrastStep: 4,
    borderOpacity: 0.08,
    activeOpacity: 0.16,
  },
  standard: {
    saturationFactor: 0.85,
    contrastStep: 7,
    borderOpacity: 0.14,
    activeOpacity: 0.22,
  },
  marked: {
    saturationFactor: 1.05,
    contrastStep: 10,
    borderOpacity: 0.22,
    activeOpacity: 0.32,
  },
};

const variationHueOffset: Record<ThemeVariation, number> = {
  monochrome: 0,
  soft: 10,
  contrasted: 22,
};

export function generateThemeCss(intent: ThemeIntent): string {
  const base = hexToHsl(intent.baseColor);
  const intensity = intensityConfig[intent.intensity];
  const offset = variationHueOffset[intent.variation];

  const navigation = world(base, -offset, intensity.saturationFactor);
  const projection = world(base, 0, intensity.saturationFactor);
  const observation = world(base, offset, intensity.saturationFactor);
  const accent = hsl({ h: base.h, s: clamp(base.s * 1.15, 42, 78), l: 64 });
  const accentHover = hsl({ h: base.h, s: clamp(base.s * 1.2, 46, 84), l: 70 });
  const info = hsl({ h: base.h, s: clamp(base.s * 1.1, 42, 74), l: 68 });
  const step = intensity.contrastStep;
  const appBg = hsl({ h: base.h, s: 10, l: 8 });
  const navPanelBg = hsl({ ...navigation, l: 15 + step * 0.2 });
  const navButtonHoverBg = alphaFromHsl(navigation, 70, intensity.activeOpacity * 0.75);
  const navButtonActiveBg = alphaFromHsl(navigation, 72, intensity.activeOpacity);
  const projectionPanelBg = hsl({ ...projection, l: 13 + step * 0.15 });
  const projectionSectionBg = hsl({ ...projection, l: 15 + step * 0.28 });
  const projectionCardBg = hsl({ ...projection, l: 18 + step * 0.3 });
  const structureBg = hsl({ ...projection, l: 11 + step * 0.2 });
  const structurePanelBg = hsl({ ...projection, l: 14 + step * 0.25 });
  const flow = { ...projection, h: rotate(projection.h, -6) };
  const semantic = { ...projection, h: rotate(projection.h, 6) };
  const trace = { ...projection, h: rotate(projection.h, 12) };
  const observationPanelBg = hsl({ ...observation, l: 14 + step * 0.18 });
  const observationSectionBg = hsl({ ...observation, l: 16 + step * 0.25 });
  const observationCardBg = hsl({ ...observation, l: 19 + step * 0.28 });
  const selectedBg = alphaFromHsl(base, 68, intensity.activeOpacity);
  const selectedBorder = alphaFromHsl(base, 74, Math.min(0.72, intensity.activeOpacity + 0.3));

  const vars: Record<string, string> = {
    "--syn-app-bg": appBg,
    "--syn-surface-0": hsl({ h: base.h, s: 10, l: 10 }),
    "--syn-surface-1": hsl({ h: base.h, s: 11, l: 13 }),
    "--syn-surface-2": hsl({ h: base.h, s: 12, l: 17 }),
    "--syn-surface-3": hsl({ h: base.h, s: 13, l: 21 }),
    "--syn-surface-overlay": hsl({ h: base.h, s: 12, l: 14 + step * 0.2 }),

    "--syn-text-primary": "hsl(210 20% 92%)",
    "--syn-text-secondary": "hsl(210 14% 74%)",
    "--syn-text-muted": "hsl(210 10% 56%)",
    "--syn-text-disabled": "hsl(210 8% 36%)",
    "--syn-text-inverse": "hsl(210 24% 8%)",

    "--syn-border-soft": alpha("255,255,255", intensity.borderOpacity),
    "--syn-border-medium": alpha("255,255,255", intensity.borderOpacity + 0.08),
    "--syn-border-strong": alpha("255,255,255", intensity.borderOpacity + 0.18),

    "--syn-nav-bg": hsl({ ...navigation, l: 12 }),
    "--syn-nav-panel-bg": navPanelBg,
    "--syn-nav-border": alphaFromHsl(navigation, 72, intensity.borderOpacity + 0.04),
    "--syn-nav-title": hsl({ ...navigation, l: 78 }),
    "--syn-nav-button-bg": "transparent",
    "--syn-nav-button-hover-bg": navButtonHoverBg,
    "--syn-nav-button-active-bg": navButtonActiveBg,
    "--syn-nav-button-active-border": alphaFromHsl(navigation, 76, Math.min(0.72, intensity.activeOpacity + 0.28)),
    "--syn-nav-tab-bg": "transparent",
    "--syn-nav-tab-active-bg": navButtonActiveBg,
    "--syn-nav-tab-active-border": accent,

    "--syn-projection-bg": hsl({ ...projection, l: 9 }),
    "--syn-projection-panel-bg": projectionPanelBg,
    "--syn-projection-section-bg": projectionSectionBg,
    "--syn-projection-card-bg": projectionCardBg,
    "--syn-projection-border": alphaFromHsl(projection, 72, intensity.borderOpacity),
    "--syn-projection-toolbar-bg": alphaFromHsl(projection, 58, 0.1 + intensity.activeOpacity * 0.14),
    "--syn-projection-toolbar-border": alphaFromHsl(projection, 72, intensity.borderOpacity + 0.04),

    "--syn-structure-bg": structureBg,
    "--syn-structure-panel-bg": structurePanelBg,
    "--syn-structure-node-bg": projectionCardBg,
    "--syn-structure-node-border": alphaFromHsl(projection, 72, intensity.borderOpacity + 0.06),
    "--syn-flow-bg": hsl({ ...flow, l: 10 + step * 0.12 }),
    "--syn-flow-panel-bg": hsl({ ...flow, l: 13 + step * 0.18 }),
    "--syn-flow-node-bg": alphaFromHsl(flow, 62, 0.14 + intensity.activeOpacity * 0.2),
    "--syn-flow-node-border": alphaFromHsl(flow, 72, intensity.borderOpacity + 0.06),
    "--syn-semantic-bg": hsl({ ...semantic, l: 10 + step * 0.12 }),
    "--syn-semantic-panel-bg": hsl({ ...semantic, l: 13 + step * 0.18 }),
    "--syn-semantic-window-bg": alphaFromHsl(semantic, 62, 0.14 + intensity.activeOpacity * 0.2),
    "--syn-semantic-window-border": alphaFromHsl(semantic, 72, intensity.borderOpacity + 0.06),
    "--syn-trace-bg": hsl({ ...trace, l: 10 + step * 0.12 }),
    "--syn-trace-panel-bg": hsl({ ...trace, l: 13 + step * 0.18 }),
    "--syn-trace-event-bg": alphaFromHsl(trace, 62, 0.14 + intensity.activeOpacity * 0.2),
    "--syn-trace-event-border": alphaFromHsl(trace, 72, intensity.borderOpacity + 0.06),

    "--syn-observation-bg": hsl({ ...observation, l: 10 }),
    "--syn-observation-panel-bg": observationPanelBg,
    "--syn-observation-section-bg": observationSectionBg,
    "--syn-observation-card-bg": observationCardBg,
    "--syn-observation-border": alphaFromHsl(observation, 72, intensity.borderOpacity),
    "--syn-inspection-bg": alphaFromHsl(observation, 58, 0.12 + intensity.activeOpacity * 0.18),
    "--syn-inspection-panel-bg": alphaFromHsl({ ...observation, h: rotate(observation.h, -4) }, 58, 0.12 + intensity.activeOpacity * 0.18),
    "--syn-inspection-card-bg": alphaFromHsl({ ...observation, h: rotate(observation.h, 4) }, 58, 0.12 + intensity.activeOpacity * 0.18),
    "--syn-operational-bg": hsl({ ...observation, h: rotate(observation.h, 8), l: 11 + step * 0.16 }),
    "--syn-operational-panel-bg": observationPanelBg,
    "--syn-operational-section-bg": observationSectionBg,
    "--syn-operational-card-bg": observationCardBg,

    "--syn-hover-bg": alphaFromHsl(base, 70, intensity.activeOpacity * 0.65),
    "--syn-active-bg": alphaFromHsl(base, 72, intensity.activeOpacity),
    "--syn-selected-bg": selectedBg,
    "--syn-selected-border": selectedBorder,
    "--syn-focus-ring": alphaFromHsl(base, 76, 0.72),
    "--syn-control-bg": alphaFromHsl(base, 58, 0.08 + intensity.activeOpacity * 0.18),
    "--syn-control-hover-bg": alphaFromHsl(base, 64, 0.1 + intensity.activeOpacity * 0.22),
    "--syn-control-active-bg": alphaFromHsl(base, 68, 0.12 + intensity.activeOpacity * 0.32),
    "--syn-control-border": alphaFromHsl(base, 72, intensity.borderOpacity + 0.08),
    "--syn-control-disabled-bg": alpha("255,255,255", 0.04),
    "--syn-control-disabled-text": "hsl(210 8% 36%)",

    "--syn-feedback-error": "hsl(0 78% 70%)",
    "--syn-feedback-error-bg": "hsl(0 78% 70% / 0.14)",
    "--syn-feedback-warning": "hsl(42 92% 72%)",
    "--syn-feedback-warning-bg": "hsl(42 92% 72% / 0.14)",
    "--syn-feedback-success": "hsl(140 58% 68%)",
    "--syn-feedback-success-bg": "hsl(140 58% 68% / 0.14)",
    "--syn-feedback-info": info,
    "--syn-feedback-info-bg": alphaFromHsl(base, 68, 0.14),

    "--syn-bg-app": appBg,
    "--syn-nav-surface": navPanelBg,
    "--syn-nav-hover": navButtonHoverBg,
    "--syn-nav-active": navButtonActiveBg,
    "--syn-projection-panel": projectionPanelBg,
    "--syn-projection-structure": alphaFromHsl(projection, 66, intensity.activeOpacity * 0.55),
    "--syn-projection-flow": alphaFromHsl(flow, 66, intensity.activeOpacity * 0.55),
    "--syn-projection-semantic": alphaFromHsl(semantic, 66, intensity.activeOpacity * 0.55),
    "--syn-projection-trace": alphaFromHsl(trace, 66, intensity.activeOpacity * 0.55),
    "--syn-projection-hierarchy-bg": structureBg,
    "--syn-projection-focus-bg": structurePanelBg,
    "--syn-observation-panel": observationPanelBg,
    "--syn-inspect-bg": alphaFromHsl(observation, 58, 0.12 + intensity.activeOpacity * 0.18),
    "--syn-browse-bg": alphaFromHsl({ ...observation, h: rotate(observation.h, -4) }, 58, 0.12 + intensity.activeOpacity * 0.18),
    "--syn-search-bg": alphaFromHsl({ ...observation, h: rotate(observation.h, 4) }, 58, 0.12 + intensity.activeOpacity * 0.18),
    "--syn-accent": accent,
    "--syn-accent-hover": accentHover,
    "--syn-selection-bg": selectedBg,
    "--syn-selection-border": selectedBorder,
    "--syn-error": "var(--syn-feedback-error)",
    "--syn-warning": "var(--syn-feedback-warning)",
    "--syn-success": "var(--syn-feedback-success)",
  };

  return `:root {\n${Object.entries(vars).map(([name, value]) => `  ${name}: ${value};`).join("\n")}\n}`;
}

function world(base: Hsl, hueOffset: number, saturationFactor: number): Hsl {
  return {
    h: rotate(base.h, hueOffset),
    s: clamp(base.s * saturationFactor, 14, 58),
    l: base.l,
  };
}

function hexToHsl(hex: string): Hsl {
  const normalized = hex.replace("#", "").trim();
  const value = /^[0-9a-fA-F]{6}$/.test(normalized) ? normalized : "1f5f73";
  const r = parseInt(value.slice(0, 2), 16) / 255;
  const g = parseInt(value.slice(2, 4), 16) / 255;
  const b = parseInt(value.slice(4, 6), 16) / 255;
  const max = Math.max(r, g, b);
  const min = Math.min(r, g, b);
  let h = 0;
  let s = 0;
  const l = (max + min) / 2;
  const d = max - min;
  if (d !== 0) {
    s = d / (1 - Math.abs(2 * l - 1));
    switch (max) {
      case r: h = 60 * (((g - b) / d) % 6); break;
      case g: h = 60 * ((b - r) / d + 2); break;
      default: h = 60 * ((r - g) / d + 4); break;
    }
  }
  return { h: rotate(h, 0), s: s * 100, l: l * 100 };
}

function hsl(color: Hsl): string {
  return `hsl(${Math.round(rotate(color.h, 0))} ${Math.round(clamp(color.s, 0, 100))}% ${Math.round(clamp(color.l, 0, 100))}%)`;
}

function alphaFromHsl(color: Hsl, lightness: number, opacity: number): string {
  return `hsl(${Math.round(rotate(color.h, 0))} ${Math.round(clamp(color.s, 0, 100))}% ${Math.round(clamp(lightness, 0, 100))}% / ${clamp(opacity, 0, 1).toFixed(2)})`;
}

function alpha(rgb: string, opacity: number): string {
  return `rgba(${rgb}, ${clamp(opacity, 0, 1).toFixed(2)})`;
}

function rotate(hue: number, offset: number): number {
  return ((hue + offset) % 360 + 360) % 360;
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value));
}
