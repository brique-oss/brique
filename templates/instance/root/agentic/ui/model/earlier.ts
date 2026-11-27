// The whole conversation, not a window of it: the end of a journal is read live (a window in bytes), and the lines before that window are read
// backwards, page by page, by ranges of lines. A page is one answer, and an answer has a size the engine's link to the browser limits: when the
// engine says a page is too large (reason response_too_large), the page is halved and read again; a single line that is too large is left out.
import type { JournalLine } from "./journal.ts";

export const EARLIER_PAGE_LINES = 20;                 // the first page: small, then it grows while the pages stay small
export const EARLIER_MAX_PAGE_LINES = 200;
export const EARLIER_GROW_BELOW_BYTES = 40_000;       // a page that came back smaller than this lets the next one be twice as long
export const TOO_LARGE = "response_too_large";
export const NO_ANSWER = "no_answer";                 // an engine that does not say the answer is too large (an older one) just does not answer: same remedy

/** The text of `length` lines of the journal from line `offset` (counted from 0). A refusal carries a `code` (response_too_large, no_answer). */
export type ReadLines = (offset: number, length: number) => Promise<string>;

export function parseLines(text: string): JournalLine[] {
  const out: JournalLine[] = [];
  for (const raw of text.split("\n")) {
    if (!raw.trim()) continue;
    try { const line = JSON.parse(raw); if (line && typeof line.seq === "number") out.push(line); } catch { /* not a whole line */ }
  }
  return out;
}

/**
 * Reads the lines with a seq below `firstSeq` that `known` does not hold yet, newest page first, and puts them in `known` (by seq). Line n of the
 * journal has seq n+1. It stops at the first line, at a line it already has, when cancelled, or when a page cannot be read at all.
 * `onPage` is called after each page so the conversation can grow in front of the person while the rest is read.
 */
export async function readEarlier(read: ReadLines, firstSeq: number, known: Map<number, JournalLine>, onPage: () => void, cancelled: () => boolean = () => false): Promise<void> {
  let end = firstSeq - 1;                              // the lines 0 .. end-1 are before the window
  while (end > 0 && known.has(end)) end -= 1;          // what a previous reading holds is not read again
  let page = EARLIER_PAGE_LINES;
  while (end > 0 && !cancelled()) {
    const length = Math.min(page, end);
    const offset = end - length;
    let text: string;
    try {
      text = await read(offset, length);
    } catch (error: any) {
      const unreadable = error?.code === TOO_LARGE || error?.code === NO_ANSWER;
      if (unreadable && length > 1) { page = Math.max(1, Math.floor(length / 2)); continue; }
      if (unreadable) { end = offset; continue; }                    // one line alone cannot be carried: it is left out, the ones before it can be shown
      return;
    }
    if (text.length < EARLIER_GROW_BELOW_BYTES) page = Math.min(EARLIER_MAX_PAGE_LINES, page * 2);
    for (const line of parseLines(text)) known.set(line.seq, line);
    end = offset;
    while (end > 0 && known.has(end)) end -= 1;
    onPage();
  }
}

/** The text of a journal range in the answer of a Matter read: {brique, data: {bytes, ...}}. Anything else is not an answer this can use. */
export function journalTextOf(payload: any): string {
  const bytes = payload?.data?.bytes;
  if (typeof bytes !== "string") throw Object.assign(new Error("the read of the journal came back without its text"), { code: "unreadable_answer" });
  return bytes;
}
