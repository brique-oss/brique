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

import type { CirculationMessage } from "../circulation/index.js";
import type { IntentionId } from "../core/types.js";

export type SubscriptionCallback = (msg: CirculationMessage) => void;

export class SubscriptionRegistry {
  private registry: Map<IntentionId, Set<SubscriptionCallback>> = new Map();

  subscribe(intentionId: IntentionId, cb: SubscriptionCallback): () => void {
    let set = this.registry.get(intentionId);

    if (!set) {
      set = new Set();
      this.registry.set(intentionId, set);
    }

    set.add(cb);

    return () => {
      this.unsubscribe(intentionId, cb);
    };
  }

  unsubscribe(intentionId: IntentionId, cb: SubscriptionCallback): void {
    const set = this.registry.get(intentionId);
    if (!set) return;

    set.delete(cb);

    if (set.size === 0) {
      this.registry.delete(intentionId);
    }
  }

  notify(intentionId: IntentionId, msg: CirculationMessage): void {
    const set = this.registry.get(intentionId);
    if (!set) return;

    for (const cb of set) {
      cb(msg);
    }
  }

  hasSubscribers(intentionId: IntentionId): boolean {
    return this.registry.has(intentionId);
  }

  clear(): void {
    this.registry.clear();
  }
}
