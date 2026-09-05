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

import { useEffect, useRef } from "react";
import type {
  MatterEventSnapshot,
  MatterSnapshot,
} from "../../runtime/index.js";
import type { MatterHookAddress, MatterOptions } from "./useMatter.js";
import { useMatterSubscription } from "./useMatterSubscription.js";

export type MatterEventCallback = (
  event: MatterEventSnapshot,
  snapshot: MatterSnapshot
) => void;

export function useMatterEvent(
  address: MatterHookAddress,
  callback: MatterEventCallback,
  options: MatterOptions = {}
): MatterSnapshot {
  const snapshot = useMatterSubscription(address, options);
  const callbackRef = useRef(callback);
  const lastEventRef = useRef<MatterEventSnapshot | null>(null);

  useEffect(() => {
    callbackRef.current = callback;
  }, [callback]);

  useEffect(() => {
    const event = snapshot.lastEvent;
    if (!event || event === lastEventRef.current) return;

    lastEventRef.current = event;
    callbackRef.current(event, snapshot);
  }, [snapshot, snapshot.lastEvent]);

  return snapshot;
}
