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

import { useCallback, useMemo, useState } from "react";
import { useBriqueSubstrate } from "../../../React_Substrate_Adapter/context.js";
import { createCapabilityClient } from "../../../Brique_Substrate/capability/index.js";
import type { StructureNode } from "../../../Brique_Substrate/capability/typed.js";

export type ContextLifecycleSectionProps = {
  selectedContext: string | undefined;
};

function parentAndChild(contextPath: string): { parent: string; child: string } | undefined {
  const parts = contextPath.split("/").filter(Boolean);
  if (parts.length === 0) return undefined;
  const child = parts[parts.length - 1];
  const parent = "/" + parts.slice(0, -1).join("/");
  return { parent: parent || "/root", child };
}

export function ContextLifecycleSection({ selectedContext }: ContextLifecycleSectionProps) {
  const substrate = useBriqueSubstrate();
  const capabilityClient = useMemo(() => createCapabilityClient(substrate), [substrate]);
  const canAct = !!selectedContext;
  const [flashContext, setFlashContext] = useState(false);
  const [flashChildren, setFlashChildren] = useState(false);
  const isRoot = selectedContext === "/root";
  const derived = selectedContext && !isRoot ? parentAndChild(selectedContext) : undefined;

  const handleRestartContext = useCallback(async () => {
    if (!derived) return;
    await substrate.mutate({
      context: derived.parent,
      capability: "restart",
      params: { child: derived.child },
      awaitResponse: false,
    });
  }, [substrate, derived]);

  const handleRestartChildren = useCallback(async () => {
    if (!selectedContext) return;
    const result = await capabilityClient.read.structure(selectedContext, { depth: 1 });
    if (!result.ok || !result.payload) return;
    const children: StructureNode[] = result.payload.root?.children ?? [];
    for (const child of children) {
      if (child.kind !== "context" || !child.name) continue;
      await substrate.mutate({
        context: selectedContext,
        capability: "restart",
        params: { child: child.name },
        awaitResponse: false,
      });
    }
  }, [substrate, capabilityClient, selectedContext]);

  return (
    <section aria-label="Context Lifecycle" style={styles.root}>
      <div style={styles.label}>Context Lifecycle</div>
      {!canAct ? (
        <p style={styles.empty}>No context selected.</p>
      ) : (
        <div style={styles.actions}>
          <button
            disabled={isRoot}
            onClick={handleRestartContext}
            onMouseDown={() => !isRoot && setFlashContext(true)}
            onMouseUp={() => setFlashContext(false)}
            onMouseLeave={() => setFlashContext(false)}
            style={{ ...styles.button, ...(isRoot ? styles.buttonDisabled : undefined), ...(flashContext ? styles.buttonFlash : undefined) }}
            title={isRoot ? "Root context has no parent" : `Restart via ${derived?.parent}`}
            type="button"
          >
            Restart Context
          </button>
          <button
            onClick={handleRestartChildren}
            onMouseDown={() => setFlashChildren(true)}
            onMouseUp={() => setFlashChildren(false)}
            onMouseLeave={() => setFlashChildren(false)}
            style={{ ...styles.button, ...(flashChildren ? styles.buttonFlash : undefined) }}
            title="Restart all direct children"
            type="button"
          >
            Restart Children
          </button>
        </div>
      )}
    </section>
  );
}

const styles: Record<string, React.CSSProperties> = {
  root: { display: "flex", flexDirection: "column", gap: 6, padding: "8px 10px", borderBottom: "1px solid var(--syn-border-soft)" },
  label: { fontSize: 9, fontWeight: 700, textTransform: "uppercase", letterSpacing: "0.08em", opacity: 0.4 },
  empty: { margin: 0, fontSize: 11, opacity: 0.4 },
  actions: { display: "flex", flexDirection: "column", gap: 4 },
  button: { alignSelf: "flex-start", border: "1px solid var(--syn-control-border)", borderRadius: 4, background: "var(--syn-control-bg)", color: "inherit", cursor: "pointer", fontFamily: "inherit", fontSize: 11, padding: "3px 8px" },
  buttonDisabled: { opacity: 0.3, cursor: "default" },
  buttonFlash: { background: "var(--syn-feedback-error-bg)", borderColor: "var(--syn-feedback-error)", color: "var(--syn-feedback-error)" },
};
