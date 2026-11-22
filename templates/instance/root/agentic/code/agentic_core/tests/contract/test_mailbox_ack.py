import unittest

from .harness import CapacityContract, covers, entry, message
from substrate.errors import ConcurrencyError, ConsistencyError, InvalidRequest, NotFound, Refused
import mailbox_ack


class MailboxAckContract(CapacityContract):
    CAP, MODULE = "mailbox.ack", mailbox_ack

    async def asyncSetUp(self):
        await super().asyncSetUp()
        self.w.full("c1", messages=[message(1, "m1"), message(2, "m2")],
                    entries=[entry("m1", status="claimed", claimed_by="R1"), entry("m2")], status="running", run_id="R1")

    async def ack(self, message_id="m1", run_id="R1", cid="c1"):
        return await self.call(continuity_id=cid, message_id=message_id, run_id=run_id)

    # --- outputs and inputs -----------------------------------------------------------
    async def test_payload_matches_the_declared_outputs(self):
        out = await self.ack()
        self.assertPayloadMatchesContract(out)
        self.assertEqual(out, {"continuity_id": "c1", "message_id": "m1", "acknowledged": True})

    async def test_every_declared_input_is_required(self):
        for missing in ("continuity_id", "message_id", "run_id"):
            params = {"continuity_id": "c1", "message_id": "m1", "run_id": "R1"}
            params.pop(missing)
            before = self.e.snapshot()
            with self.assertRaises(InvalidRequest):
                await self.call(**params)
            self.assertUnchanged(before)

    # --- invariants -----------------------------------------------------------------------
    @covers("Only the run owning the claimed entry may acknowledge it.", "Fails if claimed_by differs from run_id.",
            "Failure leaves mailbox state unchanged.")
    async def test_only_the_owning_run_may_acknowledge(self):
        before = self.e.snapshot()
        with self.assertRaises(Refused):
            await self.ack(run_id="R2")
        self.assertUnchanged(before)
        await self.ack()
        self.assertEqual([e["message_id"] for e in self.w.entries("c1")], ["m2"])

    @covers("A pending entry cannot be acknowledged.", "Fails if the entry is pending.")
    async def test_a_pending_entry_cannot_be_acknowledged(self):
        before = self.e.snapshot()
        with self.assertRaises(Refused):
            await self.ack("m2")
        self.assertUnchanged(before)

    @covers("Acknowledgement removes mailbox consumption state only.",
            "The durable message referenced by message_id is never deleted or mutated.",
            "An acknowledged message cannot later be reinserted with the same message id.")
    async def test_removes_the_entry_and_leaves_history_untouched(self):
        history = self.w.journal("c1")
        runtime_before = self.w.rt("c1")
        await self.ack()
        self.assertEqual(self.w.journal("c1"), history)              # the message stays: it is the journal
        self.assertEqual(self.w.rt("c1"), runtime_before)            # nothing else is touched
        self.assertEqual([e["message_id"] for e in self.w.entries("c1")], ["m2"])
        # "present in the journal, absent from the mailbox" is exactly what forbids a reinsertion
        self.assertIn("m1", [m["id"] for m in self.w.journal("c1")])
        self.assertNotIn("m1", [e["message_id"] for e in self.w.entries("c1")])

    @covers("Acknowledgement produces no replay, wake, or cognitive effect.")
    async def test_no_replay_wake_or_cognitive_effect(self):
        await self.ack()
        self.assertEqual(self.e.calls, [])
        self.assertEqual(self.e.tasks, [])

    @covers("The operation mutates a single Matter through one matter.write guarded by expected_revision")
    async def test_one_guarded_write_on_the_mailbox_only(self):
        await self.ack()
        self.assertEqual([(kind, mid) for kind, mid, *_ in self.e.log], [("write", "mailbox_c1")])
        self.assertIsNotNone(self.e.log[0][2])                        # expected_revision supplied

    # --- failures -----------------------------------------------------------------------------
    @covers("Fails if the mailbox does not belong to continuity_id.")
    async def test_a_mailbox_of_another_continuity_is_refused(self):
        self.w.mailbox("c1", [entry("m1", status="claimed", claimed_by="R1")])
        state = self.w.state("mailbox_c1")
        state["continuity_id"] = "other"
        self.e.matters["mailbox_c1"]["data"] = state
        before = self.e.snapshot()
        with self.assertRaises(ConsistencyError):
            await self.ack()
        self.assertUnchanged(before)

    @covers("Fails if the entry does not exist according to the chosen technical not-found policy.")
    async def test_an_absent_entry_is_not_found_and_never_recreated(self):
        before = self.e.snapshot()
        with self.assertRaises(NotFound):
            await self.ack("ghost")
        self.assertUnchanged(before)

    @covers("Fails explicitly with a concurrency failure when the bounded retries")
    async def test_exhausted_revision_conflicts_fail_explicitly(self):
        self.e.always_conflict.add("mailbox_c1")
        before = self.e.snapshot()
        with self.assertRaises(ConcurrencyError):
            await self.ack()
        self.assertUnchanged(before)

    async def test_a_conflict_is_retried_from_a_fresh_read(self):
        # another writer changes the mailbox between our read and our write
        original = self.p.matter_write
        state = {"first": True}

        async def interfering(matter_id, **kw):
            if state["first"] and matter_id == "mailbox_c1":
                state["first"] = False
                v = await self.p.matter_read("mailbox_c1")
                f = dict(v.content, entries=v.content["entries"] + [entry("m3")])
                await original("mailbox_c1", content=f, expected_revision=v.revision)
            return await original(matter_id, **kw)
        self.p.matter_write = interfering
        await self.ack()
        self.assertEqual([e["message_id"] for e in self.w.entries("c1")], ["m2", "m3"])   # the concurrent entry survives


if __name__ == "__main__":
    unittest.main()
