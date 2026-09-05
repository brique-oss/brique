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

import type { ExecuteInput } from "../types/index.js";
import type { PulseBridge } from "./bridge.js";

export type PreparedPulseIntention = {
  intentionId: string;
  message: unknown;
};

export type ProjectionPulseRuntime = {
  prepareUIIntention(input: PulseUIIntentionInput): PreparedPulseIntention;
  sendPrepared(prepared: PreparedPulseIntention): Promise<void>;
  subscribeToIntention(
    intentionId: string,
    callback: (message: unknown) => void
  ): () => void;
};

export type PulseUIIntentionInput = {
  to: {
    context: string;
    cap: string;
    type: string;
  };
  sourcePathUI?: string;
  targetPathUI?: string;
  params?: Record<string, unknown>;
  awaitResponse?: boolean;
};

export type PulseRuntimeBridgeOptions = {
  sourcePathUI?: string;
  targetPathUI?: string;
  addressType?: string;
};

export function createPulseRuntimeBridge(
  pulse: ProjectionPulseRuntime,
  options: PulseRuntimeBridgeOptions = {}
): PulseBridge {
  return {
    prepare(input) {
      const intentionInput = toPulseUIIntentionInput(input, options);
      const prepared = pulse.prepareUIIntention(intentionInput);
      return {
        intentionId: prepared.intentionId,
        send: () => pulse.sendPrepared(prepared),
      };
    },

    subscribe(intentionId, callback) {
      return pulse.subscribeToIntention(intentionId, callback);
    },
  };
}

export function toPulseUIIntentionInput(
  input: ExecuteInput,
  options: PulseRuntimeBridgeOptions = {}
): PulseUIIntentionInput {
  return {
    to: {
      context: input.context,
      cap: input.capability,
      type: options.addressType ?? resolveCapabilityAddressType(input.capability),
    },
    sourcePathUI: options.sourcePathUI,
    targetPathUI: options.targetPathUI,
    params: input.params,
    awaitResponse: input.awaitResponse,
  };
}

function resolveCapabilityAddressType(capability: string): string {
  if (capability.startsWith("matter.") || capability.startsWith("structure.")) return "matter";
  if (
    capability.startsWith("read.") ||
    capability.startsWith("edit.") ||
    capability.startsWith("trace.") ||
    capability.startsWith("meaning.") ||
    capability.startsWith("vocabulary.")
  ) return "reflexive";
  if (capability === "restart" || capability === "stop") return "control";
  if (capability.startsWith("wrapper.")) return "execution";
  return "user";
}
