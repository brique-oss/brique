"""Prompt-assembly scenarios with a real model (P1): the mechanism, not the model.

A Matter pilot whose prompt is its own definition and a plan whose prompt is assembled by pilot.infer from a list of texts and a list of Matters are
woken together, each with a different trivial task. The judgement is on the mechanism only: what the journal says was sent
(the assembled text, in the order given), how many inferences, the shape of each history. What the model answers is never asserted.
Run with BRIQUE_REAL_LLM=1; about 2 000 tokens.
"""
import asyncio
import json
import os
import time
import unittest

from ..integration.driver import AGENTIC, TEST, DriverError
from .harness import QUIET_TIMEOUT, REAL, SKIP_REASON, RealDriver, pilot_texts
from .test_tiny_scenarios import MINI, spent

PROBE = {"context": TEST, "capacity": "parts_probe"}
MODEL = {"provider": "recorded", "model": "claude-haiku-4-5-20251001", "parameters": {"max_output_tokens": 40, "temperature": 0, "upstream_provider": "anthropic"}}
REPORT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "last_parts_report.json")
MATTER_ID = "real_p1_task"


@unittest.skipUnless(REAL, SKIP_REASON)
class PartsScenarios(unittest.IsolatedAsyncioTestCase):
    reports = {}

    async def asyncSetUp(self):
        self.d = await RealDriver().__aenter__()
        self.ids = []

    async def asyncTearDown(self):
        try:
            await self.d.cleanup(*self.ids)
            try:
                await self.d.call(TEST, "matter.delete", "matter", {"matter_id": MATTER_ID})
            except DriverError:
                pass
        finally:
            await self.d.__aexit__()
            if PartsScenarios.reports:
                with open(REPORT, "w", encoding="utf-8") as fh:
                    json.dump({"at": time.strftime("%Y-%m-%dT%H:%M:%S"), "scenarios": PartsScenarios.reports}, fh, ensure_ascii=False, indent=1)

    async def test_p1_a_matter_pilot_and_an_assembling_plan_side_by_side(self):
        d = self.d
        m, p = "real_p1_matter", "real_p1_plan"
        self.ids = [m, p]
        await d.cleanup(m, p)
        await d.reset_world()
        try:
            await d.call(TEST, "matter.delete", "matter", {"matter_id": MATTER_ID})
        except DriverError:
            pass
        task = "Name the animal in the data."
        await d.call(TEST, "matter.create", "matter", {"matter_id": MATTER_ID, "payload": task, "matter": {
            "brique": {"kind": "matter", "substance_mode": "brique", "substance_type": "text"}, "functional": {"format": "txt"},
            "objective": {"name": MATTER_ID, "description": "Prompt part of the real prompt-assembly scenario."}, "subjective": {}}})
        texts = ["You answer with one word.", "Lowercase, no punctuation."]
        await d.create(m, intention="Answer in one word.", pilot_ref=MINI)
        await d.create(p, intention="Probe.", pilot_ref=PROBE)
        await asyncio.gather(d.send(m, f"{m}_m1", "Quelle est la couleur du ciel ?"),
                             d.send(p, f"{p}_m1", {"texts": texts + ["a cat on a mat"], "matters": [f"{TEST}/{MATTER_ID}"], "order": "matters_first",
                                                   "model": MODEL, "framework": None}))
        await d.quiet(m, p, timeout=QUIET_TIMEOUT)
        await d.assert_invariants(m, p)
        calls = await d.real_calls()
        report = PartsScenarios.reports["P1"] = spent(calls)

        self.assertEqual(report["errors"], [])
        self.assertEqual(report["inferences"], 2)                                                    # one for each pilot
        expected = "\n\n".join([task] + texts + ["a cat on a mat"])                                # the Matter first, then the parts, the data last
        plan_journal = await d.journal(p)
        self.assertEqual([x["kind"] for x in plan_journal if x["kind"] != "event"], ["user", "prompt", "completion", "pilot"])
        prompt = [x for x in plan_journal if x["kind"] == "prompt"][0]["content"]
        self.assertEqual(prompt["instructions"], expected)                                          # the text assembled, in the order given
        sent = [c for c in calls if c["request"]["messages"][0]["content"] == expected]                          # exactly that text
        self.assertEqual(len(sent), 1)                                                              # and that text, exactly, is what the model got
        self.assertEqual([x["kind"] for x in await d.journal(m) if x["kind"] != "event"], ["user", "completion", "pilot"])
        self.assertTrue(pilot_texts(plan_journal)[0].strip() and pilot_texts(await d.journal(m))[0].strip())
        report["plan_prompt"] = expected


if __name__ == "__main__":
    unittest.main()
