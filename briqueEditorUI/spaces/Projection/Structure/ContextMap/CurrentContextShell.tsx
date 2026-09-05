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

import type { ReactNode } from "react";
import type { ActiveContext } from "./contracts.js";

export type CurrentContextShellProps = {
  activeContext: ActiveContext;
  loading: boolean;
  error: string | undefined;
  children: ReactNode;
};

export function CurrentContextShell({
  activeContext,
  loading,
  error,
  children,
}: CurrentContextShellProps) {
  const segments = activeContext.split("/").filter(Boolean);
  const label = segments[segments.length - 1] ?? activeContext;

  return (
    <div style={styles.shell}>
      <span style={styles.label}>{label}</span>
      {loading && <div style={styles.status}>Loading...</div>}
      {error && <div style={styles.status}>{error}</div>}
      {!loading && !error && children}
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  shell: {
    display: "flex",
    flexDirection: "column",
    flex: 1,
    margin: "12px",
    border: "1px solid var(--syn-structure-node-border)",
    borderRadius: "8px",
    minHeight: 0,
    overflow: "hidden",
  },
  label: {
    padding: "8px 12px",
    borderBottom: "1px solid var(--syn-border-soft)",
    fontWeight: 700,
    fontSize: "13px",
    opacity: 0.85,
  },
  status: {
    flex: 1,
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    opacity: 0.5,
    fontSize: "13px",
  },
};
