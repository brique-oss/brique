import os
import sys
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
sys.path.insert(0, os.path.dirname(HERE))
from fake_engine import make_engine  # noqa: E402
from substrate import continuity_store, liveness  # noqa: E402
from tests.contract.harness import World, entry, message  # noqa: E402


class MemoryOfTheWrapper(unittest.IsolatedAsyncioTestCase):
    """The wrapper keeps a bounded number of continuities in memory; one that was let go is rebuilt from its journal, and an append made
    through a copy that was let go meanwhile is still sound."""

    async def asyncSetUp(self):
        self.e = make_engine(with_capacities=False)
        self.p = self.e.port("test")
        self.w = World(self.e)
        liveness._live.clear()
        for cid in ("c1", "c2", "c3"):
            self.w.full(cid, messages=[message(1, f"{cid}_m1")], entries=[entry(f"{cid}_m1")])
        self._max = continuity_store.MAX_OBJECTS
        continuity_store.MAX_OBJECTS = 2

    def tearDown(self):
        continuity_store.MAX_OBJECTS = self._max

    def held(self):
        return list(continuity_store._home(self.p)["objects"])

    async def test_the_least_recently_used_goes_first(self):
        for cid in ("c1", "c2", "c3"):
            await continuity_store.get(self.p, cid)
        self.assertEqual(self.held(), ["c2", "c3"])
        await continuity_store.get(self.p, "c2")                          # c2 is used again: c3 is now the one to go
        await continuity_store.get(self.p, "c1")
        self.assertEqual(self.held(), ["c2", "c1"])

    async def test_one_that_was_let_go_is_rebuilt_from_its_journal(self):
        first = await continuity_store.get(self.p, "c1")
        await continuity_store.get(self.p, "c2")
        await continuity_store.get(self.p, "c3")
        self.assertNotIn("c1", self.held())
        again = await continuity_store.get(self.p, "c1")
        self.assertIsNot(again, first)
        self.assertEqual((again.line_count, again.has("c1_m1")), (1, True))

    async def test_an_append_through_a_copy_that_was_let_go_is_not_lost(self):
        stale = await continuity_store.get(self.p, "c1")
        await continuity_store.get(self.p, "c2")
        await continuity_store.get(self.p, "c3")                          # c1 is let go while its old copy is still around
        await continuity_store.append(self.p, "c1", [{"kind": "user", "id": "c1_m2", "from": "user:1", "content": "after"}])
        fresh = await continuity_store.get(self.p, "c1")
        self.assertEqual((fresh.line_count, fresh.seq_of("c1_m2")), (2, 2))
        self.assertEqual(self.w.journal("c1")[-1]["id"], "c1_m2")           # in the journal, which is the truth


if __name__ == "__main__":
    unittest.main()
