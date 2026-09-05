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

import type { DisplayMode } from "../core/types.js";
import { PathRegistry } from "./PathRegistry.js";

export type DisplayModeListener = (mode: DisplayMode) => void;

export class ProjectionState {
  readonly paths: PathRegistry = new PathRegistry();

  private displayMode: DisplayMode = "idle";
  private readonly displayModeListeners: Set<DisplayModeListener> = new Set();

  getDisplayMode(): DisplayMode {
    return this.displayMode;
  }

  setDisplayMode(mode: DisplayMode): void {
    if (this.displayMode === mode) return;

    this.displayMode = mode;
    this.notifyDisplayMode();
  }

  subscribeToDisplayMode(listener: DisplayModeListener): () => void {
    this.displayModeListeners.add(listener);

    return () => {
      this.displayModeListeners.delete(listener);
    };
  }

  clear(): void {
    this.paths.clear();
    this.displayMode = "idle";
    this.notifyDisplayMode();
  }

  clearListeners(): void {
    this.paths.clearListeners();
    this.displayModeListeners.clear();
  }

  private notifyDisplayMode(): void {
    for (const listener of this.displayModeListeners) {
      listener(this.displayMode);
    }
  }
}
