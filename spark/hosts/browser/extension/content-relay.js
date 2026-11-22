/* Copyright 2026 Nicolas Cassan. Licensed under Apache-2.0. */

(() => {
  const marker = "brique.browser.page/1";
  const port = chrome.runtime.connect({ name: "brique.browser.bridge/1" });
  let pendingOpen;
  let closed = false;
  // The instance descriptor is renewed every ~90 seconds and the hello binds the current one, so the relay asks the
  // page for a fresh one at the moment of the open and accepts the answer only within this window.
  const DESCRIPTOR_WINDOW_MS = 3000;
  let descriptorWindowEnd = 0;

  const toPage = (message) => window.postMessage({ marker, direction: "extension", message }, location.origin);
  // No click is asked. A page of the local instance carries no descriptor and the native host accepts only the exact
  // address of the web server of an associated instance. A page that carries a descriptor (another instance, reached
  // with the identity of the person) can act only if its browser origin is in the descriptor's signed ui_origins:
  // the instance of the person checks it before signing anything.
  const askDescriptor = () => { descriptorWindowEnd = Date.now() + DESCRIPTOR_WINDOW_MS; toPage({ kind: "descriptor-request" }); };
  const tryOpen = () => {
    if (closed || !pendingOpen) return;
    if (pendingOpen.refreshDescriptor) { askDescriptor(); return; }
    port.postMessage(pendingOpen);
    pendingOpen = undefined;
  };
  const releaseWithDescriptor = (descriptorB64) => {
    if (closed || !pendingOpen?.refreshDescriptor || Date.now() > descriptorWindowEnd) return;
    const open = { kind: "open", uiName: pendingOpen.uiName, ...(descriptorB64 === undefined ? {} : { descriptorB64 }) };
    pendingOpen = undefined;
    descriptorWindowEnd = 0;
    port.postMessage(open);
  };
  addEventListener("pagehide", () => port.disconnect(), { once: true });

  port.onMessage.addListener((message) => {
    if (message?.kind === "ready") { toPage(message); return; }
    toPage(message);
  });
  port.onDisconnect.addListener(() => {
    closed = true;
    toPage({ kind: "disconnected" });
  });
  addEventListener("message", (event) => {
    if (event.source !== window || event.origin !== location.origin || event.data?.marker !== marker ||
        event.data?.direction !== "page" || typeof event.data.message?.kind !== "string") return;
    const message = event.data.message;
    if (message.kind === "open") { pendingOpen = message; tryOpen(); return; }
    if (message.kind === "descriptor") { releaseWithDescriptor(message.descriptorB64); return; }
    if (message.kind === "send" || message.kind === "close") port.postMessage(message);
  });
  toPage({ kind: "relay-ready" });
})();
