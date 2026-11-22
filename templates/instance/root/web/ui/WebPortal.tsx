/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

// The web home page of the instance: it asks the engine which user interfaces are published
// (ui.list) and shows them as links. The engine answers only with the interfaces the visitor
// may read, so the page lists exactly what can be opened and holds no list of its own.

import React, { useEffect, useMemo, useState } from "react";
import { PulseRuntime } from "@spark/pulse/runtime";

export type WebPortalProps = {
  pulse?: PulseRuntime;
  transportUrl?: string;
  rootContextId?: string;
};

type Entry = { contextPath: string; uiName: string; url: string };
type Listing = { state: "loading" } | { state: "error"; message: string } | { state: "ready"; entries: Entry[] };

const OWN_UI = "web"; // this page does not list itself

// The host gives the page its pulse (the connection to the engine); a page opened without a host
// (development) makes its own. The page uses it directly: it needs no context provider.
export function WebPortal({ pulse: providedPulse, transportUrl }: WebPortalProps) {
  const pulse = useMemo(() => {
    if (providedPulse) return providedPulse;
    return new PulseRuntime({ webSocketUrl: transportUrl ?? resolveTransportUrl(), maxMessagesPerFlow: 20, terminalFlowTtlMs: 30000 });
  }, [providedPulse, transportUrl]);
  useEffect(() => {
    if (providedPulse) return;
    return () => pulse.teardown();
  }, [providedPulse, pulse]);
  return <Portal pulse={pulse} />;
}

const RETRY_MS = 1500;

function Portal({ pulse }: { pulse: PulseRuntime }) {
  const [listing, setListing] = useState<Listing>({ state: "loading" });

  // The page starts on the anonymous connection and may be switched to the identified one (the browser
  // extension) a moment later, after the person's first click: a request sent before the switch fails.
  // It is therefore asked again until the engine answers, instead of staying on the first failure.
  useEffect(() => {
    let active = true;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const attempt = () => {
      pulse.emitUIIntentionAndWait({
        to: { context: "/root", type: "reflexive", cap: "ui.list" },
        params: {},
        sourcePathUI: "/web",
        targetPathUI: "/web",
      }).then((response) => {
        if (!active) return;
        const result = (response as { payload?: { result?: unknown } })?.payload?.result;
        if ((response as { status?: string })?.status !== "ok" || !Array.isArray(result)) throw new Error("The engine did not return the list of interfaces.");
        setListing({ state: "ready", entries: result.filter(isEntry).filter((entry) => entry.uiName !== OWN_UI) });
      }).catch((error) => {
        if (!active) return;
        setListing({ state: "error", message: String(error?.message ?? error) });
        timer = setTimeout(attempt, RETRY_MS);
      });
    };
    attempt();
    return () => { active = false; if (timer) clearTimeout(timer); };
  }, [pulse]);

  return (
    <main style={styles.page}>
      <h1 style={styles.title}>Interfaces</h1>
      {listing.state === "loading" && <p style={styles.muted}>Loading…</p>}
      {listing.state === "error" && (
        <p style={styles.muted}>Connecting to the instance… <span style={styles.muted}>{listing.message}</span></p>
      )}
      {listing.state === "ready" && listing.entries.length === 0 && (
        <p style={styles.muted}>No interface is available to you on this instance.</p>
      )}
      {listing.state === "ready" && listing.entries.length > 0 && (
        <ul style={styles.list}>
          {listing.entries.map((entry) => (
            <li key={`${entry.contextPath}\u0000${entry.uiName}`} style={styles.item}>
              <a href={entry.url} style={styles.link}>{entry.uiName}</a>
              <span style={styles.muted}> {entry.contextPath}</span>
            </li>
          ))}
        </ul>
      )}
    </main>
  );
}

function isEntry(value: unknown): value is Entry {
  const entry = value as Entry;
  return !!entry && typeof entry.contextPath === "string" && typeof entry.uiName === "string" && typeof entry.url === "string";
}

function resolveTransportUrl(): string {
  const env = (globalThis as { process?: { env?: Record<string, string | undefined> } }).process?.env;
  return env?.SPARK_UI_TRANSPORT_URL ?? "ws://localhost:8080/web/ws";
}

const styles: Record<string, React.CSSProperties> = {
  page: { fontFamily: "system-ui, sans-serif", maxWidth: 640, margin: "48px auto", padding: "0 16px" },
  title: { fontSize: 22, margin: "0 0 16px" },
  list: { listStyle: "none", padding: 0, margin: 0 },
  item: { padding: "10px 0", borderBottom: "1px solid #ddd" },
  link: { fontSize: 17, fontWeight: 600 },
  muted: { color: "#777" },
  error: { color: "#b3261e" },
};

export default WebPortal;
