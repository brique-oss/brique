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
import { useBriqueSubstrate } from "../../React_Substrate_Adapter/context.js";
import { createCapabilityClient } from "../../Brique_Substrate/capability/index.js";
import type { CapabilityClient, WrapperSnapshot } from "../../Brique_Substrate/capability/index.js";

type McpStatus =
  | { kind: "unknown" }
  | { kind: "stopped"; context: string; wrapperName: string }
  | { kind: "starting"; context: string; wrapperName: string }
  | { kind: "running"; context: string; wrapperName: string }
  | { kind: "error"; context?: string; wrapperName?: string; message: string };

export type McpWrapperButtonProps = {
  activeContext: string | undefined;
};

const DEFAULT_MCP_CONTEXT = "/root/mcp";
const DEFAULT_MCP_WRAPPER = "mcp_server_go";
const POLL_MS = 3000;
const STARTING_TIMEOUT_MS = 30000;

export function McpWrapperButton({ activeContext }: McpWrapperButtonProps) {
  const substrate = useBriqueSubstrate();
  const capabilityClient = useMemo(() => createCapabilityClient(substrate), [substrate]);
  const [status, setStatus] = useState<McpStatus>({ kind: "unknown" });
  const [pressed, setPressed] = useState(false);
  const [starting, setStarting] = useState(false);
  const requestRef = useRef(0);

  const refresh = useCallback(async () => {
    const request = ++requestRef.current;
    const nextStatus = await readMcpStatus(capabilityClient, activeContext);
    if (request === requestRef.current) {
      setStatus(nextStatus);
      // "stopped" is ambiguous right after a Start click — the wrapper may simply not
      // have reported "starting"/"building" yet. Only clear the local starting flag on
      // an explicit terminal outcome (confirmed running, or an explicit error), so the
      // button stays yellow through that gap instead of flashing red.
      if (nextStatus.kind === "running" || nextStatus.kind === "error") {
        setStarting(false);
      }
    }
  }, [activeContext, capabilityClient]);

  useEffect(() => {
    void refresh();
    const interval = setInterval(() => void refresh(), POLL_MS);
    return () => clearInterval(interval);
  }, [refresh]);

  const startWrapper = useCallback(() => {
    if (starting) return;
    const context = "context" in status && status.context ? status.context : DEFAULT_MCP_CONTEXT;
    const wrapperName = "wrapperName" in status && status.wrapperName ? status.wrapperName : DEFAULT_MCP_WRAPPER;
    setStarting(true);
    void substrate.mutate({
      context,
      capability: "wrapper.start",
      params: { name: wrapperName },
    }).catch((error) => {
      setStatus({
        kind: "error",
        context,
        wrapperName,
        message: error instanceof Error ? error.message : "Unable to start MCP wrapper",
      });
      setStarting(false);
    }).finally(() => void refresh());
  }, [refresh, starting, status, substrate]);

  // Safety net: wrapper.start is fire-and-forget — if the wrapper never confirms
  // running nor reports an explicit error (e.g. it silently failed to start), don't
  // leave the button stuck yellow forever.
  useEffect(() => {
    if (!starting) return;
    const timeout = setTimeout(() => setStarting(false), STARTING_TIMEOUT_MS);
    return () => clearTimeout(timeout);
  }, [starting]);

  const visualKind = starting ? "starting" : status.kind;
  const title = statusTitle(status, starting);

  return (
    <button
      type="button"
      aria-label="Start MCP wrapper"
      title={title}
      onClick={startWrapper}
      onMouseDown={() => setPressed(true)}
      onMouseLeave={() => setPressed(false)}
      onMouseUp={() => setPressed(false)}
      style={{
        ...styles.button,
        ...statusStyle(visualKind),
        ...(pressed ? styles.pressed : undefined),
      }}
    >
      MCP
    </button>
  );
}

async function readMcpStatus(
  capabilityClient: CapabilityClient,
  activeContext: string | undefined,
): Promise<McpStatus> {
  const candidates = mcpContextCandidates(activeContext);
  let lastError: string | undefined;

  for (const context of candidates) {
    let result: Awaited<ReturnType<CapabilityClient["read"]["state"]>>;
    try {
      result = await capabilityClient.read.state(context, { include: ["wrappers"] });
    } catch (error) {
      lastError = error instanceof Error ? error.message : `Unable to read ${context}`;
      continue;
    }
    if (!result.ok) {
      lastError = result.error?.message ?? `Unable to read ${context}`;
      continue;
    }

    const wrapper = resolveMcpWrapper(result.payload?.wrappers);
    if (!wrapper) {
      lastError = `No MCP wrapper found in ${context}`;
      continue;
    }

    if (wrapper.proc_state === "running") {
      return { kind: "running", context, wrapperName: wrapper.name };
    }
    if (wrapper.proc_state === "starting" || wrapper.proc_state === "building" || wrapper.starting || wrapper.building) {
      return { kind: "starting", context, wrapperName: wrapper.name };
    }
    if (wrapper.ready_error || wrapper.last_error) {
      return { kind: "error", context, wrapperName: wrapper.name, message: wrapper.ready_error ?? wrapper.last_error ?? "MCP wrapper error" };
    }
    return { kind: "stopped", context, wrapperName: wrapper.name };
  }

  return { kind: "error", message: lastError ?? "MCP context not found" };
}

function mcpContextCandidates(activeContext: string | undefined): string[] {
  const normalized = normalizeContext(activeContext);
  const candidates: string[] = [];
  if (normalized) {
    candidates.push(normalized.endsWith("/mcp") ? normalized : `${normalized}/mcp`);
  }
  candidates.push(DEFAULT_MCP_CONTEXT);
  return Array.from(new Set(candidates));
}

function normalizeContext(context: string | undefined): string | undefined {
  if (!context) return undefined;
  const trimmed = context.trim();
  if (!trimmed) return undefined;
  return trimmed.startsWith("/") ? trimmed : `/${trimmed}`;
}

function resolveMcpWrapper(wrappers: WrapperSnapshot[] | undefined): WrapperSnapshot | undefined {
  if (!wrappers || wrappers.length === 0) return undefined;
  return wrappers.find((wrapper) => wrapper.name === DEFAULT_MCP_WRAPPER)
    ?? wrappers.find((wrapper) => wrapper.name.toLowerCase().includes("mcp"))
    ?? wrappers[0];
}

function statusTitle(status: McpStatus, starting: boolean): string {
  if (starting) return "MCP starting…";
  switch (status.kind) {
    case "running": return `MCP running (${status.context}/${status.wrapperName})`;
    case "starting": return `MCP starting (${status.context}/${status.wrapperName})`;
    case "stopped": return `Start MCP (${status.context}/${status.wrapperName})`;
    case "error": return status.message;
    case "unknown": return "MCP status unknown";
  }
}

function statusStyle(kind: McpStatus["kind"] | "starting"): React.CSSProperties {
  switch (kind) {
    case "running": return styles.running;
    case "starting": return styles.starting;
    case "error": return styles.error;
    case "stopped":
    case "unknown": return styles.unknown;
  }
}

const styles: Record<string, React.CSSProperties> = {
  button: {
    alignSelf: "stretch",
    border: "1px solid var(--syn-control-border)",
    borderRadius: 4,
    padding: "0 10px",
    cursor: "pointer",
    fontFamily: "inherit",
    fontWeight: 700,
    fontSize: 11,
    whiteSpace: "nowrap",
  },
  running: {
    background: "var(--syn-feedback-success-bg)",
    borderColor: "var(--syn-feedback-success)",
    color: "var(--syn-feedback-success)",
  },
  starting: {
    background: "var(--syn-feedback-warning-bg)",
    borderColor: "var(--syn-feedback-warning)",
    color: "var(--syn-feedback-warning)",
  },
  error: {
    background: "var(--syn-feedback-error-bg)",
    borderColor: "var(--syn-feedback-error)",
    color: "var(--syn-feedback-error)",
  },
  unknown: {
    background: "var(--syn-nav-panel-bg)",
    borderColor: "var(--syn-border-medium)",
    color: "var(--syn-text-muted)",
  },
  pressed: {
    filter: "brightness(1.25)",
  },
};
