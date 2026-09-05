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
  isResponseMessage,
  type CirculationMessage,
  type Intention,
  type Response,
} from "../circulation/index.js";
import { createIntentionId } from "../circulation/intention.js";

import type { TransportConnectionState, Unsubscribe } from "../transport/Transport.js";
import { TransportManager } from "../transport/TransportManager.js";
import { WebSocketTransport } from "../transport/WebSocketTransport.js";

import { SubscriptionRegistry } from "../flow/SubscriptionRegistry.js";
import {
  FlowRegistry,
  type Flow,
  type FlowSnapshot,
} from "../flow/FlowRegistry.js";
import { DispatchEngine } from "../dispatch/DispatchEngine.js";
import { ProjectionState } from "../projection/ProjectionState.js";
import type { PathSnapshot } from "../projection/PathRegistry.js";
import { UIRuntimeIdentity } from "../ui/UIRuntimeIdentity.js";
import { MatterRuntime } from "../matter/MatterRuntime.js";
import {
  UIActionBuilder,
  type UIIntentionInput,
} from "../action/UIActionBuilder.js";

import type {
  IntentionId,
  UIPath,
  SubscriptionCallback,
  DisplayMode,
} from "./types.js";

export type PreparedIntention = {
  intentionId: IntentionId;
  message: CirculationMessage & { kind: "intention" };
};

export type PulseRuntimeConfig = {
  transportManager?: TransportManager;
  transportName?: string;
  webSocketUrl?: string;
  maxMessagesPerFlow?: number;
  terminalFlowTtlMs?: number;
  uiName?: string;
  uiAddressPrefix?: string;
  responseTimeoutMs?: number;
  identity?: UIIntentionInput["identity"];
};

export class PulseRuntime {
  private readonly transports: TransportManager;
  private readonly transportName?: string;
  private readonly transportSubscriptions: Unsubscribe[] = [];
  private readonly subscriptions: SubscriptionRegistry;
  private readonly flows: FlowRegistry;
  private readonly dispatch: DispatchEngine;
  private readonly projection: ProjectionState;
  private readonly responseTimeoutMs: number;
  private readonly actions: UIActionBuilder;
  readonly ui: UIRuntimeIdentity;
  readonly matter: MatterRuntime;

  constructor(config: PulseRuntimeConfig) {
    this.transports = config.transportManager ?? new TransportManager();

    if (config.transportManager) {
      this.transportName = config.transportName;
    } else {
      const transportName = config.transportName ?? "websocket";
      this.transportName = transportName;

      if (!config.webSocketUrl) {
        throw new Error(
          "PulseRuntime requires webSocketUrl when no transportManager is provided"
        );
      }

      this.transports.register(
        transportName,
        new WebSocketTransport(config.webSocketUrl)
      );
    }

    this.subscriptions = new SubscriptionRegistry();
    this.flows = new FlowRegistry(config.maxMessagesPerFlow, config.terminalFlowTtlMs);
    this.projection = new ProjectionState();
    this.responseTimeoutMs = config.responseTimeoutMs ?? 30_000;
    this.ui = new UIRuntimeIdentity({
      uiName: config.uiName,
      uiAddressPrefix: config.uiAddressPrefix,
    });
    this.actions = new UIActionBuilder({
      addressForPath: (pathUI) => this.ui.addressForPath(pathUI),
      identity: config.identity,
    });
    this.matter = new MatterRuntime({
      emitAndWait: (input, targetPathUI) =>
        this.emitIntentionAndWait(input, targetPathUI),
      sourceAddressForPath: (pathUI) => this.ui.addressForPath(pathUI),
      identity: config.identity,
    });
    this.dispatch = new DispatchEngine(
      this.subscriptions,
      this.flows,
      this.projection,
      this.matter
    );

    this.subscribeToConfiguredTransport();
  }

  async emitIntention(
    input: Omit<Intention, "intention_id">,
    targetPathUI?: UIPath
  ): Promise<IntentionId> {
    const prepared = this.prepareIntention(input, targetPathUI);
    await this.sendPrepared(prepared);
    return prepared.intentionId;
  }

  /**
   * Builds and activates the outbound message without sending it, so the
   * caller can subscribe to intentionId before the network write — sending
   * first and subscribing after leaves a real window where a fast response
   * arrives and is dropped (SubscriptionRegistry does not buffer).
   */
  prepareIntention(
    input: Omit<Intention, "intention_id">,
    targetPathUI?: UIPath
  ): PreparedIntention {
    const intentionId = createIntentionId();

    const intention: Intention = {
      ...input,
      intention_id: intentionId,
      await_response: input.await_response !== false,
      correlation: normalizeCorrelation(input.correlation, intentionId),
    };

    const message: CirculationMessage & { kind: "intention" } = {
      kind: "intention",
      ts: new Date().toISOString(),
      intention,
    };

    if (targetPathUI) {
      this.activatePath(targetPathUI, intentionId);
    }

    return { intentionId, message };
  }

  sendPrepared(prepared: PreparedIntention): Promise<void> {
    const intention = prepared.message.intention;
    console.info(
      [
        "[pulse:intention] emit",
        `id=${prepared.intentionId}`,
        `to=${intention.to.context}`,
        `cap=${intention.to.cap}`,
        `from=${intention.from.context}`,
      ].join(" ")
    );

    return this.transports.get(this.transportName).send(prepared.message);
  }

  createUIIntention(input: UIIntentionInput): Omit<Intention, "intention_id"> {
    return this.actions.create(input);
  }

  emitUIIntention(input: UIIntentionInput): Promise<IntentionId> {
    return this.emitIntention(
      this.createUIIntention(input),
      input.targetPathUI
    );
  }

  prepareUIIntention(input: UIIntentionInput): PreparedIntention {
    return this.prepareIntention(
      this.createUIIntention(input),
      input.targetPathUI
    );
  }

  emitUIIntentionAndWait(input: UIIntentionInput): Promise<Response> {
    return this.emitIntentionAndWait(
      this.createUIIntention(input),
      input.targetPathUI
    );
  }

  async emitIntentionAndWait(
    input: Omit<Intention, "intention_id">,
    targetPathUI?: UIPath
  ): Promise<Response> {
    const intentionId = createIntentionId();

    const intention: Intention = {
      ...input,
      intention_id: intentionId,
      await_response: true,
      correlation: normalizeCorrelation(input.correlation, intentionId),
    };

    const message: CirculationMessage = {
      kind: "intention",
      ts: new Date().toISOString(),
      intention,
    };

    if (targetPathUI) {
      this.activatePath(targetPathUI, intentionId);
    }

    console.info(
      [
        "[pulse:intention] emit-and-wait",
        `id=${intentionId}`,
        `to=${intention.to.context}`,
        `cap=${intention.to.cap}`,
        `from=${intention.from.context}`,
        `targetPathUI=${targetPathUI ?? "none"}`,
      ].join(" ")
    );

    return new Promise<Response>((resolve, reject) => {
      let settled = false;
      let timeout: ReturnType<typeof setTimeout> | null = null;

      const cleanup = () => {
        unsubscribe();
        if (timeout) {
          clearTimeout(timeout);
          timeout = null;
        }
      };

      const settle = (fn: () => void) => {
        if (settled) return;
        settled = true;
        cleanup();
        fn();
      };

      const armTimeout = () => {
        if (timeout) clearTimeout(timeout);
        timeout = setTimeout(() => {
          settle(() => {
            reject(new Error(`Timed out waiting for response: ${intentionId}`));
          });
        }, this.responseTimeoutMs);
      };

      const unsubscribe = this.subscribeToIntention(intentionId, (msg) => {
        if (!isResponseMessage(msg)) return;

        // An intermediate "running" response marks a long-running capacity
        // still in progress: stay subscribed for the eventual final
        // response, and push the timeout budget back out — only silence
        // beyond responseTimeoutMs should ever reject this promise.
        if (msg.response.status === "running") {
          armTimeout();
          return;
        }

        settle(() => {
          resolve(msg.response);
        });
      });

      armTimeout();

      this.transports.get(this.transportName).send(message).catch((err) => {
        settle(() => {
          reject(err);
        });
      });
    });
  }

  subscribeToIntention(
    intentionId: IntentionId,
    callback: SubscriptionCallback
  ): () => void {
    return this.subscriptions.subscribe(intentionId, callback);
  }

  getFlow(intentionId: IntentionId): Flow | undefined {
    return this.flows.get(intentionId);
  }

  getFlowSnapshot(intentionId: IntentionId | null): FlowSnapshot {
    return this.flows.getSnapshot(intentionId);
  }

  subscribeToFlow(
    intentionId: IntentionId,
    callback: () => void
  ): () => void {
    return this.flows.subscribe(intentionId, callback);
  }

  activatePath(path: UIPath, intentionId: IntentionId): void {
    this.projection.paths.activatePath(path, intentionId);
  }

  deactivatePath(path: UIPath): void {
    this.projection.paths.deactivatePath(path);
  }

  isPathActive(path: UIPath): boolean {
    return this.projection.paths.isPathActive(path);
  }

  getIntentionIdForPath(path: UIPath): IntentionId | null {
    return this.projection.paths.getIntentionIdForPath(path);
  }

  getPathSnapshot(path: UIPath): PathSnapshot {
    return this.projection.paths.getSnapshot(path);
  }

  subscribeToPath(
    path: UIPath,
    callback: (intentionId: IntentionId | null) => void
  ): () => void {
    return this.projection.paths.subscribe((changedPath, intentionId) => {
      if (changedPath === path) {
        callback(intentionId);
      }
    });
  }

  subscribeToPathSnapshot(path: UIPath, callback: () => void): () => void {
    return this.projection.paths.subscribePath(path, callback);
  }

  registerBlock(pathUI: UIPath, componentName: string): void {
    this.projection.paths.registerBlock(pathUI, componentName);
  }

  unregisterBlock(pathUI: UIPath): void {
    this.projection.paths.unregisterBlock(pathUI);
  }

  getActiveBlocks() {
    return this.projection.paths.getActiveBlocks();
  }

  getDisplayMode(): DisplayMode {
    return this.projection.getDisplayMode();
  }

  setDisplayMode(mode: DisplayMode): void {
    this.projection.setDisplayMode(mode);
  }

  subscribeToConnectionState(callback: (state: TransportConnectionState) => void): () => void {
    const transport = this.transports.get(this.transportName);
    if (transport.subscribeToConnectionState) {
      return transport.subscribeToConnectionState(callback);
    }
    callback("connected");
    return () => {};
  }

  subscribeToDisplayMode(callback: (mode: DisplayMode) => void): () => void {
    return this.projection.subscribeToDisplayMode(callback);
  }

  clearProjection(): void {
    this.projection.clear();
  }

  clearFlows(): void {
    this.flows.clear();
  }

  teardown(): void {
    for (const unsubscribe of this.transportSubscriptions) {
      unsubscribe();
    }

    this.transportSubscriptions.length = 0;
    this.transports.get(this.transportName).close();
    this.subscriptions.clear();
    this.clearFlows();
    this.clearProjection();
    this.matter.clear();
    this.matter.clearListeners();
    this.flows.clearListeners();
    this.projection.clearListeners();
  }

  private subscribeToConfiguredTransport(): void {
    const transport = this.transports.get(this.transportName);
    const unsubscribe = transport.subscribe((msg) => {
      this.dispatch.dispatch(msg);
    });

    this.transportSubscriptions.push(unsubscribe);
  }
}

function normalizeCorrelation(
  correlation: Intention["correlation"],
  intentionId: IntentionId
): NonNullable<Intention["correlation"]> {
  return {
    root_intention_id: correlation?.root_intention_id ?? intentionId,
    parent_intention_id: correlation?.parent_intention_id ?? null,
  };
}
