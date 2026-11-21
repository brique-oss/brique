/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

// Minimal WebDriver BiDi client for the Firefox campaign. It exposes the same
// small surface as the CDP client (evaluate, navigate, preload, click) so the
// campaign scenario is written once.
import WebSocket from "ws";
import { sleep } from "./cdp.mjs";

export class BiDi {
  static async connect(port, deadlineMilliseconds = 20000) {
    const deadline = Date.now() + deadlineMilliseconds;
    for (;;) {
      try {
        const socket = await new Promise((resolve, reject) => {
          const candidate = new WebSocket(`ws://127.0.0.1:${port}/session`, { perMessageDeflate: false });
          candidate.once("open", () => resolve(candidate));
          candidate.once("error", reject);
        });
        const client = new BiDi(socket);
        await client.send("session.new", { capabilities: {} });
        const tree = await client.send("browsingContext.getTree", {});
        client.context = tree.contexts[0].context;
        return client;
      } catch (error) {
        if (Date.now() > deadline) throw error;
        await sleep(100);
      }
    }
  }

  constructor(socket) {
    this.socket = socket;
    this.next = 1;
    this.pending = new Map();
    this.events = [];
    socket.on("message", (data) => {
      const message = JSON.parse(data.toString());
      if (message.type === "event") { this.events.push(message); return; }
      const entry = this.pending.get(message.id);
      if (!entry) return;
      this.pending.delete(message.id);
      if (message.type === "error") entry.reject(new Error(`${entry.method}: ${message.error} ${message.message}`));
      else entry.resolve(message.result);
    });
  }

  send(method, params, timeoutMilliseconds = 15000) {
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
    const result = await this.send("script.evaluate", { expression, awaitPromise: true,
      target: { context: this.context }, resultOwnership: "none" });
    if (result.type === "exception") throw new Error(`evaluate failed: ${result.exceptionDetails?.text}`);
    return result.result.value;
  }

  async navigate(url) {
    await this.send("browsingContext.navigate", { context: this.context, url, wait: "none" });
  }

  async preload(source) {
    await this.send("script.addPreloadScript", { functionDeclaration: `() => { ${source} }` });
  }

  async installExtension(directory) {
    const result = await this.send("webExtension.install", { extensionData: { type: "path", path: directory } });
    return result.extension;
  }

  async click(x, y) {
    await this.send("input.performActions", { context: this.context, actions: [{
      type: "pointer", id: "mouse", parameters: { pointerType: "mouse" },
      actions: [
        { type: "pointerMove", x: Math.round(x), y: Math.round(y), origin: "viewport" },
        { type: "pointerDown", button: 0 },
        { type: "pointerUp", button: 0 },
      ] }] });
  }

  async close() { this.socket.close(); }
}
