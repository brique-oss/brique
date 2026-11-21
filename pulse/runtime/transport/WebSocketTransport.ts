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
  CONTROL_MESSAGE_LIMIT_BYTES,
  type CirculationMessage,
} from "../circulation/index.js";
import type { Transport, TransportConnectionState, Unsubscribe } from "./Transport.js";

export type WebSocketTransportConfig = {
  maxRetries?: number;
  baseDelayMs?: number;
  maxDelayMs?: number;
  protocols?: string | string[];
  maxMessageBytes?: number;
  maxBufferedBytes?: number;
};

const DEFAULT_MAX_RETRIES = 5;
const DEFAULT_BASE_DELAY_MS = 200;
const DEFAULT_MAX_DELAY_MS = 30_000;
const DEFAULT_MAX_BUFFERED_BYTES = 1 << 20;
/**
 * Application close code of the engine: a newer connection took this route (one
 * client per route). Reconnecting would only displace the other client in turn
 * and the two would fight forever, so the transport stays down.
 */
export const CLOSE_CODE_REPLACED = 4001;

export class WebSocketTransport implements Transport {
  private socket: WebSocket;
  private readonly url: string;
  private readonly maxRetries: number;
  private readonly baseDelayMs: number;
  private readonly maxDelayMs: number;
  private readonly protocols?: string | string[];
  private readonly maxMessageBytes: number;
  private readonly maxBufferedBytes: number;
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
    this.protocols = config.protocols;
    this.maxMessageBytes = config.maxMessageBytes ?? CONTROL_MESSAGE_LIMIT_BYTES;
    this.maxBufferedBytes = config.maxBufferedBytes ?? DEFAULT_MAX_BUFFERED_BYTES;
    console.info(`[pulse:ws] create url=${this.url}`);
    this.socket = this.createSocket();
  }

  async send(message: CirculationMessage): Promise<void> {
    const data = serializeMessage(message);
	const byteLength = new TextEncoder().encode(data).byteLength;
	if (byteLength > this.maxMessageBytes) {
		throw new Error("WebSocket message exceeds its transport limit");
	}
    console.info(`[pulse:ws] send ${describeMessage(message)} state=${this.socket.readyState}`);

    if (this.socket.readyState === WebSocket.OPEN) {
		this.assertWritable(byteLength);
      this.socket.send(data);
      console.info(`[pulse:ws] sent ${describeMessage(message)}`);
      return;
    }

    await this.waitForOpen();
	this.assertWritable(byteLength);
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
    const socket = this.protocols === undefined
      ? new WebSocket(this.url)
      : new WebSocket(this.url, this.protocols);
	socket.binaryType = "arraybuffer";

    socket.onmessage = (event) => {
      try {
		const serialized = decodeWebSocketData(event.data);
		if (new TextEncoder().encode(serialized).byteLength > this.maxMessageBytes) {
          socket.close(1009, "control message too large");
			throw new Error("WebSocketTransport received a message above its limit");
        }

		const msg = deserializeMessage(serialized);
        console.info(`[pulse:ws] receive ${describeMessage(msg)}`);
        this.notify(msg);
      } catch (err) {
        console.error("Invalid message from websocket", err);
      }
    };

    socket.onopen = () => {
		const required = typeof this.protocols === "string" ? this.protocols : undefined;
		if (required !== undefined && socket.protocol !== required) {
			socket.close(1002, "required subprotocol missing");
			return;
		}
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
      if (event.code === CLOSE_CODE_REPLACED) {
        console.warn("[pulse:ws] replaced by a newer connection on this route: not reconnecting");
        return;
      }
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

	private assertWritable(byteLength: number): void {
		if (this.socket.bufferedAmount + byteLength > this.maxBufferedBytes) {
			this.socket.close(1009, "outgoing queue limit reached");
			throw new Error("WebSocket outgoing queue limit reached");
		}
	}
}

function decodeWebSocketData(value: unknown): string {
	if (typeof value === "string") return value;
	if (value instanceof ArrayBuffer) return new TextDecoder().decode(new Uint8Array(value));
	if (ArrayBuffer.isView(value)) {
		return new TextDecoder().decode(new Uint8Array(value.buffer, value.byteOffset, value.byteLength));
	}
	throw new Error("WebSocketTransport received an unsupported frame type");
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
