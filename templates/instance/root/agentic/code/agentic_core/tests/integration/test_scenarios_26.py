"""One answer per message even when its run dies between the last answer and the acknowledgement (document/capacity_pilot_activate.md, S86).

S44 kills the wrapper at random moments and hits that window only by chance (never, with the engine as fast as it is now). This one puts the state of
such a death exactly where it is, on the real engine, and lets the real takeover find it."""
import unittest

from .scenarios_base import SKIP, SKIP_REASON, Scenario, say
from .driver import AGENTIC


@unittest.skipIf(SKIP, SKIP_REASON)
class S86AnswerAlreadyWrittenIsNotAnsweredAgain(Scenario):
    PREFIX = "it_s86"

    async def test_the_takeover_acknowledges_a_message_whose_chain_already_ended(self):
        d, cid = self.d, self.PREFIX
        await d.reset_world([{"when": {}, "then": {"output": say("ok {{mid}}")}}])
        await d.create(cid)
        await d.send(cid, f"{cid}_m1", "first")
        await d.quiet(cid)
        first = [m for m in await d.journal(cid) if m["kind"] == "pilot"]
        self.assertEqual(len(first), 1)
        self.assertEqual((first[0]["correlation"].get("causation_id"), first[0]["content"].get("ends_chain")), (f"{cid}_m1", True))   # the proof is written

        # the run dies after that last answer and before acknowledging the message: the message is claimed by a run that no longer exists
        runtime = await d.runtime(cid)
        await d.write(AGENTIC, f"runtime_{cid}", dict(runtime, status="running", run_id="DEAD_RUN", current_message_id=f"{cid}_m1", active_wait_groups=[]))
        await d.write(AGENTIC, f"mailbox_{cid}", {"continuity_id": cid, "entries": [
            {"message_id": f"{cid}_m1", "priority": "normal", "status": "claimed", "claimed_by": "DEAD_RUN"}]})

        out = await d.send(cid, f"{cid}_m2", "second")                            # a new message makes the orphan visible: the run is taken over
        self.assertTrue(out["wake"])
        await d.quiet(cid, timeout=60)
        await d.assert_invariants(cid)

        journal = await d.journal(cid)
        answers = [m["content"]["content"] for m in journal if m["kind"] == "pilot"]
        self.assertEqual(answers, [f"ok {cid}_m1", f"ok {cid}_m2"])                  # m1 once, m2 once: the first message was not answered again
        recovered = [m["content"] for m in journal if m["kind"] == "event" and (m.get("content") or {}).get("event") == "run_recovered"]
        self.assertEqual([(r["message_id"], r.get("already_answered")) for r in recovered], [(f"{cid}_m1", True)])
        self.assertEqual(await d.mailbox(cid), [])
        self.assertEqual((await d.runtime(cid))["status"], "idle")


if __name__ == "__main__":
    unittest.main()
