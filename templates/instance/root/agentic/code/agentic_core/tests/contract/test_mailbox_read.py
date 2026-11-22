import asyncio
import unittest

from .harness import CapacityContract, covers, entry, message
from substrate.errors import ConcurrencyError, ConsistencyError, InvalidRequest, Refused
import mailbox_read


class MailboxReadContract(CapacityContract):
    CAP, MODULE = "mailbox.read", mailbox_read

    async def asyncSetUp(self):
        await super().asyncSetUp()
        # physical entry order is deliberately NOT the seq order
        self.w.full("c1",
                    messages=[message(1, "n1"), message(2, "k2", kind="control"), message(3, "n3"), message(4, "k4", kind="control"), message(5, "x5", kind="capacity_response")],
                    entries=[entry("n3"), entry("k4", "control"), entry("n1"), entry("x5", "expected", "claimed", "R1"), entry("k2", "control")],
                    status="running", run_id="R1")

    async def read(self, priority="normal", run_id="R1"):
        return await self.call(continuity_id="c1", run_id=run_id, priority=priority)

    async def test_payload_matches_the_declared_outputs(self):
        out = await self.read()
        self.assertPayloadMatchesContract(out)
        self.assertEqual((out["message_id"], out["priority"], out["seq"], out["status"], out["claimed_by"]), ("n1", "normal", 1, "claimed", "R1"))

    @covers("A parked entry (status failed) is never claimed.")
    async def test_a_parked_entry_is_never_claimed(self):
        self.w.mailbox("c1", [dict(entry("n1"), status="failed", attempts=3)])
        out = await self.read()
        self.assertEqual((out["message_id"], out["status"]), (None, "empty"))
        self.assertEqual(self.w.entries("c1")[0]["status"], "failed")

    async def test_every_declared_input_is_required(self):
        for missing in ("continuity_id", "run_id", "priority"):
            params = {"continuity_id": "c1", "run_id": "R1", "priority": "normal"}
            params.pop(missing)
            with self.assertRaises(InvalidRequest):
                await self.call(**params)

    @covers("Only the current runtime.run_id may claim mailbox work.", "Fails if run_id is not the current runtime.run_id.",
            "Does not alter any entry when ownership validation fails.")
    async def test_only_the_current_run_may_claim(self):
        before = self.e.snapshot()
        with self.assertRaises(Refused):
            await self.read(run_id="R2")
        self.assertUnchanged(before)

    @covers("Selection and claim form one atomic mailbox mutation", "The operation mutates a single Matter through one matter.write")
    async def test_selection_and_claim_are_one_guarded_write_of_the_mailbox(self):
        await self.read()
        self.assertEqual([(k, m) for k, m, *_ in self.e.log], [("write", "mailbox_c1")])
        self.assertIsNotNone(self.e.log[0][2])

    @covers("One mailbox entry can have at most one owner.")
    async def test_concurrent_reads_never_give_one_entry_two_owners(self):
        self.w.mailbox("c1", [entry(f"p{i}") for i in range(8)])
        self.w.lay_journal("c1", [message(i + 1, f"p{i}") for i in range(8)])
        got = await asyncio.gather(*[self.read() for _ in range(8)])
        ids = [g["message_id"] for g in got]
        self.assertEqual(sorted(ids), sorted(f"p{i}" for i in range(8)))

    @covers("Only pending entries are eligible.")
    async def test_claimed_entries_are_never_selected(self):
        self.e.matters["mailbox_c1"]["data"]["entries"] = [entry("n1", status="claimed", claimed_by="R1")]
        self.assertIsNone((await self.read())["message_id"])

    @covers("Expected entries captured by an active wait group are never selected by mailbox.read.",
            "The priority parameter accepts only normal or control; expected is forbidden",
            "Fails if priority is expected or is not one of normal or control.")
    async def test_expected_is_never_read_and_priority_is_validated(self):
        for bad in ("expected", "urgent", None):
            before = self.e.snapshot()
            with self.assertRaises(InvalidRequest):
                await self.read(priority=bad)
            self.assertUnchanged(before)

    @covers("Only entries of the requested priority class, normal or control, are selected;")
    async def test_priority_classes_are_never_mixed(self):
        self.assertEqual((await self.read("control"))["message_id"], "k2")
        self.assertEqual((await self.read("normal"))["message_id"], "n1")

    @covers("Within one priority, the referenced continuity message with the lowest seq is selected first.",
            "Physical order of mailbox.entries is irrelevant.")
    async def test_lowest_seq_first_whatever_the_physical_order(self):
        order = [(await self.read("normal"))["message_id"] for _ in range(3)]
        self.assertEqual(order, ["n1", "n3", None])
        self.assertEqual([(await self.read("control"))["message_id"] for _ in range(3)], ["k2", "k4", None])

    @covers("The journal is never mutated by mailbox.read.")
    async def test_the_journal_is_never_mutated(self):
        journal = self.w.journal("c1")
        await self.read()
        self.assertEqual(self.w.journal("c1"), journal)

    @covers("A mailbox entry whose message_id matches no durable message in the journal is never eligible for claim.")
    async def test_a_pre_commit_reservation_is_never_claimed(self):
        self.e.matters["mailbox_c1"]["data"]["entries"] = [entry("ghost"), entry("n3")]
        self.assertEqual((await self.read())["message_id"], "n3")
        self.assertIsNone((await self.read())["message_id"])
        self.assertIn("ghost", [e["message_id"] for e in self.w.entries("c1")])    # the reservation is left alone

    async def test_no_eligible_entry_is_not_an_error(self):
        self.e.matters["mailbox_c1"]["data"]["entries"] = []
        out = await self.read()
        self.assertEqual((out["message_id"], out["status"]), (None, "empty"))

    @covers("Fails if continuity, mailbox, or runtime are inconsistent.")
    async def test_a_missing_matter_is_an_inconsistency(self):
        del self.e.matters["runtime_c1"]
        with self.assertRaises(ConsistencyError):
            await self.read()

    @covers("Fails if mailbox ownership state is internally contradictory.")
    async def test_contradictory_ownership_is_refused_without_change(self):
        for bad in ([entry("n1", status="claimed")],                      # claimed without owner
                    [dict(entry("n1"), claimed_by="R1")],                  # pending with an owner
                    [entry("n1"), entry("n1")]):                           # duplicated entry
            self.e.matters["mailbox_c1"]["data"]["entries"] = bad
            before = self.e.snapshot()
            with self.assertRaises(ConsistencyError):
                await self.read()
            self.assertUnchanged(before)

    @covers("Fails explicitly with a concurrency failure when the bounded retries")
    async def test_exhausted_revision_conflicts_fail_explicitly(self):
        self.e.always_conflict.add("mailbox_c1")
        with self.assertRaises(ConcurrencyError):
            await self.read()


    @covers("With kind user, only normal entries whose durable message has kind user are eligible")
    async def test_the_absorption_read_takes_only_user_messages(self):
        self.w.full("c1", messages=[message(1, "r1", kind="capacity_response"), message(2, "u2"), message(3, "u3")],
                    entries=[entry("r1"), entry("u2"), entry("u3")], status="running", run_id="R1")
        first = await self.call(continuity_id="c1", run_id="R1", priority="normal", kind="user")
        self.assertEqual((first["message_id"], first["status"]), ("u2", "claimed"))        # not r1, though it is older: it waits for its own turn
        second = await self.call(continuity_id="c1", run_id="R1", priority="normal", kind="user")
        self.assertEqual(second["message_id"], "u3")
        third = await self.call(continuity_id="c1", run_id="R1", priority="normal", kind="user")
        self.assertEqual((third["message_id"], third["status"]), (None, "empty"))
        self.assertEqual([e["status"] for e in self.w.entries("c1")], ["pending", "claimed", "claimed"])
        for bad in ({"kind": "capacity_response"}, {"kind": "user", "priority": "control"}):
            with self.assertRaises(InvalidRequest):
                await self.call(**dict({"continuity_id": "c1", "run_id": "R1", "priority": "normal"}, **bad))


if __name__ == "__main__":
    unittest.main()
