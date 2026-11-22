import json
import unittest

from .driver import AGENTIC, PILOT_ID, TEST, DriverError
from .scenarios_base import SKIP, SKIP_REASON, Scenario, say


class SuccessionScenario(Scenario):
    """The pilot of these scenarios declares a context window, so that the compaction pressure exists."""

    async def asyncSetUp(self):
        await super().asyncSetUp()
        self.pilot_before = await self.d.content(TEST, PILOT_ID)
        await self.d.write(TEST, PILOT_ID, dict(self.pilot_before, limits={"context_tokens": 1000}))

    async def asyncTearDown(self):
        try:
            await self.d.write(TEST, PILOT_ID, self.pilot_before)
        finally:
            await super().asyncTearDown()

    def cids(self):
        return [self.PREFIX, f"{self.PREFIX}_g2", f"{self.PREFIX}_g3"]

    async def exists(self, cid):
        """The successor appears when the succession happens: wait for it before asking it to be quiet."""
        async def there():
            return await self.d.exists(AGENTIC, f"continuity_{cid}")
        await self.d.until(there, f"continuity {cid} to exist", timeout=30)


@unittest.skipIf(SKIP, SKIP_REASON)
class S22CompactionBySuccession(SuccessionScenario):
    PREFIX = "it_s22"

    async def test_a_full_context_continues_in_a_new_continuity(self):
        d, cid = self.d, self.PREFIX
        succ = f"{cid}_g2"
        await d.reset_world([
            {"when": {"compaction": True}, "then": {"output": {"succession": {"summary": "Summary: the user said hello once."}}}},
            {"when": {"message_contains": "hello"}, "then": {"output": say("hi {{cid}}"), "usage": {"input_tokens": 900}}},   # 0.9 of the window
        ])
        await d.create(cid, intention="Greet politely.")
        await d.send(cid, f"{cid}_m1", "hello one")
        await d.quiet(cid)
        self.assertEqual((await d.measures(cid))[-1]["input_tokens"], 900)    # the measure the provider reported, a fact of the journal

        await d.send(cid, f"{cid}_m2", "hello two")                            # arrives at a safe point, with the context full
        await self.exists(succ)
        await d.quiet(cid, succ)
        await d.assert_invariants(cid, succ)

        source, successor = await d.continuity(cid), await d.continuity(succ)
        self.assertEqual((source["status"], source["successor_id"]), ("archived", succ))
        self.assertEqual([m["id"] for m in source["messages"] if m["kind"] == "user"], [f"{cid}_m1", f"{cid}_m2"])   # the history is kept as it was
        self.assertEqual(await d.mailbox(cid), [])                                    # nothing waits in the archive
        self.assertEqual((successor["status"], successor["predecessor_id"], successor["lineage_id"], successor["generation"]),
                         ("active", cid, cid, 2))
        self.assertEqual((successor["initial_intention"], successor["messages"][0]["kind"], successor["messages"][0]["content"]),
                         ("Greet politely.", "summary", "Summary: the user said hello once."))
        frames = [m for m in await d.journal(succ, frame=True) if m["kind"] == "framework"]
        self.assertEqual([(m["seq"], m["id"]) for m in frames], [(1, f"framework_{succ}")])                 # the successor opens with its own frame
        self.assertEqual(frames[0]["content"]["text"], [m for m in await d.journal(cid, frame=True) if m["kind"] == "framework"][0]["content"]["text"])
        self.assertEqual([m["content"]["content"] for m in successor["messages"] if m["kind"] == "pilot"], [f"hi {succ}"])
        self.assertIn(f"{cid}_m2", [m["id"] for m in successor["messages"]])            # the pending message moved with the work
        calls = await d.llm_calls()
        self.assertEqual(len([c for c in calls if c["compaction"]]), 1)

        # whoever still writes to the archive reaches the active continuity; an id already consumed is never replayed
        out = await d.send(cid, f"{cid}_late", "hello again")
        self.assertEqual((out["continuity_id"], out["redirected_to"]), (succ, succ))
        await d.quiet(succ)
        with self.assertRaises(DriverError) as refused:
            await d.send(cid, f"{cid}_m1", "hello one")
        self.assertIn("consumed", str(refused.exception))
        await d.assert_invariants(cid, succ)


@unittest.skipIf(SKIP, SKIP_REASON)
class S23DecidedSuccession(SuccessionScenario):
    PREFIX = "it_s23"

    async def test_the_pilot_decides_and_the_successor_answers_the_message_in_progress(self):
        d, cid = self.d, self.PREFIX
        succ = f"{cid}_g2"
        await d.reset_world([
            {"when": {"continuity": cid}, "then": {"output": {"succession": {"summary": "Decided summary."}}}},
            {"when": {"continuity": succ}, "then": {"output": say("answered by {{cid}}")}},
        ])
        await d.create(cid, intention="Keep going.")
        await d.send(cid, f"{cid}_m1", "please continue the work")
        await self.exists(succ)
        await d.quiet(cid, succ)
        await d.assert_invariants(cid, succ)
        successor = await d.continuity(succ)
        self.assertEqual(successor["messages"][0]["content"], "Decided summary.")
        self.assertEqual([m["id"] for m in successor["messages"] if m["kind"] == "user"], [f"{cid}_m1"])   # handed over, not lost
        self.assertEqual([m["content"]["content"] for m in successor["messages"] if m["kind"] == "pilot"], [f"answered by {succ}"])
        self.assertEqual((await d.continuity(cid))["status"], "archived")
        self.assertEqual(await d.mailbox(cid), [])

        # a whole lineage is deleted together, or not at all
        with self.assertRaises(DriverError):
            await d.call(AGENTIC, "continuity.delete", "user", {"continuity_id": succ})
        await d.call(AGENTIC, "continuity.delete", "user", {"continuity_id": succ, "cascade": True})
        self.assertTrue(await d.gone(cid))
        self.assertTrue(await d.gone(succ))


@unittest.skipIf(SKIP, SKIP_REASON)
class S24DelegationAcrossSuccession(SuccessionScenario):
    PREFIX = "it_s24"

    async def test_the_parent_never_sees_the_succession_of_its_child(self):
        d, parent, kid = self.d, self.PREFIX, f"{self.PREFIX}_kid"
        succ = f"{kid}_g2"
        await d.reset_world([
            {"when": {"continuity": kid, "message_contains": "work"},
             "then": {"output": {"succession": {"summary": "Child summary."}}}},
            {"when": {"continuity": succ},
             "then": {"output": {"messages": [{"to": parent, "kind": "continuity", "content": "child result"}]}}},
            {"when": {"continuity": parent}, "then": {"output": say("noted")}},
        ])
        await d.create(parent, intention="Delegate.")
        await d.create(kid, intention="Do the work.", parent=parent)
        await d.send(kid, f"{kid}_m1", "work please")
        await self.exists(succ)
        await d.quiet(kid, succ, parent)
        answer = [m for m in await d.journal(parent) if m["kind"] == "continuity"]
        self.assertEqual(len(answer), 1)
        self.assertEqual(answer[0]["correlation"]["continuity_id"], kid)           # the id the parent knows, not the successor
        self.assertEqual((await d.continuity(parent))["children"], [succ])           # the parent points to the active continuity
        await d.assert_invariants(kid, succ, parent)

    def cids(self):
        return [self.PREFIX, f"{self.PREFIX}_kid", f"{self.PREFIX}_kid_g2"]


@unittest.skipIf(SKIP, SKIP_REASON)
class S25ContentByReference(Scenario):
    PREFIX = "it_s25"

    async def test_large_contents_are_written_as_is(self):
        d, cid = self.d, self.PREFIX
        big = "z" * 150_000                                                       # above the 64 KiB of a value: it travels to test.echo as a transit file
        await d.reset_world([
            {"when": {"continuity": cid, "stimulus": "none"},
             "then": {"output": {"intentions": [{"id": "big_{{call}}", "target_capacity": "/root/agentic/test/test.echo",
                                                 "parameters": {"payload": big}, "await_response": True}],
                                 "wait_group": {"condition": "all", "expected": [{"type": "intention", "ref": "big_{{call}}"}]}}}},
            {"when": {"continuity": cid, "stimulus": "active_wait_result"}, "then": {"output": say("got it whole")}},
        ])
        await d.create(cid)
        await d.send(cid, f"{cid}_inline", big)                                   # no threshold: a large message is accepted as is
        await d.send(cid, f"{cid}_m1", "fetch it")
        await d.quiet(cid)
        await d.assert_invariants(cid)
        journal = await d.journal(cid)
        self.assertEqual([m["content"] for m in journal if m["id"] == f"{cid}_inline"], [big])
        response = [m for m in journal if m["kind"] == "capacity_response"][0]["content"]["response"]
        self.assertEqual(response["status"], "ok")
        self.assertEqual(response["payload"]["echo"], big)                        # the Response is in its line, whole: no reference
        self.assertFalse(await d.exists(AGENTIC, f"content_{cid}_response_{cid}_big_1"))          # and no overflow Matter exists
        await d.call(AGENTIC, "continuity.delete", "user", {"continuity_id": cid})
        self.assertTrue(await d.gone(cid))

    def cids(self):
        return [self.PREFIX]


if __name__ == "__main__":
    unittest.main()
