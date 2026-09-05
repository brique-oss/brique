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

import type { FlowProjectionFeedbackState } from "../contracts.js";

export type FlowProjectionFeedbackProps = {
  state: FlowProjectionFeedbackState;
  onRefresh: () => void;
};

export function FlowProjectionFeedback({
  state,
  onRefresh,
}: FlowProjectionFeedbackProps) {
  if (state.kind === "hidden") return null;

  return (
    <div
      aria-live="polite"
      data-component="flow-projection-feedback"
      role={state.kind === "error" ? "alert" : "status"}
      style={styles.root}
    >
      <span>{state.message}</span>
      {state.kind === "error" && (
        <button onClick={onRefresh} style={styles.button} type="button">
          Retry
        </button>
      )}
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  root: {
    position: "absolute",
    inset: "72px 0 0",
    zIndex: 10,
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    gap: "10px",
    padding: "16px",
    boxSizing: "border-box",
    background: "var(--syn-feedback-warning-bg)",
    color: "var(--syn-feedback-warning)",
    fontSize: "12px",
    textAlign: "center",
  },
  button: {
    border: "1px solid var(--syn-control-border)",
    borderRadius: "4px",
    padding: "4px 8px",
    background: "var(--syn-control-bg)",
    color: "var(--syn-text-primary)",
    cursor: "pointer",
    fontFamily: "inherit",
  },
};
