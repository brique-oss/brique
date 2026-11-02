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

import WebSocket, { type RawData } from "ws";

import { CONTROL_MESSAGE_LIMIT_BYTES } from "../../pulse/runtime/circulation/constants.js";
import type { TransportConnectionState } from "../../pulse/runtime/transport/Transport.js";

const OPEN_TIMEOUT_MS = 10_000;
const BASE_RETRY_DELAY_MS = 200;
const MAX_RETRY_DELAY_MS = 30_000;
const MAX_RETRIES = 5;

export interface SparkTransportRelay {
  open(): void;
  send(serialized: string): Promise<void>;
  subscribe(callback: (serialized: string) => void): () => void;
  subscribeToConnectionState(callback: (state: TransportConnectionState) => void): () => void;
  close(): void;
}

export class LocalWebSocketRelay implements SparkTransportRelay {
  private readonly url: string;
  private socket: WebSocket | null = null;
  private closed = false;
  private retries = 0;
  private retryTimer: ReturnType<typeof setTimeout> | null = null;
  private state: TransportConnectionState = "disconnected";
  private readonly messageSubscribers = new Set<(serialized: string) => void>();
  private readonly stateSubscribers = new Set<(state: TransportConnectionState) => void>();

  constructor(url: string) {
    const parsed = new URL(url);
    if (parsed.protocol !== "ws:" && parsed.protocol !== "wss:") {
      throw new Error("Spark transport requires a ws:// or wss:// URL");
    }
    this.url = parsed.toString();
  }

  open(): void {
    if (this.closed || this.socket) return;
    this.connect();
  }

  async send(serialized: string): Promise<void> {
    assertSerializedSize(serialized);
    this.open();
    const socket = await this.waitForOpen();
    await new Promise<void>((resolve, reject) => {
      socket.send(serialized, (error) => error ? reject(error) : resolve());
    });
  }

  subscribe(callback: (serialized: string) => void): () => void {
    this.messageSubscribers.add(callback);
    return () => { this.messageSubscribers.delete(callback); };
  }

  subscribeToConnectionState(callback: (state: TransportConnectionState) => void): () => void {
    this.stateSubscribers.add(callback);
    callback(this.state);
    return () => { this.stateSubscribers.delete(callback); };
  }

  close(): void {
    if (this.closed) return;
    this.closed = true;
    if (this.retryTimer) clearTimeout(this.retryTimer);
    this.retryTimer = null;
    const socket = this.socket;
    this.socket = null;
    if (socket && socket.readyState !== WebSocket.CLOSED) socket.close();
    this.setState("disconnected");
    this.messageSubscribers.clear();
    this.stateSubscribers.clear();
  }

  private connect(): void {
    if (this.closed) return;
    const socket = new WebSocket(this.url, {
      handshakeTimeout: 5_000,
      maxPayload: CONTROL_MESSAGE_LIMIT_BYTES,
      perMessageDeflate: false,
    });
    this.socket = socket;

    socket.on("open", () => {
      if (this.socket !== socket || this.closed) return;
      this.retries = 0;
      this.setState("connected");
    });
    socket.on("message", (data: RawData, isBinary: boolean) => {
      if (this.socket !== socket || this.closed || isBinary) return;
      const serialized = rawDataToString(data);
      try {
        assertSerializedSize(serialized);
      } catch {
        socket.close(1009, "control message too large");
        return;
      }
      for (const subscriber of this.messageSubscribers) subscriber(serialized);
    });
    socket.on("error", () => {
      // close drives the state transition and bounded reconnect.
    });
    socket.on("close", () => {
      if (this.socket === socket) this.socket = null;
      this.setState("disconnected");
      if (!this.closed) this.scheduleReconnect();
    });
  }

  private scheduleReconnect(): void {
    if (this.retryTimer || this.retries >= MAX_RETRIES || this.closed) return;
    const delay = Math.min(BASE_RETRY_DELAY_MS * 2 ** this.retries, MAX_RETRY_DELAY_MS);
    this.retries += 1;
    this.retryTimer = setTimeout(() => {
      this.retryTimer = null;
      this.open();
    }, delay);
  }

  private waitForOpen(): Promise<WebSocket> {
    const socket = this.socket;
    if (!socket || this.closed) return Promise.reject(new Error("Spark transport is closed"));
    if (socket.readyState === WebSocket.OPEN) return Promise.resolve(socket);

    return new Promise((resolve, reject) => {
      const timeout = setTimeout(() => finish(new Error("Spark transport open timed out")), OPEN_TIMEOUT_MS);
      const onOpen = () => finish(undefined, socket);
      const onError = () => finish(new Error("Spark transport failed to open"));
      const onClose = () => finish(new Error("Spark transport closed before opening"));
      const finish = (error?: Error, opened?: WebSocket) => {
        clearTimeout(timeout);
        socket.off("open", onOpen);
        socket.off("error", onError);
        socket.off("close", onClose);
        if (error) reject(error);
        else resolve(opened!);
      };
      socket.once("open", onOpen);
      socket.once("error", onError);
      socket.once("close", onClose);
    });
  }

  private setState(state: TransportConnectionState): void {
    if (this.state === state) return;
    this.state = state;
    for (const subscriber of this.stateSubscribers) subscriber(state);
  }
}

function rawDataToString(data: RawData): string {
  if (typeof data === "string") return data;
  if (data instanceof ArrayBuffer) return Buffer.from(data).toString("utf8");
  if (Array.isArray(data)) return Buffer.concat(data).toString("utf8");
  return data.toString("utf8");
}

function assertSerializedSize(serialized: string): void {
  if (Buffer.byteLength(serialized, "utf8") > CONTROL_MESSAGE_LIMIT_BYTES) {
    throw new Error("Spark transport payload exceeds the 4 MiB control-plane limit; use Matter");
  }
}
