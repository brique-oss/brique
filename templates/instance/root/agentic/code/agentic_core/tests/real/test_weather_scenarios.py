"""Weather scenarios (W1, W2): the agentic mechanics meeting the real world.

weather.fetch makes a real HTTP request (open-meteo, no key) from the test_tools wrapper: real latency, data that changes from
one run to the next, a service that can be down. A pilot reads the weather and says what to wear. Run with BRIQUE_REAL_LLM=1;
two to three inferences of a few words each.

The judgement is structural: the capacity was called once with the right city, real numbers came back, and they reached the
pilot (W1: through the history of the continuity; W2: through the input of the prompt). W2's prompt is the Matter weather_advice_prompt, read by
pilot.infer; W1's is the prompt of its pilot_definition: two pilots, two prompts, the same path to the model. The advice itself is never asserted.
"""
import json
import os
import time
import unittest

from ..integration.driver import AGENTIC, TEST
from .harness import QUIET_TIMEOUT, REAL, SKIP_REASON, RealDriver, pilot_texts
from .test_tiny_scenarios import spent

MATTER_PILOT = {"context": TEST, "matter_id": "real_weather"}
PLAN_PILOT = {"context": TEST, "capacity": "weather_advice"}
REPORT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "last_weather_report.json")
MEASURES = ("temperature_c", "feels_like_c", "precipitation_mm", "wind_kmh")
PROMPT_CTX, PROMPT_MATTER = "/root/agentic/test", "weather_advice_prompt"      # the prompt of the plan is a Matter, not text in the plan


def is_number(value):
    return isinstance(value, (int, float)) and not isinstance(value, bool)


@unittest.skipUnless(REAL, SKIP_REASON)
class WeatherScenarios(unittest.IsolatedAsyncioTestCase):
    reports = {}

    async def asyncSetUp(self):
        self.d = await RealDriver().__aenter__()
        self.ids = []

    async def asyncTearDown(self):
        try:
            await self.d.cleanup(*self.ids)
        finally:
            await self.d.__aexit__()
            if WeatherScenarios.reports:
                with open(REPORT, "w", encoding="utf-8") as fh:
                    json.dump({"at": time.strftime("%Y-%m-%dT%H:%M:%S"), "scenarios": WeatherScenarios.reports}, fh, ensure_ascii=False, indent=1)

    async def start(self, cid):
        self.ids = [cid]
        await self.d.cleanup(cid)
        await self.d.reset_world()

    async def weather_calls(self):
        return [c for c in await self.d.tool_calls("weather.fetch")]

    # -- W1: a Matter pilot decides to look the weather up, waits for it, then advises ---------------------------------------
    async def test_w1_a_matter_pilot_reads_the_real_weather_then_advises(self):
        d, cid = self.d, "real_w1"
        await self.start(cid)
        await d.create(cid, intention="Say what to wear this morning.", pilot_ref=MATTER_PILOT)
        await d.send(cid, f"{cid}_m1", "What should I wear this morning in Paris?")
        await d.quiet(cid, timeout=QUIET_TIMEOUT)
        await d.assert_invariants(cid)
        calls = await d.real_calls()
        report = WeatherScenarios.reports["W1"] = spent(calls)
        journal = await d.journal(cid)

        self.assertEqual(report["errors"], [])
        self.assertEqual(report["inferences"], 2)                                   # decide to look it up, then advise
        fetched = await self.weather_calls()
        self.assertEqual([(c["state"], str(c["params"].get("city", "")).lower()) for c in fetched], [("done", "paris")])
        answers = [m for m in journal if m["kind"] == "capacity_response"]
        self.assertEqual(len(answers), 1)
        payload = answers[0]["content"]["response"]["payload"]
        self.assertEqual(answers[0]["content"]["response"]["status"], "ok")
        self.assertTrue(all(is_number(payload.get(k)) for k in MEASURES), payload)  # real numbers came back
        self.assertTrue(payload.get("observed_at"))
        texts = pilot_texts(journal)
        self.assertEqual(len(texts), 1)
        self.assertTrue(texts[0].strip())
        self.assertEqual([m["kind"] for m in journal if m["kind"] not in ("event",)],
                         ["user", "completion", "capacity_intention", "capacity_response", "completion", "pilot"])      # each decision, then its effects
        report["measures"] = {k: payload[k] for k in MEASURES}
        report["advice"] = texts[0]
        # this pilot's prompt is its own definition: it is not the prompt Matter the plan uses
        prompt_text = await d._text(PROMPT_CTX, PROMPT_MATTER)
        self.assertNotIn(prompt_text, str(calls[0]["request"]["messages"][0]["content"]))

    # -- W2: a plan reads the weather deterministically and asks one prompt ---------------------------------------------------
    async def test_w2_a_plan_reads_the_real_weather_and_feeds_one_prompt(self):
        d, cid = self.d, "real_w2"
        await self.start(cid)
        await d.create(cid, intention="Say what to wear this morning.", pilot_ref=PLAN_PILOT)
        await d.send(cid, f"{cid}_m1", {"city": "lyon"})
        await d.quiet(cid, timeout=QUIET_TIMEOUT)
        await d.assert_invariants(cid)
        calls = await d.real_calls()
        report = WeatherScenarios.reports["W2"] = spent(calls)
        journal = await d.journal(cid)

        self.assertEqual(report["errors"], [])
        self.assertEqual(report["inferences"], 1)
        fetched = await self.weather_calls()
        self.assertEqual([(c["state"], str(c["params"].get("city", "")).lower()) for c in fetched], [("done", "lyon")])
        self.assertEqual([m["kind"] for m in journal if m["kind"] != "event"], ["user", "prompt", "completion", "pilot"])   # the lookup leaves no trace
        prompt = [m for m in journal if m["kind"] == "prompt"][0]["content"]
        # the weather is the last part of the prompt, as compact JSON: the real numbers fed the prompt
        observed = json.loads(prompt["instructions"].split("\n\n")[-1])
        self.assertTrue(all(is_number(observed.get(k)) for k in MEASURES), observed)
        # the prompt of the step is the text of the Matter, assembled by pilot.infer: the plan holds no prompt of its own
        matter_text = await d._text(PROMPT_CTX, PROMPT_MATTER)
        self.assertTrue(prompt["instructions"].startswith(matter_text + "\n\n"))              # the Matter first, then the weather as data
        self.assertEqual(calls[0]["request"]["messages"][0]["content"], prompt["instructions"])          # what is sent is the prompt recorded
        texts = pilot_texts(journal)
        self.assertEqual(len(texts), 1)
        self.assertTrue(texts[0].strip())
        report["measures"] = {k: observed[k] for k in MEASURES}
        report["advice"] = texts[0]


if __name__ == "__main__":
    unittest.main()
