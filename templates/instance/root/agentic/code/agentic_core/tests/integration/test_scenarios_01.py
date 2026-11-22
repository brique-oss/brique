import unittest

from .scenarios_base import SKIP, SKIP_REASON, Scenario, say


@unittest.skipIf(SKIP, SKIP_REASON)
class S01QuestionAndAnswer(Scenario):
    PREFIX = "it_s01"

    async def test_question_and_answer(self):
        d, cid = self.d, self.PREFIX
        await d.reset_world([{"when": {"message_contains": "hello", "continuity": cid}, "then": {"output": say("hi back from {{cid}}")}}])
        await d.create(cid, intention="Answer politely.")
        out = await d.send(cid, f"{cid}_m1", "hello there")
        self.assertEqual((out["seq"], out["priority"]), (2, "normal"))                # the first line of the journal is the cognitive frame
        await d.quiet(cid)
        await d.assert_invariants(cid)

        journal = await d.journal(cid)
        self.assertEqual(self.kinds(journal), ["user", "completion", "pilot"])          # the decision, then its effect
        answer = [m for m in journal if m["kind"] == "pilot"]            # not by position: bookkeeping lines come between
        self.assertEqual([m["content"]["content"] for m in answer], [f"hi back from {cid}"])
        calls = await d.llm_calls()
        self.assertEqual(len(calls), 1)
        # the context reaches the (fake) LLM as a conversation: the stable system (with the initial intention), the journal, what is new
        self.assertEqual(calls[0]["context_keys"], ["continuity_id", "current_intention", "current_message"])
        self.assertEqual((calls[0]["initial_intention"], calls[0]["current_intention"]), ("Answer politely.", "Answer politely."))
        self.assertEqual((calls[0]["message_id"], calls[0]["stimulus"], calls[0]["projection_ids"]), (f"{cid}_m1", "none", [f"{cid}_m1"]))
        self.assertIn("SCENARIO-DRIVEN TEST PILOT", calls[0]["system_prompt"])


@unittest.skipIf(SKIP, SKIP_REASON)
class S02FireAndForget(Scenario):
    PREFIX = "it_s02"

    async def test_fire_and_forget_intention(self):
        d, cid = self.d, self.PREFIX
        await d.reset_world([{"when": {"message_contains": "go", "continuity": cid},
                              "then": {"output": {"intentions": [{"id": "ff_{{call}}", "target_capacity": "/root/agentic/test/test.echo",
                                                                  "parameters": {"payload": "fire"}, "await_response": False}],
                                                  "messages": [{"to": "user:1", "kind": "user", "content": "launched"}]}}}])
        await d.create(cid)
        await d.send(cid, f"{cid}_m1", "go")
        await d.quiet(cid)
        await d.assert_invariants(cid)
        await d.until(lambda: self._echo_called(), "the fire-and-forget tool to run")
        calls = await d.tool_calls("test.echo")
        self.assertEqual(len(calls), 1)                                   # exactly once
        journal = await d.journal(cid)
        self.assertEqual(sorted(self.kinds(journal)), ["capacity_intention", "completion", "pilot", "user"])   # no Response, no wait group; the decision is a line of its own
        self.assertEqual((await d.llm_calls())[0]["call_index"], 1)
        self.assertEqual(len(await d.llm_calls()), 1)

    async def _echo_called(self):
        return len(await self.d.tool_calls("test.echo")) >= 1


@unittest.skipIf(SKIP, SKIP_REASON)
class S03WaitForTwoTools(Scenario):
    PREFIX = "it_s03"

    async def test_all_condition_resumes_once_with_both_results(self):
        d, cid = self.d, self.PREFIX
        wait = {"condition": "all", "expected": [{"type": "intention", "ref": "a_{{call}}"}, {"type": "intention", "ref": "b_{{call}}"}]}
        await d.reset_world([
            {"when": {"message_contains": "work", "continuity": cid, "stimulus": "none"},
             "then": {"output": {"wait_group": wait, "intentions": [
                 {"id": "a_{{call}}", "target_capacity": "/root/agentic/test/test.slow", "parameters": {"gate": "gA", "value": "A"}, "await_response": True},
                 {"id": "b_{{call}}", "target_capacity": "/root/agentic/test/test.slow", "parameters": {"gate": "gB", "value": "B"}, "await_response": True}]}}},
            {"when": {"stimulus": "active_wait_result", "continuity": cid}, "then": {"output": say("both done")}},
        ])
        await d.create(cid)
        await d.send(cid, f"{cid}_m1", "work please")

        async def waiting():
            rt = await d.runtime(cid)
            return rt["status"] == "waiting" and len(rt["active_wait_groups"]) == 1 and rt
        rt = await d.until(waiting, "the continuity to wait")
        self.assertEqual(rt["current_message_id"], f"{cid}_m1")
        self.assertEqual([e["status"] for e in await d.mailbox(cid)], ["claimed"])         # the message stays owned by the run
        self.assertEqual(len(await d.llm_calls()), 1)                                         # no reasoning while waiting

        await d.open_gate("gA")
        await d.until(lambda: self._received(cid, 1), "the first result")
        self.assertEqual((await d.runtime(cid))["status"], "waiting")                         # `all` is not satisfied yet
        await d.open_gate("gB")
        await d.quiet(cid)
        await d.assert_invariants(cid)

        calls = await d.llm_calls()
        self.assertEqual(len(calls), 2)
        stimulus = calls[1]["active_stimulus"]
        self.assertTrue(stimulus.pop("wait_group_id"))
        self.assertEqual(stimulus, {"kind": "active_wait_result", "outcome": "satisfied", "received": 2, "expected": 2})     # and only what the model needs
        journal = await d.journal(cid)
        responses = [m for m in journal if m["kind"] == "capacity_response"]
        self.assertEqual(sorted(m["correlation"]["intention_id"] for m in responses), ["a_1", "b_1"])
        self.assertEqual(len(responses), stimulus["received"])                       # the stimulus counts the answers that the history holds
        self.assertEqual(self.kinds(journal).count("pilot"), 1)

    async def _received(self, cid, n):
        rt = await self.d.runtime(cid)
        groups = rt["active_wait_groups"]
        return bool(groups) and len(groups[0]["received"]) >= n


if __name__ == "__main__":
    unittest.main()
