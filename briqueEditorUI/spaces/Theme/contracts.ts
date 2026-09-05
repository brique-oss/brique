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

export type ThemeVariation = "monochrome" | "soft" | "contrasted";
export type ThemeIntensity = "discreet" | "standard" | "marked";

export type ThemeIntent = {
  version: 1;
  baseColor: string;
  variation: ThemeVariation;
  intensity: ThemeIntensity;
};

export const DEFAULT_THEME_INTENT: ThemeIntent = {
  version: 1,
  baseColor: "#1f5f73",
  variation: "soft",
  intensity: "standard",
};

export const THEME_VARIATIONS: Array<{ value: ThemeVariation; label: string }> = [
  { value: "monochrome", label: "Monochrome" },
  { value: "soft", label: "Soft" },
  { value: "contrasted", label: "Contrasted" },
];

export const THEME_INTENSITIES: Array<{ value: ThemeIntensity; label: string }> = [
  { value: "discreet", label: "Discreet" },
  { value: "standard", label: "Standard" },
  { value: "marked", label: "Marked" },
];
