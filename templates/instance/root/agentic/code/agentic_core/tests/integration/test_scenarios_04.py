import asyncio
import unittest

from .driver import AGENTIC
from .scenarios_base import SKIP, SKIP_REASON, Scenario, say


def wait_one_rules(cid, gate="gS"):
    return [
        {"when": {"continuity": cid, "stimulus": "active_wait_result"}, "then": {"output": say("resumed")}},
        {"when": {"continuity": cid, "stimulus": "none"},
         "then": {"output": {"wait_group": {"condition": "all", "expected": [{"type": "intention", "ref": "s_{{call}}"}]},
                             "intentions": [{"id": "s_{{call}}", "target_capacity": "/root/agentic/test/test.slow",
                                             "parameters": {"gate": gate, "value": "x"}, "await_response": True}]}}},
    ]


class ControlsBase(Scenario):
    async def wait_status(self, cid, status):
        async def reached():
            rt = await self.d.runtime(cid)
            return rt["status"] == status and rt
        return await self.d.until(reached, f"{cid} to be {status}")


@unittest.skipIf(SKIP, SKIP_REASON)
class S11SuspendResumeWhileWaiting(ControlsBase):
    PREFIX = "it_s11"

    async def test_suspend_keeps_the_wait_and_resume_continues_the_same_chain(self):
        d, cid = self.d, self.PREFIX
        await d.reset_world(wait_one_rules(cid))
        await d.create(cid)
        await d.send(cid, f"{cid}_m1", "work")
        await self.wait_status(cid, "waiting")

        out = await d.control(cid, f"{cid}_k1", "suspend")
        self.assertEqual((out["priority"], out["status"]), ("control", "pending"))        # deferred to a safe point
        rt = await self.wait_status(cid, "suspended")
        self.assertIsNone(rt["run_id"])
        self.assertEqual(len(rt["active_wait_groups"]), 1)                                 # the durable wait survives
        self.assertEqual(rt["current_message_id"], f"{cid}_m1")
        self.assertNotIn(f"{cid}_k1", [e["message_id"] for e in await d.mailbox(cid)])    # applied then acknowledged

        await d.open_gate("gS")                                                            # the Response arrives while suspended
        await d.until(lambda: self._satisfied(cid), "the Response to be captured")
        self.assertEqual(len(await d.llm_calls()), 1)                                      # no cognition while suspended
        self.assertEqual((await d.runtime(cid))["status"], "suspended")

        out = await d.control(cid, f"{cid}_k2", "resume")
        self.assertEqual(out["status"], "consumed")                                        # applied immediately, entry removed
        await d.quiet(cid)
        await d.assert_invariants(cid)
        calls = await d.llm_calls()
        self.assertEqual(len(calls), 2)
        self.assertEqual(calls[1]["stimulus"], "active_wait_result")
        self.assertEqual(calls[1]["message_id"], f"{cid}_m1")                               # the same chain, the same message

    async def _satisfied(self, cid):
        rt = await self.d.runtime(cid)
        return rt["active_wait_groups"] and rt["active_wait_groups"][0]["status"] == "satisfied"


@unittest.skipIf(SKIP, SKIP_REASON)
class S11bImmediateControls(ControlsBase):
    PREFIX = "it_s11b"

    async def test_idle_controls_apply_at_once_and_leave_no_entry(self):
        d, cid = self.d, self.PREFIX
        await d.reset_world([{"when": {"continuity": cid}, "then": {"output": say()}}])
        await d.create(cid, intention="Original.")
        out = await d.control(cid, f"{cid}_k1", "set_intention", intention="Changed.")
        self.assertEqual((out["priority"], out["status"]), ("control", "consumed"))
        c = await d.continuity(cid)
        self.assertEqual((c["initial_intention"], c["current_intention"]), ("Original.", "Changed."))
        self.assertEqual(await d.mailbox(cid), [])                                          # no re-processing later
        await d.control(cid, f"{cid}_k2", "suspend")
        self.assertEqual((await d.runtime(cid))["status"], "suspended")
        out = await d.send(cid, f"{cid}_m1", "anything")
        self.assertFalse(out["wake"])                                                        # suspended stays asleep
        await asyncio.sleep(0.5)
        self.assertEqual(len(await d.llm_calls()), 0)
        await d.control(cid, f"{cid}_k3", "resume")
        await d.quiet(cid)
        await d.assert_invariants(cid)
        self.assertEqual(len(await d.llm_calls()), 1)                                        # resume woke the pending message
        self.assertEqual([m["kind"] for m in await d.journal(cid)].count("control"), 3)     # the controls stay in the history


@unittest.skipIf(SKIP, SKIP_REASON)
class S12Delete(ControlsBase):
    PREFIX = "it_s12"

    def cids(self):
        return ["it_s12_p", "it_s12_k", "it_s12_g", "it_s12_w"]

    async def test_delete_without_cascade_detaches_children(self):
        d = self.d
        await d.reset_world([])
        await d.create("it_s12_p")
        await d.create("it_s12_k", parent="it_s12_p")
        out = await d.control("it_s12_p", "it_s12_d1", "delete")
        self.assertEqual(out["status"], "deleted")
        self.assertTrue(await d.gone("it_s12_p"))
        child = await d.continuity("it_s12_k")
        self.assertNotIn("parent_id", child)                                                # detached, alive
        self.assertIn("continuity_it_s12_k", await d.structure_refs())

    async def test_delete_with_cascade_removes_the_whole_subtree(self):
        d = self.d
        await d.reset_world([])
        await d.create("it_s12_p")
        await d.create("it_s12_k", parent="it_s12_p")
        await d.create("it_s12_g", parent="it_s12_k")
        out = await d.control("it_s12_p", "it_s12_d2", "delete", cascade=True)
        self.assertEqual(out["status"], "deleted")
        for cid in ("it_s12_p", "it_s12_k", "it_s12_g"):
            self.assertTrue(await d.gone(cid), cid)

    async def test_delete_while_waiting_is_terminal_and_a_late_result_recreates_nothing(self):
        d, cid = self.d, "it_s12_w"
        await d.reset_world(wait_one_rules(cid, gate="gDel"))
        await d.create(cid)
        await d.send(cid, f"{cid}_m1", "work")
        await self.wait_status(cid, "waiting")
        out = await d.control(cid, f"{cid}_k1", "delete")
        self.assertEqual((out["priority"], out["status"]), ("control", "pending"))        # deferred to the safe point
        await d.until(lambda: d.gone(cid), "the continuity to be deleted at the safe point")
        await d.open_gate("gDel")                                                           # the tool still answers
        await asyncio.sleep(1.0)
        self.assertTrue(await d.gone(cid))                                                  # nothing was recreated
        self.assertEqual(len(await d.llm_calls()), 1)


if __name__ == "__main__":
    unittest.main()
