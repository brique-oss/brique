import asyncio
import json
import unittest

from .driver import AGENTIC, TEST, DriverError
from .scenarios_base import SKIP, SKIP_REASON, Scenario, say

PROBE = {"context": TEST, "capacity": "parts_probe"}
MODEL = {"provider": "fake", "model": "scripted", "parameters": {"max_output_tokens": 50}}


def probe_rule():
    return {"when": {"step": "probe"}, "then": {"output": {"word": "ok"}}}


def content(texts=None, matters=(), order=None, data=None, framework=None):
    """The message the probe plan hands to pilot.infer: the parts (texts, and the data as one more part), the Matters (as
    context/id refs), the order."""
    parts = (texts or []) + ([data] if data is not None else [])
    return {"texts": parts or None, "matters": [f"{context}/{matter_id}" for context, matter_id in matters], "order": order, "model": MODEL,
            "framework": framework}


def text_matter(matter_id, text):
    return {"matter": {"brique": {"kind": "matter", "substance_mode": "brique", "substance_type": "text"}, "functional": {"format": "txt"},
                       "objective": {"name": matter_id, "description": "Prompt part of an integration scenario."}, "subjective": {}},
            "matter_id": matter_id, "payload": text}


class PartsScenario(Scenario):
    """A scenario whose prompts are assembled from text Matters the scenario lays down itself (and removes at the end)."""
    MATTERS = ()          # (context, matter_id)

    async def lay(self, context, matter_id, text):
        try:
            await self.d.call(context, "matter.delete", "matter", {"matter_id": matter_id})
        except DriverError:
            pass
        await self.d.call(context, "matter.create", "matter", text_matter(matter_id, text))

    async def change(self, context, matter_id, text):
        await self.d.call(context, "matter.write", "matter", {"matter_id": matter_id, "data": text})

    async def asyncTearDown(self):
        for context, matter_id in self.MATTERS:
            try:
                await self.d.call(context, "matter.delete", "matter", {"matter_id": matter_id})
            except DriverError:
                pass
        await super().asyncTearDown()

    def asked(self, calls, cid):
        return [c for c in calls if c.get("step") == "probe" and c["continuity"] == cid]


@unittest.skipIf(SKIP, SKIP_REASON)
class S37PromptAssembly(PartsScenario):
    """pilot.infer assembles its prompt from a list of Matters (of two contexts, bound under one name) and a list of texts, as
    is, each list in the order given and the two in the order asked."""
    PREFIX = "it_s37"
    MATTERS = ((TEST, "it_s37_alpha"), (AGENTIC, "it_s37_beta"))

    def cids(self):
        return [f"{self.PREFIX}_{k}" for k in ("mf", "pf", "matters", "texts")]

    async def test_matters_and_texts_are_assembled_in_the_order_asked_and_recorded_as_sent(self):
        d = self.d
        mf, pf, only_matters, only_texts = self.cids()
        await self.lay(TEST, "it_s37_alpha", "ALPHA\n")
        await self.lay(AGENTIC, "it_s37_beta", '{"beta": 1}')
        await d.reset_world([probe_rule()])
        matters = [(TEST, "it_s37_alpha"), (AGENTIC, "it_s37_beta")]
        texts = ["Intro.", "Outro."]
        cases = {mf: content(texts, matters, "matters_first"), pf: content(texts, matters, "parts_first"),
                 only_matters: content(None, matters), only_texts: content(texts)}
        raw = ["ALPHA\n", '{"beta": 1}']
        expected = {mf: raw + texts, pf: texts + raw, only_matters: raw, only_texts: texts}
        for cid, message in cases.items():
            await d.create(cid, intention="Probe.", pilot_ref=PROBE)
            await d.send(cid, f"{cid}_m1", message)
        await d.quiet(*cases, timeout=40)
        await d.assert_invariants(*cases)

        calls = await d.llm_calls()
        for cid, pieces in expected.items():
            text = "\n\n".join(pieces)                                                        # raw substances, a blank line between
            asked = self.asked(calls, cid)
            self.assertEqual(len(asked), 1, cid)
            self.assertEqual(asked[0]["system_prompt"], text, cid)                                         # the prompt as assembled, nothing added
            journal = await d.journal(cid)
            self.assertEqual(self.kinds(journal), ["user", "prompt", "completion", "pilot"])
            self.assertEqual([m["content"]["instructions"] for m in journal if m["kind"] == "prompt"], [text])   # the history keeps the text sent
            self.assertEqual([m["content"]["content"] for m in journal if m["kind"] == "pilot"], ["ok"])
            self.assertNotIn("prompt_matters", json.dumps(await d.journal(cid)))                # and no reference to rebuild it from
        # the Matters were only read
        self.assertEqual((await d.call(TEST, "matter.read", "matter", {"matter_id": "it_s37_alpha", "read_mode": "data"}))["data"]["bytes"], "ALPHA\n")


@unittest.skipIf(SKIP, SKIP_REASON)
class S38SharedPromptMatters(PartsScenario):
    """Several continuities assemble their prompt from the same Matters at the same time; the Matter then changes: the
    continuities that already asked keep the text they sent, the next one gets the new text."""
    PREFIX = "it_s38"
    MATTERS = ((TEST, "it_s38_role"), (TEST, "it_s38_style"))

    def cids(self):
        return [f"{self.PREFIX}_c{i}" for i in range(6)]

    async def test_a_shared_matter_that_changes_does_not_rewrite_the_history(self):
        d = self.d
        cids = self.cids()
        await self.lay(TEST, "it_s38_role", "ROLE-ONE")
        await self.lay(TEST, "it_s38_style", "STYLE")
        await d.reset_world([probe_rule()])
        matters = [(TEST, "it_s38_role"), (TEST, "it_s38_style")]
        for cid in cids:
            await d.create(cid, intention="Probe.", pilot_ref=PROBE)
        first, last = cids[:5], cids[5]
        await asyncio.gather(*[d.send(cid, f"{cid}_m1", content(None, matters, "matters_first", {"who": cid})) for cid in first])
        await d.quiet(*first, timeout=40)
        await self.change(TEST, "it_s38_role", "ROLE-TWO")
        await d.send(last, f"{last}_m1", content(None, matters, "matters_first", {"who": last}))
        await d.quiet(last)
        await d.assert_invariants(*cids)

        calls = await d.llm_calls()
        for cid in first:
            sent = self.asked(calls, cid)
            self.assertEqual(len(sent), 1)
            mine = '{"who":"%s"}' % cid                                                                         # its own data, the last part
            self.assertEqual(sent[0]["system_prompt"], f"ROLE-ONE\n\nSTYLE\n\n{mine}", cid)                      # all five saw the first text
            prompt = [m for m in await d.journal(cid) if m["kind"] == "prompt"]
            self.assertEqual([p["content"]["instructions"] for p in prompt], [f"ROLE-ONE\n\nSTYLE\n\n{mine}"])   # and still do
        sent = self.asked(calls, last)
        mine = '{"who":"%s"}' % last
        self.assertEqual(sent[0]["system_prompt"], f"ROLE-TWO\n\nSTYLE\n\n{mine}")                            # the next one saw the change
        self.assertEqual([p["content"]["instructions"] for p in await d.journal(last) if p["kind"] == "prompt"], [f"ROLE-TWO\n\nSTYLE\n\n{mine}"])
        for cid in first:                                                                                       # nothing crossed over
            self.assertEqual(len([m for m in await d.journal(cid) if m["kind"] == "user"]), 1)


@unittest.skipIf(SKIP, SKIP_REASON)
class S39PromptPartsFailBeforeAnyEffect(PartsScenario):
    """A prompt that cannot be assembled costs nothing: no inference, no prompt or completion in the history."""
    PREFIX = "it_s39"
    MATTERS = ((TEST, "it_s39_blank"), (TEST, "it_s39_ok"))

    def cids(self):
        return [f"{self.PREFIX}_{k}" for k in ("missing", "blank", "noorder", "emptytext")]

    async def test_a_missing_empty_or_malformed_prompt_stops_the_step(self):
        d = self.d
        await self.lay(TEST, "it_s39_blank", "   \n")
        await self.lay(TEST, "it_s39_ok", "fine")
        await d.reset_world([probe_rule()])
        missing, blank, noorder, emptytext = self.cids()
        cases = {
            missing: content(["Hello."], [(TEST, "it_s39_ghost")], "parts_first"),                    # a Matter that does not exist
            blank: content(["Hello."], [(TEST, "it_s39_blank")], "parts_first"),                      # an empty Matter
            noorder: content(["Hello."], [(TEST, "it_s39_ok")], None),                                # both lists, no order
            emptytext: content([""], [], None),                                                       # an empty text
        }
        for cid, message in cases.items():
            await d.create(cid, intention="Probe.", pilot_ref=PROBE)
            await d.send(cid, f"{cid}_m1", message)
        await d.quiet(*cases, timeout=40)
        calls = await d.llm_calls()
        self.assertEqual([c for c in calls if c.get("step") == "probe"], [])                       # the model was never asked
        # a Matter the engine does not know is refused by the engine itself, when it checks the Matters bound to the call; the
        # other cases are refused by pilot.infer
        # a caller with local authority is told the Matter is not in the catalogue; an admitted caller (agentic_core restricted by the
        # policy) gets the canonical refusal, which does not say whether the Matter or the plan is missing
        why = {missing: ("not in the catalogue", "does not exist or may not be used"), blank: "the Matter is empty",
               noorder: "prompt_order is required", emptytext: "non-empty texts"}
        for cid in cases:
            journal = await d.journal(cid)
            kinds = self.kinds(journal)
            failures = [m["content"]["error"] for m in journal if m["kind"] == "error" and m["content"].get("event") == "treatment_failed"]
            self.assertEqual(len(failures), 3, cid)                                               # three attempts in the run, each failing the same way
            reasons = why[cid] if isinstance(why[cid], tuple) else (why[cid],)
            self.assertTrue(any(reason in failures[0] for reason in reasons), (cid, failures[0]))   # it stopped for the right reason
            self.assertNotIn("prompt", kinds)
            self.assertNotIn("completion", kinds)
            self.assertNotIn("pilot", kinds)                                                       # nothing was answered
            rt = await d.runtime(cid)
            self.assertEqual((rt["status"], rt["active_wait_groups"]), ("idle", []))
            self.assertEqual([e["status"] for e in await d.mailbox(cid)], ["failed"])               # parked after three attempts, not lost


@unittest.skipIf(SKIP, SKIP_REASON)
class S40BothKindsOfPilotAtOnce(PartsScenario):
    """A Matter pilot with its own prompt and a plan whose prompt is assembled from Matters, woken together: each asks the
    model with its own prompt, through the same path, and keeps the history that goes with its kind."""
    PREFIX = "it_s40"
    MATTERS = ((TEST, "it_s40_head"), (TEST, "it_s40_task"))

    def cids(self):
        return [f"{self.PREFIX}_matter", f"{self.PREFIX}_plan"]

    async def test_a_matter_pilot_and_a_plan_with_assembled_prompt_side_by_side(self):
        d = self.d
        m, p = self.cids()
        await self.lay(TEST, "it_s40_head", "PLAN-HEAD")
        await self.lay(TEST, "it_s40_task", "PLAN-TASK")
        await d.reset_world([probe_rule(), {"when": {"continuity": m}, "then": {"output": say("matter pilot answered")}}])
        await d.create(m, intention="Matter pilot.")
        await d.create(p, intention="Plan pilot.", pilot_ref=PROBE)
        matters = [(TEST, "it_s40_head"), (TEST, "it_s40_task")]
        await asyncio.gather(d.send(m, f"{m}_m1", "hello"), d.send(p, f"{p}_m1", content(None, matters)))
        await d.quiet(m, p, timeout=40)
        await d.assert_invariants(m, p)

        calls = await d.llm_calls()
        by_matter = [c for c in calls if c["continuity"] == m]
        by_plan = self.asked(calls, p)
        self.assertEqual((len(by_matter), len(by_plan)), (1, 1))
        self.assertIn("SCENARIO-DRIVEN TEST PILOT", str(by_matter[0]["system_prompt"]))                  # its prompt is its definition's
        self.assertEqual(by_plan[0]["system_prompt"], "PLAN-HEAD\n\nPLAN-TASK")
        self.assertNotIn("PLAN-HEAD", json.dumps(by_matter[0], default=str))                           # nothing leaked from one to the other
        self.assertEqual(self.kinds(await d.journal(m)), ["user", "completion", "pilot"])             # a Matter pilot's history: its decision, then its effect
        self.assertEqual(self.kinds(await d.journal(p)), ["user", "prompt", "completion", "pilot"])    # a plan's: its prompts and answers


if __name__ == "__main__":
    unittest.main()
