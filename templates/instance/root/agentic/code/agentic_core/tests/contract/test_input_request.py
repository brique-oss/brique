import asyncio
import json
import unittest

from .harness import CapacityContract, covers, message
from substrate.errors import AlreadyAnswered, IdentityConflict, InvalidRequest, NotFound, RequestCancelled, RequestClosed, WaitTimeout
import input_answer
import input_request


def input_lines(world, cid="c1", event=None):
    return [m["content"] for m in world.journal(cid)
            if m["kind"] == "event" and str(m["content"].get("event", "")).startswith("input_") and (event is None or m["content"]["event"] == event)]


class InputContract(CapacityContract):
    async def asyncSetUp(self):
        await super().asyncSetUp()
        self._poll = input_request.INPUT_POLL_S
        input_request.INPUT_POLL_S = 0.02                     # a test never waits for the safety net
        self.w.full("c1", messages=[message(1, "m1", content="hello")])

    async def asyncTearDown(self):
        input_request.INPUT_POLL_S = self._poll
        await super().asyncTearDown()

    async def ask(self, **extra):
        return await input_request.run(self.p, dict({"continuity_id": "c1", "question": "Which order?", "request_id": "r1"}, **extra))

    async def answer(self, answer="CMD-1042", **extra):
        return await input_answer.run(self.p, dict({"continuity_id": "c1", "request_id": "r1", "answer": answer}, **extra))

    def asking(self, **extra):
        return asyncio.ensure_future(self.ask(**extra))

    async def until_written(self, event="input_request", n=1, cid="c1"):
        for _ in range(200):
            if len(input_lines(self.w, cid, event=event)) >= n:
                return
            await asyncio.sleep(0.01)
        self.fail(f"{event} was not written")


class InputRequestContract(InputContract):
    CAP, MODULE = "input.request", input_request

    async def call(self, **params):
        return await self.ask(**params)

    async def test_payload_matches_the_declared_outputs(self):
        task = self.asking()
        await self.until_written()
        await self.answer()
        out = await task
        self.assertPayloadMatchesContract(out)
        self.assertEqual((out["request_id"], out["answer"]), ("r1", "CMD-1042"))

    @covers("The request is a line of kind event, event input_request",
            "The answer of the call is the answer of the person given through input.answer")
    async def test_the_request_is_a_journal_event_and_the_call_answers_with_the_answer(self):
        task = self.asking()
        await self.until_written()
        self.assertFalse(task.done())                                                    # it waits for the person
        request = input_lines(self.w, event="input_request")[0]
        self.assertEqual((request["request_id"], request["question"], request["answer_kind"]), ("r1", "Which order?", "text"))
        self.assertIn("expires_at", request)
        await self.answer("CMD-7")
        self.assertEqual((await task)["answer"], "CMD-7")
        self.assertEqual(self.e.keepalives.count("input.request"), 1)                    # a running acknowledgement while it waits
        self.assertIn("input.request", self.e.yields)                                    # and the execution slot given back

    @covers("A request is open until an input_response or an input_closed line carries its request_id; if both exist, the first written in the journal wins.")
    async def test_when_both_a_response_and_a_closure_exist_the_first_in_the_journal_wins(self):
        task = self.asking()
        await self.until_written()
        await self.answer("first")
        await task
        # a closure written after the response (a deadline that passed just after): the response stands
        from substrate.journal import append_trace
        await append_trace(self.p, "c1", "event", {"event": "input_closed", "request_id": "r1", "reason": "timeout", "closed_at": "x"}, message_id="late_close")
        again = await self.ask()
        self.assertEqual(again["answer"], "first")

    @covers("The same request_id with the same question, kind and choices is the same request")
    async def test_the_same_request_again_writes_nothing_twice_and_gives_the_answer_at_once(self):
        task = self.asking()
        await self.until_written()
        await self.answer("CMD-1")
        await task
        before = len(self.w.journal("c1"))
        again = await asyncio.wait_for(self.ask(), 1)                                   # answered already: no wait
        self.assertEqual(again["answer"], "CMD-1")
        self.assertEqual(len(self.w.journal("c1")), before)

    async def test_the_wait_reads_only_the_journal_tail_after_its_cursor(self):
        from substrate import continuity_store
        from substrate.journal import append_trace
        cursors = []
        original = input_request.continuity_store.lines_after

        async def observed(port, cid, seq):
            cursors.append(seq)
            return await original(port, cid, seq)
        input_request.continuity_store.lines_after = observed
        try:
            task = self.asking()
            await self.until_written()
            await asyncio.sleep(0.06)
            await append_trace(self.p, "c1", "event", {"event": "unrelated"}, message_id="unrelated")
            await asyncio.sleep(0.06)
            await self.answer()
            await task
        finally:
            input_request.continuity_store.lines_after = original
        self.assertEqual(cursors, sorted(cursors))
        self.assertGreater(max(cursors), min(cursors))

    @covers("The same request_id with another question, kind or choices is an identity conflict")
    async def test_the_same_id_with_another_question_is_refused(self):
        task = self.asking()
        await self.until_written()
        for other in ({"question": "Another?"}, {"answer_kind": "confirm"}, {"answer_kind": "choice", "choices": ["a", "b"]}):
            with self.assertRaises(IdentityConflict):
                await self.ask(**other)
        task.cancel()

    @covers("A request lives at most timeout_s")
    async def test_an_unanswered_request_times_out_and_is_closed(self):
        with self.assertRaises(WaitTimeout) as caught:
            await self.ask(timeout_s=0.15)
        self.assertEqual((caught.exception.code, caught.exception.reason), ("refused", "timeout"))
        closed = input_lines(self.w, event="input_closed")
        self.assertEqual([(c["request_id"], c["reason"]) for c in closed], [("r1", "timeout")])
        with self.assertRaises(RequestClosed if False else (WaitTimeout)):
            await self.ask(timeout_s=0.15)                                               # a request closed before: the error of its closure
        with self.assertRaises(RequestClosed):
            await self.answer("too late")

    @covers("A request whose continuity is deleted or replaced by its successor while it waits is closed with reason cancelled")
    async def test_a_continuity_replaced_or_deleted_while_waiting_cancels_the_request(self):
        task = self.asking()
        await self.until_written()
        header = self.w.state("continuity_c1")
        header.update(status="archived", successor_id="c1_g2")
        self.w.put("continuity_c1", "continuity", header)
        with self.assertRaises(RequestCancelled) as caught:
            await asyncio.wait_for(task, 2)
        self.assertEqual(caught.exception.reason, "cancelled")
        self.assertEqual([c["reason"] for c in input_lines(self.w, event="input_closed")], ["cancelled"])
        self.w.full("c2", messages=[message(1, "m1")])
        task = asyncio.ensure_future(input_request.run(self.p, {"continuity_id": "c2", "question": "Q?", "request_id": "r2"}))
        await self.until_written(cid="c2")
        del self.e.matters["continuity_c2"]                                              # deleted outright
        with self.assertRaises(RequestCancelled):
            await asyncio.wait_for(task, 2)

    @covers("A restart of the wrapper takes the wait away, not the request")
    async def test_after_a_restart_the_same_call_waits_again_and_the_request_is_not_written_twice(self):
        lost = self.asking()
        await self.until_written()
        lost.cancel()                                                                    # the wrapper went: the wait with it
        with self.assertRaises(asyncio.CancelledError):
            await lost
        self.assertEqual(len(input_lines(self.w, event="input_request")), 1)             # the request is still open in the journal
        again = self.asking()
        await asyncio.sleep(0.1)
        self.assertEqual(len(input_lines(self.w, event="input_request")), 1)             # not written twice
        await self.answer("after the restart")
        self.assertEqual((await again)["answer"], "after the restart")

    @covers("input.request writes only lines of the journal of the continuity that asks.")
    async def test_only_the_journal_is_written(self):
        others = {k: json.dumps(v, sort_keys=True, default=str) for k, v in self.e.matters.items() if not k.startswith("journal_")}
        task = self.asking()
        await self.until_written()
        await self.answer()
        await task
        after = {k: json.dumps(v, sort_keys=True, default=str) for k, v in self.e.matters.items() if not k.startswith("journal_")}
        self.assertEqual(others, after)

    @covers("Fails with an invalid request when continuity_id or question is missing or empty")
    async def test_a_malformed_call_fails_before_any_effect(self):
        before = self.e.snapshot()
        bad = ({"continuity_id": None}, {"question": None}, {"question": "  "}, {"answer_kind": "free"}, {"answer_kind": "choice"},
               {"answer_kind": "choice", "choices": ["only"]}, {"answer_kind": "choice", "choices": ["a", "a"]}, {"choices": ["a", "b"]},
               {"timeout_s": 0}, {"timeout_s": "soon"}, {"request_id": " "})
        for params in bad:
            with self.assertRaises(InvalidRequest, msg=str(params)):
                await self.ask(**params)
        self.assertEqual(self.e.snapshot(), before)

    @covers("Fails not found when the continuity does not exist, before anything is written.")
    async def test_an_unknown_continuity_fails_before_anything_is_written(self):
        before = self.e.snapshot()
        with self.assertRaises(NotFound):
            await self.ask(continuity_id="nobody")
        self.assertEqual(self.e.snapshot(), before)

    @covers("Fails refused (reason timeout) when nobody answers in time")
    async def test_the_refusals_carry_their_own_reason(self):
        with self.assertRaises(WaitTimeout):
            await self.ask(timeout_s=0.1)


class InputAnswerContract(InputContract):
    CAP, MODULE = "input.answer", input_answer

    async def call(self, **params):
        return await self.answer(**params)

    async def open_request(self, **extra):
        task = self.asking(**extra)
        await self.until_written()
        return task

    async def test_payload_matches_the_declared_outputs(self):
        task = await self.open_request()
        out = await self.answer()
        self.assertPayloadMatchesContract(out)
        self.assertEqual(out, {"request_id": "r1", "answered": True})
        await task

    @covers("The answer is validated against the kind of the request")
    async def test_an_answer_is_validated_against_the_kind_and_an_invalid_one_leaves_the_request_open(self):
        task = await self.open_request(answer_kind="choice", choices=["red", "blue"])
        for bad in ("green", "", None, 3):
            with self.assertRaises(InvalidRequest):
                await self.answer(bad)
        self.assertEqual(input_lines(self.w, event="input_response"), [])               # still open: the person can correct
        self.assertFalse(task.done())
        await self.answer("blue")
        self.assertEqual((await task)["answer"], "blue")
        task = asyncio.ensure_future(input_request.run(self.p, {"continuity_id": "c1", "question": "Sure?", "answer_kind": "confirm", "request_id": "r2"}))
        await self.until_written(n=2)
        for bad in ("maybe", True, "Yes"):
            with self.assertRaises(InvalidRequest):
                await self.answer(bad, request_id="r2")
        await self.answer("no", request_id="r2")
        self.assertEqual((await task)["answer"], "no")
        task = asyncio.ensure_future(input_request.run(self.p, {"continuity_id": "c1", "question": "Say?", "request_id": "r3"}))
        await self.until_written(n=3)
        with self.assertRaises(InvalidRequest):
            await self.answer("   ", request_id="r3")
        await self.answer("hello", request_id="r3")
        await task

    @covers("The first valid answer closes the request: one input_response line carrying the answer is written")
    async def test_the_first_valid_answer_is_written_once_and_the_waiting_call_goes_on(self):
        task = await self.open_request()
        await self.answer("CMD-9")
        lines = input_lines(self.w, event="input_response")
        self.assertEqual([(l["request_id"], l["answer"]) for l in lines], [("r1", "CMD-9")])
        self.assertEqual((await asyncio.wait_for(task, 1))["answer"], "CMD-9")

    @covers("The same answer again gives the same result and writes nothing more")
    async def test_the_same_answer_again_is_the_same_result_and_another_is_refused(self):
        task = await self.open_request()
        first = await self.answer("one")
        before = len(self.w.journal("c1"))
        self.assertEqual(await self.answer("one"), first)
        self.assertEqual(len(self.w.journal("c1")), before)
        with self.assertRaises(AlreadyAnswered) as caught:
            await self.answer("two")
        self.assertEqual(caught.exception.reason, "already_answered")
        await task

    @covers("A closed request cannot be answered")
    async def test_a_closed_request_cannot_be_answered(self):
        with self.assertRaises(WaitTimeout):
            await self.ask(timeout_s=0.1)
        with self.assertRaises(RequestClosed) as caught:
            await self.answer("late")
        self.assertEqual((caught.exception.code, caught.exception.reason), ("refused", "closed"))
        self.assertEqual(input_lines(self.w, event="input_response"), [])

    @covers("input.answer writes only the input_response line.")
    async def test_only_the_response_line_is_written(self):
        task = await self.open_request()
        before = {k: json.dumps(v, sort_keys=True, default=str) for k, v in self.e.matters.items() if k != "journal_c1"}
        n = len(self.w.journal("c1"))
        await self.answer()
        self.assertEqual({k: json.dumps(v, sort_keys=True, default=str) for k, v in self.e.matters.items() if k != "journal_c1"}, before)
        self.assertEqual(len(self.w.journal("c1")), n + 1)
        await task

    @covers("Fails with an invalid request when continuity_id, request_id or answer is missing")
    async def test_a_malformed_call_fails_before_any_effect(self):
        task = await self.open_request()
        before = self.e.snapshot()
        for params in ({"continuity_id": None}, {"request_id": ""}, {"request_id": None}):
            with self.assertRaises(InvalidRequest):
                await self.answer(**params)
        with self.assertRaises(InvalidRequest):
            await input_answer.run(self.p, {"continuity_id": "c1", "request_id": "r1"})        # no answer at all
        self.assertEqual(self.e.snapshot(), before)
        task.cancel()

    @covers("Fails not found when the continuity has no such request.")
    async def test_an_unknown_request_is_not_found(self):
        with self.assertRaises(NotFound):
            await self.answer(request_id="nothing")
        with self.assertRaises(NotFound):
            await self.answer(continuity_id="nobody")


if __name__ == "__main__":
    unittest.main()
