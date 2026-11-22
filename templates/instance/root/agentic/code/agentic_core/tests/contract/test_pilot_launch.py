import asyncio
import inspect
import unittest

from .harness import PILOT, World, CapacityContract, covers
from fake_engine import make_engine
from substrate.errors import AgenticError, InvalidRequest
import pilot_launch
from substrate import continuity_store

PLAN_REF = {"context": "/root/shop", "capacity": "plan"}


class PilotLaunchContract(CapacityContract):
    CAP, MODULE = "pilot.launch", pilot_launch

    async def asyncSetUp(self):
        await super().asyncSetUp()
        self.e = make_engine(with_capacities=True, pilot=False)        # continuity.create and mailbox.send are wired; the pilot is not run
        self.p = self.e.port(self.CAP)
        self.w = World(self.e)
        self.e.caps["/root/shop/plan"] = lambda port, params: {}       # a Capacity pilot exists
        self.e.pilot_capacities.add("/root/shop/plan")                  # and declares itself one
        self.w.full("p1")                                               # the parent

    async def launch(self, **extra):
        return await self.call(**dict({"continuity_id": "k1", "pilot_ref": PILOT, "initial_intention": "Do the task."}, **extra))

    def fires(self):
        return [c for c in self.e.calls if c == "fire:/root/agentic/pilot.activate"]

    async def test_payload_matches_the_declared_outputs(self):
        out = await self.launch(parent_id="p1", message={"content": "work"}, wait=False)
        self.assertEqual(set(out), {"continuity_id", "created", "message_id", "seq"})
        self.assertEqual((out["continuity_id"], out["created"], out["message_id"], out["seq"]), ("k1", True, "k1_launch", 2))   # seq 1 is the cognitive frame

    @covers("The continuity is created by continuity.create from the pilot_ref")
    async def test_a_matter_pilot_and_a_capacity_pilot_are_launched_the_same_way(self):
        await self.launch(parent_id="p1")
        header = self.w.state("continuity_k1")
        self.assertEqual((header["pilot_ref"], header["parent_id"], header["initial_intention"]), (PILOT, "p1", "Do the task."))
        self.assertIn("k1", self.w.state("continuity_p1")["children"])                  # the parent knows its daughter
        await self.launch(continuity_id="k2", pilot_ref=PLAN_REF)
        self.assertEqual(self.w.state("continuity_k2")["pilot_ref"], PLAN_REF)
        self.assertIsNone(self.w.state("continuity_k2")["parent_id"])

    @covers("The first message, when given, is of kind user")
    async def test_the_first_message_is_a_request_from_the_parent_or_from_the_person(self):
        await self.launch(parent_id="p1", message={"content": "work"}, wait=False)
        first = [m for m in self.w.journal("k1") if m["kind"] != "framework"][0]
        self.assertEqual((first["kind"], first["from"], first["id"], first["content"]), ("user", "continuity:p1", "k1_launch", "work"))
        await self.launch(continuity_id="k2", message={"content": "hello", "id": "mine"})
        first = [m for m in self.w.journal("k2") if m["kind"] != "framework"][0]
        self.assertEqual((first["kind"], first["from"], first["id"]), ("user", "user:1", "mine"))     # a root: the person, and the id given

    @covers("The first message is handed over by mailbox.send")
    async def test_the_message_wakes_the_pilot_and_the_launcher_never_activates_or_waits(self):
        await self.launch(message={"content": "work"})
        self.assertEqual(len(self.fires()), 1)                                          # the wake mailbox.send fires, not awaited
        self.assertNotIn("/root/agentic/pilot.activate", self.e.calls)                  # no explicit activation
        self.assertEqual([e["message_id"] for e in self.w.entries("k1")], ["k1_launch"])

    @covers("Without a message the continuity is created and waits")
    async def test_without_a_message_the_continuity_waits_for_its_first_one(self):
        out = await self.launch()
        self.assertEqual((out["created"], out["message_id"], out["seq"]), (True, None, None))
        self.assertEqual(self.w.entries("k1"), [])
        self.assertEqual(self.fires(), [])
        self.assertEqual([m["kind"] for m in self.w.journal("k1")], ["framework"])

    @covers("Without wait the answer comes at once and carries no result of the pilot")
    async def test_the_answer_comes_at_once_and_has_no_result_of_the_pilot(self):
        out = await self.launch(message={"content": "work"})
        self.assertEqual(set(out), {"continuity_id", "created", "message_id", "seq"})
        self.assertEqual(self.w.state("runtime_k1")["status"], "idle")                  # the pilot has not run: nothing was awaited

    # -- the return of the daughter: the answer of the launch is the result of the task ---------------------------------------------------------
    def finishes(self, cid="k1", after_s=0.05, says=(), park=False, failed_with=None):
        """The pilot of the launched continuity: speaks, then acknowledges the first message (or sets it aside)."""
        async def pilot():
            await asyncio.sleep(after_s)
            for to, kind, content in says:
                await continuity_store.append(self.p, cid, [{"kind": "pilot", "content": {"event": "message_out", "kind": kind, "content": content}, "to": to}])
            if failed_with:
                await continuity_store.append(self.p, cid, [{"kind": "error", "content": {"event": "treatment_failed", "message_id": f"{cid}_launch", "error": failed_with}}])
            mailbox = self.w.state(f"mailbox_{cid}")
            if park:
                for entry in mailbox["entries"]:
                    entry["status"] = "failed"
            else:
                mailbox["entries"] = []
            self.w.put(f"mailbox_{cid}", "mailbox", mailbox)
        return asyncio.ensure_future(pilot())

    @covers("Without wait the answer comes at once and carries no result of the pilot; with wait the answer is given when the pilot has finished the first message")
    async def test_a_parent_that_delegates_a_task_gets_the_result_of_the_daughter_as_the_answer(self):
        task = self.finishes(says=[("user:1", "user", "first point"), ("continuity:p1", "continuity", "final result")])
        out = await self.launch(parent_id="p1", message={"content": "work"})
        await task
        self.assertEqual((out["treated"], out["result"]["text"]), (True, "first point\n\nfinal result"))
        self.assertEqual([(m["to"], m["kind"]) for m in out["result"]["messages"]], [("user:1", "user"), ("continuity:p1", "continuity")])   # whoever it was addressed to
        self.assertEqual(set(out), {"continuity_id", "created", "message_id", "seq", "treated", "result"})
        self.assertEqual(self.e.keepalives.count("pilot.launch"), 1)                        # a running acknowledgement while it waits
        self.assertIn("pilot.launch", self.e.yields)                                         # and the execution slot given back

    @covers("wait defaults to true only when both parent_id and message are given")
    async def test_wait_defaults_to_true_only_for_a_parent_delegating_a_task(self):
        for params in ({"message": {"content": "hello"}}, {"parent_id": "p1"}, {}):         # a root with a message, a parent without a task, a bare chat
            out = await self.launch(continuity_id="r" + str(len(self.e.calls)), **params)
            self.assertNotIn("result", out)
        task = self.finishes(cid="w1")
        out = await self.launch(continuity_id="w1", message={"content": "hello"}, wait=True)  # asked for explicitly: a root may wait too
        await task
        self.assertTrue(out["treated"])
        task = self.finishes(cid="w2")
        waited = asyncio.ensure_future(self.launch(continuity_id="w2", parent_id="p1", message={"content": "t"}))
        await task
        self.assertTrue((await waited)["treated"])
        out = await self.launch(continuity_id="w3", parent_id="p1", message={"content": "t"}, wait=False)
        self.assertNotIn("result", out)

    @covers("A message the pilot could not treat (set aside after its attempts), the first one or a later one of the same task")
    async def test_a_later_message_set_aside_is_an_error_too(self):
        async def pilot():
            await asyncio.sleep(0.05)
            mailbox = self.w.state("mailbox_k1")
            mailbox["entries"] = [{"message_id": "later_response", "priority": "normal", "status": "failed", "attempts": 3}]   # the first one was fine
            self.w.put("mailbox_k1", "mailbox", mailbox)
            await continuity_store.append(self.p, "k1", [{"kind": "error", "content": {"event": "treatment_failed", "message_id": "later_response", "error": "boom"}}])
        task = asyncio.ensure_future(pilot())
        with self.assertRaises(AgenticError) as caught:
            await self.launch(parent_id="p1", message={"content": "work"})
        await task
        self.assertEqual(caught.exception.reason, "message_parked")
        self.assertIn("boom", caught.exception.message)

    async def test_the_task_is_not_finished_while_an_awaited_intention_has_no_response_or_the_continuity_is_not_idle(self):
        """Seen with a real guide: it emitted an awaited tool call with no wait group, its turn ended and its first message was acknowledged, the launch
        answered with an empty result; the tool's answer then arrived as a new message that opened the next turn."""
        async def pilot():
            await asyncio.sleep(0.05)
            await continuity_store.append(self.p, "k1", [{"id": "call1", "kind": "capacity_intention",
                                                          "content": {"id": "t1", "target_capacity": "/root/tools/echo", "parameters": {}, "await_response": True}}])
            mailbox = self.w.state("mailbox_k1")
            mailbox["entries"] = []                                                      # the first message is acknowledged: the turn is over
            self.w.put("mailbox_k1", "mailbox", mailbox)
            await asyncio.sleep(0.4)                                                     # the tool is still working: not finished
            self.assertFalse(done.is_set())
            runtime = self.w.state("runtime_k1")
            runtime["status"] = "running"                                                # the answer opened a new turn
            self.w.put("runtime_k1", "runtime", runtime)
            await continuity_store.append(self.p, "k1", [
                {"id": "resp1", "kind": "capacity_response", "content": {"response": {"status": "ok"}}, "correlation": {"intention_id": "t1"}},
                {"kind": "pilot", "content": {"event": "message_out", "kind": "user", "content": "found it"}, "to": "user:1"}])
            await asyncio.sleep(0.4)
            self.assertFalse(done.is_set())                                              # running: still not finished
            runtime["status"] = "idle"
            self.w.put("runtime_k1", "runtime", runtime)
        done = asyncio.Event()
        task = asyncio.ensure_future(pilot())
        out = await self.launch(parent_id="p1", message={"content": "work"})
        done.set()
        await task
        self.assertEqual(out["result"]["text"], "found it")

    @covers("The result is read from the journal of the launched continuity, never from a field the model must aim at")
    async def test_the_result_is_what_the_pilot_said_after_the_task_whoever_it_was_addressed_to(self):
        task = self.finishes(says=[("nobody:0", "pilot", "said to nobody")])
        out = await self.launch(parent_id="p1", message={"content": "work"})
        await task
        self.assertEqual(out["result"]["text"], "said to nobody")
        task = self.finishes(cid="q1")
        silent = await self.launch(continuity_id="q1", parent_id="p1", message={"content": "work"})
        await task
        self.assertEqual(silent["result"], {"text": "", "messages": []})                   # a pilot that said nothing: an empty result, still a result

    async def test_the_journal_tail_advances_instead_of_being_read_again_from_the_task_start(self):
        await continuity_store.append(self.p, "p1", [{"kind": "pilot", "content": "old"}])
        tail = pilot_launch._JournalTail(0)
        cursors = []
        original = pilot_launch.continuity_store.lines_after

        async def observed(port, cid, seq):
            cursors.append(seq)
            return await original(port, cid, seq)
        pilot_launch.continuity_store.lines_after = observed
        try:
            await tail.refresh(self.p, "p1")
            await continuity_store.append(self.p, "p1", [{"kind": "pilot", "content": "new"}])
            await tail.refresh(self.p, "p1")
        finally:
            pilot_launch.continuity_store.lines_after = original
        self.assertEqual(cursors, [0, 1])

    @covers("The wait follows a succession")
    async def test_the_wait_follows_the_continuity_to_its_successor(self):
        self.w.full("k1_g2")
        async def succession():
            await asyncio.sleep(0.05)
            header = self.w.state("continuity_k1")
            header["successor_id"] = "k1_g2"
            self.w.put("continuity_k1", "continuity", header)
            await continuity_store.append(self.p, "k1_g2", [{"kind": "pilot", "content": {"event": "message_out", "kind": "user", "content": "the continuation answers"}, "to": "user:1"}])
        task = asyncio.ensure_future(succession())
        out = await self.launch(parent_id="p1", message={"content": "work"})
        await task
        self.assertEqual(out["result"]["text"], "the continuation answers")                          # the successor has no entry for the message: it finished it

    @covers("While it waits the call acknowledges itself as running at regular intervals and yields its execution slot; it is bounded")
    async def test_the_wait_is_bounded_and_a_bound_reached_is_a_refusal(self):
        bound = pilot_launch.LAUNCH_WAIT_TIMEOUT_S
        pilot_launch.LAUNCH_WAIT_TIMEOUT_S = 0.2
        try:
            with self.assertRaises(AgenticError) as caught:
                await self.launch(parent_id="p1", message={"content": "work"})           # nobody ever acknowledges the message
        finally:
            pilot_launch.LAUNCH_WAIT_TIMEOUT_S = bound
        self.assertEqual((caught.exception.code, caught.exception.reason), ("refused", "timeout"))
        self.assertEqual(self.e.keepalives.count("pilot.launch"), 1)

    @covers("A message the pilot could not treat (set aside after its attempts) is an error answer, not a result.")
    async def test_a_message_set_aside_is_an_error_not_a_result(self):
        task = self.finishes(park=True, failed_with="invalid/invalid: expedition_id is required")
        with self.assertRaises(AgenticError) as caught:
            await self.launch(parent_id="p1", message={"content": "work"})
        await task
        self.assertEqual((caught.exception.code, caught.exception.reason), ("refused", "message_parked"))
        self.assertIn("expedition_id is required", caught.exception.message)                  # the caller is told WHY, not only that it failed
        self.assertIn("expedition_id is required", caught.exception.details["last_error"])

    @covers("Fails refused (reason message_parked)")
    async def test_wait_must_be_a_boolean(self):
        with self.assertRaises(InvalidRequest):
            await self.launch(parent_id="p1", message={"content": "work"}, wait="yes")

    @covers("Every step is idempotent")
    async def test_the_same_call_again_creates_nothing_twice_and_sends_nothing_twice(self):
        first = await self.launch(parent_id="p1", message={"content": "work"}, wait=False)
        second = await self.launch(parent_id="p1", message={"content": "work"}, wait=False)
        self.assertEqual((first["created"], second["created"]), (True, False))
        self.assertEqual(second["message_id"], first["message_id"])
        self.assertEqual([m["kind"] for m in self.w.journal("k1")], ["framework", "user"])
        self.assertEqual(len(self.w.entries("k1")), 1)
        self.assertEqual(self.w.state("continuity_p1")["children"], ["k1"])

    @covers("The same continuity_id with another pilot, intention or parent is an identity conflict")
    async def test_the_same_id_with_other_parameters_is_refused(self):
        await self.launch(parent_id="p1")
        self.w.full("p2")
        for other in ({"pilot_ref": PLAN_REF}, {"initial_intention": "Another task."}, {"parent_id": "p2"}):
            with self.assertRaises(AgenticError):
                await self.launch(**dict({"parent_id": "p1"}, **other))
        self.assertEqual(self.w.state("continuity_k1")["pilot_ref"], PILOT)

    @covers("No message is sent when the creation failed")
    async def test_nothing_is_sent_when_the_creation_failed(self):
        before = self.e.snapshot()
        with self.assertRaises(AgenticError):
            await self.launch(pilot_ref={"context": "/root/agentic", "matter_id": "no_such_pilot"}, message={"content": "work"})
        self.assertEqual(self.e.snapshot(), before)
        self.assertEqual(self.fires(), [])

    @covers("pilot.launch writes no Matter itself")
    async def test_the_launcher_writes_through_the_capacities_it_composes_only(self):
        source = inspect.getsource(pilot_launch)
        for write in ("matter_write", "matter_append", "matter_create", "update_matter", "patch_matter", "append_trace", "continuity_store.append"):                # reading the journal to give its result is not writing it
            self.assertNotIn(write, source)

    @covers("Fails with an invalid request when continuity_id, pilot_ref or initial_intention is missing")
    async def test_a_malformed_call_fails_before_any_effect(self):
        before = self.e.snapshot()
        bad = ({"continuity_id": None}, {"pilot_ref": None}, {"initial_intention": None}, {"initial_intention": "  "},
               {"message": {"id": "m"}}, {"message": "work"}, {"message": {"content": "x", "id": " "}})
        for params in bad:
            with self.assertRaises(InvalidRequest):
                await self.launch(**params)
            self.assertEqual(self.e.snapshot(), before)

    @covers("Fails when the pilot named by pilot_ref or the parent named by parent_id does not exist")
    async def test_a_missing_pilot_or_parent_fails_before_any_message(self):
        before = self.e.snapshot()
        for params in ({"pilot_ref": {"context": "/root/shop", "capacity": "nothing"}}, {"parent_id": "nobody"}):
            with self.assertRaises(AgenticError):
                await self.launch(message={"content": "work"}, **params)
            self.assertEqual(self.e.snapshot(), before)
            self.assertEqual(self.fires(), [])


if __name__ == "__main__":
    unittest.main()
