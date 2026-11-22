"""The request for input (capacity_input_request.md): S84. A pilot asks a person a precise question as an intention; the answer of the person is the
Response of that intention, and the pilot goes on."""
import asyncio
import unittest

from .driver import AGENTIC, DriverError
from .scenarios_base import SKIP, SKIP_REASON, Scenario, say
from .test_scenarios_19 import awaited, intention


def input_events(journal, event):
    return [m["content"] for m in journal if m["kind"] == "event" and (m.get("content") or {}).get("event") == event]


@unittest.skipIf(SKIP, SKIP_REASON)
class S84RequestForInput(Scenario):
    PREFIX = "it_s84"

    async def test_a_pilot_asks_a_person_and_goes_on_with_the_answer(self):
        d, cid = self.d, self.PREFIX
        ask = intention("q1", f"{AGENTIC}/input.request",
                        {"continuity_id": cid, "question": "Which order?", "answer_kind": "choice", "choices": ["CMD-1", "CMD-2"], "request_id": f"{cid}_r1"}, "user")
        await d.reset_world([
            {"when": {"continuity": cid, "stimulus": "active_wait_result"}, "then": {"output": say("thanks, noted")}},
            {"when": {"continuity": cid, "message_id": f"{cid}_m1"}, "then": {"output": awaited(ask)}},
        ])
        await d.create(cid, intention="Ask which order.")
        await d.send(cid, f"{cid}_m1", "demande-moi quelle commande")

        # the request is a line of the journal the interface follows: it appears while the pilot waits
        await d.until(lambda: self._open(cid), "the open request in the journal", timeout=30)
        journal = await d.journal(cid)
        request = input_events(journal, "input_request")[0]
        self.assertEqual((request["request_id"], request["answer_kind"], request["choices"]), (f"{cid}_r1", "choice", ["CMD-1", "CMD-2"]))
        self.assertEqual((await d.runtime(cid))["status"], "waiting")                          # the pilot waits on the intention, like for any tool

        with self.assertRaises(DriverError) as invalid:                                          # an answer that does not suit leaves it open
            await d.call(AGENTIC, "input.answer", "user", {"continuity_id": cid, "request_id": f"{cid}_r1", "answer": "CMD-9"})
        self.assertEqual(invalid.exception.code, "invalid")
        self.assertEqual(await self._open(cid), True)

        out = await d.call(AGENTIC, "input.answer", "user", {"continuity_id": cid, "request_id": f"{cid}_r1", "answer": "CMD-2"})
        self.assertEqual(out, {"request_id": f"{cid}_r1", "answered": True})
        await d.quiet(cid)
        await d.assert_invariants(cid)

        journal = await d.journal(cid)
        response = next(m["content"]["response"] for m in journal if m["kind"] == "capacity_response")
        self.assertEqual((response["status"], response["payload"]["answer"]), ("ok", "CMD-2"))   # the answer of the person IS the Response of the intention
        self.assertEqual([m["content"]["content"] for m in journal if m["kind"] == "pilot"], ["thanks, noted"])
        self.assertEqual(len(input_events(journal, "input_response")), 1)

        with self.assertRaises(DriverError) as again:                                            # the first valid answer closed it
            await d.call(AGENTIC, "input.answer", "user", {"continuity_id": cid, "request_id": f"{cid}_r1", "answer": "CMD-1"})
        self.assertEqual(again.exception.reason, "already_answered")

    async def _open(self, cid):
        journal = await self.d.journal(cid)
        return bool(input_events(journal, "input_request")) and not input_events(journal, "input_response") and not input_events(journal, "input_closed")


if __name__ == "__main__":
    unittest.main()
