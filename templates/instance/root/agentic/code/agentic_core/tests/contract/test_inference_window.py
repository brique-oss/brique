"""The inference window of a continuity (substrate/inference_window.py), alone: who waits, who passes, in which order.

The contracts that use it (mailbox.send, pilot.activate, pilot.infer) have their own tests; these are the rules of the mechanism itself.
"""
import asyncio
import unittest

from . import harness  # noqa: F401  (puts agentic_core on the path)
from substrate import inference_window as iw


class InferenceWindow(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        iw._windows.clear()
        self.ttl = iw.RESERVATION_TTL_S

    async def asyncTearDown(self):
        iw.RESERVATION_TTL_S = self.ttl
        iw._windows.clear()

    async def test_without_a_window_the_door_is_open(self):
        await asyncio.wait_for(iw.door("k"), 1)
        self.assertFalse(iw.closed_for("k"))

    async def test_the_holder_writes_and_the_others_wait_for_its_end(self):
        order, started = [], asyncio.Event()

        async def holder():
            async with iw.hold("k"):
                started.set()
                await asyncio.sleep(0.05)
                await iw.door("k")                       # its own writes pass
                order.append("holder")

        async def outsider():
            await started.wait()
            await iw.door("k")
            order.append("outsider")
        await asyncio.wait_for(asyncio.gather(holder(), outsider()), 2)
        self.assertEqual(order, ["holder", "outsider"])

    async def test_the_waiters_are_served_in_arrival_order(self):
        order, first, release = [], asyncio.Event(), asyncio.Event()

        async def a():
            async with iw.hold("k"):
                first.set()
                await release.wait()
                order.append("a")

        async def waiter(name, holds):
            if holds:
                async with iw.hold("k"):
                    order.append(name)
            else:
                await iw.door("k")
                order.append(name)
        tasks = [asyncio.ensure_future(a())]
        await first.wait()
        for name, holds in (("b", True), ("c", False), ("d", True)):
            tasks.append(asyncio.ensure_future(waiter(name, holds)))
            await asyncio.sleep(0.01)
        release.set()
        await asyncio.wait_for(asyncio.gather(*tasks), 2)
        self.assertEqual(order, ["a", "b", "c", "d"])

    async def test_the_window_is_released_when_the_holder_fails(self):
        with self.assertRaises(RuntimeError):
            async with iw.hold("k"):
                raise RuntimeError("boom")
        await asyncio.wait_for(iw.door("k"), 1)
        self.assertEqual(iw._windows, {})

    async def test_a_loop_already_waiting_is_not_held_up_by_a_reservation_made_after_it_queued(self):
        """Captured on the real wrapper (dump of its tasks, three times alike): the window free, two holds queued, a reservation for the message m02 with 17.9 s
        left. The run of the continuity had asked for the window BEFORE the reservation existed, so it sat in the queue behind an outside writer; the
        reservation, which the document says closes the door to others and never to the loop, held the whole queue until its 30 s expired. mailbox.send and
        pilot.activate waited for each other: a stall of 30 s, once in about three full runs of the suite on a loaded machine."""
        iw.RESERVATION_TTL_S = 30.0                                   # the real value: the harness usually shortens it so that a test never waits for it
        first_in, release_first, served = asyncio.Event(), asyncio.Event(), []

        async def request_writer():                                    # a mailbox.send that holds the window for the write of a user request, then reserves it
            async with iw.hold("k") as handle:
                first_in.set()
                await release_first.wait()
                handle.reserve(["m02"])                                # at its exit: reserved for the run that will absorb m02

        async def outside_writer():                                    # another request, an outside writer, queued behind
            async with iw.hold("k"):
                served.append("outside")

        async def the_loop():                                          # the run of the continuity, queued while the first one still holds the window
            reset = iw.enter_loop("k")
            try:
                async with iw.hold("k"):
                    served.append("loop")
            finally:
                iw.leave_loop(reset)

        first = asyncio.ensure_future(request_writer())
        await first_in.wait()
        loop_task = asyncio.ensure_future(the_loop())                  # queued first: the window is held
        await asyncio.sleep(0)
        outside = asyncio.ensure_future(outside_writer())              # then the outside writer
        await asyncio.sleep(0.05)
        release_first.set()                                            # the first one ends and reserves the window
        await asyncio.wait_for(loop_task, 1.0)                         # the loop must go on at once, not after the 30 s of the reservation
        self.assertEqual(served, ["loop"])                             # and the outside writer still waits for the reservation
        outside.cancel()
        await first
        with self.assertRaises(asyncio.CancelledError):
            await outside

    async def test_a_cancelled_waiter_does_not_stall_the_queue(self):
        started, release = asyncio.Event(), asyncio.Event()

        async def a():
            async with iw.hold("k"):
                started.set()
                await release.wait()

        async def b():
            async with iw.hold("k"):
                pass
        ta = asyncio.ensure_future(a())
        await started.wait()
        tb = asyncio.ensure_future(b())
        await asyncio.sleep(0.02)
        tb.cancel()
        with self.assertRaises(asyncio.CancelledError):
            await tb
        release.set()
        await ta
        await asyncio.wait_for(iw.door("k"), 1)

    async def test_a_reservation_closes_the_door_to_others_but_not_to_the_loop(self):
        iw.RESERVATION_TTL_S = 5
        async with iw.hold("k") as handle:
            handle.reserve(["u1"])
        self.assertEqual(iw.reserved_ids("k"), {"u1"})
        self.assertTrue(iw.closed_for("k"))
        with self.assertRaises(asyncio.TimeoutError):
            await asyncio.wait_for(iw.door("k"), 0.05)

        async def loop():
            iw.enter_loop("k")                           # the identity of this task only
            self.assertFalse(iw.closed_for("k"))
            await asyncio.wait_for(iw.door("k"), 1)
        await asyncio.ensure_future(loop())

    async def test_the_loop_adopts_the_reservation_then_the_door_reopens(self):
        iw.RESERVATION_TTL_S = 5
        async with iw.hold("k") as handle:
            handle.reserve(["u1", "u2"])
        order = []

        async def outsider():
            await iw.door("k")
            order.append("outsider")
        waiting = asyncio.ensure_future(outsider())
        await asyncio.sleep(0.02)

        async def loop():
            iw.enter_loop("k")
            async with iw.hold("k", adopt=["u1"]):
                order.append("first")
            await asyncio.sleep(0.02)
            order.append("u2 still reserved")             # the outsider still waits: u2 has not been taken
            async with iw.hold("k", adopt=["u2"]):
                order.append("second")
        await asyncio.wait_for(asyncio.ensure_future(loop()), 2)
        await asyncio.wait_for(waiting, 2)
        self.assertEqual(order, ["first", "u2 still reserved", "second", "outsider"])

    async def test_a_reservation_nobody_takes_expires(self):
        iw.RESERVATION_TTL_S = 0.05
        async with iw.hold("k") as handle:
            handle.reserve(["u1"])
        await asyncio.wait_for(iw.door("k"), 1)
        self.assertEqual(iw.reserved_ids("k"), set())

    async def test_clear_lifts_a_reservation(self):
        iw.RESERVATION_TTL_S = 5
        async with iw.hold("k") as handle:
            handle.reserve(["u1"])
        waiting = asyncio.ensure_future(iw.door("k"))
        await asyncio.sleep(0.02)
        self.assertFalse(waiting.done())
        iw.clear("k")
        await asyncio.wait_for(waiting, 1)

    async def test_a_task_spawned_by_the_loop_is_an_outside_writer(self):
        iw.RESERVATION_TTL_S = 5
        async with iw.hold("k") as handle:
            handle.reserve(["u1"])

        async def child():
            iw.forget_role()
            return iw.closed_for("k")

        async def loop():
            iw.enter_loop("k")
            inside = iw.closed_for("k")
            spawned = await asyncio.ensure_future(child())     # a task copies the context of its parent: it would inherit the identity
            return inside, spawned
        self.assertEqual(await asyncio.ensure_future(loop()), (False, True))


if __name__ == "__main__":
    unittest.main()
