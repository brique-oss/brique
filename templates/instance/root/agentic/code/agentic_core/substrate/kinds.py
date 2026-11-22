"""Kinds of journal lines: the characterization that tells the thread of the LLM from the follow-up of the machinery.

The kind is set by the tool that writes the line, which knows where the line comes from; the LLM never sets it.
"""
from __future__ import annotations

# The thread of the LLM: kept by default in the continuity.
THREAD_KINDS = ("framework", "user", "pilot", "capacity_intention", "capacity_response", "continuity", "summary", "prompt", "completion")
# The follow-up of the machinery: kept in the journal, outside the continuity by default.
FOLLOW_UP_KINDS = ("event", "projection", "error", "control")
# Written only by agentic_core: never accepted from outside, never reach a mailbox.
HISTORY_ONLY_KINDS = ("framework", "summary", "prompt", "completion", "event", "projection", "error")
# Written first, at creation, from a Matter: the cognitive frame of the continuity. A projection always carries these lines, whatever its
# filters, limit or budget: they are never dropped, never compacted (a successor is created with its own).
PINNED_KINDS = ("framework",)
# Lines of the effects of a decision: facts of the journal, already said by the decision that produced them, so not part of the conversation
# a pilot_definition sends. A projection built for such a pilot leaves them out, and the budget never counts them.
EFFECT_KINDS = ("pilot", "capacity_intention")
# Lines a budget never drops (but counts): the summary of a succession is the anchor of the history of a continuity.
BUDGET_PROTECTED_KINDS = ("summary",)
# Lines whose full content the in-memory continuity keeps: the thread, and the control stimuli (read by pilot.activate).
RETAINED_KINDS = THREAD_KINDS + ("control",)
