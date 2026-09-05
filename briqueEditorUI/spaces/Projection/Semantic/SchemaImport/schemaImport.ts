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

import type { VocabularyPath } from "../contracts.js";

export type SchemaWindowSpec = {
  path: VocabularyPath;
  defaultKeys: string[];
};

export function parseSchemaAddress(input: string): { context: string; elementName: string } | null {
  const sep = input.indexOf("::");
  if (sep === -1) return null;
  const context = input.slice(0, sep).trim();
  const elementName = input.slice(sep + 2).trim();
  if (!context.startsWith("/") || !elementName) return null;
  return { context, elementName };
}

export function extractWindowSpecsFromSchema(fields: unknown): SchemaWindowSpec[] {
  const map = new Map<string, SchemaWindowSpec>();

  function walk(node: unknown, path: string[]): void {
    if (!node || typeof node !== "object" || Array.isArray(node)) return;
    for (const [key, value] of Object.entries(node as Record<string, unknown>)) {
      const childPath = [...path, key];
      if (Array.isArray(value)) {
        const defaultKeys = value.filter((v): v is string => typeof v === "string");
        map.set(childPath.join("."), { path: childPath, defaultKeys });
      } else if (value && typeof value === "object") {
        walk(value, childPath);
      }
    }
  }

  walk(fields, []);
  return Array.from(map.values());
}
