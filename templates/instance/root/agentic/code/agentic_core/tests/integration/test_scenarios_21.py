"""A user speaks to a pilot at work (user_message_in_flight.md): S78 to S80.

What the window must guarantee on the real engine: from the request of the user to the decision that answers it, and from the reading of the
journal to the decision of the pilot, nothing else enters the continuity. The answer of a tool that comes back in the meantime waits, and is
written after the decision.
"""
import asyncio
import unittest

from .scenarios_base import SKIP, SKIP_REASON, Scenario, say


def slow(ref, gate):
    return {"id": ref, "target_capacity": "/root/agentic/test/test.slow", "parameters": {"gate": gate, "value": "x"}, "await_response": True}


def wait_on(*pairs):
    """Wait for the tools named by (ref, gate) pairs."""
    return {"wait_group": {"condition": "all", "expected": [{"type": "intention", "ref": ref} for ref, _ in pairs]},
            "intentions": [slow(ref, gate) for ref, gate in pairs]}


class UserInFlight(Scenario):
    async def llm_in_flight(self, cid, calls):
        async def reached():
            return len([c for c in await self.d.llm_calls() if c["continuity"] == cid]) >= calls
        await self.d.until(reached, f"{calls} inferences of {cid} to be recorded")

    async def status(self, cid, status):
        async def reached():
            rt = await self.d.runtime(cid)
            return rt["status"] == status and rt
        return await self.d.until(reached, f"{cid} to be {status}")

    def index_of(self, journal, predicate):
        return next(i for i, m in enumerate(journal) if predicate(m))

    def decisions(self, journal):
        return [i for i, m in enumerate(journal) if m["kind"] == "completion"]


@unittest.skipIf(SKIP, SKIP_REASON)
class S78UserSpeaksWhileTheToolWorks(UserInFlight):
    PREFIX = "it_s78"

    async def test_the_request_passes_and_the_answer_of_the_tool_waits_for_the_decision(self):
        d, cid = self.d, self.PREFIX
        await d.reset_world([
            {"when": {"continuity": cid, "user_message": True}, "then": {"output": say("je t'entends"), "wait_gate": "gUser"}},
            {"when": {"continuity": cid, "stimulus": "active_wait_result"}, "then": {"output": say("l'outil a fini")}},
            {"when": {"continuity": cid, "message_id": f"{cid}_m1", "stimulus": "none"}, "then": {"output": wait_on(("s1", "gTool"))}},
        ])
        await d.create(cid)
        await d.send(cid, f"{cid}_m1", "lance l'outil lent")
        await self.status(cid, "waiting")

        await d.send(cid, f"{cid}_u2", "change of priority")                                # the request passes, while the tool works
        await self.llm_in_flight(cid, 2)                                                    # the model is thinking about it (held)
        await d.open_gate("gTool")                                                          # the tool finishes while the model thinks
        await asyncio.sleep(1.0)                                                            # the delivery of its answer has had every chance
        journal = await d.journal(cid)
        self.assertNotIn("capacity_response", [m["kind"] for m in journal])                 # held: nothing enters between the request and the decision
        self.assertEqual([m["id"] for m in journal if m["kind"] == "user"], [f"{cid}_m1", f"{cid}_u2"])
        rt = await d.runtime(cid)
        self.assertEqual((rt["current_message_id"], len(rt["active_wait_groups"])), (f"{cid}_m1", 1))   # still the chain of m1, its group still open

        await d.open_gate("gUser")
        await d.quiet(cid, timeout=60)
        journal = await d.journal(cid)
        request = self.index_of(journal, lambda m: m["id"] == f"{cid}_u2")
        answer = self.decisions(journal)
        reply = next(i for i in answer if i > request)                                      # the decision that answers the request
        tool = self.index_of(journal, lambda m: m["kind"] == "capacity_response")
        self.assertLess(reply, tool)                                                        # the answer of the tool is written after it
        self.assertEqual([m["kind"] for m in journal[request + 1:reply] if m["kind"] != "event"], [])    # nothing came between request and decision
        self.assertEqual([m["content"]["content"] for m in journal if m["kind"] == "pilot"], ["je t'entends", "l'outil a fini"])
        self.assertEqual(await d.mailbox(cid), [])                                          # both messages were acknowledged, with the chain
        await d.assert_invariants(cid)


@unittest.skipIf(SKIP, SKIP_REASON)
class S79ToolAnswerDuringTheInferenceOfThePilot(UserInFlight):
    PREFIX = "it_s79"

    async def test_an_answer_that_comes_back_while_the_pilot_thinks_is_written_after_its_decision(self):
        d, cid = self.d, self.PREFIX
        await d.reset_world([
            {"when": {"continuity": cid, "stimulus": "active_wait_result"}, "then": {"output": say("A est revenu"), "wait_gate": "gThink"}},
            {"when": {"continuity": cid, "message_id": f"{cid}_m1", "stimulus": "none"},
             "then": {"output": {"wait_group": {"condition": "all", "expected": [{"type": "intention", "ref": "a"}]},
                                 "intentions": [slow("a", "gA"), slow("b", "gB")]}}},
            {"when": {"continuity": cid}, "then": {"output": say("B noted")}},                   # the answer of B, a message of its own once it is written
        ])
        await d.create(cid)
        await d.send(cid, f"{cid}_m1", "lance A et B, n'attends que A")
        await self.status(cid, "waiting")
        await d.open_gate("gA")
        await self.llm_in_flight(cid, 2)                                                    # the pilot resumed and is thinking (held)
        await d.open_gate("gB")                                                             # B comes back while it thinks
        await asyncio.sleep(1.0)
        journal = await d.journal(cid)
        answers = [m for m in journal if m["kind"] == "capacity_response"]
        self.assertEqual(len(answers), 1)                                                   # A only: the answer of B waits at the door
        await d.open_gate("gThink")
        await d.quiet(cid, timeout=60)
        journal = await d.journal(cid)
        resume = self.decisions(journal)[1]                                                 # the decision taken without B (the first one asked for the tools)
        late = [i for i, m in enumerate(journal) if m["kind"] == "capacity_response" and m["correlation"].get("intention_id") == "b"]
        self.assertEqual(len(late), 1)
        self.assertLess(resume, late[0])                                             # written after the decision that had not seen it
        await d.assert_invariants(cid)


@unittest.skipIf(SKIP, SKIP_REASON)
class S80TwoRequestsInARow(UserInFlight):
    PREFIX = "it_s80"

    async def test_two_requests_are_taken_in_order_and_end_with_the_chain(self):
        d, cid = self.d, self.PREFIX
        await d.reset_world([
            {"when": {"continuity": cid, "user_message": True, "call_index": 2}, "then": {"output": say("premier"), "wait_gate": "gFirst"}},
            {"when": {"continuity": cid, "user_message": True}, "then": {"output": say("suivant")}},
            {"when": {"continuity": cid, "stimulus": "active_wait_result"}, "then": {"output": say("fini")}},
            {"when": {"continuity": cid, "message_id": f"{cid}_m1", "stimulus": "none"}, "then": {"output": wait_on(("s1", "gTool"))}},
        ])
        await d.create(cid)
        await d.send(cid, f"{cid}_m1", "lance l'outil lent")
        await self.status(cid, "waiting")
        await d.send(cid, f"{cid}_u2", "d'abord ceci")
        await self.llm_in_flight(cid, 2)                                                    # the first request is being answered (held)
        sent = asyncio.ensure_future(d.send(cid, f"{cid}_u3", "puis cela"))                 # the second waits at the door, behind the first
        await asyncio.sleep(0.5)
        self.assertNotIn(f"{cid}_u3", [m["id"] for m in await d.journal(cid)])              # not written while the first is being answered
        await d.open_gate("gFirst")
        await sent
        await d.until(lambda: self._answered(cid, "suivant"), "the second request to be answered")
        await d.open_gate("gTool")
        await d.quiet(cid, timeout=60)
        journal = await d.journal(cid)
        self.assertEqual([m["id"] for m in journal if m["kind"] == "user"], [f"{cid}_m1", f"{cid}_u2", f"{cid}_u3"])
        self.assertEqual([m["content"]["content"] for m in journal if m["kind"] == "pilot"], ["premier", "suivant", "fini"])   # each answered once, in order
        self.assertEqual(await d.mailbox(cid), [])
        await d.assert_invariants(cid)

    async def _answered(self, cid, text):
        return any(m["kind"] == "pilot" and m["content"].get("content") == text for m in await self.d.journal(cid))


if __name__ == "__main__":
    unittest.main()
