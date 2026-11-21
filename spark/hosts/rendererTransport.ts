/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

import {
  BridgeTransport,
  PulseRuntime,
  TransportManager,
	WebSocketTransport,
  type SerializedTransportBridge,
} from "../../pulse/runtime/index.js";
import type { SparkRendererPayload } from "./rendererPayload.js";
import { AuthoritySwitchTransport, type BrowserAuthority } from "../browser/AuthoritySwitchTransport.js";

export function createRendererPulse(payload: SparkRendererPayload): PulseRuntime {
  const bridge = (globalThis as { __SPARK_TRANSPORT_BRIDGE__?: SerializedTransportBridge })
    .__SPARK_TRANSPORT_BRIDGE__;
  if (!bridge) {
    throw new Error("Missing trusted Spark transport bridge");
  }
  const transports = new TransportManager();
  transports.register(payload.uiName, new BridgeTransport(bridge));
  return new PulseRuntime({
    transportManager: transports,
    transportName: payload.uiName,
    uiName: payload.uiName,
    uiAddressPrefix: payload.uiAddressPrefix,
  });
}

export function createPublicWebPulse(payload: SparkRendererPayload, url: string): PulseRuntime {
	return createBrowserWebPulse(payload, url);
}

export function createBrowserWebPulse(payload: SparkRendererPayload, url: string,
	authorityChanged?: (authority: BrowserAuthority) => void): PulseRuntime {
	const transports = new TransportManager();
	const bridge = (globalThis as { __SPARK_TRANSPORT_BRIDGE__?: SerializedTransportBridge })
		.__SPARK_TRANSPORT_BRIDGE__;
	transports.register(payload.uiName, new AuthoritySwitchTransport(
		() => new WebSocketTransport(url, {
			protocols: "brique.public-web.v1",
			maxMessageBytes: 64 << 10,
			maxBufferedBytes: 1 << 20,
		}),
		bridge ? new BridgeTransport(bridge) : undefined,
		authorityChanged,
	));
	return new PulseRuntime({
		transportManager: transports,
		transportName: payload.uiName,
		uiName: payload.uiName,
		uiAddressPrefix: payload.uiAddressPrefix,
	});
}
