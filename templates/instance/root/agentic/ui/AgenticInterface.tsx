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

// Interface agentic (spec: document/agentic_interface.md): the list of the conversations with their hierarchy, the window of one, and the
// start of a new one. The block observes the Matters of agentic and calls its capacities; the rules of what is shown live in ui/model/.

import React, { useEffect, useMemo, useRef, useState } from "react";
import { PulseProvider, usePulse } from "@spark/pulse/react";
import { PulseRuntime } from "@spark/pulse/runtime";
import { Draft, Conversation, IDENTITY, READER } from "./ConversationWindow.tsx";
import { TRANSIENT_CONNECTION_ERROR, useConnection, useContinuityIds, useMatters } from "./hooks.ts";
import { toView } from "./model/journal.ts";
import { cursorOf, jsonOf, linesOf, newId, titleFrom } from "./model/matters.ts";
import { sendWithBusyRetry } from "./model/busy.ts";
import { SettingsContext, resolveSettings, useSettings } from "./settings.ts";
import type { Settings } from "./settings.ts";
import { STATE_LABELS, stateOf, unreadCount } from "./model/state.ts";
import { buildTree, descendantsOf, flatten, visibleRows } from "./model/tree.ts";
import { openCount } from "./model/input.ts";
import { INPUT_LABELS } from "./InputRequest.tsx";
import type { Header, LineageNode } from "./model/tree.ts";
import { styles } from "./styles.ts";

export const GUIDE_REF = { context: "/root/agentic/prompt", matter_id: "guide_brique" };

export type AgenticInterfaceProps = {
  pulse?: PulseRuntime;
  transportUrl?: string;
  rootContextId?: string;
  guideRef?: { context: string; matter_id?: string; capacity?: string };
  reader?: string;          // who reads (user:1 for the person using the interface)
  settings?: Partial<Settings>;   // performance settings (ui/settings.ts): none given, the defaults hold
};

export function AgenticInterface({ pulse: providedPulse, transportUrl, rootContextId, guideRef, reader, settings }: AgenticInterfaceProps) {
  const resolved = useMemo(() => resolveSettings(settings), [JSON.stringify(settings ?? {})]);
  const pulse = useMemo(() => {
    if (providedPulse) return providedPulse;
    return new PulseRuntime({
      webSocketUrl: transportUrl ?? resolveTransportUrl(),
      maxMessagesPerFlow: 20,
      terminalFlowTtlMs: 30000,
    });
  }, [providedPulse, transportUrl]);

  return (
    <PulseProvider pulse={pulse}>
      <SettingsContext.Provider value={resolved}>
        <AgenticInterfaceInner rootContextId={rootContextId} guideRef={guideRef} reader={reader ?? READER} />
      </SettingsContext.Provider>
    </PulseProvider>
  );
}

const COLLAPSED_KEY = "agentic.collapsed";
const loadCollapsed = (): Set<string> => {
  try { const raw = window.localStorage.getItem(COLLAPSED_KEY); const list = raw ? JSON.parse(raw) : []; return new Set(Array.isArray(list) ? list.map(String) : []); } catch { return new Set(); }
};
const saveCollapsed = (set: Set<string>) => { try { window.localStorage.setItem(COLLAPSED_KEY, JSON.stringify([...set])); } catch { /* the choice is a convenience: it is just not kept */ } };

/** The height left to the block: from where it starts down to the bottom of the window. A host that gives its root no height (Spark's renderer: a body with its
 *  margin and a #root of its own height) would let the block grow with its content and the whole page scroll; sized to the window, the block holds its own
 *  scrolling (the list, the conversation, the metrics) and whatever the host shows above it stays. */
function useRemainingHeight(): [React.RefObject<HTMLElement>, number | null] {
  const ref = useRef<HTMLElement>(null);
  const [height, setHeight] = useState<number | null>(null);
  useEffect(() => {
    const measure = () => {
      const node = ref.current;
      if (!node) return;
      const top = node.getBoundingClientRect().top + window.scrollY;
      const below = parseFloat(getComputedStyle(document.body).marginBottom) || 0;       // the margin of the body under the block would add a scrollbar
      setHeight(Math.max(240, Math.floor(window.innerHeight - top - below)));
    };
    measure();
    window.addEventListener("resize", measure);
    return () => window.removeEventListener("resize", measure);
  }, []);
  return [ref, height];
}

function AgenticInterfaceInner({ rootContextId, guideRef, reader }: { rootContextId?: string; guideRef?: AgenticInterfaceProps["guideRef"]; reader: string }) {
  const pulse = usePulse();
  const context = rootContextId ?? "/root/agentic";
  const settings = useSettings();
  const { ids, error, refresh } = useContinuityIds(pulse, context, IDENTITY, settings.listPollMs, settings);
  const [selected, setSelected] = useState<string | null>(null);
  const [draft, setDraft] = useState(false);
  const [collapsed, setCollapsed] = useState<Set<string>>(loadCollapsed);
  const toggle = (lineageId: string) => setCollapsed((previous) => { const next = new Set(previous); if (!next.delete(lineageId)) next.add(lineageId); saveCollapsed(next); return next; });
  const connection = useConnection(pulse);
  const [rootRef, rootHeight] = useRemainingHeight();

  // the headers of all the continuities give the hierarchy; for each conversation the state of its head and the end of its journal give the row
  const headerSnaps = useMatters(pulse, context, ids.map((id) => ({ matterId: `continuity_${id}`, live: true, throttleMs: settings.listThrottleMs })), settings);
  const headers = ids.map((id) => jsonOf<Header>(headerSnaps[`continuity_${id}`])).filter((h): h is Header => !!h && Array.isArray(h.children));
  const roots = buildTree(headers);
  const rows = flatten(roots);
  const heads = rows.map((row) => row.node.head);
  const runtimes = useMatters(pulse, context, heads.map((head) => ({ matterId: `runtime_${head}`, live: true, throttleMs: settings.listThrottleMs })), settings);
  const tails = useMatters(pulse, context, heads.map((head) => ({ matterId: `journal_${head}`, tail: settings.journalTailBytes, live: true, throttleMs: settings.listThrottleMs })), settings);
  const cursors = useMatters(pulse, context, [{ matterId: "reading_cursors", live: true, throttleMs: settings.listThrottleMs }], settings).reading_cursors;

  const askingOf = (node: LineageNode) => openCount(toView(linesOf(tails[`journal_${node.head}`])));
  const unreadOf = (node: LineageNode) => unreadCount(toView(linesOf(tails[`journal_${node.head}`])), cursorOf(cursors, reader, node.head));
  const shown = visibleRows(roots, collapsed);

  const current = rows.find((row) => row.node.lineageId === selected)?.node ?? null;

  async function startChat(text: string) {
    const id = newId("chat");
    const response: any = await sendWithBusyRetry(() => pulse.emitUIIntentionAndWait({
      to: { context, cap: "pilot.launch", type: "user" }, targetPathUI: "/agentic/launch", identity: IDENTITY,
      params: { continuity_id: id, pilot_ref: guideRef ?? GUIDE_REF, initial_intention: titleFrom(text), message: { content: text } },
    } as any), settings);
    if (response.status === "error") throw new Error(response.error?.message ?? JSON.stringify(response.error));
    setDraft(false);
    setSelected(id);
    refresh();
  }

  return (
    <main ref={rootRef} style={{ ...styles.root, ...(rootHeight !== null ? { height: rootHeight } : {}), overflow: "hidden" }}>
      <aside style={styles.side}>
        <div style={styles.sideHeader}>
          <span style={styles.title}>Conversations</span>
          <button style={styles.ghost} onClick={() => { setDraft(true); setSelected(null); }}>+ New</button>
        </div>
        {connection === "disconnected" && <div style={styles.banner}>Engine connection lost. Reconnecting…</div>}
        <div style={styles.list}>
          {error && (TRANSIENT_CONNECTION_ERROR.test(error)
            // The page opens on the anonymous connection, which an instance does not serve; the Brique browser extension moves it to the
            // instance a moment later. Until then, and for the instant the connection changes, say so instead of failing.
            ? <div style={styles.empty}>Connecting to the instance…</div>
            : <div style={styles.error}>{error}</div>)}
          {!error && ids.length === 0 && <div style={styles.empty}>No conversation yet. Start one.</div>}
          {shown.map(({ node, depth }) => {
            const runtime = jsonOf<{ status?: string }>(runtimes[`runtime_${node.head}`]);
            const state = stateOf(runtime?.status);
            const items = toView(linesOf(tails[`journal_${node.head}`]));
            const unread = unreadCount(items, cursorOf(cursors, reader, node.head));
            const folded = collapsed.has(node.lineageId) && node.children.length > 0;
            const hidden = folded ? descendantsOf(node) : [];
            const hiddenUnread = hidden.reduce((sum, d) => sum + unreadOf(d), 0);
            const asking = askingOf(node);
            const hiddenAsking = hidden.reduce((sum, d) => sum + askingOf(d), 0);
            const hiddenTroubled = hidden.some((d) => stateOf(jsonOf<{ status?: string }>(runtimes[`runtime_${d.head}`])?.status) === "stranded");
            const isSelected = !draft && selected === node.lineageId;
            return (
              <div
                key={node.lineageId}
                style={{ ...styles.row, ...(isSelected ? styles.rowSelected : undefined), marginLeft: depth * 16 }}
                onClick={() => { setDraft(false); setSelected(node.lineageId); }}
              >
                <div style={styles.rowTop}>
                  {node.children.length > 0
                    ? <span style={{ ...styles.rowMeta, cursor: "pointer", width: 14 }} title={folded ? "Expand sub-conversations" : "Collapse sub-conversations"}
                        onClick={(event) => { event.stopPropagation(); toggle(node.lineageId); }}>{folded ? "▸" : "▾"}</span>
                    : depth > 0 && <span style={styles.rowMeta}>↳</span>}
                  <span style={styles.rowTitle}>{node.title}</span>
                  {unread > 0 && !isSelected && <span style={styles.badge}>{unread}</span>}
                  {folded && hiddenUnread > 0 && <span style={styles.badge} title="New messages in collapsed sub-conversations">+{hiddenUnread}</span>}
                  {folded && hiddenAsking > 0 && <span style={{ ...styles.badge, background: "var(--syn-feedback-warning, #fdd663)" }} title={INPUT_LABELS.waiting}>?</span>}
                  {folded && hiddenTroubled && <span style={{ ...styles.badge, background: "var(--syn-feedback-error, #f28b82)" }} title="A sub-conversation is stranded">!</span>}
                </div>
                <div style={styles.rowMeta}>
                  {node.nature === "workflow" ? "workflow · " : ""}{STATE_LABELS[state]}{asking > 0 ? ` · ${INPUT_LABELS.row}` : ""}{node.generations.length > 1 ? ` · ${node.generations.length} generations` : ""}
                </div>
              </div>
            );
          })}
        </div>
      </aside>
      {draft ? (
        <Draft onStart={startChat} />
      ) : current ? (
        <Conversation key={current.lineageId} pulse={pulse} context={context} node={current} state={stateOf(jsonOf<{ status?: string }>(runtimes[`runtime_${current.head}`])?.status)} reader={reader} onDeleted={() => setSelected(null)} />
      ) : (
        <div style={{ ...styles.main, ...styles.empty }}>Choose a conversation, or start one.</div>
      )}
    </main>
  );
}

function resolveTransportUrl(): string {
  const env = (globalThis as { process?: { env?: Record<string, string | undefined> } }).process?.env;
  return env?.SPARK_UI_TRANSPORT_URL ?? "ws://localhost:8080/agentic/ws";
}

export default AgenticInterface;
