"""A refusal "busy" is transient: the wrapper library asks again (a bounded number of times, with growing pauses; its rules and their vectors
live in wrapper/vectors/busy_retry.json), the port only names what stays refused, and a wake is never lost in silence.

The real port is exercised over a runtime whose outbound calls follow a script, standing for what is left after the library gave up. See "Un refus « busy » est transitoire" in
agentic_core_concurrency_model.md.
"""
import asyncio
import unittest

from . import harness  # noqa: F401  (puts agentic_core on the path)
from .harness import covers
from substrate import port as port_module
from substrate.errors import BUSY_REASONS, BusyError, ConsistencyError, EngineError, Refused, RevisionMismatch, from_engine_error
from substrate.port import BriquePort
from wrapper.execution import current_delegation

BUSY = {"status": "error", "error": {"code": "refused", "message": "wrapper pending-work queue is full", "details": {"reason": "busy"}}}
LOOP_BUSY = {"status": "error", "error": {"code": "unavailable", "message": "matter loop busy", "details": {"reason": "loop busy"}}}
OK = {"status": "ok", "payload": {"done": True}}


class Outbound:
    def __init__(self, script):
        self.script, self.calls = list(script), []

    async def emit_and_wait(self, **kwargs):
        self.calls.append({**kwargs, "_delegation_in_force": current_delegation.get()})
        item = self.script.pop(0) if self.script else OK
        if isinstance(item, BaseException):
            raise item
        return {"response": item}


class Runtime:
    def __init__(self, script):
        self.outbound = Outbound(script)
        self.transit = None
        self.calls = None
        self.scheduler = None


def port_with(script):
    return BriquePort(Runtime(script), "test"), None


class BusyIsAnErrorOfItsOwn(unittest.TestCase):
    def test_both_forms_of_refusal_are_recognised(self):
        for reason in BUSY_REASONS:
            error = from_engine_error({"code": "refused", "message": "x", "details": {"reason": reason}})
            self.assertIsInstance(error, BusyError)

    def test_busy_is_neither_a_refusal_of_substance_nor_an_engine_error(self):
        # mailbox.send and the delivery of responses read Refused as "already done": a busy callee must never look like that
        self.assertFalse(issubclass(BusyError, Refused))
        self.assertFalse(issubclass(BusyError, ConsistencyError))
        self.assertFalse(issubclass(BusyError, EngineError))


class CallsLeaveTheRetryToTheLibrary(unittest.IsolatedAsyncioTestCase):
    async def test_a_call_is_sent_once_and_leaves_the_attempts_to_the_library(self):
        port, _ = port_with([OK])
        self.assertEqual(await port._call("/root/agentic", "matter.read", "matter", {}, None), {"done": True})
        self.assertEqual(len(port.runtime.outbound.calls), 1)
        self.assertNotIn("busy_attempts", port.runtime.outbound.calls[0])      # BRIQUE_BUSY_ATTEMPTS decides

    async def test_a_call_that_stays_busy_fails_with_busy(self):
        for refusal in (BUSY, LOOP_BUSY):
            port, _ = port_with([refusal])
            with self.assertRaises(BusyError):
                await port._call("/root/agentic", "matter.read", "matter", {}, None)

    async def test_a_call_that_timed_out_is_never_asked_again(self):
        port, _ = port_with([asyncio.TimeoutError()])
        with self.assertRaises(asyncio.TimeoutError):
            await port._call("/root/agentic", "matter.read", "matter", {}, None)
        self.assertEqual(len(port.runtime.outbound.calls), 1)      # it may have run

    async def test_another_error_is_the_callers(self):
        mismatch = {"status": "error", "error": {"code": "refused", "message": "x", "details": {"reason": "revision_mismatch"}}}
        port, _ = port_with([mismatch])
        with self.assertRaises(RevisionMismatch):
            await port._call("/root/agentic", "matter.write", "matter", {}, None)

    async def test_a_lost_connection_is_an_unknown_outcome(self):
        """The engine answers connection_lost when a wrapper connection drops under an awaited call: the call may have been executed."""
        lost = {"status": "error", "error": {"code": "unavailable", "message": "delivery target is unavailable", "details": {"reason": "connection_lost"}}}
        self.assertNotIn("connection_lost", BUSY_REASONS)
        self.assertNotIsInstance(from_engine_error(lost["error"]), BusyError)
        port, _ = port_with([lost])
        with self.assertRaises(EngineError) as caught:
            await port._call("/root/agentic", "mailbox.send", "user", {}, None)
        self.assertEqual((caught.exception.code, caught.exception.reason), ("unavailable", "connection_lost"))

    async def test_the_whole_response_is_returned_not_raised_when_it_stays_busy(self):
        port, _ = port_with([BUSY])
        response = await port.call_capacity_response("/root/agentic/pilot.infer", {})
        self.assertEqual(response["error"]["details"]["reason"], "busy")      # the caller of this form reads the Response
        self.assertNotIn("busy_attempts", port.runtime.outbound.calls[0])
        await port.call_capacity_response("/root/agentic/pilot.infer", {}, busy_attempts=3)
        self.assertEqual(port.runtime.outbound.calls[1]["busy_attempts"], 3)


class WakesAreNotLost(unittest.IsolatedAsyncioTestCase):
    async def drain(self):
        await asyncio.gather(*list(port_module._WAKES))

    async def test_a_wake_that_is_taken_says_nothing_and_insists_longer_than_a_call(self):
        port, _ = port_with([OK])
        refused = []

        async def on_refused(error):
            refused.append(error)
        await port.wake("/root/agentic/pilot.activate", {"continuity_id": "c1", "wake": True}, on_refused=on_refused)
        await self.drain()
        self.assertEqual(refused, [])
        self.assertEqual(len(port.runtime.outbound.calls), 1)
        self.assertTrue(port.runtime.outbound.calls[0]["params"]["wake"])
        self.assertEqual(port.runtime.outbound.calls[0]["busy_attempts"], port_module.WAKE_ATTEMPTS)     # a wake insists longer than an interactive call
        self.assertGreater(port_module.WAKE_ATTEMPTS, 8)

    async def test_a_wake_that_stays_refused_is_reported(self):
        port, _ = port_with([BUSY])
        refused = []

        async def on_refused(error):
            refused.append(error)
        await port.wake("/root/agentic/pilot.activate", {"continuity_id": "c1", "wake": True}, on_refused=on_refused)
        await self.drain()
        self.assertEqual(len(refused), 1)
        self.assertEqual(refused[0]["details"]["reason"], "busy")

    async def test_a_wake_that_times_out_reports_an_unknown_outcome_without_retrying(self):
        port, _ = port_with([asyncio.TimeoutError()])
        refused = []

        async def on_refused(error):
            refused.append(error)
        await port.wake("/root/agentic/pilot.activate", {"continuity_id": "c1"}, on_refused=on_refused)
        await self.drain()
        self.assertEqual(len(refused), 1)
        self.assertEqual(refused[0]["details"], {"reason": "timeout", "outcome": "unknown"})
        self.assertEqual(len(port.runtime.outbound.calls), 1)      # it may have run: never replay it blindly

    async def test_the_request_returns_at_once_and_the_task_is_kept(self):
        gate = asyncio.Event()

        class Slow(Outbound):
            async def emit_and_wait(self, **kwargs):
                await gate.wait()
                return {"response": OK}
        runtime = Runtime([])
        runtime.outbound = Slow([])
        port = BriquePort(runtime, "test")
        await asyncio.wait_for(port.wake("/root/agentic/pilot.activate", {"continuity_id": "c1"}), 1)   # does not wait for the run
        self.assertEqual(len(port_module._WAKES), 1)
        gate.set()
        await self.drain()
        self.assertEqual(len(port_module._WAKES), 0)


class AWakeDoesNotCarryTheDelegationOfTheRequestThatCausedIt(unittest.IsolatedAsyncioTestCase):
    """A restricted caller (the browser, `local.ui`) is served under a delegation that the engine revokes when its request is answered. The wake
    outlives that request: it must go out with the wrapper's own authority, or it is refused and the pilot never runs. Callers with the local
    authority have no delegation, which is why this went unseen until the interface was used from the web."""

    async def test_the_wake_is_sent_without_the_delegation_and_the_request_keeps_its_own(self):
        port, _ = port_with([OK, OK])
        token = current_delegation.set("HANDLE-OF-THE-REQUEST")
        try:
            await port.call_capacity_response("/root/agentic/mailbox.read", {"continuity_id": "c1"})     # work done DURING the request: on behalf of the caller
            await port.wake("/root/agentic/pilot.activate", {"continuity_id": "c1", "wake": True})
            await asyncio.gather(*list(port_module._WAKES))
            self.assertEqual(current_delegation.get(), "HANDLE-OF-THE-REQUEST")                         # the request itself is untouched by the wake
        finally:
            current_delegation.reset(token)
        during, wake = port.runtime.outbound.calls
        self.assertEqual(during["_delegation_in_force"], "HANDLE-OF-THE-REQUEST")
        self.assertTrue(wake["params"]["wake"])
        self.assertEqual(wake["_delegation_in_force"], "")


if __name__ == "__main__":
    unittest.main()
