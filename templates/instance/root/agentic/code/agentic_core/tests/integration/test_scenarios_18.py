import unittest

from .driver import AGENTIC, TEST
from .scenarios_base import SKIP, SKIP_REASON, Scenario, say


class PilotVariant(Scenario):
    """A scenario that runs the Matter pilot it_pilot with something added to its definition (a policy, a wait delay); the original
    definition is put back when the scenario ends."""
    restore = None

    async def pilot_with(self, **extra):
        if self.restore is None:
            self.restore = await self.d.content(TEST, "it_pilot")
        await self.d.write(TEST, "it_pilot", dict(self.restore, **extra))       # always from the original, never from a variant
        return self.restore

    async def asyncTearDown(self):
        try:
            if self.restore is not None:
                await self.d.write(TEST, "it_pilot", self.restore)
        finally:
            await super().asyncTearDown()


def wants_echo(call="x_{{call}}"):
    return {"id": call, "target_capacity": f"{TEST}/test.echo", "parameters": {"payload": "nope"}, "await_response": False}


@unittest.skipIf(SKIP, SKIP_REASON)
class S47PolicyOfThePilot(PilotVariant):
    """The policy of a pilot is code applied before any effect: an output that aims at what is forbidden has none of its effects, the
    pilot is told and decides again; one that stays inside what is allowed goes through; a pilot that insists is stopped."""
    PREFIX = "it_s47"

    def cids(self):
        return [f"{self.PREFIX}_denied", f"{self.PREFIX}_allowed", f"{self.PREFIX}_stubborn"]

    async def test_a_forbidden_context_refuses_the_whole_output_before_any_effect(self):
        d = self.d
        denied, allowed, _ = self.cids()
        await d.reset_world([
            {"when": {"message_contains": "go", "continuity": denied, "stimulus": "none"}, "then": {"output": {"intentions": [wants_echo()], "intention_update": "sneaky"}}},
            {"when": {"continuity": denied, "stimulus": "execution_result"}, "then": {"output": say("understood")}},
            {"when": {"message_contains": "go", "continuity": allowed}, "then": {"output": {"intentions": [wants_echo()], "messages": [{"to": "user:1", "kind": "user", "content": "done"}]}}}])
        await self.pilot_with(forbidden_contexts=[TEST])
        await d.create(denied, intention="Stay put.")
        await d.send(denied, f"{denied}_m1", "go")
        await d.quiet(denied)
        await d.assert_invariants(denied)

        self.assertEqual(await d.tool_calls("test.echo"), [])                                  # nothing was invoked
        header = await d.continuity(denied)
        self.assertEqual(header["current_intention"], "Stay put.")                              # nor was the intention updated
        journal = await d.journal(denied)
        refused = [m["content"] for m in journal if m["kind"] == "error" and m["content"].get("event") == "intentions_refused"]
        self.assertEqual(len(refused), 1)
        self.assertEqual(refused[0]["failed"], "intention:x_1")
        calls = await d.llm_calls()
        self.assertEqual([c["stimulus"] for c in calls if c["continuity"] == denied], ["none", "execution_result"])    # the pilot was told…
        self.assertEqual([m["content"]["content"] for m in journal if m["kind"] == "pilot"], ["understood"])          # …and decided again

        await self.pilot_with(allowed_contexts=[TEST])                                          # the same wish, now inside an allowed context
        await d.create(allowed, intention="Go ahead.")
        await d.send(allowed, f"{allowed}_m1", "go")
        await d.quiet(allowed)
        await d.until(lambda: self._echoed(1), "the allowed tool to run")
        await d.assert_invariants(allowed)

    async def _echoed(self, n):
        return len(await self.d.tool_calls("test.echo")) == n

    async def test_a_pilot_that_keeps_asking_for_what_is_forbidden_is_stopped_after_three_refusals(self):
        d = self.d
        stubborn = self.cids()[2]
        await d.reset_world([{"when": {"continuity": stubborn}, "then": {"output": {"intentions": [wants_echo()]}}}])
        await self.pilot_with(forbidden_capacities=[f"{TEST}/*"])
        await d.create(stubborn, intention="Insist.")
        await d.send(stubborn, f"{stubborn}_m1", "go")
        await d.quiet(stubborn)
        calls = [c for c in await d.llm_calls() if c["continuity"] == stubborn]
        self.assertEqual(len(calls), 9)                                                          # three refusals per attempt, three attempts: not fifty
        journal = await d.journal(stubborn)
        self.assertEqual(len([m for m in journal if m["kind"] == "error" and m["content"].get("event") == "intentions_refused"]), 9)
        self.assertEqual(len([m for m in journal if m["kind"] == "error" and m["content"].get("event") == "treatment_failed"]), 3)
        self.assertEqual([(e["status"], e.get("attempts")) for e in await d.mailbox(stubborn)], [("failed", 3)])      # parked after three failed attempts
        self.assertEqual(await d.tool_calls("test.echo"), [])


@unittest.skipIf(SKIP, SKIP_REASON)
class S48MessageThatKeepsFailing(Scenario):
    """A message whose treatment fails three times is parked: it is no longer taken, and the messages after it are not blocked."""
    PREFIX = "it_s48"

    async def test_the_third_failure_parks_the_message_and_the_run_goes_on(self):
        d, cid = self.d, self.PREFIX
        await d.reset_world([
            {"when": {"message_contains": "poison", "continuity": cid}, "then": {"error": "provider down"}},
            {"when": {"message_contains": "fine", "continuity": cid}, "then": {"output": say("answered {{mid}}")}}])
        await d.create(cid, intention="Answer.")
        await d.send(cid, f"{cid}_m1", "poison")                       # the treatment fails, three attempts in the run, then it is parked
        await d.send(cid, f"{cid}_m2", "fine one")                     # sent at once: waits for those attempts only, then is answered
        await d.quiet(cid, allow_pending=0)
        await d.send(cid, f"{cid}_m3", "fine two")                     # a later message finds a clean queue
        await d.quiet(cid, allow_pending=0)
        await d.assert_invariants(cid)

        entries = {e["message_id"]: e for e in await d.mailbox(cid)}
        self.assertEqual(list(entries), [f"{cid}_m1"])                                          # the others were consumed
        self.assertEqual((entries[f"{cid}_m1"]["status"], entries[f"{cid}_m1"]["attempts"]), ("failed", 3))
        journal = await d.journal(cid)
        self.assertEqual([m["content"]["content"] for m in journal if m["kind"] == "pilot"], [f"answered {cid}_m2", f"answered {cid}_m3"])
        self.assertEqual(len([m for m in journal if m["kind"] == "error" and m["content"].get("event") == "message_parked"]), 1)
        poisoned = [c for c in await d.llm_calls() if c["message_id"] == f"{cid}_m1"]
        self.assertEqual(len(poisoned), 3)                                                       # asked three times, never a fourth


@unittest.skipIf(SKIP, SKIP_REASON)
class S49WaitThatNothingAnswers(PilotVariant):
    """A wait for a continuity that never answers ends after the wait delay with an error answer (timeout); the pilot decides."""
    PREFIX = "it_s49"

    async def test_the_wait_ends_with_a_timeout_answer(self):
        d, cid = self.d, self.PREFIX
        await d.reset_world([
            {"when": {"message_contains": "delegate", "continuity": cid, "stimulus": "none"},
             "then": {"output": {"wait_group": {"condition": "all", "expected": [{"type": "continuity", "ref": "ghost_child"}]}}}},
            {"when": {"continuity": cid, "stimulus": "active_wait_result"}, "then": {"output": say("gave up on the child")}}])
        original = await self.pilot_with()
        await self.pilot_with(limits=dict(original.get("limits") or {}, wait_timeout_s=1))
        await d.create(cid, intention="Delegate.")
        await d.send(cid, f"{cid}_m1", "delegate this")
        await d.quiet(cid, timeout=40)                                                           # the delay is checked each time the wait wakes: a few seconds
        await d.assert_invariants(cid)

        journal = await d.journal(cid)
        timeout = next(m for m in journal if m["id"] == f"timeout_{cid}_ghost_child")
        self.assertEqual((timeout["kind"], timeout["content"]["outcome"], timeout["content"]["error"]["code"]), ("continuity", "error", "timeout"))
        calls = await d.llm_calls()
        self.assertEqual([c["stimulus"] for c in calls], ["none", "active_wait_result"])
        self.assertEqual([m["content"]["content"] for m in journal if m["kind"] == "pilot"], ["gave up on the child"])


@unittest.skipIf(SKIP, SKIP_REASON)
class S50IdentityOfAMessage(Scenario):
    """A message is its id and its content. The same id with the same content is a retry; with another content it is a conflict of
    identity, refused in every state (waiting in the mailbox, or already consumed), and nothing is written."""
    PREFIX = "it_s50"

    async def test_the_same_id_with_another_content_is_refused_and_writes_nothing(self):
        from .driver import DriverError
        d, cid = self.d, self.PREFIX
        await d.reset_world([{"when": {"continuity": cid}, "then": {"output": say("noted")}}])
        await d.create(cid, intention="Note.")

        await d.control(cid, f"{cid}_ctl", "suspend")                       # keep the next message waiting in the mailbox
        first = await d.send(cid, f"{cid}_m1", "version A")
        again = await d.send(cid, f"{cid}_m1", "version A")                 # a retry: the same message
        self.assertEqual((again["seq"], again["message_id"]), (first["seq"], f"{cid}_m1"))
        lines = len(await d.journal(cid, frame=True))
        with self.assertRaises(DriverError) as caught:
            await d.send(cid, f"{cid}_m1", "version B")                     # another message under the same id, still waiting
        self.assertEqual((caught.exception.code, caught.exception.reason), ("conflict", "identity_conflict"))
        self.assertEqual(len(await d.journal(cid, frame=True)), lines)       # nothing was written

        await d.control(cid, f"{cid}_resume", "resume")
        await d.quiet(cid)
        with self.assertRaises(DriverError) as consumed:
            await d.send(cid, f"{cid}_m1", "version A")                     # the same message, now consumed: never reinserted
        self.assertEqual(consumed.exception.code, "refused")
        with self.assertRaises(DriverError) as other:
            await d.send(cid, f"{cid}_m1", "version B")                     # another one under its id: a conflict, not a refusal
        self.assertEqual((other.exception.code, other.exception.reason), ("conflict", "identity_conflict"))
        await d.assert_invariants(cid)
        self.assertEqual([m["content"] for m in await d.journal(cid) if m["kind"] == "user"], ["version A"])
