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

import { useHostBridge } from "../../../../HostBridge/context.js";
import type { ActiveContext } from "../HierarchyOverview/contracts.js";
import type { ActiveContextStructure } from "./contracts.js";
import { toPosixPath } from "../../../shared/pathUtils.js";

export type NativeEntrypointsProps = {
  activeContext: ActiveContext;
  activeContextStructure: ActiveContextStructure | undefined;
};

export function NativeEntrypoints({ activeContext, activeContextStructure }: NativeEntrypointsProps) {
  const { hostBridge, contextPath } = useHostBridge();

  const root = activeContextStructure?.root;
  const dirs = (root?.children ?? []).filter((c) => c.kind === "dir");
  const codeNode = dirs.find((c) => c.name === "code");
  const uiNode = dirs.find((c) => c.name === "ui");

  if (!codeNode && !uiNode) return null;

  function openDir(nodePath: string) {
    if (!hostBridge?.openLocalPath || !contextPath) return;
    // contextPath = /abs/path/to/root/context.json
    // activeContext = /root/app_test/counter_context
    // strip /root prefix from activeContext to get relative path from root dir
    const rootDir = toPosixPath(contextPath).replace(/\/context\.json$/, "");
    const contextSuffix = activeContext.replace(/^\/root\/?/, "");
    const activeContextDir = contextSuffix
      ? `${rootDir}/${contextSuffix}`
      : rootDir;
    const absPath = `${activeContextDir}/${nodePath}`;
    void hostBridge.openLocalPath({ path: absPath, reveal: true });
  }

  return (
    <div style={styles.bar}>
      {codeNode && (
        <button
          style={styles.button}
          onClick={() => openDir(codeNode.path ?? "code")}
          title="Open code directory"
        >
          CODE
        </button>
      )}
      {uiNode && (
        <button
          style={styles.button}
          onClick={() => openDir(uiNode.path ?? "ui")}
          title="Open UI directory"
        >
          UI
        </button>
      )}
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  bar: {
    display: "flex",
    justifyContent: "center",
    gap: "8px",
    padding: "8px 12px",
    borderTop: "1px solid var(--syn-border-soft)",
    flexShrink: 0,
  },
  button: {
    padding: "4px 14px",
    fontSize: "11px",
    fontWeight: 700,
    letterSpacing: "0.06em",
    border: "1px solid var(--syn-control-border)",
    borderRadius: "4px",
    background: "var(--syn-control-bg)",
    color: "var(--syn-text-primary)",
    cursor: "pointer",
    userSelect: "none",
  },
};
