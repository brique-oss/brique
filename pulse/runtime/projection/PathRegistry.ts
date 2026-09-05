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

import type { IntentionId, UIPath } from "../core/types.js";

export type PathListener = (
  path: UIPath,
  intentionId: IntentionId | null
) => void;

export type PathSnapshot = {
  pathUI: UIPath;
  intentionId: IntentionId | null;
  active: boolean;
};

export type BlockEntry = {
  pathUI: UIPath;
  componentName: string;
};

export class PathRegistry {
  private readonly activePaths: Map<UIPath, IntentionId | null> = new Map();
  private readonly listeners: Set<PathListener> = new Set();
  private readonly pathListeners: Map<UIPath, Set<() => void>> = new Map();
  private readonly snapshots: Map<UIPath, PathSnapshot> = new Map();
  private readonly blocks: Map<UIPath, string> = new Map();

  activatePath(path: UIPath, intentionId: IntentionId): void {
    this.activePaths.set(path, intentionId);
    this.snapshots.delete(path);
    this.notify(path, intentionId);
  }

  deactivatePath(path: UIPath): void {
    this.activePaths.set(path, null);
    this.snapshots.delete(path);
    this.notify(path, null);
  }

  isPathActive(path: UIPath): boolean {
    return this.activePaths.get(path) != null;
  }

  getIntentionIdForPath(path: UIPath): IntentionId | null {
    return this.activePaths.get(path) ?? null;
  }

  clear(): void {
    const paths = Array.from(this.activePaths.keys());
    this.activePaths.clear();

    for (const path of paths) {
      this.snapshots.delete(path);
      this.notify(path, null);
    }
  }

  registerBlock(pathUI: UIPath, componentName: string): void {
    this.blocks.set(pathUI, componentName);
  }

  unregisterBlock(pathUI: UIPath): void {
    this.blocks.delete(pathUI);
  }

  getActiveBlocks(): BlockEntry[] {
    return Array.from(this.blocks.entries()).map(([pathUI, componentName]) => ({
      pathUI,
      componentName,
    }));
  }

  clearListeners(): void {
    this.listeners.clear();
    this.pathListeners.clear();
  }

  subscribe(listener: PathListener): () => void {
    this.listeners.add(listener);

    return () => {
      this.listeners.delete(listener);
    };
  }

  getSnapshot(path: UIPath): PathSnapshot {
    const cached = this.snapshots.get(path);
    if (cached) return cached;

    const intentionId = this.getIntentionIdForPath(path);
    const snapshot = Object.freeze({
      pathUI: path,
      intentionId,
      active: intentionId !== null,
    });

    this.snapshots.set(path, snapshot);
    return snapshot;
  }

  subscribePath(path: UIPath, listener: () => void): () => void {
    let set = this.pathListeners.get(path);
    if (!set) {
      set = new Set();
      this.pathListeners.set(path, set);
    }

    set.add(listener);

    return () => {
      const current = this.pathListeners.get(path);
      if (!current) return;

      current.delete(listener);

      if (current.size === 0) {
        this.pathListeners.delete(path);
      }
    };
  }

  private notify(path: UIPath, intentionId: IntentionId | null): void {
    for (const listener of this.listeners) {
      listener(path, intentionId);
    }

    const listeners = this.pathListeners.get(path);
    if (!listeners) return;

    for (const listener of listeners) {
      listener();
    }
  }
}
