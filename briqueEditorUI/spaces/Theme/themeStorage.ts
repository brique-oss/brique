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

import type { CapabilityClient } from "../../Brique_Substrate/capability/index.js";
import { DEFAULT_THEME_INTENT, type ThemeIntent } from "./contracts.js";

const ROOT_CONTEXT = "/root";
const THEME_MATTER_ID = "ui_theme";

export async function loadThemeIntent(client: CapabilityClient): Promise<ThemeIntent> {
  const exists = await client.matter.exists(ROOT_CONTEXT, { matter_id: THEME_MATTER_ID });
  if (!exists.ok || exists.payload?.exist !== true) return DEFAULT_THEME_INTENT;

  const read = await client.matter.read(ROOT_CONTEXT, {
    matter_id: THEME_MATTER_ID,
    want_data: true,
  });
  if (!read.ok) return DEFAULT_THEME_INTENT;

  return parseThemeIntentFromData(read.payload?.data) ?? DEFAULT_THEME_INTENT;
}

export async function saveThemeIntent(client: CapabilityClient, intent: ThemeIntent): Promise<void> {
  // brique-mode matters with the default (text) substance_type serialize a
  // plain string payload as the file's raw bytes, and matter.read returns
  // that same raw text back unchanged — so the matter's substance IS the
  // JSON text directly, no inline/base64 envelope needed on either side.
  const data = JSON.stringify(normalizeThemeIntent(intent));
  const exists = await client.matter.exists(ROOT_CONTEXT, { matter_id: THEME_MATTER_ID });
  const shouldWrite = exists.ok && exists.payload?.exist === true;
  const result = shouldWrite
    ? await client.matter.write(ROOT_CONTEXT, { matter_id: THEME_MATTER_ID, data })
    : await client.matter.create(ROOT_CONTEXT, { matter_id: THEME_MATTER_ID, payload: data });
  if (!result.ok) {
    throw new Error(result.error?.message ?? "Unable to save UI theme");
  }
}

function parseThemeIntentFromData(data: unknown): ThemeIntent | undefined {
  const text = extractInlineText(data);
  if (!text) return undefined;
  try {
    return normalizeThemeIntent(JSON.parse(text));
  } catch {
    return undefined;
  }
}

// matter.read's brique-mode inline payload shape is { kind: "inline", bytes: string, size: number }
// with bytes carrying the raw text as-is (substance_type defaults to "text").
function extractInlineText(value: unknown): string | undefined {
  if (typeof value !== "object" || value === null) return undefined;
  const record = value as Record<string, unknown>;
  return record.kind === "inline" && typeof record.bytes === "string"
    ? record.bytes
    : undefined;
}

function normalizeThemeIntent(value: unknown): ThemeIntent {
  if (typeof value !== "object" || value === null) return DEFAULT_THEME_INTENT;
  const record = value as Record<string, unknown>;
  const baseColor = typeof record.baseColor === "string" && /^#[0-9a-fA-F]{6}$/.test(record.baseColor)
    ? record.baseColor
    : DEFAULT_THEME_INTENT.baseColor;
  const variation = record.variation === "monochrome" || record.variation === "soft" || record.variation === "contrasted"
    ? record.variation
    : DEFAULT_THEME_INTENT.variation;
  const intensity = record.intensity === "discreet" || record.intensity === "standard" || record.intensity === "marked"
    ? record.intensity
    : DEFAULT_THEME_INTENT.intensity;
  return { version: 1, baseColor, variation, intensity };
}
