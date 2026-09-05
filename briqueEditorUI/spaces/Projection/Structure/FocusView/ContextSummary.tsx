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

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useBriqueSubstrate } from "../../../../React_Substrate_Adapter/context.js";
import { createCapabilityClient } from "../../../../Brique_Substrate/capability/index.js";
import type { MeaningDescriptor } from "../../../../Brique_Substrate/capability/typed.js";
import type { ActiveContext } from "../HierarchyOverview/contracts.js";
import type { ElementSelectionRequest } from "./contracts.js";

export type ContextSummaryProps = {
  activeContext: ActiveContext;
  onSelectDescriptor: (request: ElementSelectionRequest) => void;
  onOpenInNewTab?: (contextPath: string) => void;
};

export function ContextSummary({ activeContext, onSelectDescriptor, onOpenInNewTab }: ContextSummaryProps) {
  const substrate = useBriqueSubstrate();
  const capabilityClient = useMemo(() => createCapabilityClient(substrate), [substrate]);
  const [meaning, setMeaning] = useState<MeaningDescriptor | undefined>(undefined);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | undefined>(undefined);
  const requestSeqRef = useRef(0);

  const readMeaning = useCallback(
    async (context: ActiveContext) => {
      const seq = ++requestSeqRef.current;
      setLoading(true);
      setError(undefined);
      try {
        const result = await capabilityClient.read.meaning(context, {
          input: [{ element_kind: "context", element_name: context, sections: ["objective"] }],
        });
        if (seq !== requestSeqRef.current) return;
        if (!result.ok || !result.payload) {
          setError(result.error?.message ?? "Unable to load context meaning.");
          setMeaning(undefined);
        } else {
          const item = result.payload.result[0] as Record<string, unknown> | undefined;
          const descriptor = item?.descriptor as Record<string, unknown> | undefined;
          const objective = (descriptor?.objective ?? item?.meaning) as MeaningDescriptor["objective"] | undefined;
          setMeaning(objective ? { objective } : undefined);
        }
      } catch (err) {
        if (seq !== requestSeqRef.current) return;
        setError(err instanceof Error ? err.message : "Unable to load context meaning.");
      } finally {
        if (seq === requestSeqRef.current) setLoading(false);
      }
    },
    [capabilityClient]
  );

  useEffect(() => {
    void readMeaning(activeContext);
  }, [activeContext, readMeaning]);

  const label = activeContext.split("/").filter(Boolean);
  const name = label[label.length - 1] ?? activeContext;
  const objective = meaning?.objective as Record<string, unknown> | undefined;
  const description = objective?.description as string | undefined;

  function handleSelectDescriptor() {
    onSelectDescriptor({
      element: {
        kind: "context",
        name: activeContext,
        key: activeContext,
      },
    });
  }

  return (
    <div
      style={styles.section}
      onClick={handleSelectDescriptor}
      onAuxClick={(e) => { if (e.button === 1) { e.preventDefault(); onOpenInNewTab?.(activeContext); } }}
      role="button"
      tabIndex={0}
    >
      <div style={styles.identity}>
        <span style={styles.name}>{name}</span>
        <span style={styles.path}>{activeContext}</span>
      </div>
      {loading && <span style={styles.status}>read.meaning({"{"}element_kind: "context", element_name: "{activeContext}"{"}"})</span>}
      {error && <span style={styles.status}>{error}</span>}
      {!loading && !error && description && (
        <>
          <div style={styles.separator} />
          <p style={styles.description}>{description}</p>
        </>
      )}
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  section: {
    display: "flex",
    flexDirection: "column",
    gap: "4px",
    padding: "12px",
    borderBottom: "1px solid var(--syn-border-soft)",
    cursor: "pointer",
    userSelect: "none",
  },
  identity: {
    display: "flex",
    flexDirection: "column",
    alignItems: "flex-end",
    gap: "2px",
  },
  name: {
    fontWeight: 700,
    fontSize: "14px",
    textAlign: "right",
  },
  path: {
    fontSize: "11px",
    opacity: 0.5,
    fontFamily: "monospace",
    alignSelf: "flex-end",
    textAlign: "right",
  },
  separator: {
    width: "100%",
    height: "1px",
    background: "var(--syn-border-medium)",
    margin: "4px 0",
  },
  description: {
    margin: 0,
    fontSize: "12px",
    opacity: 0.8,
    lineHeight: 1.5,
    flex: 1,
  },
  status: {
    fontSize: "12px",
    opacity: 0.5,
  },
};
