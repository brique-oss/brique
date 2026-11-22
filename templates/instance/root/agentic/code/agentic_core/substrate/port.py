from __future__ import annotations

import contextlib

import asyncio
from typing import Any, Dict, List, NamedTuple, Optional, Tuple

from wrapper.execution import current_delegation      # the delegation the engine gave the request being served
from wrapper.matter_client import INLINE_APPEND_MAX, MatterClient, MatterError, dump_lines, revision_of      # the Matter client of the wrapper library

from .errors import AgenticError, BusyError, ConsistencyError, EngineError, LineCountMismatch, NotFound, from_engine_error
from .ids import CONTEXT
from .settings import setting
from .keepalive import keepalive

DEFAULT_TIMEOUT_S = setting("DEFAULT_TIMEOUT_S", 60.0, 1.0, "inactivity budget of a call (renewed by every `running` of the callee)")


class MatterView(NamedTuple):
    """content is the parsed substance (the business content the schema describes); descriptor is the
    functional section of the Matter element (what it does: format, schema_ref)."""
    matter_id: str
    content: Dict[str, Any]
    descriptor: Dict[str, Any]
    brique: Dict[str, Any]
    revision: int


class LinesView(NamedTuple):
    """A range of lines of a line-framed (JSON Lines) Matter: each line parsed, plus the committed dimensions."""
    lines: List[Dict[str, Any]]
    total_lines: int
    revision: int
    offset: int


class StructureView(NamedTuple):
    structure_id: str
    functional: Dict[str, Any]
    brique: Dict[str, Any]
    revision: int


async def resolve_params(params: Any) -> Dict[str, Any]:
    """The parameters of a capacity as plain values, whatever the form in which they arrived.

    The runtime of the wrapper hands the capacity a Params whose transit parameters (@file, @object) are references; they are
    resolved here, once, with its own `await params.value(name)`. A plain dict (a test, a direct call) is returned as is.
    """
    resolve = getattr(params, "value", None)
    if not callable(resolve):
        return params
    out = dict(params)
    for name, raw in list(out.items()):
        if name == "@matter":
            continue
        if _is_reference(raw) or (isinstance(raw, list) and any(_is_reference(element) for element in raw)):
            out[name] = await resolve(name)               # a list parameter is walked: each of its references is resolved
    return out


def _is_reference(raw: Any) -> bool:
    return isinstance(raw, dict) and any(key in raw for key in _RESERVED)


def split_capacity_ref(ref: str):
    """'/root/llm/infer' -> ('/root/llm', 'infer')."""
    text = str(ref or "").strip()
    if not text.startswith("/") or text.count("/") < 2:
        raise AgenticError(f"capacity reference must be absolute: {ref!r}")
    context, _, cap = text.rpartition("/")
    return context, cap


# Calls between capacities of this same wrapper. False: routed (and traced) by the engine, the setting for tests. True: a plain
# function call, with no engine trace, once the tests are green and performance is wanted. One place to change.
DIRECT_CALLS = False

_RESERVED = ("@file", "@object", "@matter")        # the reserved keys of a transit reference (Wrapper.md section 17.3)


# A callee that is busy (the Matter loop of the engine, the pending-work queue of a wrapper) refuses BEFORE running the call: the wrapper library asks again
# (BRIQUE_BUSY_ATTEMPTS, BRIQUE_BUSY_BASE_MS, BRIQUE_BUSY_CAP_MS). Only a wake insists longer than that, its message being already accepted.
WAKE_ATTEMPTS = setting("WAKE_ATTEMPTS", 40, 1, "attempts in all of a wake refused \"busy\": of the order of 35 s, a waiting request costs one task")

_WAKES: set = set()        # the tasks that carry a wake: kept here so that nothing collects them before they end (see BriquePort.wake)


def _wake_done(task: asyncio.Task) -> None:
    """Keep a background wake failure observable even when its journal callback itself could not report it."""
    _WAKES.discard(task)
    if task.cancelled():
        return
    error = task.exception()
    if error is not None:
        task.get_loop().call_exception_handler({"message": "agentic wake failure could not be reported", "exception": error, "task": task})


class BriquePort:
    """Access of one capacity execution to the Brique world, through outbound intentions.

    Every method is an outbound intention routed by the engine. from_cap is the exact
    name of the capacity currently executing (required for response routing).
    """

    def __init__(self, runtime, from_cap: str, *, context: str = CONTEXT, timeout_s: float = DEFAULT_TIMEOUT_S) -> None:
        self.runtime = runtime
        self.from_cap = from_cap
        self.context = context
        self.timeout_s = timeout_s

    # -- transport -------------------------------------------------------
    @staticmethod
    def _body(message: Dict[str, Any]) -> Dict[str, Any]:
        """The runtime correlator already swallows the intermediate `running` responses: what comes back is final,
        and the timeout is an inactivity budget that every `running` renews."""
        return message.get("response", {}) if isinstance(message.get("response"), dict) else {}

    def intention_id(self) -> Optional[str]:
        """The identifier of the intention this capacity is serving (None outside a call): what makes a derived identifier replayable."""
        try:
            from wrapper.execution import current_intention
        except Exception:                           # the library is not importable (a unit test without it)
            return None
        intention = current_intention.get()
        value = (intention or {}).get("intention_id") if isinstance(intention, dict) else None
        return str(value) if value else None

    def keepalive(self):
        """Acknowledge the capacity being executed as long-running, with a heartbeat until the block ends."""
        return keepalive(self.runtime)

    def yielding(self):
        """Give the execution slot of the wrapper back while this work waits for something that is not the answer of the engine (a lock, the
        inference window). The library does it for the answers of calls; a wait for anything else holds the slot, and as many such waits as there
        are slots stop the holder of what they wait for from taking its own slot back after its next call: the wrapper then freezes."""
        scheduler = getattr(self.runtime, "scheduler", None)
        return scheduler.yielding() if scheduler is not None else _no_wait()

    async def _emit(self, context: str, cap: str, to_type: str, params: Dict[str, Any]):
        """The parameters in the form the destination calls for: a value, or a transit reference for what is large.

        The runtime of the wrapper chooses (value up to 64 KiB, @object for a capacity of this same wrapper, @file for
        another wrapper of the instance); nothing here depends on the form, and a reference nobody consumed is released.
        Engine capacities (matter.*, structure.*) take plain values. Returns the emitted parameters and the references posted.
        """
        transit = getattr(self.runtime, "transit", None)
        if to_type != "user" or transit is None:
            return params, []
        try:
            from wrapper.transit import TransitTarget, reference_kind          # the shared runtime library of the instance
        except ImportError:
            return params, []
        calls = getattr(self.runtime, "calls", None)
        target = TransitTarget(context, cap, to_type, same_wrapper=bool(calls and calls.is_local(context, cap, to_type)))
        emitted: Dict[str, Any] = {}
        posted: List[Any] = []
        try:
            for name, value in params.items():
                out = await transit.emit_async(value, target=target)
                if out is not value and reference_kind(out) is not None:
                    posted.append(out)
                emitted[name] = out
        except BaseException:
            await self._release(posted)
            raise
        return emitted, posted

    async def _release(self, posted: List[Any]) -> None:
        for ref in posted:
            try:
                if "@matter" in ref:
                    await self.runtime.transit_matters.discard(ref)
                else:
                    self.runtime.transit.release(ref)
            except Exception:
                pass                                    # releasing is best effort: the runtime also sweeps at the end of the call

    async def _call(self, context: str, cap: str, to_type: str, params: Dict[str, Any], timeout_s: Optional[float]) -> Dict[str, Any]:
        """One call. A callee that refuses it because it is busy is asked again by the wrapper library; if it stays busy the call fails with BusyError. A call
        that timed out is never asked again: it may have run. Any other error is the caller's."""
        timeout = self.timeout_s if timeout_s is None else timeout_s
        emitted, posted = await self._emit(context, cap, to_type, params)
        try:
            message = await self.runtime.outbound.emit_and_wait(
                to_context=context,
                to_cap=cap,
                to_type=to_type,
                from_context="",
                from_cap=self.from_cap,
                params=emitted,
                timeout_s=timeout,
            )
        except asyncio.TimeoutError:
            raise                                       # the callee may still be about to take what was posted: do not release
        except BaseException:
            await self._release(posted)
            raise
        body = self._body(message)
        if str(body.get("status", "")) == "error":
            await self._release(posted)
            raise from_engine_error(body.get("error"))
        payload = body.get("payload")
        return payload if isinstance(payload, dict) else {}

    # -- matters ---------------------------------------------------------
    # The semantic Matter operations are the ones of the wrapper library (wrapper/matter_client.py): inline or lease reads, guarded writes, the
    # journal protocol. This only gives them the context and the capacity of this port, and turns their errors into the ones of agentic_core.
    @property
    def matters(self) -> MatterClient:
        return MatterClient(self.runtime, self.context, from_cap=self.from_cap, timeout_s=self.timeout_s)

    @staticmethod
    async def _matter(operation):
        try:
            return await operation
        except MatterError as exc:
            raise from_engine_error({"code": exc.code, "message": str(exc), "details": exc.details}) from exc

    async def matter_read(self, matter_id: str, context: Optional[str] = None) -> MatterView:
        """Content = the substance: served inline up to 256 KB, through a one-shot HTTP lease beyond. The Matter is one of this context
        unless another context is named (a pilot_definition may live anywhere)."""
        return MatterView(*await self._matter(self.matters.read(matter_id, context=context)))

    async def matter_read_text(self, context: str, matter_id: str) -> str:
        """The substance of a Matter of any context, as the text it is: served inline up to 256 KB, through a one-shot HTTP lease
        beyond. Nothing is parsed: the caller gets exactly what the Matter holds."""
        return await self._matter(self.matters.read_text(matter_id, context=context))

    async def matter_read_many(self, matter_ids: List[str]) -> List[MatterView]:
        """Several Matters in ONE engine request, in the order given. A missing Matter is NotFound, like matter_read."""
        return [MatterView(*view) for view in await self._matter(self.matters.read_many(matter_ids))]

    # -- line-framed Matters (the journal) -----------------------------------------
    @property
    def owner(self):
        """The object that lives as long as the wrapper: the home of the in-memory continuities."""
        return self.runtime

    async def matter_stat(self, matter_id: str) -> Tuple[int, int]:
        """(revision, total_lines) of a line-framed Matter, without reading its substance."""
        return await self._matter(self.matters.stat(matter_id))

    async def matter_create_empty(self, matter_id: str, descriptor: Dict[str, Any]) -> int:
        """Create a Matter with no initial substance (the journal: line-framed, empty)."""
        return await self._matter(self.matters.create(matter_id, descriptor))

    async def matter_read_lines(self, matter_id: str, offset: int, length: int) -> LinesView:
        """A range of lines (offset is 0-based): served inline up to 256 KB, through a one-shot HTTP lease beyond."""
        return LinesView(*await self._matter(self.matters.read_lines(matter_id, offset, length)))

    async def matter_append_lines(self, matter_id: str, lines: List[Dict[str, Any]], expected_line_count: int) -> int:
        """Append a block of lines in one atomic commit, guarded by expected_line_count. Returns the new total of lines.
        A block above the inline limit goes through the engine's HTTP lease; the guard then binds the lease."""
        return await self._matter(self.matters.append_lines(matter_id, lines, expected_line_count))

    async def matter_objective(self, matter_id: str, context: Optional[str] = None) -> Dict[str, Any]:
        """The objective part of the meaning of a Matter (its name, its type, what qualifies it), without reading its substance."""
        return await self._matter(self.matters.objective(matter_id, context=context))

    async def matter_exists(self, matter_id: str, context: Optional[str] = None) -> bool:
        return await self._matter(self.matters.exists(matter_id, context=context))

    async def matter_create(self, matter_id: str, descriptor: Dict[str, Any], content: Dict[str, Any]) -> int:
        return await self._matter(self.matters.create(matter_id, descriptor, content))

    async def matter_write(self, matter_id: str, *, content: Dict[str, Any], expected_revision: Optional[int] = None) -> int:
        """One atomic write of the whole content, guarded by expected_revision."""
        return await self._matter(self.matters.write(matter_id, content, expected_revision=expected_revision))

    async def matter_delete(self, matter_id: str) -> None:
        await self._matter(self.matters.delete(matter_id))

    # -- structures ------------------------------------------------------
    async def structure_read(self, structure_id: str) -> StructureView:
        payload = await self._call(self.context, "structure.read", "matter", {"structure_id": structure_id}, None)
        brique = payload.get("brique") or {}
        return StructureView(structure_id, payload.get("functional") or {}, brique, revision_of(brique))

    async def structure_patch(self, structure_id: str, semantic_patch: Dict[str, Any], expected_revision: int) -> int:
        # same canonical guard as matter.write; decimal string keeps the nanosecond revision exact
        payload = await self._call(self.context, "structure.patch", "matter",
                                   {"structure_id": structure_id, "semantic_patch": semantic_patch,
                                    "expected_revision": str(int(expected_revision))}, None)
        return int(payload.get("revision", 0))

    # -- capacities ------------------------------------------------------
    def _is_direct(self, context: str, cap: str, to_type: str) -> bool:
        """A call that DIRECT_CALLS turns into a plain function call: only a capacity bound in this same wrapper."""
        calls = getattr(self.runtime, "calls", None)
        return bool(DIRECT_CALLS and to_type == "user" and calls is not None and calls.is_local(context, cap, to_type))

    async def _direct(self, context: str, cap: str, params: Dict[str, Any], timeout_s: Optional[float]) -> Dict[str, Any]:
        timeout = self.timeout_s if timeout_s is None else timeout_s
        result = await self.runtime.calls.call(to={"context": context, "cap": cap, "type": "user"}, params=params, direct=True, timeout_s=timeout)
        return dict(result)

    async def call_capacity(self, ref: str, params: Dict[str, Any], *, to_type: str = "user",
                            timeout_s: Optional[float] = None) -> Dict[str, Any]:
        """Invoke a capacity and wait for its final payload. Raises on an error response."""
        context, cap = split_capacity_ref(ref)
        if self._is_direct(context, cap, to_type):
            return await self._direct(context, cap, params, timeout_s)
        return await self._call(context, cap, to_type, params, timeout_s)

    async def call_capacity_response(self, ref: str, params: Dict[str, Any], *, to_type: str = "user",
                                     timeout_s: Optional[float] = None, busy_attempts: Optional[int] = None) -> Dict[str, Any]:
        """Like call_capacity, but returns the whole canonical Response (status, payload, error). A callee that refuses because it is busy is asked
        again by the wrapper library, `busy_attempts` times in all (BRIQUE_BUSY_ATTEMPTS by default)."""
        context, cap = split_capacity_ref(ref)
        timeout = self.timeout_s if timeout_s is None else timeout_s
        if self._is_direct(context, cap, to_type):
            try:
                return {"status": "ok", "payload": await self._direct(context, cap, params, timeout_s)}
            except AgenticError as exc:
                return {"status": "error", "error": {"code": exc.code, "message": exc.message, "details": exc.details}}
        emitted, posted = await self._emit(context, cap, to_type, params)
        retry = {} if busy_attempts is None else {"busy_attempts": busy_attempts}
        try:
            message = await self.runtime.outbound.emit_and_wait(
                to_context=context, to_cap=cap, to_type=to_type, from_context="", from_cap=self.from_cap,
                params=emitted, timeout_s=timeout, **retry,
            )
        except asyncio.TimeoutError:
            raise
        except BaseException:
            await self._release(posted)
            raise
        body = self._body(message)
        if str(body.get("status", "")) == "error":
            await self._release(posted)
        return {k: v for k, v in body.items() if k in ("status", "payload", "error")}

    async def wake(self, ref: str, params: Dict[str, Any], *, on_refused=None) -> None:
        """Start a capacity (an activation) without waiting for it to end, and without losing the request.

        A call sent with no wait for an answer is refused in silence when the callee is busy: nobody learns that it never ran. Here the call
        waits for its answer, in a task of its own (the run it starts may last), so that a refusal is seen and asked again (see call_capacity_response). If it is still
        refused after the attempts, `on_refused(error)` says so. The legacy callback name is kept, but every terminal error is reported; timeout and
        transport errors carry outcome=unknown because the activation may already have started.
        """
        task = asyncio.ensure_future(self._wake_on_its_own(ref, params, on_refused))
        _WAKES.add(task)
        task.add_done_callback(_wake_done)

    async def _wake_on_its_own(self, ref: str, params: Dict[str, Any], on_refused) -> None:
        """Send the activation with the wrapper's own authority, not with the delegation of the call that caused it.

        A delegation lets a wrapper act ON BEHALF OF the caller of a request, while that request is open: the engine revokes it when the request is
        answered. The wake is not a step taken for the caller. It is the agentic's own reaction to work waiting in durable state, and it must outlive
        the request (the run it starts lasts minutes). Sent with the delegation it inherited from the task that created it, a wake sent after
        the answer is refused, and so is everything its failure handler then asks of the engine: the message is stored and nothing ever runs.
        A call without a delegation is the wrapper's own, as when nobody restricted called it. What the caller may do through the pilot
        is for the pilot's own rules and the policy of the instance, not for the lifetime of one request."""
        current_delegation.set("")      # this task's own copy of the context: the caller's request keeps its delegation for everything else
        await self._wake_until_taken(ref, params, on_refused)

    async def _wake_until_taken(self, ref: str, params: Dict[str, Any], on_refused) -> None:
        try:
            response = await self.call_capacity_response(ref, params, busy_attempts=WAKE_ATTEMPTS)
        except asyncio.CancelledError:
            raise
        except Exception as exc:
            if on_refused is not None:
                if isinstance(exc, AgenticError):
                    error = {"code": exc.code, "message": exc.message,
                             "details": {**exc.details, "reason": exc.details.get("reason", exc.reason), "outcome": "unknown"}}
                else:
                    reason = "timeout" if isinstance(exc, asyncio.TimeoutError) else "transport_error"
                    error = {"code": "unavailable", "message": str(exc) or reason,
                             "details": {"reason": reason, "outcome": "unknown"}}
                await on_refused(error)
            return
        if on_refused is not None and str(response.get("status", "")) == "error":
            await on_refused(response.get("error"))

    async def fire_and_forget(self, ref: str, params: Dict[str, Any], *, to_type: str = "user") -> None:
        """Non-blocking invoke: no Response is awaited or tracked."""
        context, cap = split_capacity_ref(ref)
        await self.runtime.outbound.emit_intention(
            to_context=context, to_cap=cap, to_type=to_type, from_context="", from_cap=self.from_cap,
            params=params, await_response=False,
        )


@contextlib.asynccontextmanager
async def _no_wait():
    yield
