"""The reading cursors of the interface (capacity_reading_mark.md): S82. And the waits that must not hold an execution slot of the wrapper: S83."""
import asyncio
import unittest

from .driver import AGENTIC
from .scenarios_base import SKIP, SKIP_REASON, Scenario, say

MATTER = "reading_cursors"
READER = "it_s82_reader"


@unittest.skipIf(SKIP, SKIP_REASON)
class S82ReadingCursors(Scenario):
    """A cursor only moves forward, simultaneous marks are all kept, and the Matter is created on first use. The Matter is the person's: the test
    removes only the cursors of its own readers, and the Matter only when nothing else is left in it."""
    PREFIX = "it_s82"

    async def purge_test_entries(self):
        """The Matter is the person's: only the cursors of test readers are removed from it, and the Matter itself only when nothing else is left."""
        if not await self.d.exists(AGENTIC, MATTER):
            return
        content = await self.d.content(AGENTIC, MATTER)
        cursors = content.get("cursors", [])
        kept = [c for c in cursors if not c["reader"].startswith(("it_s82", "th_", "probe"))]
        if len(kept) == len(cursors):
            return
        if kept:
            await self.d.write(AGENTIC, MATTER, dict(content, cursors=kept))
        else:
            await self.d.call(AGENTIC, "matter.delete", "matter", {"matter_id": MATTER})

    async def asyncSetUp(self):
        await super().asyncSetUp()
        await self.purge_test_entries()

    async def asyncTearDown(self):
        try:
            await self.purge_test_entries()
        finally:
            await super().asyncTearDown()

    def cids(self):
        return ["it_s82_a", "it_s82_shared"] + [f"it_s82_{i}" for i in range(1, 61)]         # a cursor names a conversation that exists

    async def mark(self, lineage, seq, **extra):
        return await self.d.call(AGENTIC, "reading.mark", "user", dict({"lineage_id": lineage, "seq": seq, "reader": READER}, **extra))

    async def mine(self):
        cursors = (await self.d.content(AGENTIC, MATTER))["cursors"]
        return {c["lineage_id"]: c["seq"] for c in cursors if c["reader"] == READER}

    async def test_the_cursor_only_moves_forward_and_marks_that_arrive_together_are_all_kept(self):
        await asyncio.gather(*[self.d.create(cid) for cid in self.cids()])
        first = await self.mark("it_s82_a", 5)
        self.assertEqual((first["seq"], first["changed"]), (5, True))
        back = await self.mark("it_s82_a", 2)
        self.assertEqual((back["seq"], back["changed"]), (5, False))
        self.assertEqual((await self.mark("it_s82_a", 9))["seq"], 9)
        await asyncio.gather(*[self.mark(f"it_s82_{i}", i) for i in range(1, 61)])           # sixty at once (the wrapper has sixteen execution slots), each its own lineage
        await asyncio.gather(*[self.mark("it_s82_shared", n) for n in (3, 9, 5, 7, 1)])      # and five on the same cursor
        cursors = await self.mine()
        self.assertEqual({k: v for k, v in cursors.items() if k != "it_s82_a" and k != "it_s82_shared"}, {f"it_s82_{i}": i for i in range(1, 61)})
        self.assertEqual((cursors["it_s82_a"], cursors["it_s82_shared"]), (9, 9))
        with self.assertRaises(Exception):
            await self.mark("it_s82_a", -1)                                                   # a malformed mark is refused
        self.assertEqual(await self.mine(), cursors)

    async def test_a_cursor_goes_with_its_conversation_and_a_late_mark_brings_nothing_back(self):
        """The Matter used to keep a cursor for every conversation ever deleted (241, none of a conversation that still existed)."""
        await self.d.create("it_s82_a")
        await self.mark("it_s82_a", 4)
        self.assertIn("it_s82_a", await self.mine())
        await self.d.call(AGENTIC, "continuity.delete", "user", {"continuity_id": "it_s82_a", "cascade": True})
        self.assertNotIn("it_s82_a", await self.mine())                                       # deleted with it
        with self.assertRaises(Exception) as late:                                            # the mark the interface sends 300 ms after a read
            await self.mark("it_s82_a", 5)
        self.assertEqual(late.exception.code, "not_found")
        self.assertNotIn("it_s82_a", await self.mine())                                       # and nothing came back



@unittest.skipIf(SKIP, SKIP_REASON)
class S83ManyWaitersDoNotFreezeTheWrapper(Scenario):
    """Thirty messages arrive for a continuity while its inference is held: they all wait for the inference window, which is more than the sixteen
    execution slots of the wrapper. If a wait held its slot, the pilot could not take its own back after its next call and the wrapper would freeze:
    here it stays reachable, and everything is written once the inference ends. (Thirty and not more: the record of the calls of the fake model
    is a Matter that the engine refuses to write past 256 KiB, about forty calls of a pilot.)"""
    PREFIX = "it_s83"

    async def test_thirty_senders_wait_for_the_window_and_the_wrapper_stays_reachable(self):
        d, cid = self.d, self.PREFIX
        await d.reset_world([
            {"when": {"continuity": cid, "message_id": f"{cid}_m1"}, "then": {"output": say("m1"), "wait_gate": "gBusy"}},
            {"when": {"continuity": cid}, "then": {"output": say("ok {{mid}}")}},
        ])
        await d.create(cid)
        await d.send(cid, f"{cid}_m1", "start")

        async def thinking():
            return len([c for c in await d.llm_calls() if c["continuity"] == cid]) >= 1
        await d.until(thinking, "the inference to be held")
        ids = [f"{cid}_u{i:02d}" for i in range(30)]
        senders = [asyncio.ensure_future(d.send(cid, mid, f"message {mid}")) for mid in ids]
        await asyncio.sleep(1.5)
        self.assertFalse(any(task.done() for task in senders))                           # all of them wait at the window
        alive = await d.call(AGENTIC, "continuity.stranded", "user", {}, timeout=5)      # and the wrapper still answers another caller
        self.assertEqual(alive["examined"] >= 0, True)
        await d.open_gate("gBusy")
        await asyncio.wait_for(asyncio.gather(*senders), 60)
        await d.quiet(cid, timeout=90)
        journal = await d.journal(cid)
        self.assertEqual(sorted(m["id"] for m in journal if m["kind"] == "user"), sorted(ids + [f"{cid}_m1"]))   # not one lost, not one doubled
        absorbed = {i for c in await d.llm_calls() for i in ((c.get("user_message") or {}).get("message_ids") or [])}
        answered = {m["content"]["content"] for m in journal if m["kind"] == "pilot"}
        missing = [mid for mid in ids if f"ok {mid}" not in answered and mid not in absorbed]
        self.assertEqual(missing, [])                                                    # each was answered as a turn or designated to the model
        await d.assert_invariants(cid)


if __name__ == "__main__":
    unittest.main()
