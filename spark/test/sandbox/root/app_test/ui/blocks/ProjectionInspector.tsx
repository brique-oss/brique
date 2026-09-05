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

import React from "react";
import { useDisplayMode, useIntentionSubscription, usePathIntention } from "@spark/pulse/react";
import { RenderValue } from "./renderValue.js";
import type { SandboxEmission } from "./sandboxPulse.js";
import { inspectedSandboxUIPaths } from "./sandboxPaths.js";

export function ProjectionInspector({ lastEmission }: { lastEmission: SandboxEmission | null }) {
  const mode = useDisplayMode();
  const lastIntentionId = lastEmission?.intentionId ?? null;
  const latest = useIntentionSubscription(lastIntentionId);

  return (
    <section data-testid="projection-inspector" style={styles.section}>
      <h2 style={styles.heading}>Projection Inspector</h2>
      <div data-testid="display-mode-value" style={styles.meta}>displayMode: {mode}</div>
      <div data-testid="last-intention-id" style={styles.meta}>
        lastIntentionId: {lastIntentionId ?? "none"}
      </div>
      <div data-testid="last-source-address" style={styles.meta}>
        sourceAddress: {lastEmission?.sourceAddress ?? "none"}
      </div>
      <div data-testid="last-target-context" style={styles.meta}>
        targetContext: {lastEmission?.targetContext ?? "none"}
      </div>
      <div data-testid="last-target-capacity" style={styles.meta}>
        targetCapacity: {lastEmission?.targetCapacity ?? "none"}
      </div>
      <div data-testid="last-target-path-ui" style={styles.meta}>
        targetPathUI: {lastEmission?.targetPathUI ?? "none"}
      </div>
      <div data-testid="last-message-kind" style={styles.meta}>
        lastMessageKind: {latest.message?.kind ?? "none"}
      </div>
      <div style={styles.paths}>
        {inspectedSandboxUIPaths.map((path) => (
          <InspectorPath key={path} pathUI={path} />
        ))}
      </div>
      <RenderValue value={latest.error ?? latest.payload} />
    </section>
  );
}

function InspectorPath({ pathUI }: { pathUI: string }) {
  const intentionId = usePathIntention(pathUI);
  return (
    <div style={styles.pathRow}>
      <span>{pathUI}</span>
      <span>{intentionId ?? "none"}</span>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  section: {
    marginTop: 16,
    border: "1px solid #d0d7de",
    borderRadius: 8,
    padding: 14,
    background: "#ffffff",
  },
  heading: {
    fontSize: 16,
    margin: "0 0 10px",
  },
  meta: {
    color: "#57606a",
    fontSize: 12,
    overflowWrap: "anywhere",
    marginBottom: 6,
  },
  paths: {
    display: "grid",
    gap: 4,
    marginBottom: 10,
  },
  pathRow: {
    display: "grid",
    gridTemplateColumns: "minmax(120px, 180px) 1fr",
    gap: 8,
    fontSize: 12,
    overflowWrap: "anywhere",
  },
};
