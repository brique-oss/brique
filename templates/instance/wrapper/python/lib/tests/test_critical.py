# Copyright 2026 Nicolas Cassan
# Licensed under the Apache License, Version 2.0.

import asyncio
import unittest

from wrapper.scheduler import WorkScheduler


class CriticalSections(unittest.IsolatedAsyncioTestCase):
    async def test_waiting_for_a_taken_key_gives_the_slot_back(self):
        # one slot: the section that waits for the key must let the work that holds it finish
        scheduler = WorkScheduler(1, 8)
        holder_in, holder_go = asyncio.Event(), asyncio.Event()
        order = []

        async def holder():
            async with scheduler.critical("k"):
                holder_in.set()
                async with scheduler.yielding():      # a call of its own: it waits for an answer, so gives its slot back
                    await holder_go.wait()
                order.append("holder done")

        async def waiter():
            async with scheduler.critical("k"):
                order.append("waiter in")

        scheduler.submit("a", holder, ordered=False)
        await holder_in.wait()
        scheduler.submit("b", waiter, ordered=False)
        await asyncio.sleep(0.05)
        holder_go.set()
        await asyncio.wait_for(scheduler.drain(), 5)
        self.assertEqual(order, ["holder done", "waiter in"])

    async def test_keys_are_taken_in_order(self):
        scheduler = WorkScheduler(8, 8)

        async def section(keys):
            async with scheduler.critical(*keys):
                await asyncio.sleep(0)

        await asyncio.wait_for(
            asyncio.gather(*(section(("x", "y") if i % 2 == 0 else ("y", "x")) for i in range(50))), 5)
        self.assertEqual(scheduler.locks.entries, {})      # nothing leaks

    async def test_a_cancelled_waiter_leaves_no_lock_behind(self):
        scheduler = WorkScheduler(2, 8)
        gate = asyncio.Event()

        async def holder():
            async with scheduler.critical("k"):
                await gate.wait()

        first = asyncio.create_task(holder())
        await asyncio.sleep(0)
        second = asyncio.create_task(holder())
        await asyncio.sleep(0.01)
        second.cancel()
        await asyncio.gather(second, return_exceptions=True)
        gate.set()
        await first
        self.assertEqual(scheduler.locks.entries, {})


if __name__ == "__main__":
    unittest.main()
