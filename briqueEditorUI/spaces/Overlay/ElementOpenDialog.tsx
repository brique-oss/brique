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

import { useEffect } from "react";
import { useHostBridge } from "../../HostBridge/context.js";
import type { OpenTarget, ResolvedDocument, ResolvedMatter } from "./contracts.js";
import { toPosixPath } from "../shared/pathUtils.js";

export type ElementOpenDialogProps =
  | { target: OpenTarget; resolved: ResolvedDocument; resolvedMatter?: undefined; onClose: () => void }
  | { target: OpenTarget; resolved?: undefined; resolvedMatter: ResolvedMatter; onClose: () => void };

export function ElementOpenDialog({ target, resolved, resolvedMatter, onClose }: ElementOpenDialogProps) {
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [onClose]);

  const displayName = `${target.context}::${target.elementName}`;

  return (
    <div style={styles.backdrop} onMouseDown={onClose}>
      <div style={styles.dialog} onMouseDown={(e) => e.stopPropagation()}>
        <div style={styles.header}>
          <span style={styles.kind}>{target.elementKind}</span>
          <span style={styles.name}>{displayName}</span>
        </div>
        <div style={styles.body}>
          {resolved !== undefined && (
            <>
              {resolved.kind === "local" && <LocalContent resolved={resolved} />}
              {resolved.kind === "ref" && <RefContent resolved={resolved} />}
              {resolved.kind === "error" && <span style={styles.error}>{resolved.message}</span>}
            </>
          )}
          {resolvedMatter !== undefined && (
            <>
              {resolvedMatter.kind === "file_share" && <MatterFileShareContent resolved={resolvedMatter} />}
              {resolvedMatter.kind === "brique_file" && <MatterBriqueFileContent resolved={resolvedMatter} />}
              {resolvedMatter.kind === "url" && <MatterUrlContent resolved={resolvedMatter} />}
              {resolvedMatter.kind === "raw" && <MatterRawContent resolved={resolvedMatter} />}
              {resolvedMatter.kind === "unsupported" && (
                <MatterUnsupportedContent resolved={resolvedMatter} />
              )}
              {resolvedMatter.kind === "error" && <span style={styles.error}>{resolvedMatter.message}</span>}
            </>
          )}
        </div>
      </div>
    </div>
  );
}

function LocalContent({ resolved }: { resolved: Extract<ResolvedDocument, { kind: "local" }> }) {
  const { hostBridge } = useHostBridge();
  const copyPath = () => void navigator.clipboard.writeText(resolved.abs);
  const openFile = () => void hostBridge?.openWithOS?.({ path: resolved.abs });
  const parts = toPosixPath(resolved.rel).split("/");
  const fileName = parts[parts.length - 1] ?? resolved.rel;

  const onDragStart = (e: React.DragEvent) => {
    e.dataTransfer.setData("text/uri-list", `file://${resolved.abs}`);
    e.dataTransfer.setData("text/plain", resolved.abs);
    e.dataTransfer.effectAllowed = "copy";
  };

  return (
    <div style={styles.content}>
      <div style={styles.row}>
        <span style={styles.label}>File</span>
        <span
          style={styles.draggable}
          draggable
          onDragStart={onDragStart}
          title="Drag to open in another application"
        >
          {fileName}
        </span>
      </div>
      <div style={styles.row}>
        <span style={styles.label}>Path</span>
        <span style={styles.valueMono}>{resolved.abs}</span>
      </div>
      <div style={styles.actions}>
        <button style={styles.copyButton} type="button" onClick={copyPath}>
          Copy path
        </button>
        {hostBridge?.openWithOS && (
          <button style={styles.openButton} type="button" onClick={openFile}>
            Open
          </button>
        )}
      </div>
    </div>
  );
}

function RefContent({ resolved }: { resolved: Extract<ResolvedDocument, { kind: "ref" }> }) {
  const { hostBridge } = useHostBridge();

  const isFileRef = resolved.ref.startsWith("file://");
  const absPath = isFileRef ? decodeURIComponent(resolved.ref.slice(7)) : undefined;

  const copyRef = () => void navigator.clipboard.writeText(absPath ?? resolved.ref);
  const openFile = () => absPath && void hostBridge?.openWithOS?.({ path: absPath });

  const parts = absPath ? toPosixPath(absPath).split("/") : [];
  const fileName = absPath ? (parts[parts.length - 1] ?? absPath) : undefined;

  const onDragStart = (e: React.DragEvent) => {
    e.dataTransfer.setData("text/uri-list", resolved.ref);
    e.dataTransfer.setData("text/plain", absPath ?? resolved.ref);
    e.dataTransfer.effectAllowed = "copy";
  };

  return (
    <div style={styles.content}>
      {isFileRef && fileName && (
        <div style={styles.row}>
          <span style={styles.label}>File</span>
          <span
            style={styles.draggable}
            draggable
            onDragStart={onDragStart}
            title="Drag to open in another application"
          >
            {fileName}
          </span>
        </div>
      )}
      <div style={styles.row}>
        <span style={styles.label}>{isFileRef ? "Path" : "Reference"}</span>
        <span
          style={isFileRef ? styles.valueMono : { ...styles.valueMono, ...styles.draggable }}
          draggable={!isFileRef}
          onDragStart={isFileRef ? undefined : onDragStart}
          title={isFileRef ? undefined : "Drag to open in another application"}
        >
          {absPath ?? resolved.ref}
        </span>
      </div>
      <div style={styles.actions}>
        <button style={styles.copyButton} type="button" onClick={copyRef}>
          {isFileRef ? "Copy path" : "Copy reference"}
        </button>
        {isFileRef && hostBridge?.openWithOS && (
          <button style={styles.openButton} type="button" onClick={openFile}>
            Open
          </button>
        )}
      </div>
    </div>
  );
}

function MatterUnsupportedContent({ resolved }: { resolved: Extract<ResolvedMatter, { kind: "unsupported" }> }) {
  const wrapperLabel = resolved.wrapperName ? ` "${resolved.wrapperName}"` : "";
  const messages: Record<string, string> = {
    brique: "This matter's file path could not be resolved (missing context path).",
    wrapper: `This matter's substance is produced by the wrapper process${wrapperLabel}. It is supplied on demand by the wrapper and has no openable file representation.`,
  };
  const message = messages[resolved.mode] ?? `This matter (mode: ${resolved.mode}) does not point to an externally accessible resource.`;

  return (
    <div style={styles.content}>
      <div style={styles.row}>
        <span style={styles.label}>Mode</span>
        <span style={styles.valueMono}>{resolved.mode}</span>
      </div>
      <span style={styles.unsupported}>{message}</span>
    </div>
  );
}

function MatterFileShareContent({ resolved }: { resolved: Extract<ResolvedMatter, { kind: "file_share" }> }) {
  const { hostBridge } = useHostBridge();
  const parts = toPosixPath(resolved.abs).split("/");
  const fileName = parts[parts.length - 1] ?? resolved.abs;
  const copyPath = () => void navigator.clipboard.writeText(resolved.abs);
  const openFile = () => void hostBridge?.openWithOS?.({ path: resolved.abs });

  const onDragStart = (e: React.DragEvent) => {
    e.dataTransfer.setData("text/uri-list", resolved.locator);
    e.dataTransfer.setData("text/plain", resolved.abs);
    e.dataTransfer.effectAllowed = "copy";
  };

  return (
    <div style={styles.content}>
      <div style={styles.row}>
        <span style={styles.label}>File</span>
        <span style={styles.draggable} draggable onDragStart={onDragStart} title="Drag to open in another application">
          {fileName}
        </span>
      </div>
      <div style={styles.row}>
        <span style={styles.label}>Path</span>
        <span style={styles.valueMono}>{resolved.abs}</span>
      </div>
      <div style={styles.actions}>
        <button style={styles.copyButton} type="button" onClick={copyPath}>Copy path</button>
        {hostBridge?.openWithOS && (
          <button style={styles.openButton} type="button" onClick={openFile}>Open</button>
        )}
      </div>
    </div>
  );
}

function MatterBriqueFileContent({ resolved }: { resolved: Extract<ResolvedMatter, { kind: "brique_file" }> }) {
  const { hostBridge } = useHostBridge();
  const parts = toPosixPath(resolved.abs).split("/");
  const fileName = parts[parts.length - 1] ?? resolved.abs;
  const copyPath = () => void navigator.clipboard.writeText(resolved.abs);
  const openFile = () => void hostBridge?.openWithOS?.({ path: resolved.abs });

  const onDragStart = (e: React.DragEvent) => {
    e.dataTransfer.setData("text/uri-list", `file://${resolved.abs}`);
    e.dataTransfer.setData("text/plain", resolved.abs);
    e.dataTransfer.effectAllowed = "copy";
  };

  return (
    <div style={styles.content}>
      <div style={styles.row}>
        <span style={styles.label}>File</span>
        <span style={styles.draggable} draggable onDragStart={onDragStart} title="Drag to open in another application">
          {fileName}
        </span>
      </div>
      <div style={styles.row}>
        <span style={styles.label}>Path</span>
        <span style={styles.valueMono}>{resolved.abs}</span>
      </div>
      <div style={styles.actions}>
        <button style={styles.copyButton} type="button" onClick={copyPath}>Copy path</button>
        {hostBridge?.openWithOS && (
          <button style={styles.openButton} type="button" onClick={openFile}>Open</button>
        )}
      </div>
    </div>
  );
}

function MatterUrlContent({ resolved }: { resolved: Extract<ResolvedMatter, { kind: "url" }> }) {
  const copyRef = () => void navigator.clipboard.writeText(resolved.locator);

  const onDragStart = (e: React.DragEvent) => {
    e.dataTransfer.setData("text/uri-list", resolved.locator);
    e.dataTransfer.setData("text/plain", resolved.locator);
    e.dataTransfer.effectAllowed = "copy";
  };

  return (
    <div style={styles.content}>
      <div style={styles.row}>
        <span style={styles.label}>Reference</span>
        <span style={styles.draggable} draggable onDragStart={onDragStart} title="Drag to open in another application">
          {resolved.locator}
        </span>
      </div>
      <div style={styles.actions}>
        <button style={styles.copyButton} type="button" onClick={copyRef}>Copy reference</button>
      </div>
    </div>
  );
}

function YamlValue({ value, depth }: { value: unknown; depth: number }): React.ReactElement {
  const indent = depth * 14;

  if (value === null) return <span style={styles.yamlNull}>null</span>;
  if (typeof value === "boolean") return <span style={styles.yamlBool}>{String(value)}</span>;
  if (typeof value === "number") return <span style={styles.yamlNum}>{String(value)}</span>;
  if (typeof value === "string") return <span style={styles.yamlStr}>{value}</span>;

  if (Array.isArray(value)) {
    if (value.length === 0) return <span style={styles.yamlNull}>[]</span>;
    return (
      <div style={{ paddingLeft: indent }}>
        {value.map((item, i) => (
          <div key={i} style={styles.yamlArrayItem}>
            <span style={styles.yamlBullet}>-</span>
            <YamlValue value={item} depth={0} />
          </div>
        ))}
      </div>
    );
  }

  if (typeof value === "object") {
    const entries = Object.entries(value as Record<string, unknown>);
    if (entries.length === 0) return <span style={styles.yamlNull}>{"{}"}</span>;
    return (
      <div style={{ paddingLeft: indent }}>
        {entries.map(([k, v]) => {
          const isLeaf = v === null || typeof v !== "object" || Array.isArray(v) && (v as unknown[]).every(i => typeof i !== "object");
          return (
            <div key={k} style={styles.yamlRow}>
              <span style={styles.yamlKey}>{k}:</span>
              {isLeaf ? (
                <> <YamlValue value={v} depth={0} /></>
              ) : (
                <YamlValue value={v} depth={1} />
              )}
            </div>
          );
        })}
      </div>
    );
  }

  return <span style={styles.yamlStr}>{String(value)}</span>;
}

function MatterRawContent({ resolved }: { resolved: Extract<ResolvedMatter, { kind: "raw" }> }) {
  const yaml = JSON.stringify(resolved.json, null, 2);
  const copy = () => void navigator.clipboard.writeText(yaml);

  return (
    <div style={styles.content}>
      <div style={styles.row}>
        <span style={styles.label}>{resolved.label}</span>
        <div style={styles.yamlBlock}>
          <YamlValue value={resolved.json} depth={0} />
        </div>
      </div>
      <div style={styles.actions}>
        <button style={styles.copyButton} type="button" onClick={copy}>Copy</button>
      </div>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  backdrop: {
    position: "fixed",
    inset: 0,
    background: "rgba(0,0,0,0.55)",
    zIndex: 1100,
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
  },
  dialog: {
    width: 480,
    background: "var(--syn-surface-overlay)",
    border: "1px solid var(--syn-border-medium)",
    borderRadius: 10,
    boxShadow: "0 24px 80px rgba(0,0,0,0.7)",
    color: "var(--syn-text-primary)",
    fontFamily: "monospace",
    overflow: "hidden",
    display: "flex",
    flexDirection: "column",
  },
  header: {
    display: "flex",
    flexDirection: "column",
    gap: 4,
    padding: "14px 16px 12px",
    borderBottom: "1px solid var(--syn-border-soft)",
  },
  kind: {
    fontSize: 10,
    fontWeight: 900,
    letterSpacing: "0.1em",
    textTransform: "uppercase",
    color: "var(--syn-text-muted)",
  },
  name: {
    fontSize: 13,
    fontWeight: 700,
    color: "var(--syn-text-primary)",
    wordBreak: "break-all",
  },
  body: {
    padding: "16px",
  },
  content: {
    display: "flex",
    flexDirection: "column",
    gap: 12,
  },
  row: {
    display: "flex",
    flexDirection: "column",
    gap: 3,
  },
  label: {
    fontSize: 9,
    fontWeight: 900,
    letterSpacing: "0.1em",
    textTransform: "uppercase",
    color: "var(--syn-text-muted)",
  },
  value: {
    fontSize: 13,
    color: "var(--syn-text-primary)",
  },
  valueMono: {
    fontSize: 11,
    color: "var(--syn-text-primary)",
    wordBreak: "break-all",
    lineHeight: 1.5,
  },
  actions: {
    display: "flex",
    flexDirection: "row",
    gap: 8,
    marginTop: 4,
  },
  copyButton: {
    padding: "6px 14px",
    background: "var(--syn-control-bg)",
    border: "1px solid var(--syn-control-border)",
    borderRadius: 5,
    color: "var(--syn-text-secondary)",
    fontFamily: "monospace",
    fontSize: 11,
    fontWeight: 700,
    cursor: "pointer",
    letterSpacing: "0.05em",
  },
  openButton: {
    padding: "6px 14px",
    background: "var(--syn-control-active-bg)",
    border: "1px solid var(--syn-selected-border)",
    borderRadius: 5,
    color: "var(--syn-text-primary)",
    fontFamily: "monospace",
    fontSize: 11,
    fontWeight: 700,
    cursor: "pointer",
    letterSpacing: "0.05em",
  },
  draggable: {
    cursor: "grab",
    borderBottom: "1px dashed var(--syn-selected-border)",
    display: "inline-block",
  },
  error: {
    fontSize: 12,
    color: "var(--syn-feedback-error)",
  },
  unsupported: {
    fontSize: 12,
    color: "var(--syn-text-muted)",
    fontStyle: "italic",
  },
  yamlBlock: {
    background: "var(--syn-surface-0)",
    border: "1px solid var(--syn-border-soft)",
    borderRadius: 4,
    padding: "8px 10px",
    maxHeight: 280,
    overflowY: "auto",
    fontFamily: "monospace",
    fontSize: 11,
    lineHeight: 1.7,
  },
  yamlRow: {
    display: "flex",
    flexWrap: "wrap" as const,
    alignItems: "baseline",
    gap: "0 6px",
  },
  yamlArrayItem: {
    display: "flex",
    alignItems: "baseline",
    gap: 6,
  },
  yamlBullet: {
    color: "var(--syn-text-muted)",
    userSelect: "none" as const,
  },
  yamlKey: {
    color: "var(--syn-text-secondary)",
    flexShrink: 0,
  },
  yamlStr: {
    color: "var(--syn-text-primary)",
    wordBreak: "break-all" as const,
  },
  yamlNum: {
    color: "var(--syn-feedback-warning)",
  },
  yamlBool: {
    color: "var(--syn-nav-title)",
  },
  yamlNull: {
    color: "var(--syn-text-disabled)",
    fontStyle: "italic",
  },
};
