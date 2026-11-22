import unittest

from .harness import CapacityContract, covers, entry, message
from substrate import liveness
from substrate.errors import ConsistencyError, InvalidRequest
import continuity_stranded


class ContinuityStrandedContract(CapacityContract):
    CAP, MODULE = "continuity.stranded", continuity_stranded

    async def asyncSetUp(self):
        await super().asyncSetUp()
        w = self.w
        w.full("dead_running", status="running", run_id="R_DEAD", messages=[message(1, "a1")], entries=[entry("a1", status="claimed", claimed_by="R_DEAD")], current="a1")
        w.full("live_running", status="running", run_id="R_LIVE", messages=[message(1, "b1")], entries=[entry("b1", status="claimed", claimed_by="R_LIVE")], current="b1")
        w.full("dead_waiting", status="waiting", run_id="R_OLD", messages=[message(1, "c1")], groups=[{"id": "g", "condition": "all", "status": "waiting", "expected": [], "received": []}])
        w.full("pending_work", messages=[message(1, "d1")], entries=[entry("d1")])
        w.full("interrupted", messages=[message(1, "e1")], current="e1")
        w.full("open_group", groups=[{"id": "g", "condition": "all", "status": "waiting", "expected": [], "received": []}])
        w.full("calm")
        w.full("suspended", status="suspended", messages=[message(1, "f1")], entries=[entry("f1")])
        w.full("archived", status="active", messages=[message(1, "g1")], entries=[entry("g1")], **{})
        w.state("continuity_archived")      # laid below
        self.e.matters["continuity_archived"]["data"]["status"] = "archived"
        liveness._live.clear()
        liveness.mark_live("live_running", "R_LIVE")

    async def stranded(self, **params):
        return await self.call(**params)

    async def test_payload_matches_the_declared_outputs(self):
        out = await self.stranded()
        self.assertPayloadMatchesContract(out)

    @covers("Examining a continuity only reads its state: nothing is written.")
    async def test_it_only_reads(self):
        before = self.e.snapshot()
        await self.stranded()
        self.assertUnchanged(before)

    @covers("A continuity is stranded when its runtime says running or waiting for a run that is not alive in this process.")
    async def test_a_run_that_is_not_alive_in_this_process_is_stranded(self):
        out = await self.stranded()
        self.assertIn("dead_running", out["continuity_ids"])
        self.assertIn("dead_waiting", out["continuity_ids"])
        self.assertNotIn("live_running", out["continuity_ids"])          # its run is alive here: it is going on by itself
        self.assertIn("not alive", out["reasons"]["dead_running"])

    @covers("A continuity is stranded when it is idle and holds a pending message that its journal holds, a current message or an active wait group.")
    async def test_idle_work_with_no_run_is_stranded(self):
        out = await self.stranded()
        self.assertEqual([out["reasons"][c].split(" ")[0:2] for c in ("pending_work", "interrupted", "open_group")],
                         [["a", "message"], ["a", "message"], ["a", "wait"]])
        self.assertTrue({"pending_work", "interrupted", "open_group"} <= set(out["continuity_ids"]))

    @covers("An archived or suspended continuity, and an idle continuity with no work, is never stranded.")
    async def test_nothing_to_do_means_not_stranded(self):
        out = await self.stranded()
        for cid in ("calm", "suspended", "archived"):
            self.assertNotIn(cid, out["continuity_ids"])
        self.assertEqual(out["examined"], 9)

    @covers("The continuities are taken from the canonical Structure continuities, in its order; when continuity_ids is given, only these are examined.")
    async def test_the_structure_gives_the_order_and_continuity_ids_the_scope(self):
        out = await self.stranded()
        self.assertEqual(out["continuity_ids"], ["dead_running", "dead_waiting", "pending_work", "interrupted", "open_group"])    # the order of the Structure
        some = await self.stranded(continuity_ids=["open_group", "calm", "dead_running"])
        self.assertEqual((some["continuity_ids"], some["examined"]), (["dead_running", "open_group"], 3))
        with self.assertRaises(InvalidRequest):
            await self.stranded(continuity_ids="open_group")

    @covers("Fails if the Structure of the continuities cannot be read.")
    async def test_without_its_structure_it_fails(self):
        self.e.structures.pop("continuities")
        with self.assertRaises(ConsistencyError):
            await self.stranded()

    @covers("A continuity whose Matters cannot be read is skipped and named in skipped; it does not stop the others.")
    async def test_a_continuity_that_cannot_be_read_does_not_stop_the_others(self):
        for gone in ("runtime_pending_work",):
            self.e.matters.pop(gone)
        out = await self.stranded()
        self.assertEqual([s["continuity_id"] for s in out["skipped"]], ["pending_work"])
        self.assertIn("open_group", out["continuity_ids"])           # the others were examined


if __name__ == "__main__":
    unittest.main()
