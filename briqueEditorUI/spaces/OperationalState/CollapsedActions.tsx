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

import { useCallback, useEffect, useMemo, useState } from "react";
import { useBriqueSubstrate } from "../../React_Substrate_Adapter/context.js";
import { useHostBridge } from "../../HostBridge/context.js";
import { createCapabilityClient } from "../../Brique_Substrate/capability/index.js";
import type { StructureNode } from "../../Brique_Substrate/capability/typed.js";
import { toPosixPath } from "../shared/pathUtils.js";

function parentAndChild(contextPath: string): { parent: string; child: string } | undefined {
  const parts = contextPath.split("/").filter(Boolean);
  if (parts.length === 0) return undefined;
  const child = parts[parts.length - 1];
  const parent = "/" + parts.slice(0, -1).join("/");
  return { parent: parent || "/root", child };
}

export type CollapsedActionsProps = {
  selectedContext: string | undefined;
};

export function CollapsedActions({ selectedContext }: CollapsedActionsProps) {
  const substrate = useBriqueSubstrate();
  const { hostBridge, contextPath } = useHostBridge();
  const capabilityClient = useMemo(() => createCapabilityClient(substrate), [substrate]);

  const isRoot = selectedContext === "/root";
  const derived = selectedContext && !isRoot ? parentAndChild(selectedContext) : undefined;

  // UI config
  const [uiName, setUiName] = useState<string | undefined>(undefined);
  useEffect(() => {
    setUiName(undefined);
    if (!selectedContext) return;
    let cancelled = false;
    capabilityClient.read.meaning(selectedContext, {
      input: [{ element_kind: "context", element_name: selectedContext, sections: ["brique"] }],
    }).then((result) => {
      if (cancelled || !result.ok || !result.payload) return;
      const item = result.payload.result?.[0];
      if (!item?.ok) return;
      const brique = item.descriptor?.brique as Record<string, unknown> | undefined;
      const cfg = brique?.ui_config as { name?: string } | undefined;
      if (cfg?.name) setUiName(cfg.name);
    }).catch(() => {});
    return () => { cancelled = true; };
  }, [selectedContext]); // eslint-disable-line react-hooks/exhaustive-deps

  const contextJsonPath = useMemo(() => {
    if (!contextPath || !selectedContext) return undefined;
    const rootDir = toPosixPath(contextPath).replace(/\/context\.json$/, "");
    const rootId = "/root";
    if (!selectedContext.startsWith(rootId)) return undefined;
    const relative = selectedContext.slice(rootId.length);
    return `${rootDir}${relative}/context.json`;
  }, [contextPath, selectedContext]);

  const handleRestartContext = useCallback(async (e: React.MouseEvent) => {
    e.stopPropagation();
    if (!derived) return;
    await substrate.mutate({ context: derived.parent, capability: "restart", params: { child: derived.child }, awaitResponse: false });
  }, [substrate, derived]);

  const handleRestartChildren = useCallback(async (e: React.MouseEvent) => {
    e.stopPropagation();
    if (!selectedContext) return;
    const result = await capabilityClient.read.structure(selectedContext, { depth: 1 });
    if (!result.ok || !result.payload) return;
    for (const child of result.payload.root?.children ?? []) {
      if (child.kind !== "context" || !child.name) continue;
      await substrate.mutate({ context: selectedContext, capability: "restart", params: { child: child.name }, awaitResponse: false });
    }
  }, [substrate, capabilityClient, selectedContext]);

  const handleLaunchUI = useCallback(async (e: React.MouseEvent) => {
    e.stopPropagation();
    if (!contextJsonPath || !hostBridge?.launchContextUI) return;
    await hostBridge.launchContextUI(contextJsonPath);
  }, [contextJsonPath, hostBridge]);

  const handleRebuild = useCallback(async (e: React.MouseEvent) => {
    e.stopPropagation();
    if (!selectedContext) return;
    await capabilityClient.meaning.update("/root", { context: [selectedContext] });
  }, [capabilityClient, selectedContext]);

  const handleRebuildTree = useCallback(async (e: React.MouseEvent) => {
    e.stopPropagation();
    if (!selectedContext) return;
    const structResult = await capabilityClient.read.structure(selectedContext, { depth: 8 });
    const contexts: string[] = [selectedContext];
    if (structResult.ok && structResult.payload?.root) {
      function collect(node: StructureNode, parentPath: string) {
        for (const child of node.children ?? []) {
          if (child.kind === "context" && child.name) {
            const p = `${parentPath}/${child.name}`;
            contexts.push(p);
            collect(child, p);
          }
        }
      }
      collect(structResult.payload.root, selectedContext);
    }
    await capabilityClient.meaning.update("/root", { context: contexts });
  }, [capabilityClient, selectedContext]);

  if (!selectedContext) return null;

  const hasUI = !!(uiName && contextJsonPath && hostBridge?.launchContextUI);

  return (
    <div style={styles.root}>
      <span style={styles.section}>Lifecycle</span>
      {!isRoot && derived && (
        <ActionBtn label="Restart" onClick={handleRestartContext} />
      )}
      <ActionBtn label="Restart Children" onClick={handleRestartChildren} />

      {hasUI && (
        <>
          <span style={styles.sep} />
          <span style={styles.section}>UI</span>
          <ActionBtn label={uiName!} onClick={handleLaunchUI} />
        </>
      )}

      <span style={styles.sep} />
      <span style={styles.section}>Semantic</span>
      <ActionBtn label="Rebuild" onClick={handleRebuild} />
      <ActionBtn label="Rebuild Tree" onClick={handleRebuildTree} />
    </div>
  );
}

function ActionBtn({ label, onClick }: { label: string; onClick: (e: React.MouseEvent) => void }) {
  const [flash, setFlash] = useState(false);
  return (
    <button
      onClick={onClick}
      onMouseDown={(e) => { e.stopPropagation(); setFlash(true); }}
      onMouseUp={(e) => { e.stopPropagation(); setFlash(false); }}
      onMouseLeave={() => setFlash(false)}
      style={{ ...styles.btn, ...(flash ? styles.btnFlash : undefined) }}
      type="button"
    >
      {label}
    </button>
  );
}

const styles: Record<string, React.CSSProperties> = {
  root: { display: "flex", alignItems: "center", gap: 4, flex: 1, overflow: "hidden", minWidth: 0 },
  section: { fontSize: 8, fontWeight: 700, textTransform: "uppercase", letterSpacing: "0.08em", opacity: 0.4, whiteSpace: "nowrap", flexShrink: 0 },
  sep: { width: 1, height: 10, background: "var(--syn-border-medium)", flexShrink: 0 },
  btn: { border: "1px solid var(--syn-control-border)", borderRadius: 3, background: "var(--syn-control-bg)", color: "var(--syn-text-secondary)", cursor: "pointer", fontFamily: "monospace", fontSize: 9, fontWeight: 700, padding: "1px 6px", whiteSpace: "nowrap", flexShrink: 0 },
  btnFlash: { background: "var(--syn-control-active-bg)", borderColor: "var(--syn-selected-border)", color: "var(--syn-feedback-warning)" },
};
