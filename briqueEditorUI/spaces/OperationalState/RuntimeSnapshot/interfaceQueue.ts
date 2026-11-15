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

import type { InterfaceQueueSnapshot } from "../../../Brique_Substrate/capability/typed.js";

// One interface: the queue in front of its writer and what the routing loops met there. "full" counts the
// messages that found the queue full and had to wait for a place; "given up" counts the times a consumer
// that stopped reading was cut off. A steady "full" means the consumer is slower than the producer; "full" on
// bursts only means the queue (egress_buffer) is smaller than the burst.
export function interfaceQueueTone(item: InterfaceQueueSnapshot): "error" | "warning" | "normal" {
  if (item.given_up > 0) return "error";
  if (item.found_full > 0) return "warning";
  return "normal";
}

export function formatMs(value: number): string {
  if (value >= 100) return `${Math.round(value)} ms`;
  if (value >= 1) return `${value.toFixed(1)} ms`;
  return `${value.toFixed(2)} ms`;
}
