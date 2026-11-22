import unittest

from .harness import CapacityContract, covers, entry, message
from substrate.errors import ConcurrencyError, ConsistencyError, InvalidRequest, NotFound, Refused
import mailbox_release


class MailboxReleaseContract(CapacityContract):
    CAP, MODULE = "mailbox.release", mailbox_release

    async def asyncSetUp(self):
        await super().asyncSetUp()
        self.w.full("c1", messages=[message(4, "m4", kind="control"), message(5, "m5")],
                    entries=[entry("m5", "normal", "claimed", "R1"), entry("m4", "control")], status="running", run_id="R1", current="m5")

    async def release(self, message_id="m5", run_id="R1"):
        return await self.call(continuity_id="c1", message_id=message_id, run_id=run_id)

    async def test_payload_matches_the_declared_outputs(self):
        out = await self.release()
        self.assertPayloadMatchesContract(out)
        self.assertEqual(out, {"continuity_id": "c1", "message_id": "m5", "released": True, "attempts": 0, "parked": False})

    @covers("A release that reports a failed treatment (failed true) counts one attempt on the entry (attempts).")
    async def test_a_failed_release_counts_an_attempt_and_a_plain_one_does_not(self):
        out = await self.call(continuity_id="c1", message_id="m5", run_id="R1", failed=True)
        self.assertEqual((out["attempts"], out["parked"]), (1, False))
        stored = next(e for e in self.w.entries("c1") if e["message_id"] == "m5")
        self.assertEqual((stored["status"], stored["attempts"], stored.get("claimed_by")), ("pending", 1, None))
        self.w.mailbox("c1", [entry("m5", "normal", "claimed", "R1")])
        plain = await self.release()
        self.assertEqual(plain["attempts"], 0)                                        # a release that reports no failure counts nothing
        with self.assertRaises(InvalidRequest):
            await self.call(continuity_id="c1", message_id="m5", run_id="R1", failed="yes")

    @covers("An entry whose treatment failed for the third time is parked", "Fails if the entry is parked (status failed)")
    async def test_the_third_failure_parks_the_entry_and_a_parked_entry_is_not_released_again(self):
        for n in (1, 2, 3):
            self.w.mailbox("c1", [dict(entry("m5", "normal", "claimed", "R1"), attempts=n - 1), entry("m4", "control")])
            out = await self.call(continuity_id="c1", message_id="m5", run_id="R1", failed=True)
            self.assertEqual((out["attempts"], out["parked"]), (n, n == 3))
        stored = next(e for e in self.w.entries("c1") if e["message_id"] == "m5")
        self.assertEqual((stored["status"], stored["attempts"], stored.get("claimed_by")), ("failed", 3, None))
        before = self.e.snapshot()
        with self.assertRaises(Refused):
            await self.release()
        self.assertUnchanged(before)

    async def test_every_declared_input_is_required(self):
        for missing in ("continuity_id", "message_id", "run_id"):
            params = {"continuity_id": "c1", "message_id": "m5", "run_id": "R1"}
            params.pop(missing)
            before = self.e.snapshot()
            with self.assertRaises(InvalidRequest):
                await self.call(**params)
            self.assertUnchanged(before)

    @covers("Only the run owning the claimed entry may release it.", "Fails if claimed_by differs from run_id.",
            "Failure leaves mailbox state unchanged.")
    async def test_only_the_owning_run_may_release(self):
        before = self.e.snapshot()
        with self.assertRaises(Refused):
            await self.release(run_id="R2")
        self.assertUnchanged(before)

    @covers("A pending entry cannot be released.", "Fails if the entry is already pending.")
    async def test_a_pending_entry_cannot_be_released(self):
        before = self.e.snapshot()
        with self.assertRaises(Refused):
            await self.release("m4")
        self.assertUnchanged(before)

    @covers("Release changes mailbox consumption state only.", "The durable message is never deleted or mutated.",
            "The entry keeps its original message_id, priority, and referenced message seq.")
    async def test_returns_to_pending_without_an_owner_and_keeps_identity(self):
        journal, runtime = self.w.journal("c1"), self.w.rt("c1")
        await self.release()
        released = [e for e in self.w.entries("c1") if e["message_id"] == "m5"][0]
        self.assertEqual(released, {"message_id": "m5", "priority": "normal", "status": "pending"})   # no owner left
        self.assertEqual(self.w.journal("c1"), journal)
        self.assertEqual([m["seq"] for m in self.w.journal("c1") if m["kind"] != "event"], [4, 5])
        self.assertEqual(self.w.rt("c1"), runtime)

    @covers("Release never means rollback.", "Already emitted intentions are never automatically replayed.",
            "Already materialized external effects remain real.")
    async def test_release_replays_and_reverts_nothing(self):
        await self.release()
        self.assertEqual(self.e.calls, [])                                # no invocation, no re-emission
        self.assertEqual(self.e.tasks, [])
        self.assertEqual(len([m for m in self.w.journal("c1") if m["kind"] != "event"]), 2)      # history is exactly as it was

    @covers("The operation mutates a single Matter through one matter.write guarded by expected_revision")
    async def test_one_guarded_write_on_the_mailbox_only(self):
        await self.release()
        self.assertEqual([(k, m) for k, m, *_ in self.e.log], [("write", "mailbox_c1")])
        self.assertIsNotNone(self.e.log[0][2])

    @covers("Fails if the mailbox does not belong to continuity_id.")
    async def test_a_mailbox_of_another_continuity_is_refused(self):
        state = self.w.state("mailbox_c1")
        state["continuity_id"] = "other"
        self.e.matters["mailbox_c1"]["data"] = state
        before = self.e.snapshot()
        with self.assertRaises(ConsistencyError):
            await self.release()
        self.assertUnchanged(before)

    @covers("Fails if the entry does not exist.")
    async def test_an_absent_entry_is_not_found_and_never_recreated(self):
        before = self.e.snapshot()
        with self.assertRaises(NotFound):
            await self.release("ghost")
        self.assertUnchanged(before)

    @covers("Fails explicitly with a concurrency failure when the bounded retries")
    async def test_exhausted_revision_conflicts_fail_explicitly(self):
        self.e.always_conflict.add("mailbox_c1")
        before = self.e.snapshot()
        with self.assertRaises(ConcurrencyError):
            await self.release()
        self.assertUnchanged(before)


if __name__ == "__main__":
    unittest.main()
