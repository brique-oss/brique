// Pure model of the requests for input of a conversation (document/capacity_input_request.md). A function of the view of a journal.

import type { InputItem, ViewItem } from "./journal";

export const inputsOf = (items: ViewItem[]): InputItem[] => items.filter((item): item is InputItem => item.type === "input");

/** The requests waiting for the person, the oldest first: the one to answer first. */
export const openRequests = (items: ViewItem[]): InputItem[] => inputsOf(items).filter((item) => item.status === "open").sort((a, b) => a.seq - b.seq);

/** How many requests of a lineage wait for the person (its head's view), for the signal of its row. */
export const openCount = (items: ViewItem[]): number => openRequests(items).length;

/** The answer to send for a button of the window. `confirm` is yes or no, exactly: that is what the capacity accepts. */
export const CONFIRM_ANSWERS = ["yes", "no"] as const;

/** What the person is told of a request that is no longer open. */
export const closedText = (item: InputItem, labels: { timeout: string; cancelled: string; closed: string }): string =>
  item.closedReason === "timeout" ? labels.timeout : item.closedReason === "cancelled" ? labels.cancelled : labels.closed;
