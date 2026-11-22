"""A conversation must stay deletable in every state (document/capacity_mailbox_send.md, S85): suspended, waiting for a tool with a live run, and waiting for
a tool under a run that died with its wrapper. Each used to leave the delete pending for ever or until a wait timed out."""
import asyncio
import unittest

from .crash import SKIP_HARD_KILL, SKIP_HARD_KILL_REASON, kill_wrapper
from .driver import AGENTIC
from .scenarios_base import SKIP, SKIP_REASON, Scenario, say
from .test_scenarios_02 import expect, slow
from .test_scenarios_15 import after_crash

SKIPPED = SKIP or SKIP_HARD_KILL
WHY = SKIP_REASON if SKIP else SKIP_HARD_KILL_REASON


@unittest.skipIf(SKIPPED, WHY)
class S85DeleteInEveryState(Scenario):
    PREFIX = "it_s85"

    def cids(self):
        return [f"{self.PREFIX}_{name}" for name in ("suspended", "live", "dead")]

    async def prepare(self, name):
        d, cid = self.d, f"{self.PREFIX}_{name}"
        await d.reset_world([
            {"when": {"continuity": cid, "stimulus": "none"},
             "then": {"output": {"wait_group": {"condition": "all", "expected": expect("a_{{call}}")}, "intentions": [slow("a_{{call}}", "gDelete")]}}},
            {"when": {}, "then": {"output": say("ok")}},
        ])
        await d.create(cid)
        return cid

    async def deleted_within(self, cid, seconds=20):
        for _ in range(int(seconds / 0.25)):
            if await self.d.gone(cid):
                return True
            await asyncio.sleep(0.25)
        return False

    async def waiting(self, cid):
        async def is_waiting():
            return (await self.d.runtime(cid))["status"] == "waiting"
        await self.d.until(is_waiting, "the continuity to wait for the tool")

    async def test_a_suspended_conversation_can_be_deleted(self):
        d = self.d
        cid = await self.prepare("suspended")
        await d.control(cid, f"{cid}_c1", "suspend")
        self.assertEqual((await d.runtime(cid))["status"], "suspended")
        out = await d.control(cid, f"{cid}_c2", "delete", cascade=True)
        self.assertEqual(out["status"], "deleted")                                    # applied at once: nothing is in progress
        self.assertTrue(await self.deleted_within(cid, 5))

    async def test_a_conversation_waiting_for_a_tool_can_be_deleted(self):
        d = self.d
        cid = await self.prepare("live")
        await d.send(cid, f"{cid}_m1", "go")
        await self.waiting(cid)
        await d.control(cid, f"{cid}_c1", "delete", cascade=True)
        self.assertTrue(await self.deleted_within(cid), "the delete stayed pending: the waiting run never applied it")

    async def test_a_conversation_whose_run_died_while_waiting_can_be_deleted(self):
        d = self.d
        cid = await self.prepare("dead")
        await d.send(cid, f"{cid}_m1", "go")
        await self.waiting(cid)
        kill_wrapper()                                                                 # the run dies with its wrapper; the runtime still says waiting
        out = await after_crash(lambda: d.control(cid, f"{cid}_c1", "delete", cascade=True))
        self.assertTrue(out["wake"])                                                   # nobody would reach a safe point: the orphan is taken over
        self.assertTrue(await self.deleted_within(cid), "the delete stayed pending: nothing took the dead run over")


if __name__ == "__main__":
    unittest.main()
