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

import { useEffect, useSyncExternalStore } from "react";
import type { MatterSnapshot } from "../../runtime/index.js";
import type { MatterHookAddress, MatterOptions } from "./useMatter.js";
import { usePulse } from "./usePulse.js";

export function useMatterSubscription(
  address: MatterHookAddress,
  options: MatterOptions = {}
): MatterSnapshot {
  const pulse = usePulse();
  const key = pulse.matter.normalizeKey({
    context: address.context,
    matterId: address.matterId,
    readMode: options.readMode ?? null,
  });

  const snapshot = useSyncExternalStore(
    (onStoreChange) =>
      pulse.matter.subscribeToSnapshot(key, onStoreChange),
    () => pulse.matter.getSnapshot(key),
    () => pulse.matter.getSnapshot(null)
  );

  useEffect(() => {
    pulse.matter.acquireSubscription(key);

    return () => {
      pulse.matter.releaseSubscription(key);
    };
  }, [pulse, key.context, key.matterId, key.readMode]);

  return snapshot;
}
