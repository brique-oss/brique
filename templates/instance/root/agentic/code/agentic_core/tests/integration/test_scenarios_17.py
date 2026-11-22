import unittest

from .driver import AGENTIC
from .scenarios_base import SKIP, SKIP_REASON, say
from .test_scenarios_14 import PartsScenario


@unittest.skipIf(SKIP, SKIP_REASON)
class S46ExplicitRecovery(PartsScenario):
    """pilot.recover is an explicit start: a DSL orchestration that asks continuity.stranded who cannot go on by itself and wakes those,
    and only those, with pilot.activate wake true. A calm continuity is left alone; nothing is woken until it is called."""
    PREFIX = "it_s46"

    def cids(self):
        return [f"{self.PREFIX}_orphan", f"{self.PREFIX}_calm", f"{self.PREFIX}_work"]

    async def test_it_wakes_only_what_cannot_go_on_by_itself(self):
        d = self.d
        orphan, calm, work = self.cids()
        await d.reset_world([{"when": {"continuity": work}, "then": {"output": say("done")}}])
        for cid in (orphan, calm, work):
            await d.create(cid, intention="Recovery probe.")

        runtime = await d.runtime(orphan)                                           # a run that died: running, owned by a run nobody lives
        await d.write(AGENTIC, f"runtime_{orphan}", dict(runtime, status="running", run_id="DEAD_RUN"))
        await d.control(work, f"{work}_ctl", "suspend")                             # work waits in the mailbox of a continuity with no run
        await d.send(work, f"{work}_m1", "hello")
        runtime = await d.runtime(work)
        await d.write(AGENTIC, f"runtime_{work}", dict(runtime, status="idle", run_id=None))
        self.assertEqual([e["status"] for e in await d.mailbox(work)], ["pending"])

        out = await d.call(AGENTIC, "pilot.recover", "user", {"continuity_ids": [orphan, calm, work]}, timeout=40)
        self.assertEqual((out["examined"], out["woken"], out["skipped"]), (3, [orphan, work], []))       # the calm one is left alone

        await d.quiet(orphan, work, timeout=40)
        await d.assert_invariants(orphan, calm, work)
        rt = await d.runtime(orphan)
        self.assertEqual((rt["status"], rt["run_id"]), ("idle", None))              # taken over, and nothing to replay
        self.assertEqual([m["content"]["content"] for m in await d.journal(work) if m["kind"] == "pilot"], ["done"])    # the waiting message was answered

        again = await d.call(AGENTIC, "pilot.recover", "user", {"continuity_ids": [orphan, calm, work]}, timeout=40)
        self.assertEqual((again["woken"], again["examined"]), ([], 3))              # nothing left to start
