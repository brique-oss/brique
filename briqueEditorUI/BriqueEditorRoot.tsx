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

import { useEffect, useMemo, useState } from "react";
import { PulseProvider } from "../pulse/adapters/react/index.js";
import { PulseRuntime } from "../pulse/runtime/index.js";
import {
  createDefaultRawResolver,
  createPulseRuntimeBridge,
  createBriqueSubstrate,
} from "./Brique_Substrate/substrate.js";
import { BriqueSubstrateProvider } from "./React_Substrate_Adapter/index.js";
import { BriqueEditorSpace } from "./spaces/BriqueEditorSpace.js";
import { HostBridgeProvider } from "./HostBridge/context.js";
import { ThemeProvider } from "./spaces/Theme/ThemeProvider.js";
import type { ConnectionState, EditorHostBridge } from "./types.js";

export type BriqueEditorRootProps = {
  pulse?: PulseRuntime;
  transportUrl?: string;
  rootContextId?: string;
  contextPath?: string;
  childContextPaths?: string[];
  hostBridge?: EditorHostBridge;
};

export function BriqueEditorRoot({
  pulse: providedPulse,
  transportUrl,
  contextPath,
  hostBridge,
}: BriqueEditorRootProps) {
  const pulse = useMemo(() => {
    if (providedPulse) return providedPulse;
    return new PulseRuntime({ webSocketUrl: transportUrl ?? resolveTransportUrl() });
  }, [providedPulse, transportUrl]);

  useEffect(() => {
    if (providedPulse) return;
    return () => pulse.teardown();
  }, [providedPulse, pulse]);

  const substrate = useMemo(() => {
    return createBriqueSubstrate({
      bridge: createPulseRuntimeBridge(pulse),
      rawResolver: createDefaultRawResolver(),
    });
  }, [pulse]);

  useEffect(() => {
    return () => substrate.teardown();
  }, [substrate]);

  const [connectionState, setConnectionState] =
    useState<ConnectionState>("disconnected");

  useEffect(() => {
    return pulse.subscribeToConnectionState((state) => {
      setConnectionState(state === "connected" ? "connected" : "disconnected");
    });
  }, [pulse]);

  return (
    <PulseProvider pulse={pulse}>
      <BriqueSubstrateProvider
        substrate={substrate}
        pulseConnectionState={connectionState}
      >
        <HostBridgeProvider hostBridge={hostBridge} contextPath={contextPath}>
          <ThemeProvider>
            <BriqueEditorSpace />
          </ThemeProvider>
        </HostBridgeProvider>
      </BriqueSubstrateProvider>
    </PulseProvider>
  );
}

function resolveTransportUrl(): string {
  const env = (globalThis as {
    process?: { env?: Record<string, string | undefined> };
  }).process?.env;

  return env?.SPARK_UI_TRANSPORT_URL ?? "ws://localhost:8080/ws";
}

export default BriqueEditorRoot;
