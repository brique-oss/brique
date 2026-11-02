/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

import {
  BridgeTransport,
  PulseRuntime,
  TransportManager,
  type SerializedTransportBridge,
} from "../../pulse/runtime/index.js";
import type { SparkRendererPayload } from "./rendererPayload.js";

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
