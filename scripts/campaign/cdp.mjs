/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

// Minimal Chrome DevTools Protocol client for the browser campaigns.
import net from "node:net";
import WebSocket from "ws";

export const sleep = (milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds));

export function freePort() {
  return new Promise((resolve, reject) => {
    const server = net.createServer();
    server.listen(0, "127.0.0.1", () => {
      const { port } = server.address();
      server.close(() => resolve(port));
    });
    server.on("error", reject);
  });
}

export async function waitFor(probe, timeoutMilliseconds, description) {
  const deadline = Date.now() + timeoutMilliseconds;
  while (Date.now() < deadline) {
    const value = await Promise.resolve().then(probe).catch(() => undefined);
    if (value) return value;
    await sleep(50);
  }
  throw new Error(`timed out waiting for ${description}`);
}

export class CDP {
  static connect(url) {
    return new Promise((resolve, reject) => {
      const socket = new WebSocket(url, { perMessageDeflate: false });
      socket.once("open", () => resolve(new CDP(socket)));
      socket.once("error", reject);
    });
  }

  constructor(socket) {
    this.socket = socket;
    this.next = 1;
    this.pending = new Map();
    this.events = [];
    socket.on("message", (data) => {
      const message = JSON.parse(data.toString());
      if (message.method) this.events.push(message);
      const entry = this.pending.get(message.id);
      if (!entry) return;
      this.pending.delete(message.id);
      if (message.error) entry.reject(new Error(`${entry.method}: ${message.error.message}`));
      else entry.resolve(message.result);
    });
  }

  // Every call is bounded: a page or worker that stops answering must fail the
  // campaign with the method name, not hang it.
  send(method, params = {}, timeoutMilliseconds = 15000) {
    const id = this.next++;
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(id);
        reject(new Error(`${method}: no answer after ${timeoutMilliseconds} ms`));
      }, timeoutMilliseconds);
      this.pending.set(id, { resolve: (value) => { clearTimeout(timer); resolve(value); },
        reject: (error) => { clearTimeout(timer); reject(error); }, method });
      this.socket.send(JSON.stringify({ id, method, params }));
    });
  }

  async evaluate(expression) {
    const result = await this.send("Runtime.evaluate", { expression, returnByValue: true, awaitPromise: true });
    if (result.exceptionDetails) throw new Error(`evaluate failed: ${result.exceptionDetails.text}`);
    return result.result.value;
  }

  async navigate(url) { await this.send("Page.enable"); await this.send("Page.navigate", { url }); }
  async preload(source) { await this.send("Page.addScriptToEvaluateOnNewDocument", { source }); }
  async click(x, y) {
    for (const type of ["mousePressed", "mouseReleased"]) {
      await this.send("Input.dispatchMouseEvent", { type, x, y, button: "left", clickCount: 1 });
    }
  }

  async close() {
    this.socket.close();
  }
}
