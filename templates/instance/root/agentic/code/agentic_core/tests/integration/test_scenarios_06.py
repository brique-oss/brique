import asyncio
import random
import unittest

from .driver import AGENTIC, DriverError
from .scenarios_base import SKIP, SKIP_REASON, Scenario, say


@unittest.skipIf(SKIP, SKIP_REASON)
class S18CrashAndRecovery(Scenario):
    PREFIX = "it_s18"

    async def test_wrapper_restart_in_the_middle_of_a_wait_never_replays_and_resolves_explicitly(self):
        d, cid = self.d, self.PREFIX
        await d.reset_world([
            {"when": {"continuity": cid, "message_contains": "ping"}, "then": {"output": say("pong")}},
            {"when": {"continuity": cid, "stimulus": "active_wait_result"}, "then": {"output": say("recovered")}},
            {"when": {"continuity": cid, "stimulus": "none"},
             "then": {"output": {"wait_group": {"condition": "all", "expected": [{"type": "intention", "ref": "s_{{call}}"}]},
                                 "intentions": [{"id": "s_{{call}}", "target_capacity": "/root/agentic/test/test.slow",
                                                 "parameters": {"gate": "gCrash", "value": "x"}, "await_response": True}]}}},
        ])
        await d.create(cid)
        await d.send(cid, f"{cid}_m1", "long work")

        async def waiting():
            rt = await d.runtime(cid)
            return rt["status"] == "waiting" and rt
        before = await d.until(waiting, "the continuity to wait")
        self.assertEqual(len(await d.tool_calls("test.slow", "start")), 1)

        # the process holding the run disappears (the engine restarts the wrapper)
        await d.call(AGENTIC, "wrapper.restart", "execution", {"name": "agentic_core"}, timeout=60)
        rt = await d.runtime(cid)
        self.assertEqual((rt["status"], rt["run_id"]), ("waiting", before["run_id"]))      # the durable state still names the dead run

        # a new message is what makes the orphan visible: the run is taken over, not replayed
        await d.send(cid, f"{cid}_m2", "ping")
        await d.quiet(cid, timeout=40)
        await d.assert_invariants(cid)

        journal = await d.journal(cid)
        self.assertIn("run_recovered", [m["content"].get("event") for m in journal if isinstance(m["content"], dict)])
        unresolved = [m for m in journal if m["kind"] == "capacity_response"]
        self.assertEqual(len(unresolved), 1)
        self.assertTrue(unresolved[0]["content"]["unresolved"])                              # the uncertainty is explicit
        self.assertEqual(unresolved[0]["content"]["response"]["error"]["code"], "unresolved")
        self.assertEqual(len(await d.tool_calls("test.slow", "start")), 1)                  # never re-emitted
        calls = await d.llm_calls()
        self.assertEqual([(c["message_id"], c["stimulus"]) for c in calls],
                         [(f"{cid}_m1", "none"), (f"{cid}_m1", "active_wait_result")])       # the ping joins the chain: no turn of its own
        self.assertEqual(calls[1]["user_message"], {"message_ids": [f"{cid}_m2"]})          # it is designated to the resumption, not lost
        self.assertEqual(calls[1]["active_stimulus"]["received"], 1)                # the lost wait ends with one answer: the explicit "unresolved" error

        await d.open_gate("gCrash")                                                           # the old tool call finally returns
        await asyncio.sleep(1.0)
        self.assertEqual(len(await d.llm_calls()), 2)                                         # and changes nothing
        await d.assert_invariants(cid)


@unittest.skipIf(SKIP, SKIP_REASON)
class S19Trickle(Scenario):
    PREFIX = "it_s19"

    async def test_messages_arriving_around_the_idle_transition_are_never_lost(self):
        d, cid = self.d, self.PREFIX
        rng = random.Random(19)
        await d.reset_world([{"when": {"continuity": cid}, "then": {"output": say("ok {{mid}}")}}])
        await d.create(cid)
        sent = []
        for i in range(25):
            mid = f"{cid}_m{i:02d}"
            sent.append(mid)
            await d.send(cid, mid, f"trickle {i}")
            await asyncio.sleep(rng.uniform(0.0, 0.12))                                       # lands right around the idle window
        await d.quiet(cid, timeout=90)
        await d.assert_invariants(cid)
        calls = await d.llm_calls()
        self.assertEqual(sorted(c["message_id"] for c in calls), sorted(sent))                  # none lost, none twice
        self.assertEqual(len(calls), 25)


@unittest.skipIf(SKIP, SKIP_REASON)
class S20Cleanup(Scenario):
    PREFIX = "it_s20"

    def cids(self):
        return ["it_s20_p", "it_s20_k", "it_s20_g"]

    async def test_cleanup_leaves_nothing_behind(self):
        d = self.d
        await d.reset_world([])
        await d.create("it_s20_p")
        await d.create("it_s20_k", parent="it_s20_p")
        await d.create("it_s20_g", parent="it_s20_k")
        self.assertEqual([r for r in await d.structure_refs() if r.startswith("continuity_it_s20")],
                         ["continuity_it_s20_g", "continuity_it_s20_k", "continuity_it_s20_p"])
        await d.cleanup("it_s20_p")
        for cid in ("it_s20_p", "it_s20_k", "it_s20_g"):
            self.assertTrue(await d.gone(cid), cid)
        out = await d.call(AGENTIC, "continuity.delete", "user", {"continuity_id": "it_s20_p", "cascade": True})
        self.assertFalse(out["deleted"])                                                       # idempotent, no side effect
        self.assertEqual([r for r in await d.structure_refs() if "it_s20" in r], [])


if __name__ == "__main__":
    unittest.main()
