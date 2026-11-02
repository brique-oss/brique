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
  deserializeMessage,
  serializeMessage,
  type CirculationMessage,
} from "../circulation/index.js";
import type { Transport, TransportConnectionState, Unsubscribe } from "./Transport.js";

// Spark implements this boundary in its trusted host process. Browser
// renderers never connect directly to the engine, so its strict Origin policy
// remains enabled.
export interface SerializedTransportBridge {
  open(): void | Promise<void>;
  send(serialized: string): Promise<void>;
  subscribe(callback: (serialized: string) => void): Unsubscribe;
  subscribeToConnectionState(callback: (state: TransportConnectionState) => void): Unsubscribe;
  close(): void;
}

export class BridgeTransport implements Transport {
  private readonly bridge: SerializedTransportBridge;
  private readonly subscribers = new Set<(message: CirculationMessage) => void>();
  private readonly connectionSubscribers = new Set<(state: TransportConnectionState) => void>();
  private readonly unsubscribeMessage: Unsubscribe;
  private readonly unsubscribeState: Unsubscribe;
  private closed = false;

  constructor(bridge: SerializedTransportBridge) {
    this.bridge = bridge;
    this.unsubscribeMessage = bridge.subscribe((serialized) => {
      try {
        const message = deserializeMessage(serialized);
        for (const subscriber of this.subscribers) subscriber(message);
      } catch (error) {
        console.error("Invalid message from Spark transport bridge", error);
      }
    });
    this.unsubscribeState = bridge.subscribeToConnectionState((state) => {
      for (const subscriber of this.connectionSubscribers) subscriber(state);
    });
    void Promise.resolve(bridge.open()).catch((error) => {
      console.error("Spark transport bridge failed to open", error);
    });
  }

  send(message: CirculationMessage): Promise<void> {
    if (this.closed) return Promise.reject(new Error("Transport bridge is closed"));
    return this.bridge.send(serializeMessage(message));
  }

  subscribe(callback: (message: CirculationMessage) => void): Unsubscribe {
    this.subscribers.add(callback);
    return () => { this.subscribers.delete(callback); };
  }

  subscribeToConnectionState(callback: (state: TransportConnectionState) => void): Unsubscribe {
    this.connectionSubscribers.add(callback);
    return () => { this.connectionSubscribers.delete(callback); };
  }

  close(): void {
    if (this.closed) return;
    this.closed = true;
    this.unsubscribeMessage();
    this.unsubscribeState();
    this.subscribers.clear();
    this.connectionSubscribers.clear();
    this.bridge.close();
  }
}
