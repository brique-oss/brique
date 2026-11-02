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
  CONTROL_MESSAGE_LIMIT_BYTES,
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

  const serialized = JSON.stringify(base);
  assertControlMessageSize(serialized);
  return serialized;
}

export function deserializeMessage(json: string): CirculationMessage {
  assertControlMessageSize(json);
  const raw = JSON.parse(preserveUnsafeIntegers(json));

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

  return raw as CirculationMessage;
}

function assertControlMessageSize(value: string): void {
  if (new TextEncoder().encode(value).byteLength > CONTROL_MESSAGE_LIMIT_BYTES) {
    throw new Error("Circulation payload exceeds the 4 MiB control-plane limit; use Matter");
  }
}

// JSON.parse rounds integers above Number.MAX_SAFE_INTEGER before a reviver can
// see them. Revisions and byte coordinates are int64 in the engine, so quote
// only unsafe integer tokens while scanning outside strings. They then remain
// exact decimal strings and can be sent back as expected_* guards unchanged.
function preserveUnsafeIntegers(json: string): string {
  let output = "";
  let index = 0;
  let inString = false;
  let escaped = false;

  while (index < json.length) {
    const char = json[index];
    if (inString) {
      output += char;
      if (escaped) escaped = false;
      else if (char === "\\") escaped = true;
      else if (char === '"') inString = false;
      index += 1;
      continue;
    }
    if (char === '"') {
      inString = true;
      output += char;
      index += 1;
      continue;
    }
    if (char === "-" || (char >= "0" && char <= "9")) {
      const start = index;
      if (char === "-") index += 1;
      while (index < json.length && json[index] >= "0" && json[index] <= "9") index += 1;
      const integerEnd = index;
      if (json[index] === "." || json[index] === "e" || json[index] === "E") {
        while (index < json.length && /[0-9eE+.-]/u.test(json[index])) index += 1;
        output += json.slice(start, index);
        continue;
      }
      const token = json.slice(start, integerEnd);
      try {
        const integer = BigInt(token);
        if (integer > BigInt(Number.MAX_SAFE_INTEGER) || integer < BigInt(Number.MIN_SAFE_INTEGER)) {
          output += JSON.stringify(token);
        } else {
          output += token;
        }
      } catch {
        output += token;
      }
      continue;
    }
    output += char;
    index += 1;
  }
  return output;
}
