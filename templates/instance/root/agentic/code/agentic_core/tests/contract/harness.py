"""Contract-driven unit testing of one capacity at a time.

The contract is the capacity descriptor (capacity/<name>.json). Every invariant and failure
line of its #root transformation_contract must be covered by a test declared with @covers(...)
(the line is identified by its leading text, so renumbering a descriptor never misattributes a
test), or be waived below with a reason. test_zz_coverage.py enforces that.

Isolation: a capacity is exercised alone, over a world whose Matters are laid down directly in
the fake engine. Other capacities are not wired unless a test says so; fire-and-forget calls
are only recorded.
"""
from __future__ import annotations

import json
import os
import sys
import unittest
from typing import Any, Callable, Dict, List, Optional

HERE = os.path.dirname(os.path.abspath(__file__))
AGENT_CORE = os.path.dirname(os.path.dirname(HERE))
sys.path.insert(0, AGENT_CORE)
sys.path.insert(0, os.path.dirname(HERE))
sys.path.insert(0, os.path.abspath(os.path.join(AGENT_CORE, "..", "..", "..", "..", "wrapper", "python", "lib")))     # the runtime library of the wrappers

from fake_engine import PILOT_ID, FakeEngine, make_engine  # noqa: E402
from substrate import cas, inference_window, liveness, locks  # noqa: E402
from substrate.ids import matter_ids  # noqa: E402
from substrate.states import (initial_continuity, initial_mailbox, initial_runtime, matter_descriptor,  # noqa: E402
                              structure_reference)

CAPACITY_DIR = os.path.join(os.path.dirname(AGENT_CORE), "..", "capacity")
PILOT = {"context": "/root/agentic", "matter_id": PILOT_ID}

COVERAGE: Dict[str, List[str]] = {}     # capacity -> list of contract-line prefixes covered by tests


def descriptor(cap: str) -> Dict[str, Any]:
    with open(os.path.join(CAPACITY_DIR, f"{cap}.json"), encoding="utf-8") as fh:
        return json.load(fh)["functional"]["#root"]


def covers(*prefixes: str):
    """Declare which contract lines (invariants / failure) a test exercises."""
    def deco(fn):
        fn._covers = prefixes
        return fn
    return deco


class World:
    """Lays down the Matters of continuities directly, bypassing every capacity under test."""

    def __init__(self, engine: FakeEngine) -> None:
        self.e = engine

    def put(self, matter_id: str, kind: str, state: Dict[str, Any]) -> None:
        d = matter_descriptor(kind, matter_id, f"{kind} {matter_id}")
        self.e.matters[matter_id] = {"data": state, "functional": d["functional"], "objective": d["objective"],
                                     "brique": dict(d["brique"], revision=self.e.next_rev())}

    def continuity(self, cid: str, *, parent: Optional[str] = None, children=(), messages=(), intention="goal",
                   current: Optional[Any] = None, structure: bool = True, **fields) -> None:
        state = initial_continuity(cid, PILOT, intention, parent)
        state.update(fields)            # lineage fields of a succession: status, successor_id, predecessor_id, ...
        state["children"] = list(children)
        if current is not None:
            state["current_intention"] = current
        self.put(matter_ids(cid).continuity, "continuity", state)
        self.lay_journal(cid, messages)
        if structure:
            self.e.structures["continuities"]["functional"]["items"].append(structure_reference(matter_ids(cid).continuity))

    def lay_journal(self, cid: str, messages=()) -> None:
        """Lay the journal of a continuity: one line per message, in order. seq is the line number.

        A test that places a message at seq 4 means "the fourth fact of the history": the lines before it that the test does not
        describe are filled with follow-up events, so that seq stays the line number."""
        lines: List[Dict[str, Any]] = []
        for m in sorted(messages, key=lambda x: x["seq"]):
            assert m["seq"] > len(lines), f"the journal of {cid}: two messages share the seq {m['seq']}"
            while len(lines) + 1 < m["seq"]:
                n = len(lines) + 1
                lines.append({"id": f"pad{n}", "seq": n, "from": f"continuity:{cid}", "to": f"continuity:{cid}", "kind": "event",
                              "content": {"event": "run_recovered"}, "created_at": "2026-01-01T00:00:00Z", "correlation": {}})
            lines.append(dict(m))
        getattr(self.e, "_agentic_continuities", {"objects": {}})["objects"].pop(cid, None)     # a state laid by hand evicts the cached object
        d = matter_descriptor("journal", matter_ids(cid).journal, f"journal {cid}")
        self.e.matters[matter_ids(cid).journal] = {"lines": lines, "functional": d["functional"],
                                                    "objective": d["objective"], "brique": dict(d["brique"], revision=self.e.next_rev())}

    def mailbox(self, cid: str, entries=()) -> None:
        state = initial_mailbox(cid)
        state["entries"] = [dict(e) for e in entries]
        self.put(matter_ids(cid).mailbox, "mailbox", state)

    def runtime(self, cid: str, status: str = "idle", run_id: Optional[str] = None, current: Optional[str] = None, groups=()) -> None:
        state = initial_runtime(cid)
        state.update(status=status, run_id=run_id, current_message_id=current, active_wait_groups=[dict(g) for g in groups])
        self.put(matter_ids(cid).runtime, "runtime", state)

    def full(self, cid: str, *, messages=(), entries=(), status="idle", run_id=None, current=None, groups=(), **kw) -> None:
        self.continuity(cid, messages=messages, **kw)
        self.mailbox(cid, entries)
        self.runtime(cid, status, run_id, current, groups)

    # -- reading -------------------------------------------------------------------
    def state(self, matter_id: str) -> Dict[str, Any]:
        return json.loads(json.dumps(self.e.matters[matter_id]["data"]))

    def lines(self, cid: str) -> List[Dict[str, Any]]:
        return json.loads(json.dumps(self.e.matters[matter_ids(cid).journal]["lines"]))

    def entries(self, cid: str) -> List[Dict[str, Any]]:
        return self.state(matter_ids(cid).mailbox)["entries"]

    def journal(self, cid: str) -> List[Dict[str, Any]]:
        return self.lines(cid)

    def rt(self, cid: str) -> Dict[str, Any]:
        return self.state(matter_ids(cid).runtime)


def message(seq: int, mid: Optional[str] = None, kind: str = "user", content: Any = "x", **kw) -> Dict[str, Any]:
    return dict({"id": mid or f"m{seq}", "seq": seq, "from": "user:1", "to": "c1", "kind": kind, "content": content,
                 "created_at": "2026-01-01T00:00:00Z", "correlation": {}}, **kw)


def entry(mid: str, priority: str = "normal", status: str = "pending", claimed_by: Optional[str] = None) -> Dict[str, Any]:
    e = {"message_id": mid, "priority": priority, "status": status}
    if claimed_by:
        e["claimed_by"] = claimed_by
    return e


class CapacityContract(unittest.IsolatedAsyncioTestCase):
    """Base of one capacity's unit tests. Subclasses set CAP and MODULE."""
    CAP = ""
    MODULE: Any = None

    async def asyncSetUp(self) -> None:
        self.e = make_engine(with_capacities=False)
        self.p = self.e.port(self.CAP)
        self.w = World(self.e)
        liveness._live.clear()
        liveness._events.clear()
        inference_window._windows.clear()
        self._ttl = inference_window.RESERVATION_TTL_S
        inference_window.RESERVATION_TTL_S = 0.05      # a reservation nobody absorbs expires at once, so a test never waits for it
        self._pause = {name: os.environ.get(name) for name in ("BRIQUE_CAS_PAUSE_BASE_MS", "BRIQUE_CAS_PAUSE_CAP_MS")}
        os.environ["BRIQUE_CAS_PAUSE_BASE_MS"] = os.environ["BRIQUE_CAS_PAUSE_CAP_MS"] = "0"      # no pause between the attempts after a conflict: a test never waits for it

    async def asyncTearDown(self) -> None:
        inference_window.RESERVATION_TTL_S = self._ttl
        for name, value in self._pause.items():
            if value is None:
                os.environ.pop(name, None)
            else:
                os.environ[name] = value
        # the wrapper keeps no hidden persistent state between invocations
        self.assertEqual(locks._locks, {}, "a critical-section lock leaked")
        leaked = [w.cid for w in inference_window._windows.values() if w.holder is not None or w.queue]
        self.assertEqual(leaked, [], "an inference window was left held or with waiters")

    async def call(self, **params) -> Dict[str, Any]:
        return await self.MODULE.run(self.p, params)

    def assertPayloadMatchesContract(self, payload: Dict[str, Any]) -> None:
        declared = set(descriptor(self.CAP)["outputs"]["payload"])
        self.assertEqual(set(payload), declared, "payload keys differ from the declared outputs")

    def assertUnchanged(self, before: str) -> None:
        self.assertEqual(self.e.snapshot(), before, "a Matter changed although the contract forbids it")
