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
import { useHostBridge } from "../../../HostBridge/context.js";
import { useBriqueSubstrate } from "../../../React_Substrate_Adapter/context.js";
import { createCapabilityClient } from "../../../Brique_Substrate/capability/index.js";
import { toPosixPath } from "../../shared/pathUtils.js";

type UiConfig = {
  name?: string;
  adapter?: string;
  host?: string;
  root_view?: string;
  ws_addr?: string;
  ws_path?: string;
};

export type UiProjectionsSectionProps = {
  selectedContext: string | undefined;
};

export function UiProjectionsSection({ selectedContext }: UiProjectionsSectionProps) {
  const { hostBridge, contextPath } = useHostBridge();
  const substrate = useBriqueSubstrate();
  const capabilityClient = useMemo(() => createCapabilityClient(substrate), [substrate]);
  const [uiConfig, setUiConfig] = useState<UiConfig | undefined>(undefined);
  const [flash, setFlash] = useState(false);

  // Load ui_config from brique section whenever selectedContext changes
  useEffect(() => {
    setUiConfig(undefined);
    if (!selectedContext) return;
    let cancelled = false;
    capabilityClient.read.meaning(selectedContext, {
      input: [{ element_kind: "context", element_name: selectedContext, sections: ["brique"] }],
    }).then((result) => {
      if (cancelled || !result.ok || !result.payload) return;
      const item = result.payload.result?.[0];
      if (!item?.ok) return;
      const brique = item.descriptor?.brique as Record<string, unknown> | undefined;
      const cfg = brique?.ui_config as UiConfig | undefined;
      if (cfg) setUiConfig(cfg);
    }).catch(() => {});
    return () => { cancelled = true; };
  }, [selectedContext]); // eslint-disable-line react-hooks/exhaustive-deps

  // Derive context.json path from contextPath (editor root) + selectedContext
  const contextJsonPath = useMemo(() => {
    if (!contextPath || !selectedContext) return undefined;
    // contextPath = /path/to/root/context.json
    // selectedContext = /root/app_test → strip /root, append to contextDir
    const rootDir = toPosixPath(contextPath).replace(/\/context\.json$/, "");
    const rootId = "/root";
    if (!selectedContext.startsWith(rootId)) return undefined;
    const relative = selectedContext.slice(rootId.length);
    return `${rootDir}${relative}/context.json`;
  }, [contextPath, selectedContext]);

  const canStart = !!contextJsonPath && !!hostBridge?.launchContextUI && !!uiConfig;
  const label = uiConfig?.name ?? selectedContext?.split("/").pop() ?? "UI";

  const handleStart = useCallback(async () => {
    if (!contextJsonPath || !hostBridge?.launchContextUI) return;
    await hostBridge.launchContextUI(contextJsonPath);
  }, [contextJsonPath, hostBridge]);

  return (
    <section aria-label="UI Projections" style={styles.root}>
      <div style={styles.label}>UI Projections</div>
      {!selectedContext ? (
        <p style={styles.empty}>No context selected.</p>
      ) : !uiConfig ? (
        <p style={styles.empty}>No UI projections defined.</p>
      ) : (
        <button
          onClick={handleStart}
          onMouseDown={() => canStart && setFlash(true)}
          onMouseUp={() => setFlash(false)}
          onMouseLeave={() => setFlash(false)}
          style={{ ...styles.button, ...(flash ? styles.buttonFlash : undefined), ...(!canStart ? styles.buttonDisabled : undefined) }}
          disabled={!canStart}
          type="button"
        >
          {label}
        </button>
      )}
    </section>
  );
}

const styles: Record<string, React.CSSProperties> = {
  root: { display: "flex", flexDirection: "column", gap: 6, padding: "8px 10px", borderBottom: "1px solid var(--syn-border-soft)" },
  label: { fontSize: 9, fontWeight: 700, textTransform: "uppercase", letterSpacing: "0.08em", opacity: 0.4 },
  empty: { margin: 0, fontSize: 11, opacity: 0.4 },
  button: { alignSelf: "flex-start", border: "1px solid var(--syn-control-border)", borderRadius: 4, background: "var(--syn-control-bg)", color: "inherit", cursor: "pointer", fontFamily: "inherit", fontSize: 11, padding: "3px 8px" },
  buttonFlash: { background: "var(--syn-feedback-success-bg)", borderColor: "var(--syn-feedback-success)", color: "var(--syn-feedback-success)" },
  buttonDisabled: { opacity: 0.3, cursor: "default" },
};
