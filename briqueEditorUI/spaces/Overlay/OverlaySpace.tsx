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

import { useEffect, useRef, useState } from "react";
import { useHostBridge } from "../../HostBridge/context.js";
import { useBriqueSubstrate } from "../../React_Substrate_Adapter/context.js";
import { createCapabilityClient } from "../../Brique_Substrate/capability/index.js";
import type { OpenTarget, PreviewTarget, ResolvedDocument, ResolvedMatter } from "./contracts.js";
import { ElementOpenDialog } from "./ElementOpenDialog.js";
import { ElementPreviewWindow } from "./ElementPreviewWindow.js";
import { OpenContext } from "./OpenContext.js";
import { PreviewContext } from "./PreviewContext.js";
import { toPosixPath } from "../shared/pathUtils.js";

function resolveMatterBriqueFilePath(
  contextPath: string | undefined,
  openTarget: OpenTarget,
  ext: string
): string | undefined {
  if (!contextPath) return undefined;
  const rootDir = toPosixPath(contextPath).replace(/\/context\.json$/, "");
  const contextSuffix = openTarget.context.replace(/^\/root\/?/, "");
  const contextDir = contextSuffix ? `${rootDir}/${contextSuffix}` : rootDir;
  return `${contextDir}/matter/${openTarget.elementName}.${ext}`;
}

export function OverlaySpaceProvider({ children }: { children: React.ReactNode }) {
  const substrate = useRef(useBriqueSubstrate());
  const capabilityClient = useRef(createCapabilityClient(substrate.current));
  const openSeq = useRef(0);
  const { contextPath } = useHostBridge();

  const [previewTarget, setPreviewTarget] = useState<PreviewTarget | undefined>(undefined);
  const [openTarget, setOpenTarget] = useState<OpenTarget | undefined>(undefined);
  const [resolvedDocument, setResolvedDocument] = useState<ResolvedDocument | undefined>(undefined);
  const [resolvedMatter, setResolvedMatter] = useState<ResolvedMatter | undefined>(undefined);

  const windowRef = useRef<HTMLDivElement>(null);

  // Dismiss preview on mousedown outside the preview window
  // Only dismiss on non-Ctrl mousedown — Ctrl+hover is the trigger mechanism,
  // and a Ctrl+click should not permanently kill the preview for subsequent hovers.
  useEffect(() => {
    const onMouseDown = (e: MouseEvent) => {
      if (e.ctrlKey) return;
      if (windowRef.current && !windowRef.current.contains(e.target as Node)) {
        setPreviewTarget(undefined);
      }
    };
    window.addEventListener("mousedown", onMouseDown);
    return () => window.removeEventListener("mousedown", onMouseDown);
  }, []);

  // Resolve matter when openTarget is a matter
  useEffect(() => {
    if (!openTarget || openTarget.elementKind !== "matter") {
      setResolvedMatter(undefined);
      return;
    }

    const seq = ++openSeq.current;
    const name = openTarget.elementName;

    void capabilityClient.current.matter.read(openTarget.context, {
      matter_id: name,
      read_mode: "brique|functional",
    }).then(
      (r) => {
        if (seq !== openSeq.current) return;
        if (!r.ok) {
          setResolvedMatter({ kind: "error", message: r.error?.message ?? "matter.read failed" });
          return;
        }
        const p = r.payload as Record<string, unknown> | undefined;
        const brique = p?.brique as Record<string, unknown> | undefined;
        const functional = p?.functional as Record<string, unknown> | undefined;
        const mode = brique?.substance_mode as string | undefined;

        if (mode === "brique") {
          const ext = String(functional?.format ?? "data").trim() || "data";
          const abs = resolveMatterBriqueFilePath(contextPath, openTarget, ext);
          if (abs) {
            setResolvedMatter({ kind: "brique_file", name, abs });
            return;
          }
        }

        if (mode !== "ext_ref") {
          const wrapperName = brique?.wrapper_name as string | undefined;
          setResolvedMatter({ kind: "unsupported", name, mode: mode ?? "unknown", wrapperName });
          return;
        }

        const extRef = (functional?.external_retrieval as Record<string, unknown> | undefined)
          ?.ext_ref as Record<string, unknown> | undefined;
        const extKind = extRef?.kind as string | undefined;
        const locator = String(extRef?.locator ?? "");

        if (extKind === "file_share" && locator.startsWith("file://")) {
          const abs = decodeURIComponent(locator.slice(7));
          setResolvedMatter({ kind: "file_share", name, abs, locator });
        } else if (extKind === "url") {
          setResolvedMatter({ kind: "url", name, locator });
        } else {
          setResolvedMatter({
            kind: "raw",
            name,
            label: extKind ?? "ext_ref",
            json: extRef ?? {},
          });
        }
      },
      (err: unknown) => {
        if (seq !== openSeq.current) return;
        setResolvedMatter({ kind: "error", message: err instanceof Error ? err.message : "matter.read failed" });
      }
    );
  }, [openTarget, contextPath]);

  // Resolve document when openTarget changes
  useEffect(() => {
    if (!openTarget || openTarget.elementKind !== "document") {
      setResolvedDocument(undefined);
      return;
    }

    const seq = ++openSeq.current;

    void capabilityClient.current.read.document(openTarget.context, {
      items: [{ name: openTarget.elementName }],
    }).then(
      (r) => {
        if (seq !== openSeq.current) return;
        if (!r.ok) {
          setResolvedDocument({ kind: "error", message: r.error?.message ?? "read.document failed" });
          return;
        }
        const results = (r.payload as { result?: unknown[] } | undefined)?.result ?? [];
        const item = results[0] as Record<string, unknown> | undefined;
        if (!item?.ok) {
          setResolvedDocument({ kind: "error", message: "Document not found." });
          return;
        }
        const target = item.target as Record<string, unknown> | undefined;
        const kind = target?.kind as string | undefined;
        const name = openTarget.elementName;

        if (kind === "local") {
          setResolvedDocument({
            kind: "local",
            name,
            rel: String(target?.rel ?? ""),
            abs: String(target?.abs ?? ""),
          });
        } else if (kind === "ref") {
          setResolvedDocument({
            kind: "ref",
            name,
            ref: String(target?.ref ?? ""),
          });
        } else {
          setResolvedDocument({ kind: "error", message: `Unknown document kind: ${kind ?? "?"}` });
        }
      },
      (err: unknown) => {
        if (seq !== openSeq.current) return;
        setResolvedDocument({ kind: "error", message: err instanceof Error ? err.message : "read.document failed" });
      }
    );
  }, [openTarget]);

  const handleCloseDialog = () => {
    setOpenTarget(undefined);
    setResolvedDocument(undefined);
    setResolvedMatter(undefined);
  };

  return (
    <PreviewContext.Provider value={{
      requestPreview: setPreviewTarget,
      dismissPreview: () => setPreviewTarget(undefined),
    }}>
      <OpenContext.Provider value={{
        requestOpen: setOpenTarget,
      }}>
        {children}
        <div style={styles.overlay}>
          {previewTarget && (
            <div ref={windowRef} style={styles.windowWrapper}>
              <ElementPreviewWindow target={previewTarget} />
            </div>
          )}
        </div>
        {openTarget && openTarget.elementKind === "document" && resolvedDocument && (
          <ElementOpenDialog
            target={openTarget}
            resolved={resolvedDocument}
            onClose={handleCloseDialog}
          />
        )}
        {openTarget && openTarget.elementKind === "matter" && resolvedMatter && (
          <ElementOpenDialog
            target={openTarget}
            resolvedMatter={resolvedMatter}
            onClose={handleCloseDialog}
          />
        )}
      </OpenContext.Provider>
    </PreviewContext.Provider>
  );
}

const styles: Record<string, React.CSSProperties> = {
  overlay: {
    position: "fixed",
    inset: 0,
    pointerEvents: "none",
    zIndex: 1000,
  },
  windowWrapper: {
    pointerEvents: "auto",
  },
};
