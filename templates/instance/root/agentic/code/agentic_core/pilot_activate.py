from __future__ import annotations

import asyncio
import copy
import json
from typing import Any, Dict, List, Optional, Tuple

from substrate import continuity_store, inference_window, liveness
from substrate import mcp
from substrate import pilot_ref as pilot_refs
from substrate.cas import update_matter
from substrate.controls import DELETED, apply_at_safe_point, parse_control
from substrate.errors import AgenticError, ConsistencyError, InvalidRequest, NotFound, Refused
from substrate.ids import CONTEXT, check_continuity_id, matter_ids, new_id, new_run_id
from substrate.settings import setting
from substrate.families import ENGINE_FAMILIES, family_of          # noqa: F401  (the table lives in substrate/families.py)
from substrate.kinds import EFFECT_KINDS
from substrate.journal import append_trace, measure_line, usage_detail
from substrate.locks import critical
from substrate.authority import own_authority_when_self_called
from substrate.port import BriquePort, resolve_params
from substrate.policy import policy_of, refusal
from substrate.pressure import estimate_tokens, pressure
from substrate.conversation import conversation
from substrate.schemas import decode_provider_output, extract_json, provider_schema, validate_value
from substrate.states import find_entry

CAP = "pilot.activate"
POLL_S = setting("POLL_S", 5.0, 0.05, "safety net while waiting: liveness.notify wakes the run first, re-reading the Matters each second would buy nothing")
AWAIT_TIMEOUT_S = setting("AWAIT_TIMEOUT_S", 600.0, 1.0, "bound of an awaited intention's Response")
MAX_CYCLES = 10_000        # hard stop against a runaway cognitive loop
MAX_REFUSALS = 3            # a pilot that asks this many times in a row for what its policy refuses fails its treatment
WAIT_TIMEOUT_S = setting("WAIT_TIMEOUT_S", 3600.0, 1.0, "how long a wait group waits for answers that never come; limits.wait_timeout_s lowers or raises it")
DELIVERY_ATTEMPTS = setting("DELIVERY_ATTEMPTS", 3, 1, "a Response that mailbox.send does not take is offered again this many times")
DELIVERY_BACKOFF_S = setting("DELIVERY_BACKOFF_S", 0.2, 0.0, "pause before a Response that mailbox.send did not take is offered again (doubled at each attempt)")
# EFFECT_KINDS (substrate.kinds): lines of the effects of a decision, facts of the journal, not part of its conversation
MAX_CHAIN_STEPS = setting("MAX_CHAIN_STEPS", 50, 1, "inferences allowed in the chain of ONE message (projection / failure resumptions included)")

_background: set = set()   # strong references to in-flight awaited invocations
_inflight: set = set()     # (continuity_id, intention_id) of awaited invocations alive in THIS process


# <brique:capacity name="pilot.activate">
async def pilot_activate(runtime, params: dict) -> dict:
    return await run(BriquePort(runtime, CAP), await resolve_params(params))      # a large parameter arrives as a transit reference
# </brique:capacity>


def chain_limit(pilot: Dict[str, Any]) -> int:
    """Inferences allowed for ONE message: the pilot may lower the default ceiling, never raise it."""
    value = (pilot.get("limits") or {}).get("max_inferences_per_message")
    if isinstance(value, int) and not isinstance(value, bool) and value > 0:
        return min(value, MAX_CHAIN_STEPS)
    return MAX_CHAIN_STEPS


class Ctx:
    def __init__(self, port, cid: str, ids, pilot: Dict[str, Any]) -> None:
        self.port, self.cid, self.ids, self.pilot = port, cid, ids, pilot
        self.run_id: Optional[str] = None
        self.lineage_id: str = cid               # the identifier a delegating parent knows
        self.measured_tokens: Optional[int] = None
        self.measured_estimated: bool = False
        self.measured_detail: Dict[str, Any] = {}
        self.last_finish: Optional[str] = None                  # why the provider stopped on its last answer (for the diagnosis of an invalid output)
        self.capacity_ref: Optional[Dict[str, Any]] = None      # set when a Capacity, not a pilot_definition, decides
        self.compaction_failed: bool = False
        self.refusals: int = 0                                  # outputs refused by the policy in a row, in the chain of the current message
        self.pilot_ref: Optional[Dict[str, Any]] = None         # the pilot_ref this run decides with
        self.absorbed: List[str] = []                           # user messages absorbed by the chain in progress, claimed until its end
        self.mcp_projection: Optional[Dict[str, Any]] = None    # the MCP tools offered to this run (substrate/mcp.py), pinned for the run
        self.last_tool_output: Optional[Dict[str, Any]] = None  # the output of the last inference when the model called native tools


# =============================================================================
async def run(port, params: Dict[str, Any]) -> Dict[str, Any]:
    """A wake answers at once; any other activation may last as long as the run it drives or waits for, so it
    acknowledges itself as long-running (`running`, then a heartbeat) before its single final answer."""
    own_authority_when_self_called()          # a run that agentic_core started itself acts with its own principal, not on a time-bound delegation
    if params.get("wake") is True:
        return await _run(port, params)
    async with port.keepalive():
        return await _run(port, params)


async def _run(port, params: Dict[str, Any]) -> Dict[str, Any]:
    cid = check_continuity_id(params.get("continuity_id"))
    ids = matter_ids(cid)

    # --- #resolve_state / #resolve_pilot ----------------------------------------------
    try:
        header, _mailbox, _runtime = await port.matter_read_many([ids.continuity, ids.mailbox, ids.runtime])     # one request for the three state Matters
        continuity = header.content
        await continuity_store.get(port, cid)
    except NotFound as exc:
        raise ConsistencyError("continuity, journal, mailbox or runtime is missing", details=exc.details) from exc
    pilot, capacity_ref = await _resolve_pilot(port, continuity)
    ctx = Ctx(port, cid, ids, pilot)
    ctx.capacity_ref = capacity_ref
    ctx.pilot_ref = continuity["pilot_ref"]
    ctx.lineage_id = continuity.get("lineage_id") or cid
    if continuity.get("status") == "archived":      # read-only history: nothing is ever activated on it
        return {"continuity_id": cid, "run_id": None, "status": "archived", "current_message_id": None}

    # --- #ownership ---------------------------------------------------------------------
    # An activation is an ordinary awaited Capacity: when another live run owns the continuity it waits for that run
    # to end, then answers with the state the continuity is left in. A wake (the no-op activation mailbox.send
    # fires after each message) never waits: it answers at once.
    wake = params.get("wake") is True
    deadline = asyncio.get_running_loop().time() + AWAIT_TIMEOUT_S
    while True:
        acquired, runtime_state = await _acquire(ctx)
        if acquired:
            break
        if wake or runtime_state.get("status") not in ("running", "waiting") or asyncio.get_running_loop().time() >= deadline:
            return _result(cid, runtime_state, runtime_state.get("run_id"))
        await liveness.wait_change(cid, POLL_S)
    liveness.mark_live(cid, ctx.run_id)
    status = "idle"
    role = inference_window.enter_loop(cid)          # the writes of this loop pass a window reserved for it; those of the tasks it spawns do not
    try:
        status = await _drive(ctx, recovered=acquired == "recovered")
    except NotFound:
        status = "deleted"      # the continuity vanished under the run (continuity.delete)
    finally:
        inference_window.leave_loop(role)
        inference_window.clear(cid)                  # no reservation outlives the run: its requests become normal messages
        liveness.drop_live(cid, ctx.run_id)
    return {"continuity_id": cid, "run_id": ctx.run_id, "status": status, "current_message_id": None}


async def _resolve_pilot(port, continuity: Dict[str, Any]):
    """(pilot_definition content, Capacity reference) of what decides for the continuity now."""
    ref = continuity["pilot_ref"]
    if pilot_refs.is_capacity(ref):
        return {}, ref          # a Capacity pilot has no pilot_definition: its plan carries prompts, models and limits
    try:
        return (await port.matter_read(ref["matter_id"], ref.get("context"))).content, None
    except NotFound as exc:
        raise ConsistencyError("pilot_definition referenced by pilot_ref does not exist") from exc


async def _reload_pilot(ctx: Ctx) -> None:
    """A control (set_pilot) changed who decides: the next inference of this very run is the new pilot's, not the old one's."""
    continuity = (await ctx.port.matter_read(ctx.ids.continuity)).content
    if continuity["pilot_ref"] != ctx.pilot_ref:
        ctx.pilot, ctx.capacity_ref = await _resolve_pilot(ctx.port, continuity)
        ctx.pilot_ref = continuity["pilot_ref"]


def _result(cid, rt, run_id):
    return {"continuity_id": cid, "run_id": run_id, "status": rt.get("status"), "current_message_id": rt.get("current_message_id")}


async def _acquire(ctx: Ctx):
    """idle -> running with a new run_id, in one runtime write guarded by expected_revision.

    A runtime that says running/waiting for a run that is not alive in this process is an orphan:
    it is taken over by a new run_id (the claims and waits stay in the durable state).
    """
    new_id_ = new_run_id()

    def compute(f):
        state = f["status"]
        if state == "idle":
            f.update(status="running", run_id=new_id_)
            return f, ("recovered" if (f.get("current_message_id") or f.get("active_wait_groups")) else "fresh")
        if state in ("running", "waiting") and not liveness.is_live(ctx.cid, f.get("run_id")):
            f.update(status="running", run_id=new_id_)
            return f, "recovered"
        return None, None      # suspended, or a live run already owns the continuity

    async with critical(ctx.cid, yielding=ctx.port.yielding):
        mode = await update_matter(ctx.port, ctx.ids.runtime, compute, schema="runtime")
        if mode is None:
            return None, (await ctx.port.matter_read(ctx.ids.runtime)).content
        ctx.run_id = new_id_
        liveness.mark_live(ctx.cid, new_id_)     # inside the lock: a concurrent activation sees it alive
    return mode, {}


# =============================================================================
async def _drive(ctx: Ctx, *, recovered: bool) -> str:
    port, cid, ids = ctx.port, ctx.cid, ctx.ids
    stimulus: Optional[Dict[str, Any]] = None
    steps: Dict[str, int] = {}
    max_steps = chain_limit(ctx.pilot)
    if recovered:
        stimulus = await _recover(ctx)
    for _ in range(MAX_CYCLES):
        # --- #safe_point_controls ---------------------------------------------------
        verdict = await _controls(ctx)
        if verdict in ("deleted", "suspended"):
            return verdict
        if verdict == "changed":
            await _reload_pilot(ctx)
            max_steps = chain_limit(ctx.pilot)
        rt = (await port.matter_read(ids.runtime)).content
        if rt.get("run_id") != ctx.run_id:
            return rt["status"]            # ownership was lost

        # --- #compact: at the safe point (no message in progress, no wait group), when the context is too full
        if rt.get("current_message_id") is None and not rt["active_wait_groups"] \
                and not ctx.compaction_failed and await _compaction_required(ctx, rt):
            if await _compact(ctx) == "archived":
                return "archived"

        # --- #select_work: satisfied wait groups first, then the chain in progress, then new work
        group = next((g for g in rt["active_wait_groups"] if g["status"] == "satisfied"), None)
        message_id = rt.get("current_message_id")
        # --- #absorb: a user message that arrived while a chain is in progress joins that chain (a pilot_definition only)
        absorbed_now: List[str] = []
        if message_id is not None and ctx.capacity_ref is None and not mcp.tool_turn_open(rt["active_wait_groups"]):
            absorbed_now = await _absorb(ctx)
            if absorbed_now:
                steps.pop(message_id, None)                  # a new demand renews the allowance of inferences of the chain
        if message_id is None:
            got = await port.call_capacity(f"{CONTEXT}/mailbox.read", {"continuity_id": cid, "run_id": ctx.run_id, "priority": "normal"})
            message_id = got.get("message_id")
            if message_id is None:
                outcome = await _settle(ctx)
                if outcome == "continue":
                    continue
                return outcome
            await _set_current_message(ctx, message_id)
        if group is not None:
            stimulus = _wait_result(group)
        elif stimulus is None and not absorbed_now and any(g["status"] == "waiting" for g in rt["active_wait_groups"]):
            if await _wait_for_resumption(ctx) == "lost":
                return "waiting"
            continue

        steps[message_id] = steps.get(message_id, 0) + 1
        try:
            if steps[message_id] > max_steps:
                raise AgenticError(f"the cognitive chain of message {message_id} exceeded {max_steps} inferences")
            done = await _chain_step(ctx, message_id, stimulus, group, absorbed_now)
        except (AgenticError, asyncio.TimeoutError) as exc:
            if isinstance(exc, NotFound) and not await port.matter_exists(ids.continuity):
                raise                      # the continuity vanished under the run: continuity.delete
            outcome = await _fail(ctx, message_id, exc)       # anything else that was not found (a provider, a capacity) is a failure of the treatment
            if outcome in ("retry", "parked"):
                steps.pop(message_id, None)                    # each attempt has its own bound of inferences
                continue                                       # tried again at once, up to the third failure; then parked, and the messages after it go on
            return outcome
        stimulus = done.get("stimulus")
        if done["state"] == "waiting":
            if await _wait_for_resumption(ctx) == "lost":
                return "waiting"
        elif done["state"] == "complete":
            await _complete(ctx, message_id)
            stimulus = None
        elif done["state"] == "archived":
            await _set_current_message(ctx, None)
            await _publish_idle(ctx)
            return "archived"
    raise AgenticError("cognitive loop exceeded its cycle bound")


# -----------------------------------------------------------------------------
async def _absorb(ctx: Ctx) -> List[str]:
    """Claim the user messages that wait while a chain is in progress, in arrival order. They are not made the current message."""
    got: List[str] = []
    while True:
        read = await ctx.port.call_capacity(f"{CONTEXT}/mailbox.read", {"continuity_id": ctx.cid, "run_id": ctx.run_id, "priority": "normal", "kind": "user"})
        message_id = read.get("message_id")
        if message_id is None:
            return got
        got.append(message_id)
        ctx.absorbed.append(message_id)


async def _release_absorbed(ctx: Ctx) -> None:
    """The chain does not end normally (failure, succession): the absorbed messages go back in the queue, to be taken again."""
    for message_id in ctx.absorbed:
        entry = find_entry((await ctx.port.matter_read(ctx.ids.mailbox)).content["entries"], message_id)
        if entry is not None and entry["status"] == "claimed" and entry.get("claimed_by") == ctx.run_id:
            await ctx.port.call_capacity(f"{CONTEXT}/mailbox.release", {"continuity_id": ctx.cid, "message_id": message_id, "run_id": ctx.run_id})
    ctx.absorbed = []


async def _user_waiting(ctx: Ctx, mailbox_state: Dict[str, Any]) -> bool:
    """Is a user message waiting in the queue? The entries do not carry the kind of their message: it is read in the journal."""
    pending = [e["message_id"] for e in mailbox_state["entries"] if e["status"] == "pending" and e["priority"] == "normal"]
    if not pending:
        return False
    journal = await continuity_store.get(ctx.port, ctx.cid)
    return any(journal.kind_of(mid) == "user" for mid in pending)


async def _controls(ctx: Ctx) -> str:
    """Claim, apply and acknowledge pending controls one at a time, in seq order, until none remains."""
    port, cid, ids = ctx.port, ctx.cid, ctx.ids
    applied = False
    while True:
        got = await port.call_capacity(f"{CONTEXT}/mailbox.read", {"continuity_id": cid, "run_id": ctx.run_id, "priority": "control"})
        message_id = got.get("message_id")
        if message_id is None:
            return "changed" if applied else "none"
        message = (await continuity_store.get(port, cid)).message(message_id)
        action, content = parse_control(message["content"])
        verdict = await apply_at_safe_point(port, cid, ids, ctx.run_id, action, content)
        if verdict == DELETED:
            return "deleted"          # terminal: the mailbox disappeared with the continuity, no ack
        await port.call_capacity(f"{CONTEXT}/mailbox.ack", {"continuity_id": cid, "message_id": message_id, "run_id": ctx.run_id})
        applied = True
        if action == "suspend":
            return "suspended"


async def _set_current_message(ctx: Ctx, message_id: Optional[str]) -> None:
    def compute(f):
        if f.get("run_id") != ctx.run_id:
            raise Refused("the run no longer owns the continuity")
        if f.get("current_message_id") == message_id:
            return None, None
        f["current_message_id"] = message_id
        return f, None
    await update_matter(ctx.port, ctx.ids.runtime, compute, continuity_id=ctx.cid, schema="runtime")


async def _complete(ctx: Ctx, message_id: str) -> None:
    ctx.refusals = 0
    await ctx.port.call_capacity(f"{CONTEXT}/mailbox.ack", {"continuity_id": ctx.cid, "message_id": message_id, "run_id": ctx.run_id})
    for absorbed in ctx.absorbed:                    # the messages the chain absorbed end with it
        try:
            await ctx.port.call_capacity(f"{CONTEXT}/mailbox.ack", {"continuity_id": ctx.cid, "message_id": absorbed, "run_id": ctx.run_id})
        except (NotFound, Refused):
            pass
    ctx.absorbed = []
    await _set_current_message(ctx, None)


async def _fail(ctx: Ctx, message_id: str, exc: Exception) -> str:
    """The current treatment fails explicitly: error recorded, claim released with one more attempt (effects are never rolled back).
    "retry": the message is back in the queue with attempts left, and the run goes on (it takes the oldest pending message, this one again);
    "parked": the third failure, the message leaves the queue; "idle": nothing could be released, the run ends."""
    ctx.refusals = 0
    await _release_absorbed(ctx)
    await append_trace(ctx.port, ctx.cid, "error", {"event": "treatment_failed", "message_id": message_id, "error": str(exc)})
    entry = find_entry((await ctx.port.matter_read(ctx.ids.mailbox)).content["entries"], message_id)
    released: Dict[str, Any] = {}
    if entry is not None and entry["status"] == "claimed" and entry.get("claimed_by") == ctx.run_id:
        released = await ctx.port.call_capacity(f"{CONTEXT}/mailbox.release", {"continuity_id": ctx.cid, "message_id": message_id, "run_id": ctx.run_id, "failed": True})
    await _set_current_message(ctx, None)
    if released.get("parked"):
        await append_trace(ctx.port, ctx.cid, "error", {"event": "message_parked", "message_id": message_id, "attempts": released.get("attempts")})
        await _abandon_wait_groups(ctx, message_id)
        return "parked"
    if released.get("attempts"):
        return "retry"                                         # counted and back in the queue: the run keeps its ownership and goes on
    await _publish_idle(ctx)
    return "idle"


async def _abandon_wait_groups(ctx: Ctx, message_id: str) -> None:
    """The message is parked: the groups of its chain are closed with it, and the answers they captured are acknowledged, so that no later
    message finds them as its own stimulus and no answer stays claimed by a run that ends."""
    groups = (await ctx.port.matter_read(ctx.ids.runtime)).content.get("active_wait_groups") or []
    if not groups:
        return
    await append_trace(ctx.port, ctx.cid, "event", {"event": "wait_groups_abandoned", "message_id": message_id, "wait_group_ids": [g["id"] for g in groups]})
    for group in groups:
        await _complete_group(ctx, group["id"], consume=group.get("received", []))


async def _publish_idle(ctx: Ctx) -> bool:
    def compute(f):
        if f.get("run_id") != ctx.run_id:
            return None, False
        f.update(status="idle", run_id=None, current_message_id=None)
        return f, True
    return await update_matter(ctx.port, ctx.ids.runtime, compute, continuity_id=ctx.cid, schema="runtime")


async def _settle(ctx: Ctx) -> str:
    """Release-and-recheck: publish idle (guarded), then re-read; re-acquire if work appeared meanwhile."""
    port, cid, ids = ctx.port, ctx.cid, ctx.ids
    if not await _publish_idle(ctx):
        return "idle"
    liveness.drop_live(cid, ctx.run_id)
    # one request for both, read in this order (mailbox, then runtime): an entry that lands after the mailbox read finds the runtime idle
    # and wakes the continuity itself, so no wake is lost
    mbx_view, rt_view = await port.matter_read_many([ids.mailbox, ids.runtime])
    mbx, rt = mbx_view.content, rt_view.content
    journal = await continuity_store.get(port, cid)
    pending = any(e["status"] == "pending" and journal.has(e["message_id"]) for e in mbx["entries"])
    if rt["status"] == "idle" and pending:
        ctx.run_id = None
        acquired, _ = await _acquire(ctx)
        if acquired:
            liveness.mark_live(cid, ctx.run_id)
            return "continue"
    return "idle"


async def _wait_for_resumption(ctx: Ctx) -> str:
    """The run is waiting: runtime.status = waiting, and the same run resumes when a group is satisfied."""
    port, cid, ids = ctx.port, ctx.cid, ctx.ids

    def to_waiting(f):
        if f.get("run_id") != ctx.run_id:
            return None, "lost"
        if any(g["status"] == "satisfied" for g in f["active_wait_groups"]):
            return None, "go"
        if f["status"] == "waiting":
            return None, "waiting"
        if f["status"] != "running":
            return None, "lost"
        f["status"] = "waiting"
        return f, "waiting"
    state = await update_matter(port, ids.runtime, to_waiting, continuity_id=cid, schema="runtime")
    if state == "lost":
        return "lost"
    loop = asyncio.get_running_loop()
    waiting_since = loop.time()
    while state != "go":
        rt = (await port.matter_read(ids.runtime)).content
        if rt.get("run_id") != ctx.run_id:
            return "lost"
        if any(g["status"] == "satisfied" for g in rt["active_wait_groups"]):
            return "go"
        mbx = (await port.matter_read(ids.mailbox)).content
        if any(e["status"] == "pending" and e["priority"] == "control" for e in mbx["entries"]):
            return "go"       # a control reached a safe point (the waiting state is one)
        if ctx.capacity_ref is None and not mcp.tool_turn_open(rt["active_wait_groups"]) and await _user_waiting(ctx, mbx):
            return "go"       # a user message joins the chain in progress; the wait groups stay open (never an open native tool turn: its results come first)
        if loop.time() - waiting_since >= wait_timeout_s(ctx):
            await _expire_waits(ctx, rt["active_wait_groups"])      # the answers that did not come become error answers: the wait ends
            waiting_since = loop.time()
            continue
        await liveness.wait_change(cid, POLL_S)
    return "go"


def wait_timeout_s(ctx: Ctx) -> float:
    limits = (ctx.capacity_ref if ctx.capacity_ref is not None else ctx.pilot).get("limits") or {}
    value = limits.get("wait_timeout_s")
    return float(value) if isinstance(value, (int, float)) and not isinstance(value, bool) and value > 0 else WAIT_TIMEOUT_S


async def _expire_waits(ctx: Ctx, groups: List[Dict[str, Any]]) -> None:
    """Every answer a waiting group still expects becomes an error answer (timeout), delivered like any other through mailbox.send.
    Its id differs from the real answer's: if the real one comes late, it enters the history as a late result of a resolved wait."""
    for group in groups:
        if group.get("status") != "waiting":
            continue
        received = {r.get("expected_ref") for r in group.get("received", [])}
        for item in group.get("expected", []):
            ref = item.get("ref")
            if ref in received:
                continue
            error = {"origin": "agentic_core", "code": "timeout", "message": "no answer came within the wait delay: the state of what was expected is unknown"}
            if item.get("type") == "intention":
                message = {"id": f"timeout_{ctx.cid}_{ref}", "from": "agentic_core", "to": ctx.cid, "kind": "capacity_response",
                           "content": {"response": {"status": "error", "error": error}, "unresolved": True}, "correlation": {"intention_id": ref}}
            else:
                message = {"id": f"timeout_{ctx.cid}_{ref}", "from": f"timeout:{ref}", "to": ctx.cid, "kind": "continuity",
                           "content": {"outcome": "error", "unresolved": True, "error": error}, "correlation": dict(item.get("correlation") or {})}
            try:
                await ctx.port.call_capacity(f"{CONTEXT}/mailbox.send", {"continuity_id": ctx.cid, "message": message})
            except (Refused, ConsistencyError):
                pass            # already delivered, or the real answer came first under the same id


# =============================================================================
async def _recover(ctx: Ctx) -> Optional[Dict[str, Any]]:
    """Takeover of an interrupted run: claims move to the new run, nothing already emitted is replayed."""
    port, cid, ids = ctx.port, ctx.cid, ctx.ids
    rt = (await port.matter_read(ids.runtime)).content
    message_id = rt.get("current_message_id")

    def transfer(f):
        # one run per continuity: any claim held by another run_id belongs to a dead run
        changed, open_ = False, False
        for entry in f["entries"]:
            if entry["status"] == "claimed" and entry.get("claimed_by") != ctx.run_id:
                if entry.get("priority") == "normal" and entry["message_id"] != message_id:
                    entry["status"] = "pending"            # claimed, never started (no current message) or absorbed by the chain: back in the queue
                    entry.pop("claimed_by", None)
                else:
                    entry["claimed_by"] = ctx.run_id
                changed = True
            if entry["message_id"] == message_id:
                open_ = True
        return (f if changed else None), open_
    still_open = await update_matter(port, ids.mailbox, transfer, continuity_id=cid, schema="mailbox")
    if message_id is None:
        return None
    if not still_open:
        await _set_current_message(ctx, None)
        return None
    if not rt["active_wait_groups"] and await _chain_ended(ctx, message_id):
        # the dead run wrote the last message of the chain and died before acknowledging: nothing is left to answer
        await append_trace(port, cid, "event", {"event": "run_recovered", "message_id": message_id, "already_answered": True})
        await port.call_capacity(f"{CONTEXT}/mailbox.ack", {"continuity_id": cid, "message_id": message_id, "run_id": ctx.run_id})
        await _set_current_message(ctx, None)
        return None
    await append_trace(port, cid, "event", {"event": "run_recovered", "message_id": message_id})
    if any(g["status"] in ("waiting", "satisfied") for g in rt["active_wait_groups"]):
        await _resolve_lost_awaits(ctx, rt["active_wait_groups"])
        return None          # the durable wait state drives the resumption
    return {"kind": "recovery", "run_id": ctx.run_id, "message_id": message_id, "emitted": await _emitted_for(ctx, message_id),
            "note": "The previous run was interrupted. Emitted intentions are never replayed; decide from the actual state."}


async def _chain_ended(ctx: Ctx, message_id: str) -> bool:
    """Did the chain of this message end? Its last message (ends_chain) is in the journal, after the message, with the message as its cause."""
    journal = await continuity_store.get(ctx.port, ctx.cid)
    seq = journal.seq_of(message_id)
    if not seq:
        return False
    for line in await continuity_store.lines_after(ctx.port, ctx.cid, seq):
        content = line.get("content")
        if (line.get("kind") == "pilot" and isinstance(content, dict) and content.get("ends_chain") is True
                and (line.get("correlation") or {}).get("causation_id") == message_id):
            return True
    return False


async def _emitted_for(ctx: Ctx, message_id: str) -> List[Dict[str, Any]]:
    """What the journal knows of the chain of this message: each intention emitted since it, whether an answer was expected, whether one came."""
    thread = (await continuity_store.get(ctx.port, ctx.cid)).thread()
    since = next((m["seq"] for m in thread if m["id"] == message_id), 0)
    answered = {(m.get("correlation") or {}).get("intention_id") for m in thread if m["kind"] == "capacity_response"}
    return [{"id": m["content"].get("id"), "await_response": m["content"].get("await_response"), "answered": m["content"].get("id") in answered}
            for m in thread if m["kind"] == "capacity_intention" and m["seq"] > since and isinstance(m.get("content"), dict)]


async def _resolve_lost_awaits(ctx: Ctx, groups: List[Dict[str, Any]]) -> None:
    """An awaited invocation whose process is gone can never deliver its Response.

    The real state of that intention is unknown: it is never replayed, and the wait is not left hanging.
    The uncertainty is made explicit as an error Response, delivered through mailbox.send like any other.
    """
    for group in groups:
        if group.get("status") != "waiting":
            continue
        received = {r.get("expected_ref") for r in group.get("received", [])}
        for item in group.get("expected", []):
            if item.get("type") != "intention" or item.get("ref") in received:
                continue
            if (ctx.cid, item["ref"]) in _inflight:
                continue                       # still alive in this process: its Response will arrive
            if str(item["ref"]).startswith("mcp_"):
                known = await _mcp_status_answer(ctx, item["ref"])
                if known is not None:          # the gateway holds the terminal state of the call: that is its answer
                    try:
                        await ctx.port.call_capacity(f"{CONTEXT}/mailbox.send", {"continuity_id": ctx.cid, "message": build_response_message(
                            ctx.cid, {"id": item["ref"], "target_capacity": mcp.CALL_CAPACITY}, known)})
                    except (Refused, ConsistencyError):
                        pass
                    continue
            response = {"status": "error", "error": {"origin": "agentic_core", "code": "unresolved",
                                                      "message": "the state of this intention is unknown: its invocation was interrupted and is never replayed"}}
            message = build_response_message(ctx.cid, {"id": item["ref"], "target_capacity": "unknown"}, response)
            message["content"]["unresolved"] = True
            try:
                await ctx.port.call_capacity(f"{CONTEXT}/mailbox.send", {"continuity_id": ctx.cid, "message": message})
            except (Refused, ConsistencyError):
                pass            # already delivered, or the real answer came first under the same id


def _wait_result(group: Dict[str, Any]) -> Dict[str, Any]:
    """What the resumed cognition is told: that the wait ended and how many answers it holds. The answers themselves are in the
    conversation, each with the id of its intention; the group, its condition and the message ids are the machinery's business."""
    received = group.get("received", [])
    no_success = group["condition"] == "first_success" and received and all(r.get("outcome") == "error" for r in received)
    # the id of the group makes every resumption a different text: pilot.infer memoizes a step by the text of its prompt, and two waits that
    # ended alike (three answers of three) must never be taken for one
    return {"kind": "active_wait_result", "wait_group_id": group["id"], "outcome": "no_success" if no_success else "satisfied",
            "received": len(received), "expected": len(group.get("expected") or [])}


# =============================================================================



COMPACTION_GUIDE = "\n".join([
    "The context of this continuity is too full to go on. Write the synthesis that will be the first message of its successor.",
    "Answer with exactly one JSON object whose only used field is succession: { summary }.",
    "The summary must carry what is needed to continue without the history: the founding intention, what was decided and done,",
    "the facts and identifiers still relevant, what is waiting or pending, and what remains to do. Do not address the user.",
    "The runtime appends to your summary an exact ledger of the calls made (capacities, parameters, outcomes, identifiers of what was produced): do not copy it.",
    "Write what the ledger cannot say: where the work stands, what was decided and why, what is waiting and what remains to do.",
])


async def _record_context_tokens(ctx: Ctx, message_id: Optional[str]) -> None:
    """The size of the context of the last inference is a follow-up fact: an event line of the journal, not a state of runtime."""
    tokens = ctx.measured_tokens
    if tokens is None:
        return
    last = (await continuity_store.get(ctx.port, ctx.cid)).measure
    if last is not None and last.get("input_tokens") == tokens and last.get("message_id") == message_id:
        return
    await continuity_store.append(ctx.port, ctx.cid, [measure_line(ctx.cid, message_id, tokens, estimated=ctx.measured_estimated,
                                                                    detail=ctx.measured_detail)])


async def _last_tokens(ctx: Ctx) -> Optional[int]:
    """The last measured size of the context, read from the in-memory continuity."""
    last = (await continuity_store.get(ctx.port, ctx.cid)).measure
    return last.get("input_tokens") if last else None


async def _compaction_required(ctx: Ctx, rt: Dict[str, Any]) -> bool:
    level = pressure(ctx.pilot, await _last_tokens(ctx))
    if level is None or level["level"] != "required":
        return False
    mbx = (await ctx.port.matter_read(ctx.ids.mailbox)).content
    return any(e["status"] == "pending" and e["priority"] == "normal" for e in mbx["entries"])    # only when work is waiting


async def _compact(ctx: Ctx) -> str:
    """Inference of compaction at the safe point, then succession. Best effort: a failure leaves the continuity as it is."""
    port, cid, ids = ctx.port, ctx.cid, ctx.ids
    continuity = (await port.matter_read(ids.continuity)).content
    view = await _project(ctx, {}, "merge", None)
    # the compaction is one more turn of the conversation, the biggest one: the same system text, the same history and the same schema as
    # every other turn, so the provider reads them from its cache; only the last message differs, and says what is asked
    tools = (ctx.mcp_projection or {}).get("tools") or None
    request = build_request(ctx.pilot, continuity, view, None, {"kind": "compaction", "instruction": COMPACTION_GUIDE},
                            tools=tools, tool_choice="none" if tools else None)
    try:
        answer = await _infer(ctx, request)
        summary = (answer.get("succession") or {}).get("summary") if isinstance(answer, dict) else None
        if not (isinstance(summary, (str, dict)) and summary):
            raise InvalidRequest("the compaction inference returned no summary")
        await _release_absorbed(ctx)
        await port.call_capacity(f"{CONTEXT}/continuity.succeed", {"continuity_id": cid, "summary": summary, "run_id": ctx.run_id})
    except (AgenticError, asyncio.TimeoutError) as exc:
        await append_trace(port, cid, "error", {"event": "compaction_failed", "error": str(exc)})
        ctx.compaction_failed = True          # not asked again by this run: the next cycle would pay the biggest request again for the same answer
        return "failed"
    await _publish_idle(ctx)
    return "archived"


async def _decide_succession(ctx: Ctx, succession: Dict[str, Any]) -> Dict[str, Any]:
    """The pilot decided to continue in a new continuity. The message in progress goes with it, so the successor answers it."""
    port, cid, ids = ctx.port, ctx.cid, ctx.ids
    rt = (await port.matter_read(ids.runtime)).content
    if rt["active_wait_groups"]:
        result = {"succeeded": [], "failed": "succession", "error": "a continuity with an active wait group cannot succeed", "remaining": []}
        await append_trace(port, cid, "error", {"event": "execution_error", **result})
        return {"state": "again", "stimulus": dict(result, kind="execution_result")}
    await _release_absorbed(ctx)
    current = rt.get("current_message_id")
    if current is not None:
        entry = find_entry((await port.matter_read(ids.mailbox)).content["entries"], current)
        if entry is not None and entry["status"] == "claimed" and entry.get("claimed_by") == ctx.run_id:
            await port.call_capacity(f"{CONTEXT}/mailbox.release", {"continuity_id": cid, "message_id": current, "run_id": ctx.run_id})
    await port.call_capacity(f"{CONTEXT}/continuity.succeed", {"continuity_id": cid, "summary": succession["summary"], "run_id": ctx.run_id})
    return {"state": "archived", "stimulus": None}


async def _chain_step(ctx: Ctx, message_id: str, stimulus: Optional[Dict[str, Any]], group: Optional[Dict[str, Any]],
                      absorbed: Optional[List[str]] = None) -> Dict[str, Any]:
    """One inference of the chain of the current message, and the application of its decision."""
    port, cid, ids = ctx.port, ctx.cid, ctx.ids
    continuity = (await port.matter_read(ids.continuity)).content                    # the header
    journal = await continuity_store.get(port, cid)                                   # the in-memory continuity
    current = journal.message(message_id)

    if ctx.capacity_ref is not None:
        return await _capacity_step(ctx, continuity, current, stimulus, group)

    # --- #cognition: projection, context, inference, validation (one correction at most) -------
    # The inference window: from the reading of the journal to the writing of the decision nothing else enters the continuity and no other
    # inference runs for it; the request of a user that opened it for this run is taken over here. Applying the effects is outside it.
    tools = await _mcp_tools(ctx)          # outside the window: the gateway may take seconds to list a server
    async with inference_window.hold(ctx.cid, adopt=[message_id] + list(absorbed or []), yielding=ctx.port.yielding):
        dynamic = stimulus.get("projection_view") if stimulus and stimulus.get("kind") == "projection" else None
        view = dynamic or await _project(ctx, {}, "merge", None)
        request = build_request(ctx.pilot, continuity, view, current, stimulus, user_messages=absorbed, tools=tools,
                                mcp_unavailable=(ctx.mcp_projection or {}).get("unavailable") or None)       # the model never sees the pressure of the context: compacting is a measured threshold, so code
        output, said = await _infer_valid(ctx, request, await _used_intention_ids(ctx))
        if output == mcp.TOOL_TURN:
            await _record_context_tokens(ctx, message_id)
            turn = await _record_tool_turn(ctx, message_id, said)
        else:
            output = declare_families(ctx.pilot, output)
            await _record_context_tokens(ctx, message_id)
            await _record_decision(ctx, message_id, said)

    # the active_wait_result was consumed by this inference: its group completes before the decision applies
    if group is not None:
        await _complete_group(ctx, group["id"], consume=group.get("received", []))

    # --- #apply_decision ------------------------------------------------------------------
    if output == mcp.TOOL_TURN:
        return await _apply_mcp(ctx, turn)
    return await _apply(ctx, continuity, output)


async def _capacity_step(ctx: Ctx, continuity: Dict[str, Any], current: Optional[Dict[str, Any]],
                         stimulus: Optional[Dict[str, Any]], group: Optional[Dict[str, Any]]) -> Dict[str, Any]:
    """The decision of a Capacity pilot: its plan runs in the engine, each prompt goes through pilot.infer.

    The plan is stateless and re-entrant: a resumption invokes it again with the stimulus. Its output is a pilot_output,
    validated and applied exactly as a pilot_definition's, without a correction inference: an invalid output is a defect of the plan.
    """
    ref = ctx.capacity_ref
    params: Dict[str, Any] = {"continuity_id": ctx.cid, "run_id": ctx.run_id, "message": current,
                              "max_inferences": pilot_refs.max_inferences(ref)}
    level = pressure({"limits": ref.get("limits")}, await _last_tokens(ctx))      # None without limits.context_tokens in the pilot_ref
    if level is not None and level["level"] != "none":
        params["context_pressure"] = level
    if stimulus is not None:
        shown = {k: v for k, v in stimulus.items() if k != "projection_view"}
        if stimulus.get("projection_view") is not None:
            shown["projection"] = stimulus["projection_view"]
        params["stimulus"] = shown
    try:
        output = await ctx.port.call_capacity(f"{ref['context']}/{ref['capacity']}", params, timeout_s=pilot_refs.timeout_s(ref))
    except NotFound as exc:       # the Capacity is missing: this is not the continuity vanishing
        # An admitted caller gets one answer for "no such capacity", "not authorized" and "a Matter the call names does not exist":
        # the engine does not reveal which (SECURITY.md, invariant 7), so the message says all that the answer can mean.
        raise ConsistencyError("the Capacity pilot referenced by pilot_ref, or an element its call names, does not exist or may not be used: "
                               + exc.message, details={"pilot_ref": ref}) from exc
    if isinstance(output, dict):
        output = decode_provider_output("pilot_output", output)       # a strict step schema carries free values as JSON text: same decoding as a pilot_definition's
    try:
        used = await _used_intention_ids(ctx)
        output = validate_output(normalize_output({}, output, used), used)       # a plan's output is written by code and normalized the same way
    except InvalidRequest as exc:
        await append_trace(ctx.port, ctx.cid, "error", {"event": "pilot_output_invalid", "errors": exc.details.get("errors", [exc.message])})
        raise
    if group is not None:
        await _complete_group(ctx, group["id"], consume=group.get("received", []))
    return await _apply(ctx, continuity, output)


async def _project(ctx: Ctx, projection: Dict[str, Any], mode: str, budget) -> Dict[str, Any]:
    if ctx.capacity_ref is None:        # a pilot_definition sends decisions, which already say their effects: those lines are neither sent nor counted
        projection = dict(projection, exclude_kinds=sorted(set(projection.get("exclude_kinds") or []) | set(EFFECT_KINDS)))
    params: Dict[str, Any] = {"continuity_id": ctx.cid, "projection": projection, "mode": mode}
    if budget is not None:
        params["budget"] = budget
    if ctx.pilot.get("default_projection"):                  # read once with the pilot_definition, passed as a value
        params["default_projection"] = ctx.pilot["default_projection"]
    return await ctx.port.call_capacity(f"{CONTEXT}/continuity.project", params)


def _model_of(pilot: Dict[str, Any]) -> Tuple[Optional[str], Optional[str]]:
    model = pilot.get("model")
    if isinstance(model, dict):
        return model.get("provider"), model.get("model")
    if isinstance(model, str) and "/" in model:
        provider, _, name = model.partition("/")
        return provider, name
    return None, model if isinstance(model, str) else None


# Stable (cache-friendly) guidance, kept short: it is paid at every inference and read by weaker models too.
# The provider-facing schema carries the field names and types; this text carries what they mean.
OUTPUT_GUIDE = "\n".join([
    "Reply with one JSON object matching the schema. {unused}At least one field must be used.",
    "- intentions: capacities to invoke (target_capacity, parameters, await_response). await_response true: the Response comes back "
    "to you through a wait_group; false: fire and forget.",
    "- wait_group: only to wait. It waits for all the intentions of this same output with await_response true; list expected only to wait for a "
    "delegated continuity (type continuity, ref = its id). condition: all | any | first_success | n_of_m (with n), over what it waits for.",
    "- messages: to, kind, content (text, or JSON as text). kind user or pilot is recorded in the history; continuity delivers to another continuity (to = its id).",
    "- Free values (parameters, content, intention_update) are JSON written as text, or a plain string.",
    "- projection_request: ask for another view of the history; alone. projection and correlation are JSON text.",
    "- intention_update: your new current intention. succession: continue in a new continuity from your summary; alone.",
])


def schema_style(pilot: Dict[str, Any]) -> str:
    """Provider-facing schema style: Anthropic limits union-typed parameters, so it gets optional properties
    (absent when unused); the other providers get the strict all-required nullable form."""
    provider, _ = _model_of(pilot)
    upstream = (pilot.get("parameters") or {}).get("upstream_provider") or provider
    return "optional" if upstream == "anthropic" else "nullable"


def output_guide(style: str) -> str:
    unused = "Omit every field you do not use. " if style == "optional" else "Use null for every field you do not use. "
    return OUTPUT_GUIDE.replace("{unused}", unused)


def build_request(pilot: Dict[str, Any], continuity: Dict[str, Any], view: Dict[str, Any], current: Optional[Dict[str, Any]],
                  stimulus: Optional[Dict[str, Any]], correction: Optional[Dict[str, Any]] = None,
                  user_messages: Optional[List[str]] = None, tools: Optional[List[Dict[str, Any]]] = None,
                  tool_choice: Optional[str] = None, mcp_unavailable: Optional[List[Dict[str, Any]]] = None) -> Dict[str, Any]:
    """The request is the conversation, in this order: the stable system text (cognitive frame, pilot_definition, rules, guide, stable
    capacities, initial intention), the journal as messages with their roles, then what is new (the current intention and message, the
    active stimulus, the context pressure). What was sent at the previous inference is the start of what is sent now."""
    provider, model = _model_of(pilot)
    projection = view.get("projection", view)
    frame, history = conversation(projection, skip_kinds=EFFECT_KINDS)      # the cognitive frame opens the system text, once; the decisions say their effects
    system = frame + [str(pilot.get("prompt", ""))]
    rules = pilot.get("rules") or []
    if rules:
        system.append("Rules:\n" + "\n".join(f"- {r}" for r in rules))
    style = schema_style(pilot)
    system.append(output_guide(style))
    if (mcp.section(pilot) or {}).get("servers"):                             # a pilot with MCP servers: stable, so in the cached prefix
        system.append(mcp.REMOTE_RULE)
    # the stable capacities of the pilot_definition belong to the cached prefix: they grow with the pilot, not with the conversation
    capacities = [{"ref": c.get("ref"), "target_type": c.get("target_type") or "user", "description": c.get("description")} for c in (pilot.get("known_capacities") or [])]
    if capacities:
        system.append("Available capacities (target_capacity = ref):\n" + json.dumps(capacities, ensure_ascii=False))
    allowed, forbidden, allowed_contexts, forbidden_contexts = policy_of(pilot, None)
    if forbidden or forbidden_contexts:
        system.append("Never invoke (refused before any effect): " + ", ".join(forbidden + [f"anything in {c}" for c in forbidden_contexts]))
    if allowed or allowed_contexts:
        system.append("You may invoke only: " + ", ".join(allowed + [f"anything in {c}" for c in allowed_contexts]) + " (and read.capacity / read.meaning to read a contract)")
    system.append("Initial intention:\n" + json.dumps(continuity["initial_intention"], ensure_ascii=False, sort_keys=True))   # immutable: stable
    # the current message is in the history when the projection holds it: the news then only designates it (who and what), never repeats it
    shown_ids = {m.get("id") for m in projection.get("messages") or []}
    designated = {k: current[k] for k in ("id", "kind", "from") if k in current} if current and current.get("id") in shown_ids else current
    news: Dict[str, Any] = {"continuity_id": continuity["id"], "current_intention": continuity["current_intention"], "current_message": designated}
    if stimulus is not None:
        news["active_stimulus"] = {k: v for k, v in stimulus.items() if k != "projection_view"}
    if mcp_unavailable:
        # the MCP servers of the pilot that could not be prepared now, and why: the model is told, so it neither invents a capacity for
        # them nor promises what it cannot do (authentication_required: an operator must log in; nothing the model can do about it)
        news["mcp_unavailable"] = [{"server": u.get("server"), "reason": u.get("reason")} for u in mcp_unavailable]
    if user_messages:                                  # separate from the stimulus, which may be pending at the same time
        block = {"message_ids": list(user_messages)}
        validate_value("user_message", block)
        news["user_message"] = block
    history = [dict(m) for m in history]
    if history:
        history[-1]["cache"] = True                                       # the end of the history: the provider reads the cache up to here next time
    # the system text is stable across inferences: it ends a prefix worth caching
    messages = [{"role": "system", "content": "\n\n".join(system), "cache": True}] + history \
        + [{"role": "user", "content": json.dumps(news, ensure_ascii=False)}]
    if correction is not None:
        messages.append({"role": "user", "content": json.dumps({"validation_error": correction}, ensure_ascii=False)})
    request: Dict[str, Any] = {
        "provider": provider, "model": model, "messages": messages,
        # for whoever observes the request (no provider reads it): who asked, and what the view of the journal was
        "metadata": {"continuity_id": continuity.get("id"), "message_id": (current or {}).get("id"), "current_message": current,
                     "projection_ids": [m.get("id") for m in projection.get("messages") or []], "projection_definition": projection.get("definition")},
        "parameters": pilot.get("parameters") or {},
        "requirements": {"provider": provider, "model": model},
        # a strict-compatible projection of pilot_output: accepted by every provider adapter
        "output": {"format": "json_schema", "schema": provider_schema("pilot_output", style=style, describe_depth=0)},
    }
    if tools:                       # the native MCP tools of a pilot that has some, in the gateway's fixed order (a stable cache prefix)
        request["tools"] = tools
        if tool_choice:
            request["tool_choice"] = tool_choice
    return request


def _window_of(ctx: Ctx) -> Optional[int]:
    """The context window the pilot is given (limits.context_tokens of its pilot_ref), when there is one."""
    for source in (ctx.pilot_ref, ctx.capacity_ref, ctx.pilot):
        value = ((source or {}).get("limits") or {}).get("context_tokens")
        if isinstance(value, int) and not isinstance(value, bool) and value > 0:
            return value
    return None


async def _infer(ctx: Ctx, request: Dict[str, Any]) -> Any:
    """#extract_pilot_output: the structured answer of the provider, back to the canonical pilot_output."""
    payload = await ctx.port.call_capacity("/root/llm/infer", {"request": request, "provider": request.get("provider")})
    reported = ((payload.get("response") or {}).get("usage") or {}).get("input_tokens")
    # the provider reports the exact size of the context it received; an estimation stands in until it does
    ctx.measured_estimated = not (isinstance(reported, int) and reported > 0)
    measured = [request["messages"], request["tools"]] if request.get("tools") else request["messages"]      # the tools are part of the context
    ctx.measured_tokens = estimate_tokens(measured) if ctx.measured_estimated else reported
    response = payload.get("response") or {}
    ctx.last_finish = response.get("finish_reason") if isinstance(response.get("finish_reason"), str) else None
    ctx.measured_detail = {} if ctx.measured_estimated else usage_detail(response.get("usage") or {}, response, _window_of(ctx))
    output = response.get("output") or {}
    ctx.last_tool_output = output if output.get("tool_calls") else None      # a native tool turn: no pilot_output to read
    content = output.get("content")
    if isinstance(content, str):
        content = extract_json(content)
    if isinstance(content, dict):
        content = decode_provider_output("pilot_output", content)
    return content


async def _used_intention_ids(ctx: Ctx) -> set:
    return set((await continuity_store.get(ctx.port, ctx.cid)).intention_ids)


SUCCESSION_EXCLUSIVE = ("intentions", "wait_group", "messages", "projection_request")


def _succession_alone(output: Dict[str, Any], repairs: Optional[List[Dict[str, Any]]]) -> Dict[str, Any]:
    """A succession stands alone. A model that writes one next to other fields has decided one of two things, and the code knows which:
    with a summary it decided to succeed, and nothing was executed yet, so the rest is set aside (the successor takes over the message in progress
    and its summary says what remains); without a summary it meant to work, and the succession is set aside."""
    succession = output.get("succession")
    others = [k for k in SUCCESSION_EXCLUSIVE if output.get(k)]
    if succession is None or not others:
        return output
    out = {k: v for k, v in output.items() if k not in SUCCESSION_EXCLUSIVE and k != "succession"}
    summary = succession.get("summary") if isinstance(succession, dict) else None
    if isinstance(summary, (str, dict)) and summary:
        out["succession"] = succession
        if repairs is not None:
            repairs.append({"kind": "succession_alone", "kept": "succession", "dropped": others})
    else:
        for key in others:
            out[key] = output[key]
        if repairs is not None:
            repairs.append({"kind": "succession_alone", "kept": "work", "dropped": ["succession"]})
    return out


def normalize_output(pilot: Dict[str, Any], output: Any, used_ids: Optional[set] = None, repairs: Optional[List[Dict[str, Any]]] = None) -> Any:
    """What is mechanical is the code's, not the model's. Identifiers and families are written here:

    - an intention without an identifier gets one; an identifier already used (in this continuity or earlier in this output) is renamed, and the
      references to it in the wait group follow;
    - the family of an intention is set when the code knows it for certain (a declared or an engine capacity), whatever the model wrote or omitted.
    The decision the journal keeps is the normalized one: the model reads back the identifiers its answers will carry.
    Each repair is appended to `repairs` (when given), so that what the model got wrong can be detected and counted."""
    if not isinstance(output, dict):
        return output
    output = _succession_alone(output, repairs)
    if not isinstance(output.get("intentions"), list):
        group = output.get("wait_group")
        if isinstance(group, dict) and not group.get("expected"):          # no intention, nothing listed: a wait group over nothing is no wait at all
            output = {k: v for k, v in output.items() if k != "wait_group"}
            if repairs is not None:
                repairs.append({"kind": "empty_wait_group_dropped"})
        return output
    out = copy.deepcopy(output)
    taken = set(used_ids or ())
    renamed: Dict[str, str] = {}
    for intention in out["intentions"]:                      # first the identifiers the model wrote: a taken one is renamed
        if isinstance(intention, dict) and isinstance(intention.get("id"), str) and intention["id"].strip():
            original = intention["id"]
            if original in taken:
                suffix = 2
                while f"{original}_{suffix}" in taken:
                    suffix += 1
                intention["id"] = f"{original}_{suffix}"
                renamed.setdefault(original, intention["id"])
                if repairs is not None:
                    repairs.append({"kind": "id_renamed", "from": original, "to": intention["id"]})
            taken.add(intention["id"])
    counter = 0
    for intention in out["intentions"]:                      # then the missing ones, in what is left
        if isinstance(intention, dict) and not (isinstance(intention.get("id"), str) and intention["id"].strip()):
            counter += 1
            while f"i{counter}" in taken:
                counter += 1
            intention["id"] = f"i{counter}"
            taken.add(intention["id"])
    group = out.get("wait_group")
    if isinstance(group, dict) and isinstance(group.get("expected"), list):
        ids = {i.get("id") for i in out["intentions"] if isinstance(i, dict)}
        # A pilot launched with a task answers its launch with its result (document/capacity_pilot_launch.md): waiting for "its continuity" is waiting for
        # that intention. A model that lists the continuity instead would wait for a message that never comes (and for ever if the launch failed).
        launches = {(i.get("parameters") or {}).get("continuity_id"): i for i in out["intentions"]
                    if isinstance(i, dict) and str(i.get("target_capacity", "")).endswith("/pilot.launch") and isinstance(i.get("parameters"), dict) and _launch_waits(i["parameters"])}
        kept = []
        for item in group["expected"]:
            if isinstance(item, dict) and item.get("type") == "continuity" and item.get("ref") in launches:
                launch = launches[item["ref"]]
                if repairs is not None:
                    repairs.append({"kind": "continuity_wait_is_the_launch", "continuity": item["ref"], "intention": launch.get("id")})
                launch["await_response"] = True                   # its answer is what the group waits for; the item itself is dropped (the default covers it)
                continue
            if isinstance(item, dict) and item.get("type", "intention") == "intention":
                ref = renamed.get(item.get("ref"), item.get("ref"))
                if ref not in ids:
                    if repairs is not None:
                        repairs.append({"kind": "expected_dropped", "ref": item.get("ref")})
                    continue                                      # it designates no intention of this output: the model cannot know their identifiers
                item = dict(item, ref=ref)
            kept.append(item)
        group["expected"] = kept                                  # none left for intentions: validation applies the default, every awaited intention
        if not kept and not any(isinstance(i, dict) and i.get("await_response") is True for i in out["intentions"]):
            out.pop("wait_group", None)                           # nothing to wait for (a pilot that only speaks): a wait group over nothing is no wait at all
            if repairs is not None:
                repairs.append({"kind": "empty_wait_group_dropped"})
    known = {c.get("ref"): c.get("target_type") or "user" for c in pilot.get("known_capacities") or []}
    for intention in out["intentions"]:
        if isinstance(intention, dict) and isinstance(intention.get("target_capacity"), str):
            certain = family_of(intention["target_capacity"], known)
            if certain:
                written = intention.get("target_type")
                if repairs is not None and written is not None and written != certain:
                    repairs.append({"kind": "family_corrected", "capacity": intention["target_capacity"], "wrote": written, "set": certain})
                intention["target_type"] = certain
        if isinstance(intention, dict) and not intention.get("target_type"):
            intention["target_type"] = "user"                      # a capacity the code does not know is the architect's: user
    return out


def _launch_waits(params: Dict[str, Any]) -> bool:
    """Does a pilot.launch with these parameters wait for the task and answer with its result? (the rule of pilot_launch._waits)"""
    wait = params.get("wait")
    if isinstance(wait, bool):
        return wait and params.get("message") is not None
    return bool(params.get("parent_id")) and params.get("message") is not None


def declare_families(pilot: Dict[str, Any], output: Dict[str, Any]) -> Dict[str, Any]:
    """An intention aimed at a capacity that the pilot_definition lists is routed with the family the definition declares (user by default),
    whatever the model wrote: the architect knows it, the model guesses it."""
    families = {c.get("ref"): c.get("target_type") or "user" for c in pilot.get("known_capacities") or []}
    for intention in output.get("intentions") or []:
        if isinstance(intention, dict) and intention.get("target_capacity") in families:
            intention["target_type"] = families[intention["target_capacity"]]
    return output


def validate_output(output: Any, used_intention_ids: Optional[set] = None) -> Dict[str, Any]:
    """Complete validation before any effect. Returns the normalized output or raises InvalidRequest."""
    if not isinstance(output, dict):
        raise InvalidRequest("the pilot output is not a structured object")
    out = copy.deepcopy(output)
    if not out:
        raise InvalidRequest("an empty pilot output is invalid: answer with at least one message to the person, for example "
                             '{"messages": [{"to": "user:1", "kind": "user", "content": "<what you say>"}]}, or with an intention, or a succession')
    errors: List[str] = []
    intentions = out.get("intentions") or []
    by_id = {}
    for it in intentions:
        if isinstance(it, dict):
            if it.get("id") in by_id:
                errors.append(f"duplicate intention id {it.get('id')!r}")
            if it.get("id") in (used_intention_ids or ()):
                errors.append(f"intention id {it.get('id')!r} was already used in this continuity; choose a new id")
            by_id[it.get("id")] = it
    if out.get("projection_request") and (intentions or out.get("wait_group") or out.get("messages")):
        errors.append("projection_request is exclusive with intentions, wait_group and messages")
    succession = out.get("succession")
    if succession is not None:
        summary = succession.get("summary") if isinstance(succession, dict) else None
        if not (isinstance(summary, (str, dict)) and summary):
            errors.append("succession needs a non-empty summary")
        if intentions or out.get("wait_group") or out.get("messages") or out.get("projection_request"):
            errors.append("succession is exclusive with intentions, wait_group, messages and projection_request")
    group = out.get("wait_group")
    if isinstance(group, dict):
        group.setdefault("id", new_id("wg"))
        group.setdefault("received", [])
        group.setdefault("status", "waiting")
        if not group.get("expected"):
            # by default a wait group waits for what the same output awaits: the model need not list the ids it already wrote
            group["expected"] = [{"type": "intention", "ref": it["id"]} for it in intentions if isinstance(it, dict) and it.get("await_response") is True]
        if group.get("condition") == "n_of_m" and not isinstance(group.get("n"), int):
            errors.append("n_of_m needs an integer n")
        if group.get("condition") == "n_of_m" and isinstance(group.get("n"), int) and group["n"] > len(group.get("expected") or []):
            errors.append("n exceeds the number of expected items")
        if not group.get("expected"):
            errors.append("a wait_group needs at least one expected item")
        for item in group.get("expected") or []:
            if not isinstance(item, dict):
                continue
            if item.get("type") == "intention":
                target = by_id.get(item.get("ref"))
                if target is None:
                    errors.append(f"expected intention {item.get('ref')!r} is absent from the same output")
                elif target.get("await_response") is not True:
                    errors.append(f"expected intention {item.get('ref')!r} has await_response false")
                item.setdefault("correlation", {"intention_id": item.get("ref")})
            elif item.get("type") == "continuity" and not item.get("correlation"):
                item["correlation"] = {"continuity_id": item.get("ref")}    # a delegated continuity answers under its lineage id
            elif not item.get("correlation"):
                errors.append(f"expected {item.get('ref')!r} needs a correlation")
    for it in intentions:
        if isinstance(it, dict):
            if not isinstance(it.get("await_response"), bool):
                errors.append(f"intention {it.get('id')!r}: await_response must be a boolean")
            if not str(it.get("target_capacity", "")).startswith("/"):
                errors.append(f"intention {it.get('id')!r}: target_capacity must be an absolute reference")
            if it.get("target_type", "user") not in ("user", "matter", "reflexive", "execution", "control"):
                errors.append(f"intention {it.get('id')!r}: unknown target_type")
    try:
        validate_value("pilot_output", out)
    except InvalidRequest as exc:
        errors.extend(exc.details.get("errors", [exc.message]))
    if errors:
        raise InvalidRequest("invalid pilot output: " + "; ".join(errors[:8]), details={"errors": errors})
    return out


async def _record_decision(ctx: Ctx, message_id: Optional[str], said: Any) -> None:
    """The decision of the pilot, as it wrote it, is a line of the journal before any of its effects: it is what the model reads back of itself."""
    provider, model = _model_of(ctx.pilot)
    await continuity_store.append(ctx.port, ctx.cid, [{
        "kind": "completion", "id": new_id("decision"), "content": {"step": "decision", "output": said},
        "from": f"model:{provider}/{model}", "to": f"continuity:{ctx.cid}", "correlation": {"causation_id": message_id} if message_id else {}}])


async def _trace_repairs(ctx: Ctx, repairs: List[Dict[str, Any]]) -> None:
    """What the code had to write or correct is a fact of the journal: it measures what the model gets wrong, and says where to improve what it is given."""
    if repairs:
        await append_trace(ctx.port, ctx.cid, "event", {"event": "output_repaired", "repairs": repairs})


def _what_the_model_wrote(ctx: Ctx, output: Any) -> Dict[str, Any]:
    """The invalid output as the model wrote it (bounded) and why the provider stopped: without them an invalid answer cannot be diagnosed afterwards."""
    text = output if isinstance(output, str) else json.dumps(output, ensure_ascii=False, default=str)
    return {"output": text[:2000], **({"finish_reason": ctx.last_finish} if ctx.last_finish else {})}


async def _infer_valid(ctx: Ctx, request: Dict[str, Any], used_ids: Optional[set] = None):
    """(the validated output, the output as the model wrote it with its free values decoded)."""
    repairs: List[Dict[str, Any]] = []
    first = normalize_output(ctx.pilot, await _infer(ctx, request), used_ids, repairs)
    if ctx.last_tool_output is not None:
        return mcp.TOOL_TURN, ctx.last_tool_output          # the model called tools: a decision of its own kind, never validated as a pilot_output
    await _trace_repairs(ctx, repairs)
    try:
        return validate_output(first, used_ids), first
    except InvalidRequest as exc:
        await append_trace(ctx.port, ctx.cid, "error", {"event": "validation_error", "errors": exc.details.get("errors", [exc.message]),
                                                        **_what_the_model_wrote(ctx, first)})
        corrected_request = copy.deepcopy(request)
        corrected_request["messages"].append({"role": "assistant", "content": json.dumps(first, ensure_ascii=False) if not isinstance(first, str) else first})
        corrected_request["messages"].append({"role": "user", "content": json.dumps({"validation_error": exc.details.get("errors", [exc.message])}, ensure_ascii=False)})
        repairs = []
        second = normalize_output(ctx.pilot, await _infer(ctx, corrected_request), used_ids, repairs)
        if ctx.last_tool_output is not None:
            return mcp.TOOL_TURN, ctx.last_tool_output
        await _trace_repairs(ctx, repairs)
        try:
            return validate_output(second, used_ids), second
        except InvalidRequest as exc2:
            raise InvalidRequest("the pilot output stays invalid after its single correction: " + exc2.message, details=exc2.details) from exc2


# =============================================================================
def pilot_reference(pilot_ref: Any) -> Optional[str]:
    """A pilot_ref read as a reference the policy can judge: <context>/<capacity> for a Capacity pilot, <context>/<matter_id> for a Matter pilot."""
    if not isinstance(pilot_ref, dict) or not isinstance(pilot_ref.get("context"), str):
        return None
    name = pilot_ref.get("capacity") if pilot_refs.is_capacity(pilot_ref) else pilot_ref.get("matter_id")
    return f"{pilot_ref['context'].rstrip('/')}/{name}" if isinstance(name, str) and name else None


def _refusal_of(intention: Dict[str, Any], allowed, forbidden, allowed_contexts, forbidden_contexts) -> Optional[str]:
    """Why the policy refuses an intention: its target, and for a launch the pilot it names too."""
    target = intention.get("target_capacity")
    if isinstance(target, str) and target.startswith(mcp.PROTECTED_PREFIX):
        return "the MCP namespace is reached through native tools only, never by an intention"
    why = refusal(allowed, forbidden, target, allowed_contexts, forbidden_contexts)
    if why or not (isinstance(target, str) and target.endswith("/pilot.launch")):
        return why
    ref = pilot_reference((intention.get("parameters") or {}).get("pilot_ref"))
    if ref is None:
        return None                      # malformed: pilot.launch refuses it before any effect
    reason = refusal(allowed, forbidden, ref, allowed_contexts, forbidden_contexts)
    return f"the pilot to launch {ref}: {reason}" if reason else None


async def _apply(ctx: Ctx, continuity: Dict[str, Any], output: Dict[str, Any]) -> Dict[str, Any]:
    """Order: intention_update, projection_request, wait_group, intentions, messages."""
    port, cid, ids = ctx.port, ctx.cid, ctx.ids
    succeeded: List[str] = []

    # --- #check_policy: the whole output is refused, with no effect at all, when one intention addresses what the pilot may not ---------
    allowed, forbidden, allowed_contexts, forbidden_contexts = policy_of(ctx.pilot, ctx.capacity_ref)
    intentions_asked = [i for i in output.get("intentions") or [] if isinstance(i, dict)]
    refused = [(i, why) for i in intentions_asked for why in [_refusal_of(i, allowed, forbidden, allowed_contexts, forbidden_contexts)] if why]
    if refused:
        failed_id = refused[0][0]["id"]
        result = {"succeeded": [], "failed": f"intention:{failed_id}",
                  "error": "refused by the pilot policy before any effect: " + "; ".join(f"intention:{i['id']} {i.get('target_capacity')} ({why})" for i, why in refused),
                  "remaining": [f"intention:{i['id']}" for i in intentions_asked if i["id"] != failed_id] + [f"message:{n}" for n in range(len(output.get("messages") or []))]}
        validate_value("execution_result", result)
        await append_trace(port, cid, "error", {"event": "intentions_refused", **result})
        ctx.refusals += 1
        if ctx.refusals >= MAX_REFUSALS:
            raise AgenticError(f"the pilot asked {ctx.refusals} times in a row for what its policy refuses")
        return {"state": "again", "stimulus": dict(result, kind="execution_result")}
    ctx.refusals = 0

    if output.get("intention_update") is not None:
        from substrate.controls import set_intention
        await set_intention(port, cid, ids, output["intention_update"])
        await append_trace(port, cid, "event", {"event": "intention_update", "intention": output["intention_update"]})
        succeeded.append("intention_update")

    succession = output.get("succession")
    if succession:
        return await _decide_succession(ctx, succession)

    request = output.get("projection_request")
    if request:
        await append_trace(port, cid, "projection", {"event": "projection_request", "request": request})
        view = await _project(ctx, request["projection"], request.get("mode", "merge"), request.get("budget"))
        await append_trace(port, cid, "projection", {"event": "projection_result", "selected_message_ids": view["selected_message_ids"],
                                                       "omitted_count": view["omitted_count"]})
        return {"state": "again", "stimulus": {"kind": "projection", "projection_view": view, "selected_message_ids": view["selected_message_ids"]}}

    group = output.get("wait_group")
    if group:
        await _create_wait_group(ctx, group)
        succeeded.append(f"wait_group:{group['id']}")

    intentions = output.get("intentions") or []
    for index, intention in enumerate(intentions):
        try:
            await _emit(ctx, intention)
            succeeded.append(f"intention:{intention['id']}")
        except Exception as exc:  # an effect failed: stop, keep earlier effects, hand the real state back to cognition
            remaining = [f"intention:{i['id']}" for i in intentions[index + 1:]] + [f"message:{n}" for n in range(len(output.get("messages") or []))]
            result = {"succeeded": succeeded, "failed": f"intention:{intention['id']}", "error": str(exc), "remaining": remaining}
            validate_value("execution_result", result)
            if group:
                await _complete_group(ctx, group["id"])     # the failed decision's wait is void: cognition re-decides
            await append_trace(port, cid, "error", {"event": "execution_error", **result})
            return {"state": "again", "stimulus": dict(result, kind="execution_result")}

    messages = output.get("messages") or []
    rt_before = (await port.matter_read(ids.runtime)).content
    final_step = not intentions and not group and not any(g["status"] in ("waiting", "satisfied") for g in rt_before["active_wait_groups"])
    current_message_id = rt_before.get("current_message_id")
    for index, pilot_message in enumerate(messages):
        try:
            await _send_message(ctx, pilot_message, current_message_id, ends_chain=final_step and index == len(messages) - 1)
            succeeded.append(f"message:{index}")
        except Exception as exc:
            result = {"succeeded": succeeded, "failed": f"message:{index}", "error": str(exc),
                      "remaining": [f"message:{n}" for n in range(index + 1, len(messages))]}
            await append_trace(port, cid, "error", {"event": "execution_error", **result})
            if group:
                await _complete_group(ctx, group["id"])
            return {"state": "again", "stimulus": dict(result, kind="execution_result")}

    rt = (await port.matter_read(ids.runtime)).content
    if any(g["status"] in ("waiting", "satisfied") for g in rt["active_wait_groups"]):
        return {"state": "waiting", "stimulus": None}
    return {"state": "complete", "stimulus": None}


async def _create_wait_group(ctx: Ctx, group: Dict[str, Any]) -> None:
    """Materialized BEFORE any awaited intention is invoked, so no Response can precede its wait."""
    def compute(f):
        if f.get("run_id") != ctx.run_id:
            raise Refused("the run no longer owns the continuity")
        if any(g["id"] == group["id"] for g in f["active_wait_groups"]):
            return None, None
        f["active_wait_groups"].append(group)
        return f, None
    await update_matter(ctx.port, ctx.ids.runtime, compute, continuity_id=ctx.cid, schema="runtime")
    await append_trace(ctx.port, ctx.cid, "event", {"event": "wait_group_created", "wait_group": group})


async def _complete_group(ctx: Ctx, group_id: str, *, consume: Optional[List[Dict[str, Any]]] = None) -> None:
    for item in consume or []:       # the captured results were consumed by the resumed inference
        try:
            await ctx.port.call_capacity(f"{CONTEXT}/mailbox.ack", {"continuity_id": ctx.cid, "message_id": item["message_id"], "run_id": ctx.run_id})
        except (NotFound, Refused):
            pass                     # already acknowledged
    def compute(f):
        remaining = [g for g in f["active_wait_groups"] if g["id"] != group_id]
        if len(remaining) == len(f["active_wait_groups"]):
            return None, None
        f["active_wait_groups"] = remaining
        return f, None
    await update_matter(ctx.port, ctx.ids.runtime, compute, continuity_id=ctx.cid, schema="runtime")
    await append_trace(ctx.port, ctx.cid, "event", {"event": "wait_group_completed", "wait_group_id": group_id})


async def _emit(ctx: Ctx, intention: Dict[str, Any]) -> None:
    to_type = intention.get("target_type") or "user"
    target = intention.get("target_capacity") or ""
    if (intention.get("await_response") and target.endswith("/pilot.activate")
            and (intention.get("parameters") or {}).get("continuity_id") == ctx.cid and not (intention.get("parameters") or {}).get("wake")):
        raise InvalidRequest("a continuity cannot wait for its own activation")      # it would wait for the run that is asking
    await append_trace(ctx.port, ctx.cid, "capacity_intention",
                       {"id": intention["id"], "target_capacity": intention["target_capacity"],
                        "parameters": intention.get("journal_parameters", intention.get("parameters")),
                        "await_response": intention["await_response"]},
                       to=intention["target_capacity"], correlation={"intention_id": intention["id"]}, message_id=f"intention_{ctx.cid}_{intention['id']}")
    if not intention["await_response"]:
        await ctx.port.fire_and_forget(intention["target_capacity"], intention.get("parameters") or {}, to_type=to_type)
        return
    _inflight.add((ctx.cid, intention["id"]))
    task = asyncio.ensure_future(_awaited(ctx, intention, to_type))
    _background.add(task)
    task.add_done_callback(_background.discard)


async def _awaited(ctx: Ctx, intention: Dict[str, Any], to_type: str) -> None:
    """Invoke and wait for the Response, then deliver it as a capacity_response message.

    It runs beside the cognitive loop: the pilot is not blocked by slow members of a wait group,
    and late results of an already satisfied group enter the history without reopening it.
    """
    inference_window.forget_role()       # the task inherited the identity of the loop: the answer of a tool is an outside writer
    try:
        response = await ctx.port.call_capacity_response(intention["target_capacity"], intention.get("parameters") or {},
                                                         to_type=to_type, timeout_s=AWAIT_TIMEOUT_S)
    except Exception as exc:
        response = {"status": "error", "error": {"origin": "wrapper", "code": "internal", "message": f"no Response obtained: {exc}"}}
    finally:
        _inflight.discard((ctx.cid, intention["id"]))
    if intention["target_capacity"] == mcp.CALL_CAPACITY and response.get("status") != "ok":
        # the gateway answers every call it took with a state: an error answer (a lost connection, a timeout, a refusal of the engine) says nothing
        # of the call itself, so the gateway is asked; what it cannot show terminal stays an error answer, read as an unknown outcome
        response = await _mcp_status_answer(ctx, intention["id"]) or response
    message = build_response_message(ctx.cid, intention, response)         # written as is in the journal, whatever its size
    last: Optional[Exception] = None
    for attempt in range(DELIVERY_ATTEMPTS):
        try:
            await ctx.port.call_capacity(f"{CONTEXT}/mailbox.send", {"continuity_id": ctx.cid, "message": message})
            return
        except Refused:
            return           # the same Response was already delivered (idempotent retry)
        except (NotFound, ConsistencyError):
            return           # the continuity was deleted meanwhile: a late result never recreates anything
        except (AgenticError, asyncio.TimeoutError) as exc:      # transient (a conflict, a timeout) or not: delivery is idempotent, so ask again
            last = exc
            await asyncio.sleep(DELIVERY_BACKOFF_S * (2 ** attempt))
    try:                     # no one reads a background task: the loss is written where the continuity can see it
        await append_trace(ctx.port, ctx.cid, "error", {"event": "response_delivery_failed", "intention_id": intention["id"], "error": str(last)})
    except Exception:
        pass


def build_response_message(cid: str, intention: Dict[str, Any], response: Dict[str, Any]) -> Dict[str, Any]:
    """The canonical Message of a Response: never send the raw Response to mailbox.send."""
    return {
        "id": f"response_{cid}_{intention['id']}",        # deterministic: a repeated delivery is an idempotent retry
        "from": intention["target_capacity"],
        "to": cid,
        "kind": "capacity_response",
        "content": {"response": response},
        "correlation": {"intention_id": intention["id"]},
    }


async def _send_message(ctx: Ctx, pilot_message: Dict[str, Any], current_message_id: Optional[str] = None, ends_chain: bool = False) -> None:
    kind, to = pilot_message["kind"], pilot_message["to"]
    correlation = dict(pilot_message.get("correlation") or {})
    out_id = new_id("out")
    # The line of the journal says which message it answers, and (the last message of a step that ends the chain) that the treatment is complete: what a
    # takeover reads to know that the message must not be answered again. What goes to another continuity keeps the correlation the model gave.
    line_correlation = dict(correlation, causation_id=current_message_id) if current_message_id else correlation
    content = {"event": "message_out", "kind": kind, "content": pilot_message["content"], **({"ends_chain": True} if ends_chain else {})}
    await append_trace(ctx.port, ctx.cid, "pilot", content, to=str(to), correlation=line_correlation, message_id=out_id)
    if kind == "continuity":
        target = check_continuity_id(to)
        correlation.setdefault("continuity_id", ctx.lineage_id)      # the id the delegating parent knows, whatever the generation
        await ctx.port.call_capacity(f"{CONTEXT}/mailbox.send", {"continuity_id": target, "message": {
            "id": out_id, "from": f"continuity:{ctx.cid}", "to": target, "kind": "continuity",
            "content": pilot_message["content"], "correlation": correlation}})


# =============================================================================
# MCP tools of a Matter pilot (substrate/mcp.py)
async def _mcp_tools(ctx: Ctx) -> Optional[List[Dict[str, Any]]]:
    """The native tools offered to this inference: the gateway's projection for the pilot's servers, asked once per run and kept for it (the
    same tools in the same order keep the provider's cache prefix). None for a pilot without an mcp section. An optional MCP that cannot be
    prepared leaves the pilot without tools; a mandatory one fails the treatment."""
    section = mcp.section(ctx.pilot)
    if section is None or ctx.capacity_ref is not None:
        return None
    now = asyncio.get_running_loop().time()
    if ctx.mcp_projection is not None and now - ctx.mcp_projection["at"] < mcp.PROJECTION_REFRESH_S:
        return ctx.mcp_projection["tools"] or None
    if ctx.mcp_projection is not None and mcp.tool_turn_open((await ctx.port.matter_read(ctx.ids.runtime)).content["active_wait_groups"]):
        return ctx.mcp_projection["tools"] or None          # never a new projection while a tool turn waits for its results
    try:
        mcp.require_key()                                      # no key: no tool is offered, rather than tools that can never be called
        payload = await ctx.port.call_capacity(mcp.TOOLS_CAPACITY, {"servers": list(section.get("servers") or []),
                                                                    "mandatory": list(section.get("mandatory") or [])})
    except (mcp.McpUnavailable, AgenticError, asyncio.TimeoutError) as exc:
        await append_trace(ctx.port, ctx.cid, "error", {"event": "mcp_unavailable", "error": str(exc)})
        if section.get("mandatory"):
            raise AgenticError(f"a mandatory MCP server cannot be prepared: {exc}") from exc
        ctx.mcp_projection = {"projection_id": None, "tools": [], "at": now,
                              "unavailable": [{"server": alias, "reason": "gateway_unavailable"} for alias in section.get("servers") or []]}
        return None
    tools = [{"name": t["model_name"], "description": t.get("description") or "", "input_schema": t["input_schema"]} for t in payload.get("tools") or []]
    ctx.mcp_projection = {"projection_id": payload.get("projection_id"), "tools": tools, "at": now, "unavailable": payload.get("unavailable") or []}
    await append_trace(ctx.port, ctx.cid, "event", {"event": "mcp_projection", "projection_id": payload.get("projection_id"),
                                                     "registry_version": payload.get("registry_version"), "tools": [t["name"] for t in tools],
                                                     "unavailable": payload.get("unavailable") or []})
    return tools or None


async def _record_tool_turn(ctx: Ctx, message_id: Optional[str], output: Dict[str, Any]) -> Dict[str, Any]:
    """The tool turn of the model is its decision line, written before any call: the exact assistant turn (sent back unchanged at the next
    inference) and, for each call, the call_id it is executed under."""
    calls = [{"id": c.get("id"), "call_id": mcp.call_id(ctx.cid, str(c.get("id"))), "name": c.get("name"), "arguments": c.get("arguments")}
             for c in output.get("tool_calls") or []]
    projection_id = (ctx.mcp_projection or {}).get("projection_id")
    provider, model = _model_of(ctx.pilot)
    await continuity_store.append(ctx.port, ctx.cid, [{
        "kind": "completion", "id": new_id("decision"),
        "content": {"step": mcp.TOOL_TURN, "assistant_content": output.get("assistant_content") or [], "projection_id": projection_id,
                    "calls": [{k: c[k] for k in ("id", "call_id", "name")} for c in calls]},
        "from": f"model:{provider}/{model}", "to": f"continuity:{ctx.cid}", "correlation": {"causation_id": message_id} if message_id else {}}])
    return {"calls": calls, "projection_id": projection_id}


async def _apply_mcp(ctx: Ctx, turn: Dict[str, Any]) -> Dict[str, Any]:
    """The calls of a tool turn: the whole batch is checked first; then one wait group over all of them, created before the first is sent,
    and one awaited intention to the gateway per call. A call that is not sent gets an explicit answer, so the turn always has one result
    per call and the run resumes when all are there."""
    calls = turn["calls"]
    offered = {t["name"] for t in (ctx.mcp_projection or {}).get("tools") or []}
    why = mcp.check_batch(calls, offered, mcp.max_calls(ctx.pilot))
    envelopes: Dict[str, str] = {}
    if why is None:
        pilot = pilot_reference(ctx.pilot_ref) or ""
        try:
            for c in calls:
                envelopes[c["call_id"]] = mcp.envelope(ctx.cid, c["call_id"], turn["projection_id"], c["name"], mcp.arguments_text(c["arguments"]), pilot)
        except mcp.McpUnavailable as exc:
            why = str(exc)
    group = {"id": new_id("wg_mcp"), "condition": "all", "received": [], "status": "waiting",
             "expected": [{"type": "intention", "ref": c["call_id"], "correlation": {"intention_id": c["call_id"]}} for c in calls]}
    await _create_wait_group(ctx, group)
    if why is not None:
        await append_trace(ctx.port, ctx.cid, "error", {"event": "mcp_calls_not_sent", "reason": why, "call_ids": [c["call_id"] for c in calls]})
        for c in calls:
            await _deliver(ctx, c["call_id"], mcp.not_executed(c["call_id"], "not_sent: " + why))
        return {"state": "waiting", "stimulus": None}
    for index, c in enumerate(calls):
        args_text = mcp.arguments_text(c["arguments"])
        intention = {"id": c["call_id"], "target_capacity": mcp.CALL_CAPACITY, "target_type": "user", "await_response": True,
                     "parameters": {"call_id": c["call_id"], "origin": "agentic", "envelope": envelopes[c["call_id"]],
                                    "projection_id": turn["projection_id"], "tool": c["name"], "arguments_json": args_text},
                     # the journal keeps what was called, never the envelope; the arguments are in the decision line already
                     "journal_parameters": {"call_id": c["call_id"], "tool": c["name"], "projection_id": turn["projection_id"]}}
        try:
            await _emit(ctx, intention)
        except Exception as exc:       # nothing of this call and of those after it was sent: each is answered as such
            for rest in calls[index:]:
                await _deliver(ctx, rest["call_id"], mcp.not_executed(rest["call_id"], f"not_sent: {exc}"))
            break
    return {"state": "waiting", "stimulus": None}


async def _deliver(ctx: Ctx, call: str, response: Dict[str, Any]) -> None:
    try:
        await ctx.port.call_capacity(f"{CONTEXT}/mailbox.send", {"continuity_id": ctx.cid, "message": build_response_message(
            ctx.cid, {"id": call, "target_capacity": mcp.CALL_CAPACITY}, response)})
    except (Refused, ConsistencyError):
        pass                 # already delivered under the same id


async def _mcp_status_answer(ctx: Ctx, call: str) -> Optional[Dict[str, Any]]:
    """The gateway's terminal state of a call, as the answer of its intention; None when the gateway cannot give one (unknown, still in progress,
    or not reachable): the call then stays unresolved, never presented as unsent."""
    try:
        status = await ctx.port.call_capacity(mcp.STATUS_CAPACITY, {"call_id": call})
    except Exception:
        return None
    if status.get("known") and status.get("state") in ("completed", "remote_error", "outcome_unknown", "failed_before_transmission"):
        return {"status": "ok", "payload": status}
    return None
