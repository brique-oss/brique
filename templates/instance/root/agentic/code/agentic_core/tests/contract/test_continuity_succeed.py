import unittest

from .harness import CapacityContract, covers, entry, message
from substrate.errors import ConcurrencyError, ConsistencyError, InvalidRequest, NotFound, Refused
from substrate.ids import matter_ids
import continuity_create, continuity_succeed, mailbox_send

SUMMARY = "Summary of what happened so far."


class ContinuitySucceedContract(CapacityContract):
    CAP, MODULE = "continuity.succeed", continuity_succeed

    async def asyncSetUp(self):
        await super().asyncSetUp()
        base = "/root/agentic/"
        self.e.caps[base + "continuity.create"] = continuity_create.run
        self.e.caps[base + "mailbox.send"] = mailbox_send.run
        self.w.full("p", messages=[], children=["c1"])
        self.w.full("c1", parent="p", messages=[message(1, "m1"), message(2, "m2"), message(3, "m3", kind="control", content={"action": "set_intention", "intention": "g2"})],
                    entries=[entry("m3", "control"), entry("m2")], current="goal")

    async def succeed(self, **extra):
        return await self.call(**dict({"continuity_id": "c1", "summary": SUMMARY}, **extra))

    def source(self):
        return self.w.state(matter_ids("c1").continuity)

    def successor(self):
        """The successor as the tests read it: its header plus the lines of its journal under "messages"."""
        lines = self.w.lines("c1_g2")
        return dict(self.w.state(matter_ids("c1_g2").continuity), messages=[m for m in lines if m["kind"] != "framework"],
                    frame=[m for m in lines if m["kind"] == "framework"])

    async def test_payload_matches_the_declared_outputs(self):
        out = await self.succeed()
        self.assertPayloadMatchesContract(out)
        self.assertEqual((out["continuity_id"], out["successor_id"], out["archived"]), ("c1", "c1_g2", True))
        self.assertEqual(out["transferred"], ["m2", "m3"])

    @covers("Fails when summary is absent or empty.")
    async def test_a_summary_is_required(self):
        for bad in (None, "", "  ", {}):
            before = self.e.snapshot()
            with self.assertRaises(InvalidRequest):
                await self.succeed(summary=bad)
            self.assertEqual(self.e.snapshot(), before)

    @covers("Fails when the continuity does not exist.")
    async def test_an_unknown_continuity_fails(self):
        with self.assertRaises(NotFound):
            await self.call(continuity_id="ghost", summary=SUMMARY)

    @covers("A continuity with an active wait group cannot succeed.", "Fails when the continuity has an active wait group.")
    async def test_an_active_wait_group_forbids_the_succession(self):
        self.w.runtime("c1", "waiting", "R1", "m1", groups=[{"id": "wg1", "condition": "all", "expected": [], "received": [], "status": "waiting"}])
        before = self.e.snapshot()
        with self.assertRaises(Refused):
            await self.succeed()
        self.assertEqual(self.e.snapshot(), before)

    @covers("Only an idle continuity, or the run that owns it, can succeed.",
            "Fails when the continuity is not idle and no run_id is given, or when run_id is not its owner.")
    async def test_only_idle_or_the_owning_run_can_succeed(self):
        self.w.runtime("c1", "running", "R1", "m2")
        before = self.e.snapshot()
        with self.assertRaises(Refused):
            await self.succeed()
        with self.assertRaises(Refused):
            await self.succeed(run_id="R2")
        self.assertEqual(self.e.snapshot(), before)
        out = await self.succeed(run_id="R1")
        self.assertTrue(out["archived"])

    @covers("The successor identifier derives from the lineage: <lineage_id>_g<generation + 1>.")
    async def test_the_successor_id_derives_from_lineage_and_generation(self):
        await self.succeed()
        self.assertEqual(self.successor()["generation"], 2)
        self.assertEqual(self.successor()["lineage_id"], "c1")
        await continuity_succeed.run(self.p, {"continuity_id": "c1_g2", "summary": "second"})
        self.assertEqual(self.w.state(matter_ids("c1_g3").continuity)["lineage_id"], "c1")
        self.assertEqual(self.w.state(matter_ids("c1_g3").continuity)["generation"], 3)

    @covers("A lineage has exactly one active continuity once the operation completes.")
    async def test_a_lineage_has_one_active_continuity(self):
        await self.succeed()
        self.assertEqual((self.source()["status"], self.successor()["status"]), ("archived", "active"))
        self.assertEqual((self.source()["successor_id"], self.successor()["predecessor_id"]), ("c1_g2", "c1"))
        self.assertNotIn("successor_id", self.successor())

    @covers("The successor opens with its own cognitive frame")
    async def test_the_successor_summary_ends_with_the_exact_ledger_of_the_source(self):
        self.w.full("c1", parent="p", messages=[
            message(1, "m1"),
            message(2, "intention_a", kind="capacity_intention", content={"id": "a", "target_capacity": "/ctx/support/reclamation.preparer",
                                                                       "parameters": {"commande_id": "CMD-4001"}, "await_response": True}),
            message(3, "response_a", kind="capacity_response", content={"response": {"status": "ok", "payload": {"brouillon_id": "BR-0001"}}},
                    correlation={"intention_id": "a"})], entries=[entry("m1")], current="goal")
        await self.succeed()
        first = self.successor()["messages"][0]
        self.assertEqual(first["kind"], "summary")
        self.assertTrue(first["content"].startswith(SUMMARY))                                              # what the model wrote, first
        self.assertIn("reclamation.preparer", first["content"])                                            # then what the code wrote
        self.assertIn("brouillon_id=BR-0001", first["content"])

    async def test_a_source_without_calls_keeps_the_summary_as_written(self):
        await self.succeed()
        self.assertEqual(self.successor()["messages"][0]["content"], SUMMARY)

    async def test_the_summary_is_the_first_message_and_never_a_stimulus(self):
        await self.succeed()
        self.assertEqual([(m["kind"], m["seq"]) for m in self.successor()["frame"]], [("framework", 1)])      # its own frame, first
        first = self.successor()["messages"][0]
        self.assertEqual((first["kind"], first["content"], first["seq"], first["from"]), ("summary", SUMMARY, 2, "continuity:c1"))
        self.assertNotIn(first["id"], [e["message_id"] for e in self.w.entries("c1_g2")])      # no mailbox entry: history only

    @covers("successor_id is written on the source before any pending message is transferred, so no message can be stranded in the source.")
    async def test_routing_is_switched_before_the_transfer(self):
        seen = []
        original = self.e.caps["/root/agentic/mailbox.send"]

        async def spy(port, params):
            seen.append(self.source().get("successor_id"))     # what the source says when a transfer happens
            return await original(port, params)
        self.e.caps["/root/agentic/mailbox.send"] = spy
        await self.succeed()
        self.assertEqual(seen, ["c1_g2", "c1_g2"])

    @covers("Pending messages are transferred in seq order with their identifiers preserved, then consumed at the source.")
    async def test_pending_messages_move_in_seq_order_with_their_ids(self):
        self.e.log.clear()
        await self.succeed()
        self.assertEqual([m["id"] for m in self.successor()["messages"]], ["summary_c1_g2", "m2", "m3"])
        self.assertEqual(self.w.entries("c1"), [])
        # the control was applied at once by the idle successor (and its own entry removed); the normal message waits there
        self.assertEqual({e["message_id"] for e in self.w.entries("c1_g2")}, {"m2"})

    @covers("The archive status is the last write and the commit point; the journal of the source is never modified.")
    async def test_archive_is_the_last_write_and_the_journal_is_untouched(self):
        journal = self.w.journal("c1")
        self.e.log.clear()
        await self.succeed()
        self.assertEqual(self.w.journal("c1"), journal)
        last_source_write = [e for e in self.e.log if e[0] == "write" and e[1] == "continuity_c1"][-1]
        self.assertEqual(self.e.log[-1][:2], last_source_write[:2])         # nothing is written after the archive
        self.assertEqual(self.source()["status"], "archived")

    @covers("The operation is a recoverable multi-Matter operation: every step is idempotent and relaunching it completes the missing steps;")
    async def test_an_interruption_is_repaired_by_a_rerun(self):
        self.e.fail_write.add("mailbox_c1")                    # the transfer cannot consume its source entries
        with self.assertRaises(Exception):
            await self.succeed()
        self.e.fail_write.discard("mailbox_c1")
        self.assertEqual(self.source()["status"], "active")     # not archived: the commit point was not reached
        out = await self.succeed()
        self.assertTrue(out["archived"])
        self.assertEqual(sorted(m["id"] for m in self.successor()["messages"] if m["kind"] != "summary"), ["m2", "m3"])   # no duplicate
        again = await self.succeed()                            # a finished succession is an idempotent success
        self.assertEqual((again["successor_id"], again["archived"]), ("c1_g2", True))

    @covers("Every write of a document Matter is guarded by expected_revision and every journal append by expected_line_count,")
    async def test_every_write_is_guarded(self):
        self.e.log.clear()
        await self.succeed()
        for kind, mid, guard, how in self.e.log:
            if kind in ("write", "structure_patch", "append"):
                self.assertIsNotNone(guard, (kind, mid))                # a document by expected_revision, a journal by expected_line_count
        appends = [e for e in self.e.log if e[0] == "append"]
        self.assertTrue(appends)                                        # the summary of the successor is a guarded journal append

    async def test_the_successor_continues_the_source(self):
        before = self.source()
        await self.succeed()
        s, c = self.successor(), self.source()
        self.assertEqual((s["initial_intention"], s["pilot_ref"], s["parent_id"]), (c["initial_intention"], c["pilot_ref"], c["parent_id"]))
        # the pending control set_intention g2 was transferred and applied by the successor, which started from "goal"
        self.assertEqual((before["current_intention"], s["current_intention"]), ("goal", "g2"))
        self.assertEqual((s["predecessor_id"], s["lineage_id"], s["generation"]), ("c1", "c1", 2))

    async def test_an_archived_source_is_an_idempotent_success(self):
        await self.succeed()
        before = self.e.snapshot()
        out = await self.succeed()
        self.assertEqual(out["successor_id"], "c1_g2")
        self.assertEqual(self.e.snapshot(), before)

    async def test_the_parent_points_to_the_successor(self):
        await self.succeed()
        self.assertEqual(self.w.state(matter_ids("p").continuity)["children"], ["c1_g2"])

    @covers("Fails with a consistency failure when an existing continuity contradicts the planned successor or the source names another successor.")
    async def test_a_contradicting_successor_is_refused(self):
        self.w.continuity("c1_g2", intention="other")          # exists, but is not the successor of c1
        with self.assertRaises(ConsistencyError):
            await self.succeed()

    @covers("Fails explicitly with a concurrency failure when the bounded retries on revision conflict are exhausted.")
    async def test_exhausted_conflicts_fail_explicitly(self):
        self.e.always_conflict.add("continuity_c1")
        with self.assertRaises(ConcurrencyError):
            await self.succeed()


if __name__ == "__main__":
    unittest.main()
