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

import type { ExecuteInput, ExecuteResult } from "../types/index.js";

export const EXECUTION_TIMEOUT_MS = 5_000;

export type PreparedEmit = {
  intentionId: string;
  send(): Promise<void>;
};

export type PulseBridge = {
  /**
   * Prepares an outbound intention synchronously (assigning intentionId
   * before any network write) so the caller can subscribe before calling
   * send() — subscribing after the network write leaves a real window where
   * a fast response arrives and is silently dropped by the subscription
   * registry (no buffering).
   */
  prepare(input: ExecuteInput): PreparedEmit;
  subscribe(
    intentionId: string,
    callback: (message: unknown) => void
  ): () => void;
};

export async function executeThroughBridge(
  bridge: PulseBridge,
  input: ExecuteInput,
  timeoutMs = EXECUTION_TIMEOUT_MS
): Promise<ExecuteResult> {
  const prepared = bridge.prepare(input);

  if (input.awaitResponse === false) {
    await prepared.send();
    return { intentionId: prepared.intentionId, response: undefined };
  }

  const { intentionId } = prepared;

  return new Promise<ExecuteResult>((resolve, reject) => {
    let settled = false;
    let timeout: ReturnType<typeof setTimeout> | undefined;

    function cleanup(): void {
      unsubscribe();
      if (timeout) {
        clearTimeout(timeout);
        timeout = undefined;
      }
    }

    function settle(fn: () => void): void {
      if (settled) return;
      settled = true;
      cleanup();
      fn();
    }

    function armTimeout(): void {
      if (timeout) clearTimeout(timeout);
      timeout = setTimeout(() => {
        settle(() => {
          reject(new Error(`BriqueSubstrate execute timed out: ${intentionId || input.capability}`));
        });
      }, timeoutMs);
    }

    // Subscribed before send() is ever called below — a response arriving
    // between send() resolving and the next microtask can no longer be
    // dropped, since the callback is already registered by construction.
    const unsubscribe = bridge.subscribe(intentionId, (message) => {
      const response = extractMatchingResponse(message, intentionId);
      if (response === undefined) return;

      // An intermediate "running" response marks a long-running capacity
      // still in progress: stay subscribed for the eventual final response,
      // and push the timeout budget back out instead of settling.
      if (isRecord(response) && response.status === "running") {
        armTimeout();
        return;
      }

      settle(() => resolve({ intentionId, response }));
    });

    armTimeout();

    prepared.send().catch((err) => {
      settle(() => reject(err));
    });
  });
}

function extractMatchingResponse(
  message: unknown,
  intentionId: string
): unknown {
  if (!isRecord(message)) return undefined;

  const response = message.response;
  if (isRecord(response) && response.intention_id === intentionId) {
    return response;
  }

  if (message.intention_id === intentionId) {
    return message;
  }

  return undefined;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}
