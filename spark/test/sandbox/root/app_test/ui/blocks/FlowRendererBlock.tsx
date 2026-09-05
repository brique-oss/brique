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
import { useIntentionSubscription } from "@spark/pulse/react";
import { RenderValue } from "./renderValue.js";

export type FlowRendererBlockProps = {
  intentionId: string | null;
  pathUI: string;
};

export function FlowRendererBlock({ intentionId, pathUI }: FlowRendererBlockProps) {
  const state = useIntentionSubscription(intentionId);

  return (
    <div data-testid="flow-renderer" style={styles.renderer}>
      <div style={styles.row}>
        <strong data-testid="flow-renderer-path">{pathUI}</strong>
        <span data-testid="flow-renderer-status">{state.status}</span>
      </div>
      <div data-testid="flow-renderer-intention-id" style={styles.meta}>
        intentionId: {intentionId ?? "none"}
      </div>
      <div data-testid="flow-renderer-message-kind" style={styles.meta}>
        message: {state.message?.kind ?? "none"}
      </div>
      <div data-testid="flow-renderer-payload">
        {state.error ? <RenderValue value={state.error} /> : <RenderValue value={state.payload} />}
      </div>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  renderer: {
    border: "1px solid #d0d7de",
    borderRadius: 6,
    padding: 10,
    background: "#ffffff",
    minHeight: 116,
  },
  row: {
    display: "flex",
    justifyContent: "space-between",
    gap: 8,
    marginBottom: 6,
  },
  meta: {
    color: "#57606a",
    fontSize: 12,
    overflowWrap: "anywhere",
    marginBottom: 6,
  },
};
