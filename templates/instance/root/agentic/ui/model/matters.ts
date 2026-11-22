// Reading what Pulse gives back for a Matter: its content is text in `data.bytes` (a JSON document, or one JSON document per line for a journal).
import type { JournalLine } from "./journal.ts";

export const textOf = (snapshot: any): string => (snapshot && snapshot.data && typeof snapshot.data.bytes === "string" ? snapshot.data.bytes : "");

export function jsonOf<T = any>(snapshot: any): T | null {
  const text = textOf(snapshot);
  if (!text.trim()) return null;
  try { return JSON.parse(text) as T; } catch { return null; }
}

/** The lines of a journal read by a range of lines: one JSON document per line, a line that does not parse is left out. */
export function linesOf(snapshot: any): JournalLine[] {
  const out: JournalLine[] = [];
  for (const raw of textOf(snapshot).split("\n")) {
    if (!raw.trim()) continue;
    try { out.push(JSON.parse(raw)); } catch { /* a partial line: the next read has it whole */ }
  }
  return out;
}

export type Cursor = { reader: string; lineage_id: string; seq: number };

/** Where the reader has read up to, by continuity read. Nothing read: 0. */
export function cursorOf(snapshot: any, reader: string, id: string): number {
  const cursors: Cursor[] = jsonOf<{ cursors: Cursor[] }>(snapshot)?.cursors ?? [];
  return cursors.find((c) => c.reader === reader && c.lineage_id === id)?.seq ?? 0;
}

export const newId = (prefix: string): string => `${prefix}_${Date.now().toString(36)}${Math.random().toString(36).slice(2, 8)}`;

/** The title of a conversation made from the first thing the person said. */
export const titleFrom = (text: string): string => {
  const line = text.trim().split("\n")[0] ?? "";
  return line.length > 80 ? `${line.slice(0, 77)}…` : line || "New conversation";
};
