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

export type WebSocketTransportConfig = {
  maxRetries?: number;
  baseDelayMs?: number;
  maxDelayMs?: number;
};

const DEFAULT_MAX_RETRIES = 5;
const DEFAULT_BASE_DELAY_MS = 200;
const DEFAULT_MAX_DELAY_MS = 30_000;

export class WebSocketTransport implements Transport {
  private socket: WebSocket;
  private readonly url: string;
  private readonly maxRetries: number;
  private readonly baseDelayMs: number;
  private readonly maxDelayMs: number;
  private retryCount = 0;
  private retryTimer: ReturnType<typeof setTimeout> | null = null;
  private closed = false;
  private readonly subscribers: Set<(msg: CirculationMessage) => void> = new Set();
  private readonly connectionSubscribers: Set<(state: TransportConnectionState) => void> = new Set();

  constructor(url: string, config: WebSocketTransportConfig = {}) {
    this.url = url;
    this.maxRetries = config.maxRetries ?? DEFAULT_MAX_RETRIES;
    this.baseDelayMs = config.baseDelayMs ?? DEFAULT_BASE_DELAY_MS;
    this.maxDelayMs = config.maxDelayMs ?? DEFAULT_MAX_DELAY_MS;
    console.info(`[pulse:ws] create url=${this.url}`);
    this.socket = this.createSocket();
  }

  async send(message: CirculationMessage): Promise<void> {
    const data = serializeMessage(message);
    console.info(`[pulse:ws] send ${describeMessage(message)} state=${this.socket.readyState}`);

    if (this.socket.readyState === WebSocket.OPEN) {
      this.socket.send(data);
      console.info(`[pulse:ws] sent ${describeMessage(message)}`);
      return;
    }

    await this.waitForOpen();
    this.socket.send(data);
    console.info(`[pulse:ws] sent ${describeMessage(message)}`);
  }

  subscribe(cb: (msg: CirculationMessage) => void): Unsubscribe {
    this.subscribers.add(cb);
    return () => { this.subscribers.delete(cb); };
  }

  subscribeToConnectionState(cb: (state: TransportConnectionState) => void): Unsubscribe {
    this.connectionSubscribers.add(cb);
    cb(this.socket.readyState === WebSocket.OPEN ? "connected" : "disconnected");
    return () => { this.connectionSubscribers.delete(cb); };
  }

  close(): void {
    this.closed = true;
    this.subscribers.clear();

    if (this.retryTimer !== null) {
      clearTimeout(this.retryTimer);
      this.retryTimer = null;
    }

    if (
      this.socket.readyState === WebSocket.OPEN ||
      this.socket.readyState === WebSocket.CONNECTING
    ) {
      this.socket.close();
    }
  }

  private createSocket(): WebSocket {
    console.info(`[pulse:ws] opening url=${this.url}`);
    const socket = new WebSocket(this.url);

    socket.onmessage = (event) => {
      try {
        if (typeof event.data !== "string") {
          throw new Error("WebSocketTransport expects text messages");
        }

        const msg = deserializeMessage(event.data);
        console.info(`[pulse:ws] receive ${describeMessage(msg)}`);
        this.notify(msg);
      } catch (err) {
        console.error("Invalid message from websocket", err);
      }
    };

    socket.onopen = () => {
      console.info(`[pulse:ws] open url=${this.url}`);
      this.retryCount = 0;
      this.notifyConnectionState("connected");
    };

    socket.onerror = (event) => {
      console.error("[pulse:ws] error", event);
    };

    socket.onclose = (event) => {
      console.warn(
        `[pulse:ws] close url=${this.url} code=${event.code} reason=${event.reason}`
      );
      this.notifyConnectionState("disconnected");
      if (!this.closed) {
        this.scheduleReconnect();
      }
    };

    return socket;
  }

  private scheduleReconnect(): void {
    if (this.retryCount >= this.maxRetries) {
      console.error(
        `WebSocketTransport: giving up after ${this.maxRetries} retries`
      );
      return;
    }

    const delay = Math.min(
      this.baseDelayMs * 2 ** this.retryCount,
      this.maxDelayMs
    );
    this.retryCount += 1;

    this.retryTimer = setTimeout(() => {
      this.retryTimer = null;

      if (!this.closed) {
        this.socket = this.createSocket();
      }
    }, delay);
  }

  private notify(msg: CirculationMessage) {
    for (const cb of this.subscribers) {
      cb(msg);
    }
  }

  private notifyConnectionState(state: TransportConnectionState) {
    for (const cb of this.connectionSubscribers) {
      cb(state);
    }
  }

  private waitForOpen(): Promise<void> {
    return new Promise((resolve, reject) => {
      if (this.socket.readyState === WebSocket.OPEN) {
        resolve();
        return;
      }

      if (this.closed) {
        reject(new Error("WebSocket is not open"));
        return;
      }

      if (
        this.socket.readyState === WebSocket.CLOSED ||
        this.socket.readyState === WebSocket.CLOSING
      ) {
        reject(new Error("WebSocket is not open"));
        return;
      }

      const cleanup = () => {
        this.socket.removeEventListener("open", onOpen);
        this.socket.removeEventListener("error", onError);
        this.socket.removeEventListener("close", onClose);
      };

      const onOpen = () => {
        cleanup();
        resolve();
      };

      const onError = () => {
        cleanup();
        reject(new Error("WebSocket failed to open"));
      };

      const onClose = () => {
        cleanup();
        reject(new Error("WebSocket closed before opening"));
      };

      this.socket.addEventListener("open", onOpen, { once: true });
      this.socket.addEventListener("error", onError, { once: true });
      this.socket.addEventListener("close", onClose, { once: true });
    });
  }
}

function describeMessage(message: CirculationMessage): string {
  if (message.kind === "intention") {
    return [
      "intention",
      `id=${message.intention.intention_id}`,
      `to=${message.intention.to.context}`,
      `cap=${message.intention.to.cap}`,
      `from=${message.intention.from.context}`,
    ].join(" ");
  }

  if (message.kind === "response") {
    return [
      "response",
      `id=${message.response.intention_id}`,
      `status=${message.response.status}`,
      `to=${message.response.to.context}`,
    ].join(" ");
  }

  return "unknown";
}
