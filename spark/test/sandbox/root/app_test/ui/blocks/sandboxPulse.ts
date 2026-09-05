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

import type { PulseRuntime } from "@spark/pulse/runtime";

export type SandboxTarget = {
  context: string;
  cap: string;
  params?: Record<string, unknown>;
  targetPathUI: string;
  sourcePathUI?: string;
};

export type SandboxEmission = {
  intentionId: string;
  sourceAddress: string;
  targetContext: string;
  targetCapacity: string;
  targetPathUI: string;
};

export function childContext(
  rootContextId: string | undefined,
  childName: string
): string {
  const root = normalizeRootContextId(rootContextId);
  return `${root}/${childName}`;
}

export function normalizeRootContextId(rootContextId: string | undefined): string {
  const value = rootContextId?.trim() || "/root";
  return value.startsWith("/") ? value.replace(/\/+$/u, "") || "/" : `/${value}`;
}

export async function emitSandboxIntention(
  pulse: PulseRuntime,
  target: SandboxTarget
): Promise<string> {
  return pulse.emitUIIntention({
    to: {
      context: target.context,
      cap: target.cap,
      type: "user",
    },
    sourcePathUI: target.sourcePathUI,
    targetPathUI: target.targetPathUI,
    identity: {
      id: "sandbox-user",
      kind: "human",
    },
    params: target.params ?? {},
  });
}

export function describeSandboxEmission(
  pulse: PulseRuntime,
  target: SandboxTarget,
  intentionId: string
): SandboxEmission {
  return {
    intentionId,
    sourceAddress: pulse.ui.addressForPath(target.sourcePathUI ?? target.targetPathUI),
    targetContext: target.context,
    targetCapacity: target.cap,
    targetPathUI: target.targetPathUI,
  };
}
