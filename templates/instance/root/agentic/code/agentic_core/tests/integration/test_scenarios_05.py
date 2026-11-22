import asyncio
import unittest

from .driver import DriverError
from .scenarios_base import SKIP, SKIP_REASON, Scenario, say

BOGUS = {"bogus": 1}


@unittest.skipIf(SKIP, SKIP_REASON)
class S13InvalidThenCorrected(Scenario):
    PREFIX = "it_s13"

    async def test_one_correction_and_no_effect_from_the_invalid_output(self):
        d, cid = self.d, self.PREFIX
        await d.reset_world([
            {"when": {"continuity": cid, "correction": True}, "then": {"output": say("fixed")}},
            {"when": {"continuity": cid}, "then": {"invalid": BOGUS}},
        ])
        await d.create(cid)
        await d.send(cid, f"{cid}_m1", "hello")
        await d.quiet(cid)
        await d.assert_invariants(cid)
        calls = await d.llm_calls()
        self.assertEqual([(c["call_index"], c["correction"]) for c in calls], [(1, False), (2, True)])
        journal = await d.journal(cid)
        self.assertEqual([m["content"]["content"] for m in journal if m["kind"] == "pilot"], ["fixed"])
        errors = [m["content"] for m in journal if m["kind"] == "error"]
        self.assertEqual([e["event"] for e in errors], ["validation_error"])


@unittest.skipIf(SKIP, SKIP_REASON)
class S14InvalidTwice(Scenario):
    PREFIX = "it_s14"

    async def test_the_treatment_fails_explicitly_without_a_loop(self):
        d, cid = self.d, self.PREFIX
        await d.reset_world([{"when": {"continuity": cid}, "then": {"invalid": BOGUS}}])
        await d.create(cid)
        await d.send(cid, f"{cid}_m1", "hello")
        await d.quiet(cid)
        await d.assert_invariants(cid)
        self.assertEqual(len(await d.llm_calls()), 6)                                       # three attempts of one inference and one correction
        await asyncio.sleep(1.5)
        self.assertEqual(len(await d.llm_calls()), 6)                                       # then parked: no retry loop
        events = [m["content"]["event"] for m in await d.journal(cid) if m["kind"] == "error"]
        self.assertEqual(events, ["validation_error", "treatment_failed"] * 3 + ["message_parked"])
        entries = await d.mailbox(cid)
        self.assertEqual([(e["message_id"], e["status"]) for e in entries], [(f"{cid}_m1", "failed")])    # parked, not lost


@unittest.skipIf(SKIP, SKIP_REASON)
class S15ExecutionFailure(Scenario):
    PREFIX = "it_s15"

    async def test_first_failure_stops_the_rest_and_nothing_is_replayed(self):
        d, cid = self.d, self.PREFIX
        echo = lambda ref: {"id": ref, "target_capacity": "/root/agentic/test/test.echo", "parameters": {"payload": ref}, "await_response": False}
        await d.reset_world([
            {"when": {"continuity": cid, "stimulus": "execution_result"}, "then": {"output": say("replanned")}},
            {"when": {"continuity": cid}, "then": {"output": {
                "intentions": [echo("i1_{{call}}"), {"id": "i2_{{call}}", "target_capacity": "/nowhere", "parameters": {}, "await_response": False}, echo("i3_{{call}}")],
                "messages": [{"to": "user:1", "kind": "user", "content": "never sent"}]}}},
        ])
        await d.create(cid)
        await d.send(cid, f"{cid}_m1", "go")
        await d.quiet(cid)
        await d.assert_invariants(cid)
        calls = await d.llm_calls()
        self.assertEqual(len(calls), 2)
        stimulus = calls[1]["active_stimulus"]
        self.assertEqual((stimulus["kind"], stimulus["succeeded"], stimulus["failed"], stimulus["remaining"]),
                         ("execution_result", ["intention:i1_1"], "intention:i2_1", ["intention:i3_1", "message:0"]))
        await d.until(lambda: self._echoed(1), "the first intention to run")
        await asyncio.sleep(0.5)
        self.assertEqual(len(await d.tool_calls("test.echo")), 1)                           # i1 once, i3 never, nothing replayed
        outs = [m["content"]["content"] for m in await d.journal(cid) if m["kind"] == "pilot"]
        self.assertEqual(outs, ["replanned"])

    async def _echoed(self, n):
        return len(await self.d.tool_calls("test.echo")) >= n


@unittest.skipIf(SKIP, SKIP_REASON)
class S16Burst(Scenario):
    PREFIX = "it_s16"

    async def test_twenty_messages_one_at_a_time_in_seq_order(self):
        d, cid = self.d, self.PREFIX
        await d.reset_world([{"when": {"continuity": cid}, "then": {"output": say("ack {{mid}}")}}])
        await d.create(cid)
        for i in range(20):
            await d.send(cid, f"{cid}_m{i:02d}", f"burst {i}")
        await d.quiet(cid, timeout=90)
        await d.assert_invariants(cid)
        calls = await d.llm_calls()
        journal = await d.journal(cid)
        user_ids = [m["id"] for m in sorted(journal, key=lambda m: m["seq"]) if m["kind"] == "user"]
        self.assertEqual([c["message_id"] for c in calls], user_ids)                         # one inference each, in seq order
        self.assertEqual(len(calls), 20)
        self.assertEqual(self.kinds(journal).count("pilot"), 20)


@unittest.skipIf(SKIP, SKIP_REASON)
class S17RetryAndConsumed(Scenario):
    PREFIX = "it_s17"

    async def test_a_retry_is_idempotent_and_a_consumed_id_is_refused(self):
        d, cid = self.d, self.PREFIX
        await d.reset_world([{"when": {"continuity": cid}, "then": {"output": say()}}])
        await d.create(cid)
        first = await d.send(cid, f"{cid}_m1", "hello")
        again = await d.send(cid, f"{cid}_m1", "hello")
        self.assertEqual(first["seq"], again["seq"])
        await d.quiet(cid)
        await d.assert_invariants(cid)
        self.assertEqual(len([m for m in await d.journal(cid) if m["kind"] == "user"]), 1)
        self.assertEqual(len(await d.llm_calls()), 1)
        with self.assertRaises(DriverError) as ctx:                                          # accepted then consumed
            await d.send(cid, f"{cid}_m1", "hello")
        # the generic wrapper router answers every hosted failure as code "internal": the agentic code is in the message
        self.assertIn("refused/refused", str(ctx.exception))
        self.assertIn("consumed", str(ctx.exception))
        self.assertEqual(len([m for m in await d.journal(cid) if m["kind"] == "user"]), 1)


if __name__ == "__main__":
    unittest.main()
