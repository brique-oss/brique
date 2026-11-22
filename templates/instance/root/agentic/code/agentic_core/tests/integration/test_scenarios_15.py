import asyncio
import collections
import random
import unittest

from .crash import SKIP_HARD_KILL, SKIP_HARD_KILL_REASON, kill_wrapper, wrapper_pids
from .driver import AGENTIC, DriverError
from .scenarios_base import SKIP, SKIP_REASON, Scenario, say
from .test_scenarios_14 import PROBE, content

SKIPPED = SKIP or SKIP_HARD_KILL
WHY = SKIP_REASON if SKIP else SKIP_HARD_KILL_REASON


async def after_crash(call, *, tries: int = 6):
    """The first call addressed to a dead wrapper makes the engine start a new one: it may need a moment to be ready."""
    for attempt in range(tries):
        try:
            return await call()
        except DriverError:
            if attempt == tries - 1:
                raise
            await asyncio.sleep(1.0)


@unittest.skipIf(SKIPPED, WHY)
class S41HardKillWhileWaiting(Scenario):
    """The wrapper process is killed (SIGKILL) while the continuity waits for a tool: S18, with no chance to stop cleanly."""
    PREFIX = "it_s41"

    async def test_a_killed_wrapper_leaves_an_orphan_that_the_next_message_takes_over(self):
        d, cid = self.d, self.PREFIX
        await d.reset_world([
            {"when": {"continuity": cid, "message_contains": "ping"}, "then": {"output": say("pong")}},
            {"when": {"continuity": cid, "stimulus": "active_wait_result"}, "then": {"output": say("recovered")}},
            {"when": {"continuity": cid, "stimulus": "none"},
             "then": {"output": {"wait_group": {"condition": "all", "expected": [{"type": "intention", "ref": "s_{{call}}"}]},
                                 "intentions": [{"id": "s_{{call}}", "target_capacity": "/root/agentic/test/test.slow",
                                                 "parameters": {"gate": "gHardKill", "value": "x"}, "await_response": True}]}}},
        ])
        await d.create(cid)
        await d.send(cid, f"{cid}_m1", "long work")

        async def waiting():
            rt = await d.runtime(cid)
            return rt["status"] == "waiting" and rt
        before = await d.until(waiting, "the continuity to wait")
        self.assertEqual(len(await d.tool_calls("test.slow", "start")), 1)

        pid = kill_wrapper()                                                              # no clean stop: the process is simply gone
        self.assertNotIn(pid, wrapper_pids())
        rt = await d.runtime(cid)
        self.assertEqual((rt["status"], rt["run_id"]), ("waiting", before["run_id"]))      # the durable state still names the dead run
        self.assertEqual([g["status"] for g in rt["active_wait_groups"]], ["waiting"])

        # nothing relaunches the orphan by itself: a new message is what makes it visible, and the run is taken over
        out = await after_crash(lambda: d.send(cid, f"{cid}_m2", "ping"))
        self.assertTrue(out["wake"])
        await d.quiet(cid, timeout=60)
        await d.assert_invariants(cid)

        journal = await d.journal(cid)
        self.assertIn("run_recovered", [m["content"].get("event") for m in journal if isinstance(m["content"], dict)])
        unresolved = [m for m in journal if m["kind"] == "capacity_response"]
        self.assertEqual(len(unresolved), 1)
        self.assertTrue(unresolved[0]["content"]["unresolved"])                           # the lost intention is resolved explicitly
        self.assertEqual(unresolved[0]["content"]["response"]["error"]["code"], "unresolved")
        self.assertEqual(len(await d.tool_calls("test.slow", "start")), 1)               # never re-emitted
        calls = await d.llm_calls()
        self.assertEqual([(c["message_id"], c["stimulus"]) for c in calls],
                         [(f"{cid}_m1", "none"), (f"{cid}_m1", "active_wait_result")])    # the ping joins the chain: no turn of its own
        self.assertEqual(calls[1]["user_message"], {"message_ids": [f"{cid}_m2"]})        # it is designated to the resumption, not lost
        await d.open_gate("gHardKill")                                                    # the tool call of the dead process returns
        await asyncio.sleep(1.0)
        self.assertEqual(len(await d.llm_calls()), 2)                                     # and changes nothing
        await d.assert_invariants(cid)


@unittest.skipIf(SKIPPED, WHY)
class S42HardKillDuringAnInference(Scenario):
    """The wrapper dies while the model is being asked, in a step of a plan: the prompt is in the journal, the completion is not.
    The step is asked again once, and the history holds one prompt, one completion and one answer for the message."""
    PREFIX = "it_s42"

    async def test_an_inference_in_flight_when_the_process_dies_is_asked_again_without_a_duplicate(self):
        d, cid = self.d, self.PREFIX
        await d.reset_world([{"when": {"step": "probe"}, "then": {"output": {"word": "ok"}, "wait_gate": "gInfer"}}])
        await d.create(cid, intention="Probe.", pilot_ref=PROBE)
        await d.send(cid, f"{cid}_m1", content(["Say ok."]))

        async def in_flight():
            return [c for c in await d.llm_calls() if c.get("step") == "probe"]
        await d.until(in_flight, "the model to be asked")
        journal = await d.journal(cid)
        kinds = self.kinds(journal)
        self.assertEqual((kinds.count("prompt"), kinds.count("completion")), (1, 0))      # asked, not answered

        kill_wrapper()
        await d.open_gate("gInfer")                                                       # the stale call answers a dead process; later ones pass
        out = await after_crash(lambda: d.send(cid, f"{cid}_m2", content(["Say ok."])))
        self.assertTrue(out["wake"])
        await d.quiet(cid, timeout=60)
        await d.assert_invariants(cid)

        journal = await d.journal(cid)
        kinds = self.kinds(journal)
        self.assertEqual((kinds.count("prompt"), kinds.count("completion")), (2, 2))      # one pair per message, none doubled
        self.assertEqual([m["id"] for m in journal if m["kind"] == "user"], [f"{cid}_m1", f"{cid}_m2"])
        self.assertEqual([m["content"]["content"] for m in journal if m["kind"] == "pilot"], ["ok", "ok"])   # one answer each
        self.assertEqual(len(await in_flight()), 3)                                       # the crash cost one more inference, nothing else


class BurstRounds(Scenario):
    """The wrapper is killed at a random moment of a burst of messages; a message sent afterwards makes the engine's new wrapper
    take the orphan run over."""
    BURST = 30

    def cids(self):
        return [f"{self.PREFIX}_r{r}" for r in range(self.MAX_ROUNDS)]

    async def burst_round(self, r, rng):
        """One round: a burst, a SIGKILL after a random number of answers, a message to wake the continuity. Returns what the
        journal then holds: (answers before the kill, the messages sent, how many answers each message got)."""
        d, cid = self.d, self.cids()[r]
        # a fresh world each round: the record of the fake model's calls grows with every call, and the engine refuses to write a
        # Matter past 256 KiB inline, which would fail the model call itself and have nothing to do with the crash
        await d.reset_world([{"when": {}, "then": {"output": say("ok {{mid}}")}}])
        await d.create(cid, intention="Answer each message.")
        sent = [f"{cid}_m{i:02d}" for i in range(self.BURST)]
        for mid in sent:
            await d.send(cid, mid, f"message {mid}")
        wanted = rng.randint(3, self.BURST - 8)

        async def enough():
            return len([m for m in await d.journal(cid) if m["kind"] == "pilot"]) >= wanted
        await d.until(enough, f"{wanted} answers before the kill", timeout=60)
        answered = len([m for m in await d.journal(cid) if m["kind"] == "pilot"])
        kill_wrapper()
        final = f"{cid}_final"
        sent.append(final)
        await after_crash(lambda: d.send(cid, final, "after the crash"))
        try:
            await d.quiet(cid, timeout=120)
        except AssertionError as exc:                                      # say what state the continuity is stuck in
            rt, mailbox, journal = await d.runtime(cid), await d.mailbox(cid), await d.journal(cid)
            raise AssertionError(
                f"round {r} (killed after {answered} answers): {exc}; runtime={rt['status']} run={rt.get('run_id')} "
                f"current={rt.get('current_message_id')} groups={len(rt['active_wait_groups'])}; "
                f"mailbox={dict(collections.Counter(e['status'] for e in mailbox))}; "
                f"stuck={[(e['message_id'], e['status'], e.get('claimed_by'), e.get('priority')) for e in mailbox if e['status'] != 'pending'][:3]}; "
                f"pending={len([e for e in mailbox if e['status'] == 'pending'])}; "
                f"final_answered={any(m['kind'] == 'pilot' and final in str(m.get('correlation')) for m in journal)}; "
                f"users={[m['id'][-3:] for m in journal if m['kind'] == 'user'][-4:]}; "
                f"errors={[m['content'] for m in journal if m['kind'] == 'error'][:3]}; "
                f"last lines={[(m['seq'], m['kind']) for m in journal[-6:]]}") from exc
        await d.assert_invariants(cid)
        journal = await d.journal(cid)
        self.assertEqual(sorted(m["id"] for m in journal if m["kind"] == "user"), sorted(sent), f"round {r}: messages lost or doubled")
        answers = collections.Counter(m["content"]["content"] for m in journal if m["kind"] == "pilot")
        return answered, sent, answers


@unittest.skipIf(SKIPPED, WHY)
class S43HardKillInABurst(BurstRounds):
    """Whatever the moment of the kill, no message of the burst is lost: each one is answered, and the continuity ends at rest."""
    PREFIX = "it_s43"
    MAX_ROUNDS = 3

    async def test_no_message_of_a_burst_is_lost_whatever_the_moment_of_the_kill(self):
        rng = random.Random(43)
        for r in range(self.MAX_ROUNDS):
            answered, sent, answers = await self.burst_round(r, rng)
            absorbed = {i for c in await self.d.llm_calls() for i in ((c.get("user_message") or {}).get("message_ids") or [])}
            missing = sorted(mid for mid in sent if f"ok {mid}" not in answers and mid not in absorbed)   # answered as a turn, or absorbed by a chain
            self.assertEqual(missing, [], f"round {r} (killed after {answered} answers): messages neither answered nor designated to the model")


@unittest.skipIf(SKIPPED, WHY)
class S44HardKillAnswersExactlyOnce(BurstRounds):
    """A hard kill between the last answer of a message and its acknowledgement must not answer the message twice.

    The run writes the answer of a message, then acknowledges the message. Killed between the two, the dead run leaves the message claimed with its
    answer already written. The messages of the pilot now carry the message they answer, and the last one of a chain ends it (ends_chain): the takeover
    reads that proof and acknowledges the message instead of presenting it again (document/capacity_pilot_activate.md, "Exactly one response per
    message"). This scenario kills the wrapper at random moments of a burst; the window is narrow, so it hits it only some of the time. Its deterministic
    form is the contract test test_a_takeover_does_not_answer_again_a_message_whose_chain_already_ended (test_pilot_activate.py).
    """
    PREFIX = "it_s44"
    MAX_ROUNDS = 12

    async def test_every_message_is_answered_exactly_once_after_a_hard_kill(self):
        rng = random.Random(44)
        hits = 0
        for r in range(self.MAX_ROUNDS):
            answered, sent, answers = await self.burst_round(r, rng)
            journal = await self.d.journal(self.cids()[r])
            hits += any(isinstance(m.get("content"), dict) and m["content"].get("already_answered") for m in journal if m["kind"] == "event")
            doubled = {a: n for a, n in answers.items() if n != 1}
            if doubled:                                                   # the window is hit in about one round out of two
                self.assertEqual(doubled, {}, f"round {r} (killed after {answered} answers): answered more than once")
        # said, not asserted (the kill is random): how many rounds the takeover found an answer already written and acknowledged instead of answering again
        print(f"\n[S44] rounds where the takeover acknowledged a message that was already answered: {hits} of {self.MAX_ROUNDS}", file=__import__("sys").stderr, flush=True)


if __name__ == "__main__":
    unittest.main()
