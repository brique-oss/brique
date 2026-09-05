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

import { createHash } from "node:crypto";
import { createServer, type Server, type Socket } from "node:net";
import type { CirculationMessage, Transport, Unsubscribe } from "../runtime";

export class ManualTransport implements Transport {
  sent: CirculationMessage[] = [];
  closed = false;
  private readonly subscribers = new Set<(message: CirculationMessage) => void>();

  async send(message: CirculationMessage): Promise<void> {
    if (this.closed) {
      throw new Error("transport closed");
    }

    this.sent.push(message);
  }

  subscribe(callback: (message: CirculationMessage) => void): Unsubscribe {
    this.subscribers.add(callback);

    return () => {
      this.subscribers.delete(callback);
    };
  }

  receive(message: CirculationMessage): void {
    for (const callback of this.subscribers) {
      callback(message);
    }
  }

  close(): void {
    this.closed = true;
    this.subscribers.clear();
  }
}

export type FakeWebSocketServer = {
  close: () => Promise<void>;
  closeConnection: () => void;
  received: string[];
  sendBinary: (bytes: Uint8Array) => void;
  sendText: (text: string) => void;
  url: string;
  waitForConnection: () => Promise<void>;
  waitForReconnection: () => Promise<void>;
  waitForMessage: () => Promise<string>;
};

export async function createFakeWebSocketServer(): Promise<FakeWebSocketServer> {
  const received: string[] = [];
  const pendingConnections: Array<() => void> = [];
  const pendingReconnections: Array<() => void> = [];
  const pendingMessages: Array<(message: string) => void> = [];
  let connectedSocket: Socket | null = null;
  let connectionCount = 0;

  const server = createServer((socket) => {
    let handshakeDone = false;
    let buffer = Buffer.alloc(0);

    socket.on("data", (chunk) => {
      buffer = Buffer.concat([buffer, chunk]);

      if (!handshakeDone) {
        const marker = buffer.indexOf("\r\n\r\n");
        if (marker === -1) return;

        const request = buffer.slice(0, marker).toString("utf8");
        const keyMatch = request.match(/^Sec-WebSocket-Key:\s*(.+)$/im);
        if (!keyMatch) {
          socket.destroy();
          return;
        }

        socket.write(createHandshakeResponse(keyMatch[1].trim()));
        connectedSocket = socket;
        handshakeDone = true;
        connectionCount += 1;
        buffer = buffer.slice(marker + 4);

        for (const resolve of pendingConnections.splice(0)) {
          resolve();
        }

        if (connectionCount > 1) {
          for (const resolve of pendingReconnections.splice(0)) {
            resolve();
          }
        }
      }

      while (buffer.length > 0) {
        const decoded = decodeTextFrame(buffer);
        if (!decoded) break;

        buffer = buffer.slice(decoded.bytesRead);
        received.push(decoded.text);

        const resolve = pendingMessages.shift();
        if (resolve) {
          resolve(decoded.text);
        }
      }
    });
  });

  await new Promise<void>((resolve) => {
    server.listen(0, "127.0.0.1", resolve);
  });

  const address = server.address();
  if (!address || typeof address === "string") {
    throw new Error("failed to bind fake websocket server");
  }

  return {
    close: () => closeServer(server, connectedSocket),
    closeConnection: () => {
      connectedSocket?.destroy();
    },
    received,
    sendBinary: (bytes: Uint8Array) => {
      if (!connectedSocket) {
        throw new Error("websocket client not connected");
      }

      connectedSocket.write(encodeBinaryFrame(bytes));
    },
    sendText: (text: string) => {
      if (!connectedSocket) {
        throw new Error("websocket client not connected");
      }

      connectedSocket.write(encodeTextFrame(text));
    },
    url: `ws://127.0.0.1:${address.port}`,
    waitForConnection: () => {
      if (connectedSocket) return Promise.resolve();

      return new Promise<void>((resolve) => {
        pendingConnections.push(resolve);
      });
    },
    waitForReconnection: () => {
      return new Promise<void>((resolve) => {
        pendingReconnections.push(resolve);
      });
    },
    waitForMessage: () => {
      const last = received.at(-1);
      if (last) return Promise.resolve(last);

      return new Promise<string>((resolve) => {
        pendingMessages.push(resolve);
      });
    },
  };
}

export function sampleIntentionInput() {
  return {
    await_response: true,
    to: {
      context: "/ctx/target",
      cap: "echo",
      type: "user",
    },
    from: {
      context: "@ui_main",
      cap: "ui",
      type: "user",
    },
    identity: {
      id: "user-1",
    },
    params: {
      value: "hello",
    },
  };
}

export function sampleResponse(intentionId: string, context = "@ui_main:/result") {
  return {
    kind: "response" as const,
    ts: "2026-04-25T00:00:00.000Z",
    response: {
      intention_id: intentionId,
      to: {
        context,
        cap: "ui",
        type: "user",
      },
      from: {
        context: "/ctx/target",
        cap: "echo",
        type: "user",
      },
      identity: {
        id: "brique",
      },
      status: "ok",
      payload: {
        value: "done",
      },
    },
  };
}

function createHandshakeResponse(key: string): string {
  const accept = createHash("sha1")
    .update(`${key}258EAFA5-E914-47DA-95CA-C5AB0DC85B11`)
    .digest("base64");

  return [
    "HTTP/1.1 101 Switching Protocols",
    "Upgrade: websocket",
    "Connection: Upgrade",
    `Sec-WebSocket-Accept: ${accept}`,
    "",
    "",
  ].join("\r\n");
}

function encodeTextFrame(text: string): Buffer {
  const payload = Buffer.from(text, "utf8");

  if (payload.length <= 125) {
    return Buffer.concat([Buffer.from([0x81, payload.length]), payload]);
  }

  if (payload.length <= 65535) {
    const header = Buffer.alloc(4);
    header[0] = 0x81;
    header[1] = 126;
    header.writeUInt16BE(payload.length, 2);
    return Buffer.concat([header, payload]);
  }

  {
    throw new Error("test websocket frame too large");
  }
}

function encodeBinaryFrame(bytes: Uint8Array): Buffer {
  const payload = Buffer.from(bytes);

  if (payload.length > 125) {
    throw new Error("test websocket frame too large");
  }

  return Buffer.concat([Buffer.from([0x82, payload.length]), payload]);
}

function decodeTextFrame(buffer: Buffer): { bytesRead: number; text: string } | null {
  if (buffer.length < 2) return null;

  const first = buffer[0];
  const second = buffer[1];
  const opcode = first & 0x0f;
  const masked = (second & 0x80) !== 0;
  const lengthCode = second & 0x7f;

  if (opcode === 0x8) {
    return { bytesRead: 2, text: "" };
  }

  if (opcode !== 0x1) {
    throw new Error("unsupported websocket frame");
  }

  let offset = 2;
  let length = lengthCode;

  if (lengthCode === 126) {
    if (buffer.length < 4) return null;
    length = buffer.readUInt16BE(2);
    offset = 4;
  } else if (lengthCode === 127) {
    throw new Error("test websocket frame too large");
  }

  const maskLength = masked ? 4 : 0;
  const headerLength = offset + maskLength;
  const frameLength = headerLength + length;

  if (buffer.length < frameLength) return null;

  const payload = Buffer.from(buffer.slice(headerLength, frameLength));
  if (masked) {
    const mask = buffer.slice(offset, offset + 4);
    for (let i = 0; i < payload.length; i += 1) {
      payload[i] ^= mask[i % 4];
    }
  }

  return {
    bytesRead: frameLength,
    text: payload.toString("utf8"),
  };
}

function closeServer(server: Server, socket: Socket | null): Promise<void> {
  socket?.destroy();

  return new Promise((resolve) => {
    server.close(() => resolve());
  });
}
