"""The fake model of the tests writes the calls that arrive together in ONE write of its log (llm_calls), not one write each.

One write per call, with a retry on conflict, made forty simultaneous calls take 62 s instead of 2 s (see tests/load/README.md). The guarantee is unchanged:
a call is in the log, numbered, before its answer goes out.
"""
import asyncio
import copy
import os
import sys
import unittest

from . import harness

TOOLS = os.path.abspath(os.path.join(harness.AGENT_CORE, "..", "..", "test", "code", "test_tools"))
sys.path.insert(0, TOOLS)
import infer_fake  # noqa: E402


class SlowStore:
    """The Matter of the log with a revision, as the engine guards it: a write is refused when another one went in between."""

    def __init__(self):
        self.content, self.revision = {}, 0
        self.reads = self.writes = self.conflicts = 0

    async def update(self, matter_id, fn):
        assert matter_id == "llm_calls"
        while True:
            self.reads += 1
            seen, snapshot = self.revision, copy.deepcopy(self.content)
            new, result = fn(snapshot)
            await asyncio.sleep(0.005)                  # the time a real read-recompute-write takes: where the callers collide
            if self.revision != seen:
                self.conflicts += 1
                continue
            self.content, self.revision = new, self.revision + 1
            self.writes += 1
            return result


def recorder(label):
    def record(content):
        calls = content.setdefault("calls", [])
        entry = {"n": len(calls) + 1, "label": label, "previous_of_same": sum(1 for c in calls if c["label"] == label)}
        calls.append(entry)
        return content, entry["n"]
    return record


class CallsAreWrittenTogether(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        infer_fake._pending.clear()
        infer_fake._leading = False

    async def test_sixty_simultaneous_calls_make_a_handful_of_writes_and_none_is_lost(self):
        store = SlowStore()
        numbers = await asyncio.gather(*[infer_fake.log_call(store, recorder(f"c{i % 3}")) for i in range(60)])
        self.assertEqual(sorted(numbers), list(range(1, 61)))                       # every call numbered once, none lost
        self.assertEqual(len(store.content["calls"]), 60)
        self.assertLessEqual(store.writes, 6)                                       # not 60
        self.assertEqual(store.conflicts, 0)                                        # a single writer: nothing to fight over

    async def test_the_calls_keep_the_order_in_which_they_arrived(self):
        store = SlowStore()
        tasks = []
        for i in range(10):
            tasks.append(asyncio.ensure_future(infer_fake.log_call(store, recorder("a"))))
            await asyncio.sleep(0)                                                  # arrival order is the order of the scheduling
        numbers = await asyncio.gather(*tasks)
        self.assertEqual(numbers, list(range(1, 11)))
        self.assertEqual([c["previous_of_same"] for c in store.content["calls"]], list(range(10)))     # the per-continuity count a rule reads

    async def test_a_call_is_in_the_log_before_it_returns(self):
        store = SlowStore()
        number = await infer_fake.log_call(store, recorder("x"))
        self.assertEqual(store.content["calls"][number - 1]["label"], "x")

    async def test_a_failing_write_fails_the_calls_of_its_batch_only_and_the_writer_is_not_left_dead(self):
        class Failing(SlowStore):
            async def update(self, matter_id, fn):
                if not getattr(self, "failed", False):
                    self.failed = True
                    await asyncio.sleep(0.01)                  # the write takes a moment: the other calls queue up behind it meanwhile
                    raise RuntimeError("the log cannot be written")
                return await super().update(matter_id, fn)
        store = Failing()
        results = await asyncio.gather(*[infer_fake.log_call(store, recorder("x")) for _ in range(5)], return_exceptions=True)
        self.assertIsInstance(results[0], RuntimeError)                                 # the call whose batch was being written learns it
        self.assertEqual(sorted(results[1:]), [1, 2, 3, 4])                             # the calls that queued behind it are written by the next batch
        self.assertEqual(await infer_fake.log_call(store, recorder("x")), 5)            # and the writer is free for the next call


class WaitingCallsGiveTheirSlotBack(unittest.IsolatedAsyncioTestCase):
    """As many calls waiting for the writer as there are execution slots must not stop the writer from going on (the freeze of a wait that holds its slot)."""

    def setUp(self):
        infer_fake._pending.clear()
        infer_fake._leading = False
        sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(harness.AGENT_CORE), "..", "..", "..", "wrapper", "python", "lib")))
        from wrapper.scheduler import WorkScheduler
        self.WorkScheduler = WorkScheduler

    def port_with_scheduler(self, scheduler, store):
        class Runtime:
            pass
        runtime = Runtime()
        runtime.scheduler = scheduler

        class Port:
            def __init__(self):
                self.runtime = runtime

            async def update(self, matter_id, fn):
                # a call to the engine: the slot is given back while it waits for the answer, as the library does
                async with scheduler.yielding():
                    return await store.update(matter_id, fn)
        return Port()

    async def run_calls(self, calls, slots, timeout=3.0):
        scheduler = self.WorkScheduler(slots, 256)
        port = self.port_with_scheduler(scheduler, SlowStore())
        done = []

        async def one(i):
            await infer_fake.log_call(port, recorder("c"))
            done.append(i)
        for i in range(calls):
            scheduler.submit(f"call{i}", lambda i=i: one(i), ordered=False)
        try:
            await asyncio.wait_for(scheduler.drain(), timeout)
        except asyncio.TimeoutError:
            pass
        return len(done)

    async def test_forty_calls_on_four_slots_all_finish(self):
        self.assertEqual(await self.run_calls(40, 4), 40)

    async def test_the_control_freezes_when_the_wait_holds_the_slot(self):
        original = infer_fake._waiting
        infer_fake._waiting = lambda port, future: future            # the mutation: the wait holds the slot
        try:
            finished = await self.run_calls(40, 4, timeout=1.0)
        finally:
            infer_fake._waiting = original
        self.assertLess(finished, 40)
