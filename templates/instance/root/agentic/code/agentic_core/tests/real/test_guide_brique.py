"""A real model plays the guide of the Brique world (prompt/matter/guide_brique): it finds an element by its meaning, and only reads.

Exact: every intention of the guide is one of its read-only tools, it searched with the vocabulary before it queried the meaning, nothing was refused by
its policy, the chain ended with an answer. Reported, not judged: the text of the answer."""
import json
import unittest

from .harness import REAL, SKIP_REASON, RealDriver, pilot_texts

CID = "real_guide"
GUIDE = {"context": "/root/agentic/prompt", "matter_id": "guide_brique"}
READ_ONLY = ("/vocabulary.query", "/meaning.query", "/read.capacity", "/read.meaning", "/read.structure")


@unittest.skipUnless(REAL, SKIP_REASON)
class RealGuideBrique(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.d = await RealDriver().__aenter__()
        await self.d.cleanup(CID, *[f"{CID}_g{n}" for n in range(2, 5)])

    async def asyncTearDown(self):
        try:
            await self.d.cleanup(CID, *[f"{CID}_g{n}" for n in range(2, 5)])
        finally:
            await self.d.__aexit__()

    async def test_the_guide_finds_a_capacity_by_its_meaning_and_only_reads(self):
        d = self.d
        await d.create(CID, intention="Guide a person in the Brique world.", pilot_ref=GUIDE)
        await d.send(CID, f"{CID}_m1", "In this world, which capacity follows the tracking of a shipment, and where is it?")
        await d.quiet(CID, timeout=180)
        journal = await d.journal(CID)
        intentions = [m["content"] for m in journal if m["kind"] == "capacity_intention"]
        targets = [i["target_capacity"] for i in intentions]
        texts = pilot_texts(journal)
        print("\ntargets:", targets)
        print("answer:", json.dumps(texts, ensure_ascii=False)[:1500])
        self.assertTrue(all(any(t.endswith(ok) for ok in READ_ONLY) for t in targets), targets)     # it only reads
        first = lambda suffix: next((i for i, t in enumerate(targets) if t.endswith(suffix)), None)   # noqa: E731
        self.assertIsNotNone(first("/vocabulary.query"))
        self.assertIsNotNone(first("/meaning.query"))
        self.assertLess(first("/vocabulary.query"), first("/meaning.query"))                         # the vocabulary first, then the meaning
        events = [(m.get("content") or {}).get("event") for m in journal if isinstance(m.get("content"), dict)]
        self.assertNotIn("intentions_refused", events)
        self.assertNotIn("treatment_failed", events)
        self.assertTrue(texts and texts[-1].strip())
        await d.assert_invariants(CID)


    async def test_a_message_that_asks_for_nothing_is_still_answered(self):
        """"test" as the whole conversation (the founding intention is "test" too, as the interface writes it) once made the model answer {}: an
        empty output, invalid, three times, then the message was set aside. The frame now says that an answer is never empty."""
        d = self.d
        cid = f"{CID}_short"
        await d.cleanup(cid)
        try:
            out = await d.call("/root/agentic", "pilot.launch", "user", {"continuity_id": cid, "pilot_ref": GUIDE, "initial_intention": "test",
                                                                          "message": {"content": "test"}}, timeout=60)
            self.assertFalse(out.get("treated"))                        # a root launch answers at once
            await d.quiet(cid, timeout=150)
            journal = await d.journal(cid)
            events = [(m.get("content") or {}).get("event") for m in journal if isinstance(m.get("content"), dict)]
            self.assertNotIn("validation_error", events)
            self.assertNotIn("treatment_failed", events)
            self.assertTrue(pilot_texts(journal) and pilot_texts(journal)[-1].strip())
        finally:
            await d.cleanup(cid)

    async def test_the_guide_launches_a_pilot_when_asked_and_the_child_reports_back(self):
        d = self.d
        child = f"{CID}_child"
        await d.cleanup(child)
        try:
            await d.create(CID, intention="Guide a person in the Brique world.", pilot_ref=GUIDE)
            await d.send(CID, f"{CID}_m1", "Launch another guide pilot (the Matter pilot guide_brique of /root/agentic/prompt) as a sub-continuity named "
                                           f"{child}, and ask it to find which capacity follows the tracking of a shipment.")
            try:
                await d.quiet(CID, timeout=240)
            except AssertionError:
                for c in (CID, child):
                    try:
                        for m in await d.journal(c):
                            print(c, m.get("seq"), m["kind"], json.dumps(m.get("content"), ensure_ascii=False)[:1200])
                    except Exception as e:      # noqa: BLE001
                        print(c, "no journal", e)
                raise
            journal = await d.journal(CID)
            launches = [m["content"] for m in journal if m["kind"] == "capacity_intention" and m["content"]["target_capacity"].endswith("/pilot.launch")]
            print("\nlaunches:", json.dumps(launches, ensure_ascii=False)[:800])
            print("answer:", json.dumps(pilot_texts(journal), ensure_ascii=False)[:800])
            events = [(m.get("content") or {}).get("event") for m in journal if isinstance(m.get("content"), dict)]
            self.assertNotIn("intentions_refused", events)
            self.assertNotIn("treatment_failed", events)
            self.assertTrue(launches, "the guide did not launch the pilot")
            params = launches[0]["parameters"]
            self.assertEqual(params.get("parent_id"), CID)
            self.assertEqual((params.get("pilot_ref") or {}).get("matter_id"), "guide_brique")
            await d.quiet(child, timeout=240)
            # the return of the daughter is the answer of the launch: its result, read from her journal, not a message she had to aim at the parent
            answers = [m["content"]["response"] for m in await d.journal(CID) if m["kind"] == "capacity_response"
                       and (m["content"]["response"].get("payload") or {}).get("continuity_id") == child]
            print("launch answers:", json.dumps(answers, ensure_ascii=False)[:800])
            self.assertTrue(answers and answers[0]["status"] == "ok" and answers[0]["payload"].get("treated"), "the launch did not answer with the result of the child")
            self.assertTrue(answers[0]["payload"]["result"]["text"].strip(), "the child said nothing")
        finally:
            await d.cleanup(child)


if __name__ == "__main__":
    unittest.main()
