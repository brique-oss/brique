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
  isResponseMessage,
  type CirculationMessage,
} from "../circulation/index.js";
import type { IntentionId } from "../core/types.js";
import type { KeyedRuntimeStore } from "../store/types.js";

export type Flow = {
  messages: CirculationMessage[];
  lastMessage?: CirculationMessage;
};

export type FlowStatus =
  | "idle"
  | "accepted"
  | "running"
  | "success"
  | "error";

export type FlowSnapshot = {
  intentionId: IntentionId | null;
  status: FlowStatus;
  message: CirculationMessage | null;
  messages: readonly CirculationMessage[];
  payload: unknown;
  error: unknown;
  terminal: boolean;
};

const TERMINAL_STATUSES = new Set(["result", "error", "ok"]);
const DEFAULT_TERMINAL_TTL_MS = 60_000;
const IDLE_FLOW_SNAPSHOT: FlowSnapshot = Object.freeze({
  intentionId: null,
  status: "idle",
  message: null,
  messages: Object.freeze([]),
  payload: null,
  error: null,
  terminal: false,
});

function isTerminalMessage(message: CirculationMessage): boolean {
  return (
    isResponseMessage(message) &&
    TERMINAL_STATUSES.has(message.response.status)
  );
}

export class FlowRegistry
  implements KeyedRuntimeStore<IntentionId, FlowSnapshot>
{
  private readonly flows: Map<IntentionId, Flow> = new Map();
  private readonly pendingCleanup: Map<IntentionId, ReturnType<typeof setTimeout>> = new Map();
  private readonly listeners: Map<IntentionId, Set<() => void>> = new Map();
  private readonly snapshots: Map<IntentionId, FlowSnapshot> = new Map();
  private readonly maxMessagesPerFlow: number;
  private readonly terminalTtlMs: number;

  constructor(maxMessagesPerFlow = 100, terminalTtlMs = DEFAULT_TERMINAL_TTL_MS) {
    this.maxMessagesPerFlow = maxMessagesPerFlow;
    this.terminalTtlMs = terminalTtlMs;
  }

  update(intentionId: IntentionId, message: CirculationMessage): void {
    let flow = this.flows.get(intentionId);

    if (!flow) {
      flow = { messages: [] };
      this.flows.set(intentionId, flow);
    }

    flow.messages.push(message);
    if (flow.messages.length > this.maxMessagesPerFlow) {
      flow.messages.splice(0, flow.messages.length - this.maxMessagesPerFlow);
    }

    flow.lastMessage = message;
    this.snapshots.delete(intentionId);

    if (isTerminalMessage(message)) {
      this.scheduleCleanup(intentionId);
    }

    this.notify(intentionId);
  }

  get(intentionId: IntentionId): Flow | undefined {
    return this.flows.get(intentionId);
  }

  has(intentionId: IntentionId): boolean {
    return this.flows.has(intentionId);
  }

  delete(intentionId: IntentionId): void {
    this.cancelCleanup(intentionId);
    const hadFlow = this.flows.delete(intentionId);
    const hadSnapshot = this.snapshots.delete(intentionId);

    if (hadFlow || hadSnapshot) {
      this.notify(intentionId);
    }
  }

  clear(): void {
    const intentionIds = new Set([
      ...this.flows.keys(),
      ...this.snapshots.keys(),
    ]);

    for (const intentionId of this.pendingCleanup.keys()) {
      this.cancelCleanup(intentionId);
    }

    this.flows.clear();
    this.snapshots.clear();

    for (const intentionId of intentionIds) {
      this.notify(intentionId);
    }
  }

  clearListeners(): void {
    this.listeners.clear();
  }

  getSnapshot(intentionId: IntentionId | null): FlowSnapshot {
    if (!intentionId) return IDLE_FLOW_SNAPSHOT;

    const cached = this.snapshots.get(intentionId);
    if (cached) return cached;

    const flow = this.flows.get(intentionId);
    if (!flow) return IDLE_FLOW_SNAPSHOT;

    const snapshot = createFlowSnapshot(intentionId, flow);
    this.snapshots.set(intentionId, snapshot);
    return snapshot;
  }

  subscribe(intentionId: IntentionId, listener: () => void): () => void {
    let set = this.listeners.get(intentionId);
    if (!set) {
      set = new Set();
      this.listeners.set(intentionId, set);
    }

    set.add(listener);

    return () => {
      const current = this.listeners.get(intentionId);
      if (!current) return;

      current.delete(listener);

      if (current.size === 0) {
        this.listeners.delete(intentionId);
      }
    };
  }

  private scheduleCleanup(intentionId: IntentionId): void {
    this.cancelCleanup(intentionId);

    const timer = setTimeout(() => {
      this.pendingCleanup.delete(intentionId);
      this.delete(intentionId);
    }, this.terminalTtlMs);

    this.pendingCleanup.set(intentionId, timer);
  }

  private cancelCleanup(intentionId: IntentionId): void {
    const timer = this.pendingCleanup.get(intentionId);

    if (timer !== undefined) {
      clearTimeout(timer);
      this.pendingCleanup.delete(intentionId);
    }
  }

  private notify(intentionId: IntentionId): void {
    const listeners = this.listeners.get(intentionId);
    if (!listeners) return;

    for (const listener of listeners) {
      listener();
    }
  }
}

function createFlowSnapshot(
  intentionId: IntentionId,
  flow: Flow
): FlowSnapshot {
  const message = flow.lastMessage ?? null;
  const status = deriveFlowStatus(message);
  const messages = Object.freeze([...flow.messages]);

  return Object.freeze({
    intentionId,
    status,
    message,
    messages,
    payload: extractPayload(message),
    error: extractError(message, status),
    terminal: status === "success" || status === "error",
  });
}

function deriveFlowStatus(message: CirculationMessage | null): FlowStatus {
  if (!message) return "idle";

  if (isIntentionMessage(message)) {
    return "accepted";
  }

  if (isResponseMessage(message)) {
    switch (message.response.status) {
      case "accepted":
        return "accepted";
      case "running":
      case "deferred":
      case "progress":
        return "running";
      case "ok":
      case "result":
      case "success":
        return "success";
      case "error":
      case "refused":
        return "error";
      default:
        return "running";
    }
  }

  return "running";
}

function extractPayload(message: CirculationMessage | null): unknown {
  if (message && isResponseMessage(message)) {
    return message.response.payload ?? null;
  }

  return null;
}

function extractError(
  message: CirculationMessage | null,
  status: FlowStatus
): unknown {
  if (status === "error" && message && isResponseMessage(message)) {
    return message.response.error ?? null;
  }

  return null;
}
