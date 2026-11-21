/* Copyright 2026 Nicolas Cassan. Licensed under Apache-2.0. */

(() => {
  const marker = "brique.browser.page/1";
  const port = chrome.runtime.connect({ name: "brique.browser.bridge/1" });
  let pendingOpen;
  let activated = false;
  let closed = false;
  // The instance descriptor is renewed every ~90 seconds and the hello binds the
  // current one, so a descriptor fetched at page load is stale by the time the user
  // clicks. When the page asks for a refresh, the relay releases the open only with a
  // descriptor the page fetched after the trusted click, within this window.
  const DESCRIPTOR_WINDOW_MS = 3000;
  let descriptorWindowEnd = 0;

  const toPage = (message) => window.postMessage({ marker, direction: "extension", message }, location.origin);
  const tryOpen = () => {
    if (closed || !activated || !pendingOpen) return;
    if (pendingOpen.refreshDescriptor) {
      descriptorWindowEnd = Date.now() + DESCRIPTOR_WINDOW_MS;
      toPage({ kind: "descriptor-request" });
      return;
    }
    port.postMessage(pendingOpen);
    pendingOpen = undefined;
    activated = false;
  };
  const releaseWithDescriptor = (descriptorB64) => {
    if (closed || !pendingOpen?.refreshDescriptor || Date.now() > descriptorWindowEnd) return;
    const open = { kind: "open", uiName: pendingOpen.uiName, ...(descriptorB64 === undefined ? {} : { descriptorB64 }) };
    pendingOpen = undefined;
    descriptorWindowEnd = 0;
    port.postMessage(open);
  };
  const activation = (event) => {
    if (event.isTrusted && navigator.userActivation?.isActive) {
      activated = true;
      tryOpen();
      activated = false;
    }
  };
  addEventListener("pointerdown", activation, true);
  addEventListener("keydown", activation, true);
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
