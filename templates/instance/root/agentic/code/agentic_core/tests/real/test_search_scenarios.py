"""Search scenario (R-SEARCH): a Capacity pilot whose prompt aggregates the prompt Matter /root/agentic/prompt/brique_search.

The pilot is the Capacity /root/agentic/test/search_pilot: a plan with one pilot.infer per round, whose prompt is a task text written in
the plan followed by the Matter brique_search (prompt_matters). The plan's answer is a pilot_output: the model asks for queries of
the engine (intentions and a wait_group), pilot.activate sends them, waits, and invokes the plan again with the answers in the
history, until the model reports a JSON array of {kind, context, name, why}. Run with BRIQUE_REAL_LLM=1; a few inferences.

The judgement is structural and about the mechanics: the model searched through the capacities of the engine, the steps came in an
order that respects their dependencies, what it reports was returned by a query, and the report has the shape asked for. Which element it
finds first is asserted loosely (weather.fetch must be in the list), the wording of 'why' never.
"""
import json
import os
import re
import time
import unittest

from ..integration.driver import TEST
from .harness import QUIET_TIMEOUT, REAL, SKIP_REASON, RealDriver, pilot_texts
from .test_tiny_scenarios import spent

PROMPT_CTX, PROMPT_MATTER = "/root/agentic/prompt", "brique_search"
PILOT_REF = {"context": TEST, "capacity": "search_pilot"}
REPORT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "last_search_report.json")
SEARCH_CAPACITIES = ("read.capacity", "vocabulary.query", "meaning.query", "read.meaning")


@unittest.skipUnless(REAL, SKIP_REASON)
class SearchScenarios(unittest.IsolatedAsyncioTestCase):
    reports = {}

    async def asyncSetUp(self):
        self.d = await RealDriver().__aenter__()
        self.ids = []

    async def asyncTearDown(self):
        try:
            await self.d.cleanup(*self.ids)
        finally:
            await self.d.__aexit__()
            if SearchScenarios.reports:
                with open(REPORT, "w", encoding="utf-8") as fh:
                    json.dump({"at": time.strftime("%Y-%m-%dT%H:%M:%S"), "scenarios": SearchScenarios.reports}, fh, ensure_ascii=False, indent=1)

    async def test_search_the_capacity_that_gives_the_weather(self):
        d, cid = self.d, "real_search1"
        self.ids = [cid]
        await d.cleanup(cid)
        await d.reset_world()
        await d.create(cid, intention="Find elements of the Brique world.", pilot_ref=PILOT_REF)
        await d.send(cid, f"{cid}_m1", "Which capacity gives the current weather of a city?")
        await d.quiet(cid, timeout=QUIET_TIMEOUT)
        await d.assert_invariants(cid)
        calls = await d.real_calls()
        report = SearchScenarios.reports["SEARCH"] = spent(calls)
        journal = await d.journal(cid)

        # the search method reaches the model once, in the conversation, and is not sent again by the later rounds
        method = await d._text(PROMPT_CTX, PROMPT_MATTER)
        for call in calls:
            self.assertEqual(sum(m["content"].count(method) for m in call["request"]["messages"]), 1)
        self.assertTrue(calls[0]["request"]["messages"][0]["content"].startswith("## How you act in the Brique world"))     # the frame, as the system text
        # the cache: what a round sent is the start of what the next sends, and the provider says how much it read back
        report["cached_input_tokens"] = [c["usage"].get("cached_input_tokens", 0) for c in calls]
        report["input_tokens_per_call"] = [c["usage"].get("input_tokens", 0) for c in calls]
        # where the tokens go (characters, a tool to see where to save, not a contract): the last request by part, and each tool return
        size = lambda v: len(v if isinstance(v, str) else json.dumps(v, ensure_ascii=False, sort_keys=True, separators=(",", ":")))
        last = calls[-1]["request"]
        report["last_request_chars"] = {"system": sum(size(m["content"]) for m in last["messages"] if m["role"] == "system"),
                                        "output_schema": size(last["output"]["schema"]),
                                        "history": sum(size(m["content"]) for m in last["messages"][1:-1]),
                                        "news": size(last["messages"][-1]["content"])}
        by_id = {c["id"]: str(c.get("target_capacity", "")) for m in journal if m["kind"] == "capacity_intention"
                 for c in (m["content"].get("intentions") or [m["content"]])}
        report["tool_returns_chars"] = [[by_id.get((m.get("correlation") or {}).get("intention_id"), "?"), size(m["content"])]
                                        for m in journal if m["kind"] == "capacity_response"]
        report["completions_chars"] = [size(m["content"].get("output")) for m in journal if m["kind"] == "completion"]
        self.assertEqual(report["errors"], [])
        # it searched: intentions went to the reading capacities, as the prompt says, and every one was answered
        asked = [m["content"] for m in journal if m["kind"] == "capacity_intention"]
        targets = [str(i.get("target_capacity", "")) for c in asked for i in (c.get("intentions") or [c])]
        report["targets"] = targets
        self.assertTrue(targets, "the model searched nothing")
        self.assertTrue(all(any(t.endswith(name) for name in SEARCH_CAPACITIES) for t in targets), targets)
        self.assertTrue(any(t.endswith("meaning.query") for t in targets), targets)
        # the steps depend on one another: nothing is asked of meaning.query before the vocabulary has answered, nothing is read
        # (read.meaning) before meaning.query has answered; queries of the same step may be in flight together
        sequence = [(m["kind"], str(m["content"].get("target_capacity", ""))) for m in journal if m["kind"] in ("capacity_intention", "capacity_response")]
        report["sequence"] = sequence
        voc_answered = next((i for i, (k, _) in enumerate(sequence) if k == "capacity_response"), None)   # contracts may be read before
        self.assertIsNotNone(voc_answered, sequence)
        first_meaning = next((i for i, (k, t) in enumerate(sequence) if k == "capacity_intention" and t.endswith("meaning.query")), None)
        first_vocabulary_answer = None
        seen_vocabulary = False
        for i, (k, t) in enumerate(sequence):
            if k == "capacity_intention" and t.endswith("vocabulary.query"):
                seen_vocabulary = True
            elif k == "capacity_response" and seen_vocabulary:
                first_vocabulary_answer = i
                break
        self.assertIsNotNone(first_vocabulary_answer, sequence)
        self.assertGreater(first_meaning, first_vocabulary_answer, sequence)                # no meaning.query before the vocabulary answered
        answers = [m for m in journal if m["kind"] == "capacity_response"]
        self.assertEqual(len(answers), len(asked))
        # the report: a JSON array of {kind, context, name, why}
        texts = pilot_texts(journal)
        self.assertEqual(len(texts), 1)
        found = re.search(r"\[.*\]", texts[0], re.S)
        self.assertTrue(found, texts[0])
        listed = json.loads(found.group(0))
        report["listed"] = listed
        self.assertTrue(listed)
        for item in listed:
            self.assertEqual(set(item), {"kind", "context", "name", "why"}, item)
        self.assertIn(("capacity", "weather.fetch"), [(i["kind"], i["name"]) for i in listed])
        # nothing reported from memory: each element listed appears in what a query returned
        returned = json.dumps([m["content"] for m in answers], ensure_ascii=False)
        for item in listed:
            self.assertIn(item["name"], returned, item)


if __name__ == "__main__":
    unittest.main()
