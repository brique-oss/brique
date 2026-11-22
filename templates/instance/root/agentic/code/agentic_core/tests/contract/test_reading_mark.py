import asyncio
import unittest

from .harness import CapacityContract, covers
from substrate.errors import InvalidRequest, NotFound
import reading_mark

MATTER = "reading_cursors"


class ReadingMarkContract(CapacityContract):
    CAP, MODULE = "reading.mark", reading_mark

    async def asyncSetUp(self):
        await super().asyncSetUp()
        for cid in ["l1", "l2", "l3", "shared"] + [f"l{i}" for i in range(1, 21)]:
            self.w.continuity(cid)                                                         # a cursor names a conversation that exists

    async def mark(self, lineage="l1", seq=3, **extra):
        return await self.call(**dict({"lineage_id": lineage, "seq": seq}, **extra))

    def cursors(self):
        return {(c["reader"], c["lineage_id"]): c["seq"] for c in self.w.state(MATTER)["cursors"]}

    def writes(self):
        return [entry for entry in self.e.log if entry[1] == MATTER and entry[0] == "write"]

    async def test_payload_matches_the_declared_outputs(self):
        out = await self.mark("l1", 7)
        self.assertPayloadMatchesContract(out)
        self.assertEqual((out["lineage_id"], out["reader"], out["seq"], out["changed"]), ("l1", "user:1", 7, True))

    @covers("The cursor of a reader on a lineage never moves back", "The answer carries the cursor after the mark")
    async def test_the_cursor_only_moves_forward(self):
        self.assertEqual((await self.mark(seq=10))["seq"], 10)
        back = await self.mark(seq=4)
        self.assertEqual((back["seq"], back["changed"]), (10, False))                    # the answer says where the cursor is, and that it did not move
        self.assertEqual(self.cursors()[("user:1", "l1")], 10)
        ahead = await self.mark(seq=12)
        self.assertEqual((ahead["seq"], ahead["changed"]), (12, True))
        self.assertEqual(self.cursors()[("user:1", "l1")], 12)

    @covers("A mark that does not move the cursor writes nothing")
    async def test_a_mark_that_does_not_move_the_cursor_writes_nothing(self):
        await self.mark(seq=10)
        before = self.e.snapshot()
        count = len(self.writes())
        await self.mark(seq=10)
        await self.mark(seq=2)
        self.assertEqual(self.e.snapshot(), before)
        self.assertEqual(len(self.writes()), count)

    @covers("The reading_cursors Matter is created empty on first use")
    async def test_the_matter_is_created_on_first_use_and_only_then(self):
        self.assertNotIn(MATTER, self.e.matters)
        asked, original = [], self.p.matter_create

        async def spy(matter_id, *args, **kwargs):
            asked.append(matter_id)
            return await original(matter_id, *args, **kwargs)
        self.p.matter_create = spy
        await self.mark(seq=1)
        self.assertEqual(self.w.state(MATTER)["cursors"], [{"reader": "user:1", "lineage_id": "l1", "seq": 1}])      # the first mark is in it
        descriptor = self.e.matters[MATTER]
        self.assertEqual((descriptor["objective"]["type"], descriptor["functional"]["schema_ref"]), ("reading", "/root/agentic/schema/reading_data"))
        await self.mark("l2", 1)
        await self.mark("l3", 1)
        self.assertEqual(asked, [MATTER])                                                  # asked for once: later marks find it

    @covers("A mark never touches the cursors of other lineages or of other readers")
    async def test_a_mark_touches_only_its_own_cursor(self):
        await self.mark("l1", 5)
        await self.mark("l2", 8)
        await self.mark("l1", 6, reader="someone:2")
        await self.mark("l1", 20)
        self.assertEqual(self.cursors(), {("user:1", "l1"): 20, ("user:1", "l2"): 8, ("someone:2", "l1"): 6})

    @covers("Marks that arrive together are all kept")
    async def test_marks_that_arrive_together_are_all_kept(self):
        await asyncio.gather(*[self.mark(f"l{i}", i) for i in range(1, 21)])
        self.assertEqual(self.cursors(), {("user:1", f"l{i}"): i for i in range(1, 21)})
        self.assertGreaterEqual(len(self.writes()), 20)                                    # one write per mark that moved a cursor
        for entry in self.writes():
            self.assertIsNotNone(entry[2])                                                 # every write carries the revision it read
        await asyncio.gather(*[self.mark("shared", n) for n in (3, 9, 5, 7, 1)])
        self.assertEqual(self.cursors()[("user:1", "shared")], 9)                         # and the greatest of simultaneous marks wins

    @covers("A mark requires the continuity it names to exist", "Fails not found when the continuity named by lineage_id does not exist")
    async def test_a_cursor_never_outlives_its_conversation_nor_is_brought_back_by_a_late_mark(self):
        """The interface marks 300 ms after a read: a conversation deleted in between must not get its cursor back (the Matter would grow for ever)."""
        before = self.e.snapshot()
        for ghost in ("a_conversation_that_never_was", "deleted_a_moment_ago"):
            with self.assertRaises(NotFound):
                await self.mark(ghost, 3)
        self.assertEqual(self.e.snapshot(), before)                                        # nothing written, the Matter was not even created
        self.assertNotIn(MATTER, self.e.matters)

    @covers("Fails with an invalid request when lineage_id or reader is empty or seq is not a non-negative integer")
    async def test_a_malformed_call_writes_nothing(self):
        before = self.e.snapshot()
        for params in ({"lineage_id": None, "seq": 1}, {"lineage_id": " ", "seq": 1}, {"lineage_id": "l1"}, {"lineage_id": "l1", "seq": -1},
                       {"lineage_id": "l1", "seq": 1.5}, {"lineage_id": "l1", "seq": True}, {"lineage_id": "l1", "seq": "3"},
                       {"lineage_id": "l1", "seq": 1, "reader": ""}):
            with self.assertRaises(InvalidRequest):
                await self.call(**params)
            self.assertEqual(self.e.snapshot(), before)                                    # not even the Matter is created


if __name__ == "__main__":
    unittest.main()
