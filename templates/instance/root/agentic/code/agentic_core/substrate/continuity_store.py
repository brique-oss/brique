"""The continuity: a Matter (the journal) for persistence, duplicated in an in-memory object for performance.

This duplication is an implementation constraint, not a formalization: the journal is the only source of truth. The
object is rebuilt from it when absent, and kept up to date by every write (write-through): an append to the journal
is followed, under the lock of the continuity, by the update of the object. A stale object is detected by the
expected_line_count guard of the next append, then completed from the tail of the journal.

What the object keeps is decided while it is built from the journal:
  - the thread of the LLM and the control stimuli, in full (THREAD_KINDS + control);
  - for every line, its seq and kind (an index: idempotence, ordering);
  - from the follow-up events, only what is read back: the intentions a wait group expected, the last context measure.
The other follow-up lines stay in the journal and are read from it on demand (all_messages).
"""
from __future__ import annotations

import asyncio
import json
from collections import OrderedDict
from typing import Any, Dict, Iterable, List, Optional, Tuple

from .conversation import message_of
from . import inference_window
from .errors import ConsistencyError, LineCountMismatch, NotFound
from .ids import matter_ids, new_id, now
from .kinds import RETAINED_KINDS, THREAD_KINDS
from .locks import critical
from .schemas import validate_value
from .settings import setting

MAX_OBJECTS = setting("MAX_OBJECTS", 512, 1, "continuities kept in memory by one wrapper; the least recently used goes first, archived ones included")
CHUNK_LINES = setting("CHUNK_LINES", 200, 1, "lines read per matter.read while (re)building the object")
MAX_ATTEMPTS = setting("STORE_MAX_ATTEMPTS", 12, 1, "attempts of a journal append that keeps finding the journal moved on, before it fails")


def size_of(message: Dict[str, Any]) -> int:
    """What the line costs the conversation, in characters: the text it says to the model, never the line with its identifiers, dates and
    correlation (the model does not see them). A line the conversation does not say counts zero."""
    said = message_of(message)
    return len(said["content"]) if said else 0


class Continuity:
    """The in-memory continuity of one cid, built from the journal."""

    def __init__(self, cid: str) -> None:
        self.cid = cid
        self.line_count = 0                                   # lines of the journal this object reflects
        self.index: Dict[str, Tuple[int, str]] = {}           # message id -> (seq, kind), for every line
        self.retained: Dict[str, Dict[str, Any]] = {}         # message id -> message, for RETAINED_KINDS
        self.order: List[str] = []                            # retained ids in seq order
        self._thread: List[Dict[str, Any]] = []               # the thread of the LLM in seq order, kept up to date by apply
        self.intention_ids: set = set()                       # ids of the capacity intentions already in the thread
        self.sizes: Dict[str, int] = {}                       # retained id -> serialized size, computed once
        self.expected_refs: set = set()                       # intention ids a wait group expected
        self.measure: Optional[Dict[str, Any]] = None         # last inference_measured content

    # -- building ---------------------------------------------------------------
    def apply(self, message: Dict[str, Any]) -> None:
        seq = message.get("seq")
        if seq != self.line_count + 1:
            raise ConsistencyError("the journal and the in-memory continuity disagree on a line number",
                                   details={"continuity_id": self.cid, "expected_seq": self.line_count + 1, "seq": seq})
        mid, kind = message["id"], message["kind"]
        self.line_count = seq
        self.index[mid] = (seq, kind)
        if kind in RETAINED_KINDS:
            self.retained[mid] = message
            self.order.append(mid)
            self.sizes[mid] = size_of(message)
            if kind in THREAD_KINDS:
                self._thread.append(message)
                if kind == "capacity_intention" and isinstance(message.get("content"), dict):
                    self.intention_ids.add(message["content"].get("id"))
        elif kind == "event" and isinstance(message.get("content"), dict):
            content = message["content"]
            if content.get("event") == "wait_group_created":
                for item in (content.get("wait_group") or {}).get("expected", []):
                    if item.get("type") == "intention" and item.get("ref"):
                        self.expected_refs.add(item["ref"])
            elif content.get("event") == "inference_measured":
                self.measure = content

    # -- reading ----------------------------------------------------------------
    def has(self, message_id: str) -> bool:
        return message_id in self.index

    def seq_of(self, message_id: str) -> Optional[int]:
        found = self.index.get(message_id)
        return found[0] if found else None

    def kind_of(self, message_id: str) -> Optional[str]:
        found = self.index.get(message_id)
        return found[1] if found else None

    def message(self, message_id: str) -> Optional[Dict[str, Any]]:
        """The full message when its kind is retained; None for a follow-up line that is only indexed."""
        return self.retained.get(message_id)

    def thread(self) -> List[Dict[str, Any]]:
        """The continuity proper: the thread of the LLM, in seq order. The messages are shared: read only."""
        return self._thread

    def messages(self) -> List[Dict[str, Any]]:
        """Every retained line (thread and control), in seq order. Shared: read only."""
        return [self.retained[i] for i in self.order]


# -- the objects of one wrapper ------------------------------------------------------
def _home(port) -> Dict[str, Any]:
    owner = port.owner
    home = getattr(owner, "_agentic_continuities", None)
    if home is None:
        home = {"objects": OrderedDict(), "loading": {}}
        setattr(owner, "_agentic_continuities", home)
    return home


async def _read_all(port, journal_id: str, start: int, obj: Optional[Continuity]) -> List[Dict[str, Any]]:
    _, total = await port.matter_stat(journal_id)
    out: List[Dict[str, Any]] = []
    offset = start
    while offset < total:
        view = await port.matter_read_lines(journal_id, offset, CHUNK_LINES)
        if not view.lines:
            break
        for message in view.lines:
            if obj is not None:
                obj.apply(message)
            out.append(message)
        offset += len(view.lines)
    return out


async def _build(port, cid: str) -> Continuity:
    obj = Continuity(cid)
    try:
        await _read_all(port, matter_ids(cid).journal, 0, obj)
    except NotFound as exc:
        raise NotFound("the continuity journal does not exist", details={"continuity_id": cid}) from exc
    return obj


async def get(port, cid: str) -> Continuity:
    """The in-memory continuity, rebuilt from the journal when absent."""
    home = _home(port)
    obj = home["objects"].get(cid)
    if obj is not None:
        home["objects"].move_to_end(cid)
        return obj
    task = home["loading"].get(cid)
    if task is None:
        task = asyncio.ensure_future(_build(port, cid))
        home["loading"][cid] = task
    try:
        obj = await task
    finally:
        if home["loading"].get(cid) is task:
            home["loading"].pop(cid, None)
    kept = home["objects"].setdefault(cid, obj)
    home["objects"].move_to_end(cid)
    while len(home["objects"]) > MAX_OBJECTS:        # rebuilt from the journal when asked again; a stale copy in flight is caught by expected_line_count
        home["objects"].popitem(last=False)
    return kept


def forget(port, cid: str) -> None:
    """Evict the object (the continuity was deleted, or its object must be rebuilt)."""
    home = _home(port)
    home["objects"].pop(cid, None)
    home["loading"].pop(cid, None)


async def refresh(port, cid: str) -> Continuity:
    """Complete the object with the lines it has not seen yet (a stale object found by a refused append)."""
    obj = await get(port, cid)
    await _read_all(port, matter_ids(cid).journal, obj.line_count, obj)
    return obj


async def lines_after(port, cid: str, seq: int) -> List[Dict[str, Any]]:
    """The lines of the journal written after line `seq` (read from it): the tail a waiting capacity polls, not the whole journal."""
    return await _read_all(port, matter_ids(cid).journal, max(0, int(seq)), None)


async def all_messages(port, cid: str) -> List[Dict[str, Any]]:
    """Every line of the journal, read from it: for the projections that ask for follow-up kinds."""
    return await _read_all(port, matter_ids(cid).journal, 0, None)


async def append(port, cid: str, entries: Iterable[Dict[str, Any]]) -> List[Dict[str, Any]]:
    """Append lines to the journal in ONE atomic commit, guarded by expected_line_count, then update the object.

    Each entry: kind and content, optionally id, from, to, correlation, created_at. An entry whose id is already in the
    journal is not appended again (idempotent): the existing message is returned. seq is the line number. On a refused
    guard the object is completed from the tail of the journal and the lines are recomputed (bounded retry).
    """
    wanted = list(entries)
    journal_id = matter_ids(cid).journal
    if inference_window.closed_for(cid):      # waits while an inference holds the window; BEFORE the lock, the holder needs it to write
        async with port.yielding():
            await inference_window.door(cid)
    async with critical(cid, yielding=port.yielding):
        for _ in range(MAX_ATTEMPTS):
            obj = await get(port, cid)
            fresh: List[Dict[str, Any]] = []
            result: List[Dict[str, Any]] = []
            seen: Dict[str, Dict[str, Any]] = {}
            for entry in wanted:
                mid = entry.get("id") or new_id("evt")
                if obj.has(mid):
                    result.append(obj.message(mid) or {"id": mid, "seq": obj.seq_of(mid), "kind": obj.index[mid][1]})
                    continue
                if mid in seen:
                    result.append(seen[mid])
                    continue
                stored = {"id": mid, "seq": obj.line_count + len(fresh) + 1, "from": entry.get("from") or f"continuity:{cid}",
                          "to": entry.get("to") or f"continuity:{cid}", "kind": entry["kind"], "content": entry["content"],
                          "created_at": entry.get("created_at") or now(), "correlation": entry.get("correlation") or {}}
                validate_value("message", stored)
                fresh.append(stored)
                seen[mid] = stored
                result.append(stored)
            if not fresh:
                return result
            try:
                await port.matter_append_lines(journal_id, fresh, obj.line_count)
            except LineCountMismatch:
                await refresh(port, cid)
                await asyncio.sleep(0)
                continue
            for message in fresh:
                obj.apply(message)
            return result
    from .errors import ConcurrencyError
    raise ConcurrencyError(f"journal append conflicts on {journal_id} exhausted {MAX_ATTEMPTS} attempts",
                           details={"matter_id": journal_id, "attempts": MAX_ATTEMPTS})
