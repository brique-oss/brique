import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import vm from "node:vm";

// The relay is a content script: it runs in the page's isolated world. It is loaded here in a minimal stand-in of that world.
const plain = (value: unknown) => JSON.parse(JSON.stringify(value));      // objects made inside the isolated context have another prototype
const source = readFileSync(new URL("../hosts/browser/extension/content-relay.js", import.meta.url), "utf8");

function relay() {
  const listeners: Record<string, Array<(event: any) => void>> = {};
  const toNative: any[] = [];
  const toPage: any[] = [];
  const port = { postMessage: (message: any) => toNative.push(message), disconnect() {}, onMessage: { addListener() {} }, onDisconnect: { addListener() {} } };
  const window: any = { postMessage: (message: any) => toPage.push(message.message) };
  const sandbox: any = {
    chrome: { runtime: { connect: () => port } }, window, location: { origin: "http://127.0.0.1:1" },
    navigator: { userActivation: { isActive: true } }, Date,
    addEventListener: (type: string, listener: (event: any) => void) => { (listeners[type] ??= []).push(listener); },
  };
  vm.runInNewContext(source, sandbox);
  const fromPage = (message: any) => listeners.message.forEach((listener) => listener({ source: window, origin: "http://127.0.0.1:1", data: { marker: "brique.browser.page/1", direction: "page", message } }));
  return { fromPage, toNative, toPage };
}

test("the page of the local instance (no descriptor) is opened without any click", () => {
  const { fromPage, toNative, toPage } = relay();
  assert.deepEqual(plain(toPage.shift()), { kind: "relay-ready" });
  fromPage({ kind: "open", uiName: "web", refreshDescriptor: true });
  assert.deepEqual(plain(toPage.shift()), { kind: "descriptor-request" });       // the relay first learns whether the page has a descriptor
  assert.equal(toNative.length, 0);
  fromPage({ kind: "descriptor" });                                       // none: this is the local instance
  assert.deepEqual(plain(toNative), [{ kind: "open", uiName: "web" }]);
});

test("a page with an instance descriptor is opened without any click, with the descriptor it just provided", () => {
  const { fromPage, toNative, toPage } = relay();
  toPage.shift();
  fromPage({ kind: "open", uiName: "web", refreshDescriptor: true });
  assert.deepEqual(plain(toPage.shift()), { kind: "descriptor-request" });
  fromPage({ kind: "descriptor", descriptorB64: "FRESH" });
  assert.deepEqual(plain(toNative), [{ kind: "open", uiName: "web", descriptorB64: "FRESH" }]);
});

test("a page that embeds its descriptor is opened at once", () => {
  const { fromPage, toNative, toPage } = relay();
  toPage.shift();
  fromPage({ kind: "open", uiName: "web", descriptorB64: "EMBEDDED" });
  assert.deepEqual(plain(toNative), [{ kind: "open", uiName: "web", descriptorB64: "EMBEDDED" }]);
});

test("a descriptor nobody asked for is ignored", () => {
  const { fromPage, toNative, toPage } = relay();
  toPage.shift();
  fromPage({ kind: "descriptor", descriptorB64: "UNASKED" });
  assert.equal(toNative.length, 0);
});
