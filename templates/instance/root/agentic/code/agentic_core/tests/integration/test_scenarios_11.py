import unittest

from .driver import AGENTIC, TEST
from .scenarios_base import SKIP, SKIP_REASON, Scenario

PLAN = {"context": TEST, "capacity": "investigate_orders"}


@unittest.skipIf(SKIP, SKIP_REASON)
class S30PlanWithPromptsActivationAndLookups(Scenario):
    """One Capacity pilot combines ordinary capacities, pilot.infer prompts and a sub-continuity run by pilot.activate:
    each step feeds the next, and the sub-continuity's result is the input of a prompt."""
    PREFIX = "it_s30"

    async def test_lookup_feeds_a_prompt_which_opens_a_subcontinuity_whose_result_feeds_a_prompt(self):
        d, cid = self.d, self.PREFIX
        kid = f"{cid}_kid"
        await d.reset_world([
            {"when": {"step": "triage"}, "then": {"output": {"order_id": "A300"}}},
            {"when": {"step": "conclude"}, "then": {"output": {"answer": "A300 is held at customs: expect a delay."}}},
            # the sub-continuity is a pilot_definition driven by the fake model: it uses a tool, waits, then answers
            {"when": {"continuity": kid, "stimulus": "none"},
             "then": {"output": {"intentions": [{"id": "look_{{call}}", "target_capacity": "/root/agentic/test/test.echo",
                                                 "parameters": {"payload": "checking the customs file"}, "await_response": True}],
                                 "wait_group": {"condition": "all", "expected": [{"type": "intention", "ref": "look_{{call}}"}]}}}},
            {"when": {"continuity": kid, "stimulus": "active_wait_result"},
             "then": {"output": {"messages": [{"to": "user:1", "kind": "user", "content": "order A300 is blocked at customs since Monday"}]}}},
        ])
        await d.create(cid, intention="Investigate the customer's orders.", pilot_ref=PLAN)
        await d.send(cid, f"{cid}_m1", {"customer_id": "C1", "child_id": kid, "child_message_id": f"{kid}_task"})
        await d.quiet(cid, kid)
        await d.assert_invariants(cid, kid)

        # --- the parent: only its prompts and its answer, never the lookups or the sub-continuity's steps
        journal = await d.journal(cid)
        self.assertEqual(self.kinds(journal), ["user", "prompt", "completion", "prompt", "completion", "pilot"])
        self.assertEqual([m["content"]["step"] for m in journal if m["kind"] == "prompt"], ["triage", "conclude"])
        self.assertEqual([m["content"]["content"] for m in journal if m["kind"] == "pilot"], ["A300 is held at customs: expect a delay."])

        # --- the prompts were fed by what the other steps returned
        inputs = {c["step"]: c["system_prompt"] for c in await d.llm_calls() if c.get("step")}       # the data is a part of each prompt
        self.assertIn("A300", inputs["triage"])                                        # the records fetched by an ordinary capacity
        self.assertIn("delayed", inputs["triage"])                                     # ... with the status that makes the choice possible
        self.assertIn("A100", inputs["triage"])
        self.assertIn("blocked at customs since Monday", inputs["conclude"])           # the sub-continuity's answer

        # --- the sub-continuity: a continuity of its own, child of the plan's, with its own history
        child = await d.continuity(kid)
        self.assertEqual(child["parent_id"], cid)
        self.assertEqual(self.kinds(child["messages"]), ["user", "completion", "event", "capacity_intention", "capacity_response", "completion", "event", "pilot"])   # each decision, then its effects
        self.assertEqual(child["messages"][0]["content"], {"investigate": "A300"})        # fed by the triage prompt's output
        self.assertEqual(len(await d.tool_calls("world.get_customer")), 1)
        self.assertEqual(len(await d.tool_calls("world.get_order")), 2)                    # the plan's loop: one lookup per open order
        self.assertEqual(len(await d.tool_calls("test.echo")), 1)                          # the child's own tool
        self.assertEqual((await d.continuity(cid))["children"], [kid])

    def cids(self):
        return [self.PREFIX, f"{self.PREFIX}_kid"]


if __name__ == "__main__":
    unittest.main()
