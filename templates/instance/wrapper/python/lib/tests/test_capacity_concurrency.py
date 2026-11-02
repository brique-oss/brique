# Copyright 2026 Nicolas Cassan
# Licensed under the Apache License, Version 2.0.

import asyncio
import unittest
from types import SimpleNamespace

from wrapper import serial
from wrapper.execution import CapacityExecutor
from wrapper.models import CapacityBinding
from wrapper.registry import RuntimeRegistry
from wrapper.runtime import WrapperRuntime
from wrapper.scheduler import WorkScheduler


def runtime_with(**capacities):
    """A runtime reduced to what routing a capacity call needs."""
    registry = RuntimeRegistry(SimpleNamespace())
    registry.capacity_bindings = {
        ("", name): CapacityBinding("", name, symbol, serial=bool(getattr(symbol, "_brique_serial", False)))
        for name, symbol in capacities.items()
    }
    return SimpleNamespace(registry=registry, transit=SimpleNamespace(emit_returns=False, release=lambda *_a, **_k: None))


class SchedulerOrderTests(unittest.IsolatedAsyncioTestCase):
    async def test_unordered_work_under_one_key_runs_in_parallel(self) -> None:
        scheduler = WorkScheduler(max_concurrency=4, queue_limit=8)
        release = asyncio.Event()
        started: list[str] = []
        both = asyncio.Event()

        async def work(name: str) -> None:
            started.append(name)
            if len(started) == 2:
                both.set()
            await release.wait()

        scheduler.submit("cap:a", lambda: work("first"), ordered=False)
        scheduler.submit("cap:a", lambda: work("second"), ordered=False)
        await asyncio.wait_for(both.wait(), 1)          # the second did not wait for the first
        release.set()
        await scheduler.close()

    async def test_ordered_work_under_one_key_still_runs_one_at_a_time(self) -> None:
        scheduler = WorkScheduler(max_concurrency=4, queue_limit=8)
        release = asyncio.Event()
        started: list[str] = []

        async def work(name: str) -> None:
            started.append(name)
            await release.wait()

        scheduler.submit("matter:a", lambda: work("first"))
        scheduler.submit("matter:a", lambda: work("second"))
        await asyncio.sleep(0.05)
        self.assertEqual(started, ["first"])
        release.set()
        await scheduler.close()
        self.assertEqual(started, ["first", "second"])


class RuntimeOrderingTests(unittest.TestCase):
    def test_a_capacity_is_parallel_unless_declared_serial(self) -> None:
        async def plain(_params): return {}

        @serial
        async def one_at_a_time(_params): return {}

        runtime = runtime_with(plain=plain, one_at_a_time=one_at_a_time)
        check = WrapperRuntime._capacity_is_serial
        self.assertFalse(check(SimpleNamespace(registry=runtime.registry), "", "plain"))
        self.assertTrue(check(SimpleNamespace(registry=runtime.registry), "", "one_at_a_time"))
        self.assertFalse(check(SimpleNamespace(registry=runtime.registry), "", "unknown"))


class ExecutionTests(unittest.IsolatedAsyncioTestCase):
    async def test_two_calls_of_a_parallel_capacity_overlap(self) -> None:
        release = asyncio.Event()
        running = 0
        peak = 0

        async def capacity(_params):
            nonlocal running, peak
            running += 1
            peak = max(peak, running)
            await release.wait()
            running -= 1
            return {}

        executor = CapacityExecutor(runtime_with(cap=capacity))
        calls = [asyncio.ensure_future(executor.execute_capacity("", "cap", {"params": {}})) for _ in range(3)]
        await asyncio.sleep(0.05)
        self.assertEqual(peak, 3)
        release.set()
        await asyncio.gather(*calls)

    async def test_two_calls_of_a_serial_capacity_never_overlap(self) -> None:
        release = asyncio.Event()
        running = 0
        peak = 0

        @serial
        async def capacity(_params):
            nonlocal running, peak
            running += 1
            peak = max(peak, running)
            await release.wait()
            running -= 1
            return {}

        executor = CapacityExecutor(runtime_with(cap=capacity))
        calls = [asyncio.ensure_future(executor.execute_capacity("", "cap", {"params": {}})) for _ in range(3)]
        await asyncio.sleep(0.05)
        self.assertEqual(peak, 1)
        release.set()
        await asyncio.gather(*calls)

    async def test_a_capacity_that_waits_for_another_call_of_itself_does_not_deadlock(self) -> None:
        """A parent that waits for the child it started, both being calls of the same capacity."""
        child_ran = asyncio.Event()
        holder = {}

        async def capacity(params):
            if params.get("role") == "parent":
                child = asyncio.ensure_future(holder["executor"].execute_capacity("", "cap", {"params": {"role": "child"}}))
                await asyncio.wait_for(child, 1)
                return {"parent": "done"}
            child_ran.set()
            return {"child": "done"}

        holder["executor"] = CapacityExecutor(runtime_with(cap=capacity))
        out = await asyncio.wait_for(holder["executor"].execute_capacity("", "cap", {"params": {"role": "parent"}}), 2)
        self.assertEqual(out, {"parent": "done"})
        self.assertTrue(child_ran.is_set())


if __name__ == "__main__":
    unittest.main()


class SlotsWhileWaitingTests(unittest.IsolatedAsyncioTestCase):
    async def test_calls_waiting_for_each_other_do_not_exhaust_the_slots(self) -> None:
        """As many parents as there are slots, each waiting for a child that needs a slot."""
        scheduler = WorkScheduler(max_concurrency=3, queue_limit=16)
        children_done = 0
        parents_done = 0
        all_done = asyncio.Event()

        async def child() -> None:
            nonlocal children_done
            children_done += 1

        async def parent() -> None:
            nonlocal parents_done
            started = asyncio.get_running_loop().create_future()
            scheduler.submit("cap", lambda: finish(started), ordered=False)
            async with scheduler.yielding():
                await started
            parents_done += 1
            if parents_done == 3:
                all_done.set()

        async def finish(started) -> None:
            await child()
            started.set_result(True)

        for _ in range(3):
            scheduler.submit("cap", parent, ordered=False)
        await asyncio.wait_for(all_done.wait(), 2)
        self.assertEqual((parents_done, children_done), (3, 3))
        await scheduler.close()

    async def test_the_slot_is_taken_back_and_the_bound_holds(self) -> None:
        scheduler = WorkScheduler(max_concurrency=2, queue_limit=16)
        running = 0
        peak = 0
        release = asyncio.Event()
        done = 0

        async def work() -> None:
            nonlocal running, peak, done
            async with scheduler.yielding():
                await asyncio.sleep(0.01)            # waiting: not counted
            running += 1
            peak = max(peak, running)
            await release.wait()                     # working: counted
            running -= 1
            done += 1

        for _ in range(6):
            scheduler.submit("cap", work, ordered=False)
        await asyncio.sleep(0.1)
        self.assertEqual(peak, 2)                    # never more than the bound at work together
        release.set()
        await scheduler.close()
        self.assertEqual(done, 6)

    async def test_several_calls_waiting_together_release_the_slot_once(self) -> None:
        scheduler = WorkScheduler(max_concurrency=1, queue_limit=4)
        order: list[str] = []

        async def waits_on_two() -> None:
            async def wait_a() -> None:
                async with scheduler.yielding():
                    await asyncio.sleep(0.02)

            async def wait_b() -> None:
                async with scheduler.yielding():
                    await asyncio.sleep(0.04)

            await asyncio.gather(wait_a(), wait_b())
            order.append("first-done")

        async def other() -> None:
            order.append("other")

        scheduler.submit("one", waits_on_two, ordered=False)
        scheduler.submit("two", other, ordered=False)
        await scheduler.close()
        self.assertEqual(sorted(order), ["first-done", "other"])
        self.assertEqual(scheduler._slots._value, 1)   # one slot, given back exactly once
