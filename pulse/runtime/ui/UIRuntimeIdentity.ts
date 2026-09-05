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

import type { UIPath } from "../core/types.js";

const DEFAULT_UI_NAME = "main";

export type UIRuntimeIdentityConfig = {
  uiName?: string;
  uiAddressPrefix?: string;
};

export class UIRuntimeIdentity {
  private readonly uiName: string;
  private readonly uiAddressPrefix: string;

  constructor(config: UIRuntimeIdentityConfig = {}) {
    this.uiName = normalizeUIName(config.uiName);
    this.uiAddressPrefix =
      normalizeAddressPrefix(config.uiAddressPrefix) ?? `@ui_${this.uiName}`;
  }

  getName(): string {
    return this.uiName;
  }

  getAddressPrefix(): string {
    return this.uiAddressPrefix;
  }

  addressForPath(pathUI: UIPath): string {
    return `${this.uiAddressPrefix}:${normalizeUIPath(pathUI)}`;
  }
}

function normalizeUIName(uiName: string | undefined): string {
  const normalized = uiName?.trim();
  return normalized || DEFAULT_UI_NAME;
}

function normalizeAddressPrefix(prefix: string | undefined): string | null {
  const normalized = prefix?.trim();
  if (!normalized) return null;
  return normalized.startsWith("@") ? normalized : `@${normalized}`;
}

function normalizeUIPath(pathUI: UIPath): UIPath {
  const normalized = pathUI.trim();
  if (!normalized) return "/";
  return normalized.startsWith("/") ? normalized : `/${normalized}`;
}
