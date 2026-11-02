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

import {
  isIntentionMessage,
  type CirculationMessage,
} from "../circulation/index.js";
import type { MatterEventSnapshot } from "./MatterRegistry.js";

export type MatterEventDispatch = MatterEventSnapshot & {
  subId: string | null;
};

export function isMatterEventMessage(
  message: CirculationMessage
): boolean {
  return (
    isIntentionMessage(message) &&
    message.intention.from.type === "matter" &&
    message.intention.from.cap === "matter_event"
  );
}

export function extractMatterEvent(
  message: CirculationMessage
): MatterEventDispatch | null {
  if (!isMatterEventMessage(message) || !isIntentionMessage(message)) {
    return null;
  }

  const params = message.intention.params ?? {};
  const event = params.event;
  const matterId = params.matter_id;

  if (
    (
      event !== "matter_written" &&
      event !== "matter_deleted" &&
      event !== "matter_appended" &&
      event !== "matter_truncated"
    ) ||
    typeof matterId !== "string" ||
    !matterId.trim()
  ) {
    return null;
  }

  return {
    event,
    matterId: matterId.trim(),
    revision: exactIntegerOrNull(params.revision),
    substanceMode: stringOrNull(params.substance_mode),
    sourceIntentionId: stringOrNull(params.source_intention_id),
    op: stringOrNull(params.op),
    subId: stringOrNull(params.sub_id),
    eventId: stringOrNull(params.event_id),
    byteOffset: exactIntegerOrNull(params.byte_offset),
    byteLength: exactIntegerOrNull(params.byte_length),
    totalBytes: exactIntegerOrNull(params.total_bytes),
    previousTotalBytes: exactIntegerOrNull(params.previous_total_bytes),
    lineOffset: exactIntegerOrNull(params.line_offset),
    lineLength: exactIntegerOrNull(params.line_length),
    totalLines: exactIntegerOrNull(params.total_lines),
    previousTotalLines: exactIntegerOrNull(params.previous_total_lines),
  };
}

function stringOrNull(value: unknown): string | null {
  return typeof value === "string" && value.trim() ? value : null;
}

function exactIntegerOrNull(value: unknown): number | string | null {
  return typeof value === "number" || typeof value === "string" ? value : null;
}
