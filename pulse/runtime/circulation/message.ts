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
  KeyKind,
  KeyTS,
  KeyIntention,
  KeyResponse,
  ValueKindIntention,
  ValueKindResponse,
  type MessageKind,
} from "./constants.js";
import type { Intention } from "./intention.js";
import type { Response } from "./response.js";

export type CirculationMessage =
  | {
      kind: typeof ValueKindIntention;
      ts: string;
      intention: Intention;
    }
  | {
      kind: typeof ValueKindResponse;
      ts: string;
      response: Response;
    };

// Helpers (type guards)

export function isIntentionMessage(
  msg: CirculationMessage
): msg is Extract<CirculationMessage, { kind: "intention" }> {
  return msg.kind === ValueKindIntention;
}

export function isResponseMessage(
  msg: CirculationMessage
): msg is Extract<CirculationMessage, { kind: "response" }> {
  return msg.kind === ValueKindResponse;
}

// (Optional) wire serialization helpers

export function serializeMessage(msg: CirculationMessage): string {
  const base: any = {
    [KeyKind]: msg.kind,
    [KeyTS]: msg.ts,
  };

  switch (msg.kind) {
    case ValueKindIntention:
      base[KeyIntention] = msg.intention;
      break;
    case ValueKindResponse:
      base[KeyResponse] = msg.response;
      break;
  }

  return JSON.stringify(base);
}

export function deserializeMessage(json: string): CirculationMessage {
  const raw = JSON.parse(json);

  const kind: MessageKind = raw[KeyKind];
  const ts: string = raw[KeyTS];

  if (kind === ValueKindIntention) {
    return {
      kind,
      ts,
      intention: raw[KeyIntention],
    };
  }

  if (kind === ValueKindResponse) {
    return {
      kind,
      ts,
      response: raw[KeyResponse],
    };
  }

  // fallback (unknown kind)
  return raw as CirculationMessage;
}
