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

import React, { useEffect, useMemo, useState } from "react";
import { PulseProvider } from "@spark/pulse/react";
import { PulseRuntime } from "@spark/pulse/runtime";
import { CounterPlayground } from "../counter_context/ui/CounterPlayground.js";
import { DisplayModeBlock } from "./blocks/DisplayModeBlock.js";
import { FlowPlayground } from "../inspector_context/ui/FlowPlayground.js";
import { StreamPlayground } from "../stream_context/ui/StreamPlayground.js";
import { NestedProjectionPlayground } from "../inspector_context/ui/NestedProjectionPlayground.js";
import { ProjectionInspector } from "./blocks/ProjectionInspector.js";
import type { SandboxEmission } from "./blocks/sandboxPulse.js";
import { TextPlayground } from "../text_context/ui/TextPlayground.js";

export type SandboxRootProps = {
  pulse?: PulseRuntime;
  transportUrl?: string;
  rootContextId?: string;
};

export function SandboxRoot({
  pulse: providedPulse,
  transportUrl,
  rootContextId,
}: SandboxRootProps) {
  const pulse = useMemo(() => {
    if (providedPulse) return providedPulse;
    return new PulseRuntime({
      webSocketUrl: transportUrl ?? resolveTransportUrl(),
      maxMessagesPerFlow: 20,
      terminalFlowTtlMs: 30000,
    });
  }, [providedPulse, transportUrl]);

  const [lastEmission, setLastEmission] = useState<SandboxEmission | null>(null);

  useEffect(() => {
    if (providedPulse) return;
    return () => pulse.teardown();
  }, [providedPulse, pulse]);

  return (
    <PulseProvider pulse={pulse}>
      <main data-testid="sandbox-root" style={styles.root}>
        <header style={styles.header}>
          <h1 style={styles.title}>Spark Pulse Sandbox</h1>
          <div data-testid="runtime-identity" style={styles.runtime}>
            <div data-testid="runtime-ui-name">uiName: {pulse.ui.getName()}</div>
            <div data-testid="runtime-ui-address-prefix">
              uiAddressPrefix: {pulse.ui.getAddressPrefix()}
            </div>
            <div data-testid="runtime-connection-state">connection: runtime-created</div>
          </div>
          <DisplayModeBlock />
        </header>
        <section style={styles.grid}>
          <TextPlayground rootContextId={rootContextId} onIntention={setLastEmission} />
          <CounterPlayground rootContextId={rootContextId} onIntention={setLastEmission} />
          <FlowPlayground rootContextId={rootContextId} onIntention={setLastEmission} />
          <NestedProjectionPlayground rootContextId={rootContextId} onIntention={setLastEmission} />
          <StreamPlayground rootContextId={rootContextId} />
        </section>
        <ProjectionInspector lastEmission={lastEmission} />
      </main>
    </PulseProvider>
  );
}

function resolveTransportUrl(): string {
  const processEnv = (globalThis as { process?: { env?: Record<string, string | undefined> } }).process?.env;
  return processEnv?.SPARK_UI_TRANSPORT_URL ?? "ws://localhost:8080/ws";
}

const styles: Record<string, React.CSSProperties> = {
  root: {
    fontFamily: "system-ui, sans-serif",
    padding: 24,
    color: "#17202a",
    background: "#f7f8fa",
    minHeight: "100vh",
  },
  header: {
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between",
    gap: 16,
    marginBottom: 20,
  },
  title: {
    fontSize: 24,
    lineHeight: 1.2,
    margin: 0,
  },
  runtime: {
    color: "#57606a",
    fontSize: 12,
    overflowWrap: "anywhere",
  },
  grid: {
    display: "grid",
    gridTemplateColumns: "repeat(auto-fit, minmax(280px, 1fr))",
    gap: 16,
  },
};

export default SandboxRoot;
