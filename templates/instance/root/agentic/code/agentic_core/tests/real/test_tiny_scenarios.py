"""Tiny real-LLM scenarios (T1..T4): the agentic mechanics with a real model, for as few tokens as possible.

Prompts are a few words and answers one word. Run with BRIQUE_REAL_LLM=1. The judgement is structural, as in the R scenarios:
which pilot answered, in how many inferences, with what shape of journal. The wording of an answer is never asserted.
T4 makes the provider fail on purpose (a model that does not exist): it spends no token.
"""
import asyncio
import json
import os
import time
import unittest

from ..integration.driver import AGENTIC, TEST
from .harness import QUIET_TIMEOUT, REAL, SKIP_REASON, RealDriver, pilot_texts

MESSAGE_ATTEMPTS = 3      # mailbox_release.MAX_ATTEMPTS: a message whose treatment failed this many times is parked

MINI = {"context": TEST, "matter_id": "real_mini"}
BROKEN = {"context": TEST, "matter_id": "real_mini_broken"}
WORD = {"context": TEST, "capacity": "tiny_word"}
RELAY = {"context": TEST, "capacity": "tiny_relay"}
REPORT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "last_tiny_report.json")
THINGS = ["le ciel", "l'herbe", "la neige", "le charbon", "le soleil", "une tomate"]


def spent(calls):
    usage = [c.get("usage") or {} for c in calls]
    return {"inferences": len(calls), "input_tokens": sum(u.get("input_tokens", 0) for u in usage),
            "output_tokens": sum(u.get("output_tokens", 0) for u in usage), "errors": [c["error"] for c in calls if c.get("error")]}


@unittest.skipUnless(REAL, SKIP_REASON)
class TinyScenarios(unittest.IsolatedAsyncioTestCase):
    reports = {}

    async def asyncSetUp(self):
        self.d = await RealDriver().__aenter__()
        self.ids = []

    async def asyncTearDown(self):
        try:
            await self.d.cleanup(*self.ids)
        finally:
            await self.d.__aexit__()
            if TinyScenarios.reports:
                with open(REPORT, "w", encoding="utf-8") as fh:
                    json.dump({"at": time.strftime("%Y-%m-%dT%H:%M:%S"), "scenarios": TinyScenarios.reports}, fh, ensure_ascii=False, indent=1)

    async def start(self, *cids):
        self.ids = list(cids)
        await self.d.cleanup(*cids)
        await self.d.reset_world()

    async def finish(self, name, cids, **extra):
        calls = await self.d.real_calls()
        report = dict(spent(calls), elapsed_s=round(sum(c.get("elapsed_ms", 0) for c in calls) / 1000, 1), **extra)
        TinyScenarios.reports[name] = report
        return report

    # -- T1: a Matter pilot, no capacity, one inference -----------------------------------------------------------------
    async def test_t1_matter_pilot_answers_in_one_word(self):
        d, cid = self.d, "real_t1"
        await self.start(cid)
        await d.create(cid, intention="Answer in one word.", pilot_ref=MINI)
        await d.send(cid, f"{cid}_m1", "Quelle est la couleur du ciel ?")
        await d.quiet(cid, timeout=QUIET_TIMEOUT)
        await d.assert_invariants(cid)
        report = await self.finish("T1", [cid])
        journal = await d.journal(cid)
        texts = pilot_texts(journal)
        self.assertEqual(report["errors"], [])
        self.assertEqual(report["inferences"], 1)
        self.assertEqual(len(texts), 1)
        self.assertTrue(texts[0].strip())
        self.assertLessEqual(len(texts[0].split()), 5)                          # asked for one word: a short answer
        self.assertEqual([m["kind"] for m in journal if m["kind"] in ("capacity_intention", "error")], [])

    # -- T2: width, six one-step plans and two Matter pilots woken together -----------------------------------------------
    async def test_t2_eight_pilots_at_once(self):
        d = self.d
        plans = [f"real_t2_p{i}" for i in range(len(THINGS))]
        minis = ["real_t2_m0", "real_t2_m1"]
        await self.start(*plans, *minis)
        for cid in plans:
            await d.create(cid, intention="One word.", pilot_ref=WORD)
        for cid in minis:
            await d.create(cid, intention="Answer in one word.", pilot_ref=MINI)
        await asyncio.gather(*[d.send(cid, f"{cid}_m1", t) for cid, t in zip(plans, THINGS)],
                             *[d.send(cid, f"{cid}_m1", "Quelle est la couleur du ciel ?") for cid in minis])
        await d.quiet(*plans, *minis, timeout=QUIET_TIMEOUT)
        await d.assert_invariants(*plans, *minis)
        report = await self.finish("T2", plans + minis)
        self.assertEqual(report["errors"], [])
        self.assertEqual(report["inferences"], len(plans) + len(minis))        # one inference per pilot, none lost, none repeated
        for cid, thing in zip(plans, THINGS):
            journal = await d.journal(cid)
            self.assertEqual([m["kind"] for m in journal if not (m["kind"] == "event")], ["user", "prompt", "completion", "pilot"])
            self.assertEqual([m["content"] for m in journal if m["kind"] == "user"], [thing])           # its own message only
            text = pilot_texts(journal)
            self.assertEqual(len(text), 1)
            self.assertTrue(text[0].strip())
        for cid in minis:
            texts = pilot_texts(await d.journal(cid))
            self.assertEqual(len(texts), 1)
            self.assertTrue(texts[0].strip())

    # -- T3: depth, plan -> plan -> Matter pilot --------------------------------------------------------------------------
    async def test_t3_a_plan_relays_a_plan_relays_a_matter_pilot(self):
        d = self.d
        a, b, c = "real_t3_a", "real_t3_b", "real_t3_c"
        await self.start(a, b, c)
        await d.create(a, intention="Relay.", pilot_ref=RELAY)
        await d.send(a, f"{a}_m1", {
            "child_id": b, "child_message_id": f"{b}_task", "child_pilot": RELAY,
            "child_content": {"child_id": c, "child_message_id": f"{c}_task", "child_pilot": MINI,
                              "child_content": "Quelle est la couleur de la neige ?"}})
        await d.quiet(a, b, c, timeout=QUIET_TIMEOUT)
        await d.assert_invariants(a, b, c)
        report = await self.finish("T3", [a, b, c])
        self.assertEqual(report["errors"], [])
        self.assertEqual(report["inferences"], 3)                               # c answers, b repeats, a repeats
        ja, jb, jc = [await d.continuity(x) for x in (a, b, c)]
        self.assertEqual([jb["parent_id"], jc["parent_id"]], [a, b])
        self.assertEqual([("matter_id" in x["pilot_ref"]) for x in (ja, jb, jc)], [False, False, True])
        self.assertEqual([m["content"]["step"] for m in ja["messages"] if m["kind"] == "prompt"], ["relay"])
        self.assertEqual([m["content"]["step"] for m in jb["messages"] if m["kind"] == "prompt"], ["relay"])
        self.assertEqual([m["kind"] for m in jc["messages"] if m["kind"] not in ("event", "completion")], ["user", "pilot"])
        word_c, word_b, word_a = (pilot_texts(x["messages"]) for x in (jc, jb, ja))
        self.assertTrue(len(word_a) == len(word_b) == len(word_c) == 1)
        self.assertEqual(word_a[0], word_a[0].upper())                          # each relay repeated the word in uppercase
        self.assertEqual(word_b[0], word_b[0].upper())
        self.assertIn(word_c[0].strip().strip(".").lower(), word_a[0].lower())  # the word really travelled up the chain

    # -- T4: the provider fails: no token spent, and the continuity does not loop or hang ---------------------------------
    async def test_t4_a_provider_error_fails_the_treatment_and_stops(self):
        d, cid = self.d, "real_t4"
        await self.start(cid)
        await d.create(cid, intention="Answer in one word.", pilot_ref=BROKEN)
        await d.send(cid, f"{cid}_m1", "Bonjour")
        await d.quiet(cid, timeout=QUIET_TIMEOUT, allow_pending=1)
        report = await self.finish("T4", [cid])
        journal = await d.journal(cid)
        rt = await d.runtime(cid)
        self.assertEqual(rt["status"], "idle")
        self.assertFalse(rt.get("run_id"))
        self.assertEqual(rt["active_wait_groups"], [])
        self.assertEqual(pilot_texts(journal), [])                              # nothing was answered
        self.assertGreaterEqual(report["inferences"], 1)
        self.assertLessEqual(report["inferences"], MESSAGE_ATTEMPTS)               # bounded by the attempts of the message, then parked: no retry storm
        self.assertEqual(report["input_tokens"], 0)                             # the provider refused: nothing was spent
        self.assertTrue(report["errors"])                                       # and the error is on record
        TinyScenarios.reports["T4"]["journal_kinds"] = [m["kind"] for m in journal]
        TinyScenarios.reports["T4"]["mailbox"] = await d.mailbox(cid)


if __name__ == "__main__":
    unittest.main()
