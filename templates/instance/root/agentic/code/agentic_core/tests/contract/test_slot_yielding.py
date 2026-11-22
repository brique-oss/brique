"""A wait for a lock or for the inference window must not hold an execution slot of the wrapper.

The library of the wrappers gives the slot back while a piece of work waits for the answer to a call (`yielding`), but not while it waits for anything
else. As many waits for a lock as there are slots (16 by default) then stop the holder of the lock from taking its own slot back after its next call,
and the wrapper freezes for good. The first test replays that failure with the real scheduler of the library; the others check that the waits of
agentic give the slot back.
"""
import asyncio
import os
import sys
import unittest

from . import harness  # noqa: F401  (puts agentic_core on the path)
from .harness import CapacityContract
from substrate import inference_window
from substrate.locks import critical
import mailbox_send

LIB = os.path.join(os.path.dirname(harness.AGENT_CORE), "..", "..", "..", "wrapper", "python", "lib")
sys.path.insert(0, os.path.abspath(LIB))
from wrapper.scheduler import WorkScheduler  # noqa: E402


class RealScheduler(unittest.IsolatedAsyncioTestCase):
    async def run_all(self, waiters: int, slots: int, yielding_locks: bool, timeout: float = 3.0) -> int:
        scheduler = WorkScheduler(slots, 256)
        done = []

        async def work(i):
            # a piece of work that takes a short lock, makes a call (its slot is given back while it waits) and goes on
            async with critical("shared", yielding=scheduler.yielding if yielding_locks else None):
                async with scheduler.yielding():
                    await asyncio.sleep(0.005)
            done.append(i)

        for i in range(waiters):
            scheduler.submit(f"work{i}", lambda i=i: work(i), ordered=False)
        try:
            await asyncio.wait_for(scheduler.drain(), timeout)
        except asyncio.TimeoutError:
            pass
        return len(done)

    async def test_more_waiters_than_slots_finish_when_the_lock_wait_gives_the_slot_back(self):
        self.assertEqual(await self.run_all(waiters=40, slots=4, yielding_locks=True), 40)

    async def test_the_same_load_freezes_the_wrapper_when_the_lock_wait_holds_the_slot(self):
        # the control: this is the failure observed on the real engine with 20 simultaneous calls on one lock and 16 slots
        self.assertLess(await self.run_all(waiters=12, slots=4, yielding_locks=False, timeout=1.0), 12)

    async def test_a_lock_that_is_free_needs_no_yielding(self):
        scheduler = WorkScheduler(2, 8)
        entered = []

        class Counting:
            def __call__(self):
                entered.append(1)
                return scheduler.yielding()
        async with critical("alone", yielding=Counting()):
            pass
        self.assertEqual(entered, [])


class WaitsGiveTheSlotBack(CapacityContract):
    CAP, MODULE = "mailbox.send", mailbox_send

    async def asyncSetUp(self):
        await super().asyncSetUp()
        self.e.caps["/root/agentic/mailbox.send"] = mailbox_send.run
        self.w.full("c1")

    def message(self, mid, kind="user"):
        return {"id": mid, "from": "user:1", "kind": kind, "content": "hello"}

    async def test_the_inference_window_wait_of_a_send_gives_the_slot_back(self):
        held, release = asyncio.Event(), asyncio.Event()

        async def holder():
            async with inference_window.hold("c1"):
                held.set()
                await release.wait()
        busy = asyncio.ensure_future(holder())
        await held.wait()
        waiting = asyncio.ensure_future(self.call(continuity_id="c1", message=self.message("w1")))
        await asyncio.sleep(0.05)
        self.assertIn("mailbox.send", self.e.yields)                          # it waits without holding its slot
        release.set()
        await waiting
        await busy

    async def test_a_user_request_waiting_for_its_turn_on_the_window_gives_the_slot_back(self):
        from substrate import liveness
        self.w.runtime("c1", "running", "R1")
        liveness.mark_live("c1", "R1")                                       # a working run: a user request opens the window, which waits behind the holder
        held, release = asyncio.Event(), asyncio.Event()

        async def holder():
            async with inference_window.hold("c1"):
                held.set()
                await release.wait()
        busy = asyncio.ensure_future(holder())
        await held.wait()
        waiting = asyncio.ensure_future(self.call(continuity_id="c1", message=self.message("u1")))
        await asyncio.sleep(0.05)
        self.assertIn("mailbox.send", self.e.yields)
        release.set()
        await waiting
        await busy
        inference_window.clear("c1")

    async def test_a_send_that_finds_the_window_free_does_not_yield(self):
        await self.call(continuity_id="c1", message=self.message("free1"))
        self.assertEqual(self.e.yields, [])


class ManyMarks(CapacityContract):
    CAP = "reading.mark"

    async def asyncSetUp(self):
        import reading_mark
        self.MODULE = reading_mark
        await super().asyncSetUp()
        from .harness import World
        world = World(self.e)
        for i in range(1, 31):
            world.continuity(f"l{i}")                                          # a mark names a conversation that exists

    async def test_simultaneous_marks_wait_for_the_lock_without_holding_their_slot(self):
        await asyncio.gather(*[self.call(lineage_id=f"l{i}", seq=i) for i in range(1, 31)])
        self.assertGreater(len(self.e.yields), 0)                              # the ones that found the lock taken waited by giving their slot back


if __name__ == "__main__":
    unittest.main()
