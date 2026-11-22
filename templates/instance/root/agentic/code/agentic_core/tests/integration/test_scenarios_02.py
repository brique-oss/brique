import asyncio
import unittest

from .driver import AGENTIC, PILOT_ID, TEST
from .scenarios_base import SKIP, SKIP_REASON, Scenario, say


def slow(ref, gate, value=None):
    return {"id": ref, "target_capacity": "/root/agentic/test/test.slow", "parameters": {"gate": gate, "value": value or gate}, "await_response": True}


def expect(*refs):
    return [{"type": "intention", "ref": r} for r in refs]


@unittest.skipIf(SKIP, SKIP_REASON)
class S04AnyAndLateResult(Scenario):
    PREFIX = "it_s04"

    async def test_any_resumes_early_and_the_late_result_is_history_only(self):
        d, cid = self.d, self.PREFIX
        await d.reset_world([
            {"when": {"continuity": cid, "stimulus": "none", "message_contains": "race"},
             "then": {"output": {"wait_group": {"condition": "any", "expected": expect("fast_{{call}}", "slow_{{call}}")},
                                 "intentions": [slow("fast_{{call}}", "gFast"), slow("slow_{{call}}", "gSlow")]}}},
            {"when": {"continuity": cid, "stimulus": "active_wait_result"}, "then": {"output": say("first one wins")}},
        ])
        await d.create(cid)
        await d.send(cid, f"{cid}_m1", "race")
        await d.until(lambda: self._waiting(cid), "the continuity to wait")
        await d.open_gate("gFast")
        await d.quiet(cid)
        calls = await d.llm_calls()
        self.assertEqual(len(calls), 2)
        self.assertEqual((calls[1]["active_stimulus"]["received"], calls[1]["active_stimulus"]["outcome"]), (1, "satisfied"))

        await d.open_gate("gSlow")                                      # the slow one finishes after the group is resolved
        await d.until(lambda: self._responses(cid, 2), "the late Response in the history")
        await d.quiet(cid)
        await d.assert_invariants(cid)
        self.assertEqual(len(await d.llm_calls()), 2)                   # no new inference
        responses = [m for m in await d.journal(cid) if m["kind"] == "capacity_response"]
        self.assertEqual(sorted(m["correlation"]["intention_id"] for m in responses), [f"fast_1", f"slow_1"])

    async def _waiting(self, cid):
        rt = await self.d.runtime(cid)
        return rt["status"] == "waiting" and rt["active_wait_groups"]

    async def _responses(self, cid, n):
        return len([m for m in await self.d.journal(cid) if m["kind"] == "capacity_response"]) >= n


@unittest.skipIf(SKIP, SKIP_REASON)
class S05FirstSuccessWithError(Scenario):
    PREFIX = "it_s05"

    async def test_all_errors_end_the_wait_with_no_success(self):
        d, cid = self.d, self.PREFIX
        await d.reset_world([
            {"when": {"continuity": cid, "stimulus": "none"},
             "then": {"output": {"wait_group": {"condition": "first_success", "expected": expect("f_{{call}}")},
                                 "intentions": [{"id": "f_{{call}}", "target_capacity": "/root/agentic/test/test.fail",
                                                 "parameters": {"message": "boom"}, "await_response": True}]}}},
            {"when": {"continuity": cid, "stimulus": "active_wait_result"}, "then": {"output": say("it failed")}},
        ])
        await d.create(cid)
        await d.send(cid, f"{cid}_m1", "try")
        await d.quiet(cid)
        await d.assert_invariants(cid)
        stimulus = (await d.llm_calls())[1]["active_stimulus"]
        self.assertEqual((stimulus["outcome"], stimulus["received"]), ("no_success", 1))              # the error itself is read in the answer, below
        response = [m for m in await d.journal(cid) if m["kind"] == "capacity_response"][0]
        self.assertEqual(response["content"]["response"]["status"], "error")
        # a tool that fails with an unexpected exception is answered with a generic failure and an error_id
        # (the detail stays in the wrapper's log): the text of the exception does not cross
        error = response["content"]["response"]["error"]
        self.assertEqual(error["details"]["reason"], "wrapper_capacity_failed")
        self.assertTrue(error["details"]["error_id"])
        self.assertNotIn("boom", error["message"])


@unittest.skipIf(SKIP, SKIP_REASON)
class S06NOfM(Scenario):
    PREFIX = "it_s06"

    async def test_two_of_three_resume_and_the_third_is_late(self):
        d, cid = self.d, self.PREFIX
        await d.reset_world([
            {"when": {"continuity": cid, "stimulus": "none"},
             "then": {"output": {"wait_group": {"condition": "n_of_m", "n": 2, "expected": expect("a_{{call}}", "b_{{call}}", "c_{{call}}")},
                                 "intentions": [slow("a_{{call}}", "g1"), slow("b_{{call}}", "g2"), slow("c_{{call}}", "g3")]}}},
            {"when": {"continuity": cid, "stimulus": "active_wait_result"}, "then": {"output": say("two is enough")}},
        ])
        await d.create(cid)
        await d.send(cid, f"{cid}_m1", "go")
        await d.until(lambda: self._waiting(cid), "waiting")
        await d.open_gate("g1")
        await asyncio.sleep(0.5)
        calls = await d.llm_calls()
        self.assertEqual(len(calls), 1, "one of three is not enough, yet the model was asked %d times: %s" % (
            len(calls), [(c.get("n"), c.get("continuity"), c.get("message_id"), c.get("stimulus"), c.get("at")) for c in calls]))
        await d.open_gate("g2")
        await d.quiet(cid)
        stimulus = (await d.llm_calls())[1]["active_stimulus"]
        self.assertEqual((stimulus["received"], stimulus["expected"]), (2, 3))
        await d.open_gate("g3")
        await d.until(lambda: self._three(cid), "the third Response in the history")
        await d.quiet(cid)
        await d.assert_invariants(cid)
        self.assertEqual(len(await d.llm_calls()), 2)

    async def _waiting(self, cid):
        rt = await self.d.runtime(cid)
        return rt["status"] == "waiting" and rt["active_wait_groups"]

    async def _three(self, cid):
        return len([m for m in await self.d.journal(cid) if m["kind"] == "capacity_response"]) == 3


@unittest.skipIf(SKIP, SKIP_REASON)
class S07DynamicProjection(Scenario):
    PREFIX = "it_s07"

    async def test_projection_request_gives_a_new_view_without_touching_the_default(self):
        d, cid = self.d, self.PREFIX
        pilot_before = await d.content(TEST, PILOT_ID)
        await d.reset_world([
            {"when": {"continuity": cid, "stimulus": "projection"}, "then": {"output": say("seen")}},
            {"when": {"continuity": cid, "message_contains": "project"}, "then": {"output": {"projection_request": {
                "projection": {"include_kinds": ["user"], "limit": 2, "selection": "last"}, "mode": "replace"}}}},
            {"when": {"continuity": cid}, "then": {"output": say("noted")}},
        ])
        await d.create(cid)
        for i in (1, 2, 3):
            await d.send(cid, f"{cid}_m{i}", f"plain {i}")
            await d.quiet(cid)
        await d.send(cid, f"{cid}_m4", "project now")
        await d.quiet(cid)
        await d.assert_invariants(cid)

        calls = [c for c in await d.llm_calls() if c["message_id"] == f"{cid}_m4"]
        self.assertEqual(len(calls), 2)                                   # decision, then the new view
        self.assertEqual(calls[0]["projection_definition"], {"exclude_kinds": ["projection", "capacity_intention", "pilot"]})   # the pilot default, and the lines of effects a pilot_definition never sends
        self.assertEqual(calls[1]["stimulus"], "projection")
        self.assertEqual(calls[1]["projection_ids"], [f"{cid}_m3", f"{cid}_m4"])                 # exactly the requested view
        self.assertEqual(calls[1]["projection_definition"]["include_kinds"], ["user"])
        self.assertEqual(await d.content(TEST, PILOT_ID), pilot_before)                    # default untouched
        events = [m["content"].get("event") for m in await d.journal(cid) if m["kind"] == "projection"]
        self.assertEqual(events, ["projection_request", "projection_result"])


@unittest.skipIf(SKIP, SKIP_REASON)
class S08IntentionUpdate(Scenario):
    PREFIX = "it_s08"

    async def test_current_intention_changes_initial_never(self):
        d, cid = self.d, self.PREFIX
        await d.reset_world([
            {"when": {"continuity": cid, "message_contains": "pivot"}, "then": {"output": {"intention_update": "Now translate everything.", "messages": say("pivoted")["messages"]}}},
            {"when": {"continuity": cid}, "then": {"output": say("ok")}},
        ])
        await d.create(cid, intention="Answer politely.")
        await d.send(cid, f"{cid}_m1", "pivot please")
        await d.quiet(cid)
        await d.send(cid, f"{cid}_m2", "anything")
        await d.quiet(cid)
        await d.assert_invariants(cid)
        c = await d.continuity(cid)
        self.assertEqual((c["initial_intention"], c["current_intention"]), ("Answer politely.", "Now translate everything."))
        calls = await d.llm_calls()
        self.assertEqual(calls[0]["current_intention"], "Answer politely.")
        self.assertEqual((calls[1]["initial_intention"], calls[1]["current_intention"]), ("Answer politely.", "Now translate everything."))
        events = [m["content"].get("event") for m in c["messages"] if m["kind"] == "event" and m["content"].get("event") != "inference_measured"]
        self.assertEqual(events, ["intention_update"])


if __name__ == "__main__":
    unittest.main()
