import json
import unittest

from .driver import AGENTIC, TEST, DriverError
from .scenarios_base import SKIP, SKIP_REASON, Scenario

PLAN = {"context": TEST, "capacity": "review_orders"}


@unittest.skipIf(SKIP, SKIP_REASON)
class S26CapacityPilot(Scenario):
    """A real `dsl` Capacity decides for the continuity: deterministic lookups, one prompt per order, one summary prompt."""
    PREFIX = "it_s26"

    async def test_a_plan_with_a_loop_and_recorded_prompts(self):
        d, cid = self.d, self.PREFIX
        await d.reset_world([
            {"when": {"step": "analyse", "prompt_contains": "A100"}, "then": {"output": {"verdict": "ok", "why": "shipped, on time"},
                                                                              "usage": {"input_tokens": 40}}},
            {"when": {"step": "analyse", "prompt_contains": "A300"}, "then": {"output": {"verdict": "risk", "why": "held at customs"}}},
            {"when": {"step": "summary"}, "then": {"output": {"summary": "A100 is on time, A300 is at risk (customs)."}}},
        ])
        await d.create(cid, intention="Review the open orders.", pilot_ref=PLAN)
        await d.send(cid, f"{cid}_m1", {"customer_id": "C1"})
        await d.quiet(cid)
        await d.assert_invariants(cid)

        journal = await d.journal(cid)
        # the history holds the prompts and their completions, and the final answer: not the lookups
        # the two analyses run side by side (the plan's for_each has a limit of 2), so their lines may interleave:
        # what holds is the frame, the count, and that every completion follows its own prompt
        kinds = self.kinds(journal)
        self.assertEqual((kinds[0], kinds[-1]), ("user", "pilot"))
        self.assertEqual((kinds.count("prompt"), kinds.count("completion")), (3, 3))
        position = {m["id"]: i for i, m in enumerate(journal)}
        for completion in (m for m in journal if m["kind"] == "completion"):
            self.assertLess(position[completion["correlation"]["causation_id"]], position[completion["id"]])
        self.assertEqual(sorted(m["content"]["step"] for m in journal if m["kind"] == "prompt"), ["analyse", "analyse", "summary"])
        # the order each analysis is about is a part of its prompt, as compact JSON
        by_order = {order: m for m in journal if m["kind"] == "prompt" and m["content"]["step"] == "analyse"
                    for order in ("A100", "A300") if f'"order_id":"{order}"' in m["content"]["instructions"]}
        self.assertEqual(sorted(by_order), ["A100", "A300"])
        verdicts = [m["content"]["output"]["verdict"] for m in journal if m["kind"] == "completion" and "why" in m["content"]["output"]]
        self.assertEqual(sorted(verdicts), ["ok", "risk"])
        self.assertEqual([m["content"]["content"] for m in journal if m["kind"] == "pilot"], ["A100 is on time, A300 is at risk (customs)."])
        # deterministic actions ran once each and left no trace in the continuity
        self.assertEqual(len(await d.tool_calls("world.get_customer")), 1)
        self.assertEqual(len(await d.tool_calls("world.get_order")), 2)
        self.assertNotIn("capacity_intention", self.kinds(journal))
        self.assertEqual(len(await d.llm_calls()), 3)                              # one inference per prompt, no more
        # every prompt is correlated to the message in progress, every completion to its prompt
        prompts = {m["id"]: m for m in journal if m["kind"] == "prompt"}
        self.assertEqual({m["correlation"]["causation_id"] for m in prompts.values()}, {f"{cid}_m1"})
        self.assertEqual({m["correlation"]["causation_id"] for m in journal if m["kind"] == "completion"}, set(prompts))
        runtime = await d.runtime(cid)
        self.assertEqual(await d.measures(cid), [])                               # no step saw the journal: no measure

    async def test_the_pilot_can_be_switched_between_a_matter_and_a_capacity(self):
        d, cid = self.d, f"{self.PREFIX}_sw"
        await d.reset_world([{"when": {"continuity": cid, "message_contains": "hello"},
                              "then": {"output": {"messages": [{"to": "user:1", "kind": "user", "content": "from the pilot_definition"}]}}},
                             {"when": {"step": "summary"}, "then": {"output": {"summary": "from the plan"}}},
                             {"when": {"step": "analyse"}, "then": {"output": {"verdict": "ok", "why": "-"}}}])
        await d.create(cid)                                                           # a pilot_definition first
        await d.send(cid, f"{cid}_m1", "hello")
        await d.quiet(cid)
        await d.control(cid, f"{cid}_c1", "set_pilot", pilot_ref=PLAN)
        await d.quiet(cid)
        self.assertEqual((await d.continuity(cid))["pilot_ref"], PLAN)
        await d.send(cid, f"{cid}_m2", {"customer_id": "C2"})                         # C2 has no open order: summary only
        await d.quiet(cid)
        texts = [m["content"]["content"] for m in await d.journal(cid) if m["kind"] == "pilot"]
        self.assertEqual(texts, ["from the pilot_definition", "from the plan"])
        await d.assert_invariants(cid)

    def cids(self):
        return [self.PREFIX, f"{self.PREFIX}_sw"]


@unittest.skipIf(SKIP, SKIP_REASON)
class S27CapacityPilotFailures(Scenario):
    PREFIX = "it_s27"

    async def test_a_step_that_does_not_conform_fails_the_treatment_and_is_not_a_completion(self):
        d, cid = self.d, self.PREFIX
        await d.reset_world([
            {"when": {"step": "analyse"}, "then": {"output": {"verdict": "maybe", "why": 3}}},       # outside the step's own schema
        ])
        await d.create(cid, pilot_ref=PLAN)
        await d.send(cid, f"{cid}_m1", {"customer_id": "C1"})
        await d.quiet(cid)
        journal = await d.journal(cid)
        self.assertNotIn("completion", self.kinds(journal))
        errors = [m["content"]["event"] for m in journal if m["kind"] == "error"]
        self.assertIn("step_output_invalid", errors)
        self.assertIn("treatment_failed", errors)
        self.assertEqual([e["status"] for e in await d.mailbox(cid)], ["failed"])           # released each time, parked on the third, never completed

    async def test_an_unknown_capacity_is_refused_at_creation(self):
        with self.assertRaises(DriverError):
            await self.d.create(self.PREFIX, pilot_ref={"context": TEST, "capacity": "no_such_plan"})
        self.assertTrue(await self.d.gone(self.PREFIX))


if __name__ == "__main__":
    unittest.main()
