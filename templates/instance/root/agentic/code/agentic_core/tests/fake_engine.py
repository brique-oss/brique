"""In-memory stand-in for the Brique engine, faithful to the guarantees agentic_core relies on:
one atomic write per Matter, a strictly increasing revision, expected_revision guard,
whole-substance writes of Matters, semantic_patch on Structures, structure expected_revision, and capacity routing.
Every operation yields to the event loop so that concurrent tasks really interleave."""
from __future__ import annotations

import asyncio
import copy
import os
import sys
from typing import Any, Awaitable, Callable, Dict, List, Optional

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from substrate.errors import EngineError, LineCountMismatch, NotFound, RevisionMismatch  # noqa: E402
from substrate.port import LinesView, MatterView, StructureView, split_capacity_ref  # noqa: E402

PILOT_ID = "pilot_default"


def _walk(root: Dict[str, Any], path: List[Any]):
    node = root
    for p in path[:-1]:
        node = node[p]
    return node, path[-1]


def apply_ops(doc: Dict[str, Any], operations: List[Dict[str, Any]]) -> Dict[str, Any]:
    doc = copy.deepcopy(doc)
    for op in operations:
        parent, key = _walk(doc, op["path"])
        kind = op["op"]
        if kind == "add":
            target = parent.setdefault(key, [])
            if op["value"] not in target:
                target.append(copy.deepcopy(op["value"]))
        elif kind == "remove":
            if op["value"] in parent[key]:
                parent[key].remove(op["value"])
        elif kind == "delete":
            parent.pop(key, None)
        elif kind == "set":
            parent[key] = copy.deepcopy(op["value"])
        else:
            raise AssertionError(f"unsupported fake op {kind}")
    return doc


class FakeEngine:
    def __init__(self) -> None:
        self.matters: Dict[str, Dict[str, Any]] = {}
        self.texts: Dict[tuple, str] = {}           # (context, matter_id) -> the text substance of a Matter of another context (prompt parts)
        self.structures: Dict[str, Dict[str, Any]] = {"continuities": {"brique": {"revision": 1}, "functional": {"items": []}}}
        self.caps: Dict[str, Callable[..., Awaitable[Dict[str, Any]]]] = {}
        self.calls: List[str] = []
        self.tasks: List[asyncio.Task] = []
        self._rev = 1000
        self.conflicts = 0      # number of revision mismatches observed
        self.line_conflicts = 0  # number of journal appends refused by expected_line_count
        self.fail_append: set = set()               # matter ids whose appends fail
        self.always_line_conflict: set = set()      # matter ids whose guarded appends are always refused
        self.matter_contexts: List[tuple] = []      # (matter_id, context) of the reads and existence checks that named a context
        self.reads: List[tuple] = []                # ("read"|"read_lines"|"stat", matter_id): to count the reads of the journal
        self.bad_refs: set = set()   # capacity refs whose emission fails
        # observation and fault injection used by the contract tests
        self.log: List[tuple] = []                 # ("create"|"write"|"delete"|"structure_patch", id, guard, kind)
        self.always_conflict: set = set()          # matter ids whose guarded writes always hit a revision conflict
        self.fail_create: set = set()
        self.fail_delete: set = set()
        self.fail_write: set = set()
        self.fail_structure_patch = False
        self.on_fire: Optional[Callable[..., None]] = None
        self.keepalives: List[str] = []             # capacities that acknowledged themselves as long-running
        self.yields: List[str] = []                 # capacities that gave their execution slot back while waiting (a lock, the inference window)
        self.known_capacities: set = set()         # "<context>/<capacity>" the engine would find with read.capacity
        self.pilot_capacities: set = set()         # "<context>/<capacity>" that declare themselves pilots (objective.agentic.pilot): the plans
        # a Matter is a descriptor (brique / objective / functional = what it does) plus its substance: "data"
        self.matters[PILOT_ID] = {"data": {"id": PILOT_ID, "prompt": "p", "model": {"provider": "fake", "model": "m"}, "parameters": {},
                                           "default_projection": {"limit": 20}, "known_capacities": [], "rules": []},
                                  "functional": {"format": "json"}, "brique": {"revision": 1},
                                  "objective": {"name": PILOT_ID, "type": "pilot_definition"}}

    def next_rev(self) -> int:
        self._rev += 1
        return self._rev

    def snapshot(self) -> str:
        import json
        return json.dumps({"m": self.matters, "s": self.structures}, sort_keys=True)

    def writes(self, matter_id: str) -> List[tuple]:
        return [e for e in self.log if e[0] == "write" and e[1] == matter_id]

    def port(self, from_cap: str = "test") -> "FakePort":
        return FakePort(self, from_cap)

    async def drain(self) -> None:
        import pilot_activate
        while True:
            pending = [t for t in self.tasks if not t.done()] + [t for t in list(pilot_activate._background) if not t.done()]
            if not pending:
                return
            await asyncio.gather(*pending, return_exceptions=True)

    def raise_task_errors(self) -> None:
        for t in self.tasks:
            if t.done() and not t.cancelled() and t.exception() is not None:
                raise t.exception()


class FakePort:
    def __init__(self, engine: FakeEngine, from_cap: str) -> None:
        self.engine, self.from_cap = engine, from_cap

    async def _tick(self) -> None:
        await asyncio.sleep(0)

    def keepalive(self):
        """Counts the long-running acknowledgements instead of sending them."""
        engine = self.engine
        import contextlib

        @contextlib.asynccontextmanager
        async def counting():
            engine.keepalives.append(self.from_cap)
            yield
        return counting()

    def yielding(self):
        """Counts the waits that give the execution slot back instead of giving it back."""
        engine = self.engine
        import contextlib

        @contextlib.asynccontextmanager
        async def counting():
            engine.yields.append(self.from_cap)
            yield
        return counting()

    async def matter_read(self, matter_id: str, context=None) -> MatterView:
        await self._tick()
        if context is not None:
            self.engine.matter_contexts.append((matter_id, context))        # the fake world has one namespace; the context asked is recorded
        m = self.engine.matters.get(matter_id)
        if m is None:
            raise NotFound("matter not found", details={"matter_id": matter_id})
        self.engine.reads.append(("read", matter_id))
        return MatterView(matter_id, copy.deepcopy(m["data"]), copy.deepcopy(m.get("functional", {})), dict(m["brique"]), m["brique"]["revision"])

    async def matter_read_text(self, context: str, matter_id: str) -> str:
        await self._tick()
        if (context, matter_id) not in self.engine.texts:
            raise NotFound("matter not found", details={"context": context, "matter_id": matter_id})
        self.engine.reads.append(("read_text", f"{context}/{matter_id}"))
        return self.engine.texts[(context, matter_id)]

    # -- line-framed Matters (the journal): one parsed line per element ---------------------
    @property
    def owner(self):
        return self.engine

    async def matter_stat(self, matter_id: str):
        await self._tick()
        m = self.engine.matters.get(matter_id)
        if m is None:
            raise NotFound("matter not found", details={"matter_id": matter_id})
        self.engine.reads.append(("stat", matter_id))
        return m["brique"]["revision"], len(m.get("lines", []))

    async def matter_create_empty(self, matter_id: str, descriptor: Dict[str, Any]) -> int:
        await self._tick()
        if matter_id in self.engine.matters:
            raise EngineError("refused", "already_exists", "matter already exists")
        if matter_id in self.engine.fail_create:
            raise EngineError("internal", "io_failure", "create failed")
        self.engine.log.append(("create", matter_id, None, None))
        rev = self.engine.next_rev()
        brique = dict(descriptor.get("brique", {}), revision=rev)
        self.engine.matters[matter_id] = {"lines": [], "functional": copy.deepcopy(descriptor.get("functional", {})),
                                          "brique": brique, "objective": descriptor.get("objective", {})}
        return rev

    async def matter_read_lines(self, matter_id: str, offset: int, length: int) -> LinesView:
        await self._tick()
        m = self.engine.matters.get(matter_id)
        if m is None:
            raise NotFound("matter not found", details={"matter_id": matter_id})
        self.engine.reads.append(("read_lines", matter_id))
        lines = m.get("lines", [])
        return LinesView(copy.deepcopy(lines[offset:offset + length]), len(lines), m["brique"]["revision"], offset)

    async def matter_append_lines(self, matter_id: str, lines, expected_line_count: int) -> int:
        await self._tick()
        m = self.engine.matters.get(matter_id)
        if m is None:
            raise NotFound("matter not found", details={"matter_id": matter_id})
        if matter_id in self.engine.fail_append:
            raise EngineError("internal", "io_failure", "append failed")
        if matter_id in self.engine.always_line_conflict or int(expected_line_count) != len(m["lines"]):
            self.engine.line_conflicts += 1
            raise LineCountMismatch("line count mismatch", details={"matter_id": matter_id})
        m["lines"].extend(copy.deepcopy(list(lines)))      # one contiguous block, one commit
        m["brique"]["revision"] = self.engine.next_rev()
        self.engine.log.append(("append", matter_id, int(expected_line_count), len(lines)))
        return len(m["lines"])

    async def matter_read_many(self, matter_ids):
        """One engine request for several Matters: one entry in the log of reads, whatever their number."""
        await self._tick()
        views = []
        for matter_id in matter_ids:
            m = self.engine.matters.get(matter_id)
            if m is None:
                raise NotFound("matter not found", details={"matter_id": matter_id})
            views.append(MatterView(matter_id, copy.deepcopy(m["data"]), copy.deepcopy(m.get("functional", {})), dict(m["brique"]), m["brique"]["revision"]))
        self.engine.reads.append(("read_batch", ",".join(matter_ids)))
        return views

    async def matter_objective(self, matter_id: str, context=None) -> Dict[str, Any]:
        await self._tick()
        m = self.engine.matters.get(matter_id)
        if m is None:
            raise NotFound("matter not found", details={"matter_id": matter_id})
        return copy.deepcopy(m.get("objective") or {})

    async def matter_exists(self, matter_id: str, context=None) -> bool:
        await self._tick()
        if context is not None:
            self.engine.matter_contexts.append((matter_id, context))
        return matter_id in self.engine.matters

    async def matter_create(self, matter_id: str, matter: Dict[str, Any], content: Dict[str, Any]) -> int:
        await self._tick()
        if matter_id in self.engine.matters:
            raise EngineError("refused", "already_exists", "matter already exists")
        if matter_id in self.engine.fail_create:
            raise EngineError("internal", "io_failure", "create failed")
        self.engine.log.append(("create", matter_id, None, None))
        rev = self.engine.next_rev()
        brique = dict(matter.get("brique", {}), revision=rev)
        self.engine.matters[matter_id] = {"data": copy.deepcopy(content), "functional": copy.deepcopy(matter.get("functional", {})),
                                          "brique": brique, "objective": matter.get("objective", {})}
        return rev

    async def matter_create_raw(self, matter_id: str, descriptor: Dict[str, Any], text: str) -> int:
        import json
        return await self.matter_create(matter_id, descriptor, json.loads(text))

    async def matter_write(self, matter_id: str, *, content, expected_revision=None) -> int:
        await self._tick()
        m = self.engine.matters.get(matter_id)
        if m is None:
            raise NotFound("matter not found", details={"matter_id": matter_id})
        if matter_id in self.engine.fail_write:
            raise EngineError("internal", "io_failure", "write failed")
        if matter_id in self.engine.always_conflict and expected_revision is not None:
            self.engine.conflicts += 1
            raise RevisionMismatch("revision mismatch", details={"matter_id": matter_id})
        if expected_revision is not None and int(expected_revision) != m["brique"]["revision"]:
            self.engine.conflicts += 1
            raise RevisionMismatch("revision mismatch", details={"matter_id": matter_id})
        m["data"] = copy.deepcopy(content)               # the whole substance is replaced by one guarded write
        m["brique"]["revision"] = self.engine.next_rev()
        self.engine.log.append(("write", matter_id, expected_revision, "data"))
        return m["brique"]["revision"]

    async def matter_delete(self, matter_id: str) -> None:
        await self._tick()
        if matter_id not in self.engine.matters:
            raise NotFound("matter not found", details={"matter_id": matter_id})
        if matter_id in self.engine.fail_delete:
            raise EngineError("internal", "io_failure", "delete failed")
        self.engine.log.append(("delete", matter_id, None, None))
        del self.engine.matters[matter_id]

    async def structure_read(self, structure_id: str) -> StructureView:
        await self._tick()
        s = self.engine.structures.get(structure_id)
        if s is None:
            raise NotFound("structure not found")
        return StructureView(structure_id, copy.deepcopy(s["functional"]), dict(s["brique"]), s["brique"]["revision"])

    async def structure_patch(self, structure_id: str, semantic_patch: Dict[str, Any], expected_revision: int) -> int:
        await self._tick()
        s = self.engine.structures[structure_id]
        if self.engine.fail_structure_patch:
            raise EngineError("internal", "io_failure", "structure patch failed")
        if int(expected_revision) != s["brique"]["revision"]:
            self.engine.conflicts += 1
            raise RevisionMismatch("revision mismatch")
        s["functional"] = apply_ops({"functional": s["functional"]}, semantic_patch["operations"])["functional"]
        s["brique"]["revision"] = self.engine.next_rev()
        self.engine.log.append(("structure_patch", structure_id, expected_revision, None))
        return s["brique"]["revision"]

    async def call_capacity(self, ref: str, params: Dict[str, Any], *, to_type: str = "user", timeout_s=None) -> Dict[str, Any]:
        await self._tick()
        self.engine.calls.append(ref)
        if ref.endswith("/read.meaning"):         # reflexive: does the element exist in that context
            context = ref.rsplit("/", 1)[0]
            out = []
            for item in params.get("input") or []:
                wanted = f"{context}/{item.get('element_name')}"
                found = wanted in self.engine.known_capacities or wanted in self.engine.caps
                objective = {"name": item.get("element_name")}
                if wanted in self.engine.pilot_capacities:
                    objective["agentic"] = {"pilot": {"subject": "test"}}
                out.append({"element_kind": item.get("element_kind"), "element_name": item.get("element_name"), "ok": found,
                            "descriptor": {"objective": objective}})
            return {"result": out}
        fn = self.engine.caps.get(ref)
        if fn is None:
            raise NotFound("unknown capacity", details={"capacity": ref})
        return await fn(FakePort(self.engine, split_capacity_ref(ref)[1]), copy.deepcopy(params))

    async def call_capacity_response(self, ref: str, params: Dict[str, Any], *, to_type: str = "user", timeout_s=None, busy_attempts=None) -> Dict[str, Any]:
        try:
            return {"status": "ok", "payload": await self.call_capacity(ref, params, to_type=to_type)}
        except Exception as exc:  # engine would answer with status error
            return {"status": "error", "error": {"code": getattr(exc, "code", "internal"), "message": str(exc)}}

    async def wake(self, ref: str, params: Dict[str, Any], *, on_refused=None) -> None:
        """The same observable effect as the real wake (an activation requested, not awaited); the refusal path is tested on the real port."""
        await self.fire_and_forget(ref, params)

    async def fire_and_forget(self, ref: str, params: Dict[str, Any], *, to_type: str = "user") -> None:
        self.engine.calls.append(f"fire:{ref}")
        if ref in self.engine.bad_refs:
            raise EngineError("not_found", "unknown_cap", f"unknown capacity {ref}")
        if self.engine.on_fire is not None:
            self.engine.on_fire(self.engine, ref, params)
        fn = self.engine.caps.get(ref)
        if fn is None:
            return
        self.engine.tasks.append(asyncio.ensure_future(fn(FakePort(self.engine, split_capacity_ref(ref)[1]), copy.deepcopy(params))))


def make_engine(with_capacities: bool = True, pilot: bool = True) -> FakeEngine:
    import continuity_create, continuity_delete, continuity_project, continuity_succeed, input_answer, input_request, mailbox_ack, mailbox_read, mailbox_release, mailbox_send, pilot_infer, pilot_launch, reading_mark
    engine = FakeEngine()
    engine.texts[("/root/agentic/prompt", "pilot_framework")] = "FRAMEWORK TEXT"      # the cognitive frame every continuity opens with
    if with_capacities:
        base = "/root/agentic/"
        engine.caps[base + "continuity.create"] = continuity_create.run
        engine.caps[base + "continuity.delete"] = continuity_delete.run
        engine.caps[base + "continuity.project"] = continuity_project.run
        engine.caps[base + "continuity.succeed"] = continuity_succeed.run
        engine.caps[base + "pilot.infer"] = pilot_infer.run
        engine.caps[base + "pilot.launch"] = pilot_launch.run
        engine.caps[base + "input.answer"] = input_answer.run
        engine.caps[base + "input.request"] = input_request.run
        engine.caps[base + "reading.mark"] = reading_mark.run
        engine.caps[base + "mailbox.send"] = mailbox_send.run
        engine.caps[base + "mailbox.read"] = mailbox_read.run
        engine.caps[base + "mailbox.ack"] = mailbox_ack.run
        engine.caps[base + "mailbox.release"] = mailbox_release.run
        if pilot:
            import pilot_activate
            engine.caps[base + "pilot.activate"] = pilot_activate.run
    return engine
