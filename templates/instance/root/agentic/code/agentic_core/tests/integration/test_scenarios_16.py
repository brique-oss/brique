import unittest

from .driver import AGENTIC, TEST
from .scenarios_base import SKIP, SKIP_REASON, say
from .test_scenarios_14 import PROBE, PartsScenario, content, probe_rule

PROMPT_CTX, FRAME_MATTER = "/root/agentic/prompt", "pilot_framework"


@unittest.skipIf(SKIP, SKIP_REASON)
class S45CognitiveFrame(PartsScenario):
    """Every continuity opens with the cognitive frame of the pilots, copied from the Matter pilot_framework as the first line of its
    journal (kind framework). A Matter pilot has it at the head of its system text; a plan has it only when its step asks (framework),
    and never inside the prompt the history keeps."""
    PREFIX = "it_s45"

    def cids(self):
        return [f"{self.PREFIX}_matter", f"{self.PREFIX}_framed", f"{self.PREFIX}_bare"]

    async def frame_text(self):
        read = await self.d.call(PROMPT_CTX, "matter.read", "matter", {"matter_id": FRAME_MATTER, "read_mode": "data"})
        return read["data"]["bytes"]

    async def test_the_frame_opens_the_journal_and_the_pilot_that_asks_for_it(self):
        d = self.d
        m, framed, bare = self.cids()
        frame = await self.frame_text()
        self.assertTrue(frame.strip())
        await d.reset_world([probe_rule(), {"when": {"continuity": m}, "then": {"output": say("matter pilot answered")}}])
        await d.create(m, intention="Matter pilot.")
        await d.create(framed, intention="Plan with the frame.", pilot_ref=PROBE)
        await d.create(bare, intention="Plan without the frame.", pilot_ref=PROBE)
        await d.send(m, f"{m}_m1", "hello")
        await d.send(framed, f"{framed}_m1", content(["Intro."], framework=True))
        await d.send(bare, f"{bare}_m1", content(["Intro."]))
        await d.quiet(m, framed, bare, timeout=40)
        await d.assert_invariants(m, framed, bare)

        for cid in (m, framed, bare):                                                     # the first line of every journal
            first = (await d.journal(cid, frame=True))[0]
            self.assertEqual((first["kind"], first["seq"], first["id"]), ("framework", 1, f"framework_{cid}"))
            self.assertEqual(first["content"], {"text": frame, "source": f"{PROMPT_CTX}/{FRAME_MATTER}"})
            self.assertNotIn("framework", self.kinds(await d.journal(cid)))               # and nothing else of that kind

        calls = await d.llm_calls()
        by_matter = [c for c in calls if c["continuity"] == m]
        by_framed, by_bare = self.asked(calls, framed), self.asked(calls, bare)
        self.assertEqual((len(by_matter), len(by_framed), len(by_bare)), (1, 1, 1))
        self.assertTrue(str(by_matter[0]["system_prompt"]).startswith(frame + "\n\n"))     # a Matter pilot: the frame, then its own prompt
        self.assertEqual(str(by_matter[0]["system_prompt"]).count(frame), 1)
        self.assertEqual(by_framed[0]["system_prompt"], frame + "\n\nIntro.")             # a plan that asked: the frame, then its prompt
        self.assertEqual(by_bare[0]["system_prompt"], "Intro.")                           # a plan that did not: its prompt, as before
        for cid in (framed, bare):                                                        # the history keeps the prompt, not the frame
            prompts = [p["content"]["instructions"] for p in await d.journal(cid) if p["kind"] == "prompt"]
            self.assertEqual(prompts, ["Intro."])


if __name__ == "__main__":
    unittest.main()
