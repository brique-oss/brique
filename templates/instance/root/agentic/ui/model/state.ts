// What the person reads about a conversation: the labels of its state and its unread messages. The labels are in one place so that they can be validated.
import type { ViewItem } from "./journal.ts";

export const STATE_LABELS = {
  idle: "idle",
  running: "running",
  waiting: "waiting for a tool",
  suspended: "suspended",
  stranded: "stranded",
  superseded: "superseded by the rest of the conversation",
} as const;

export type StateKey = keyof typeof STATE_LABELS;

/** The state of a conversation as shown: `stranded` (a run that does not restart by itself) wins over what the runtime says. */
export function stateOf(runtimeStatus: string | undefined, options: { stranded?: boolean; superseded?: boolean } = {}): StateKey {
  if (options.superseded) return "superseded";
  if (options.stranded) return "stranded";
  return runtimeStatus === "running" || runtimeStatus === "waiting" || runtimeStatus === "suspended" ? runtimeStatus : "idle";
}

/** New messages: what the pilot or a daughter said to the person, and the alerts, written after the line the person has read up to. */
export function unreadCount(items: ViewItem[], cursorSeq: number): number {
  return items.filter((item) => item.seq > cursorSeq && ((item.type === "pilot" && item.recipient === null) || item.type === "daughter" || item.type === "alert")).length;
}

export function lastSeq(items: ViewItem[]): number {
  return items.reduce((max, item) => Math.max(max, item.seq), 0);
}
