# Copyright 2026 Nicolas Cassan
# Licensed under the Apache License, Version 2.0.

import asyncio
import os
import tempfile
import unittest

from wrapper import diagnostic
from wrapper.scheduler import SchedulerFullError, WorkScheduler


class SchedulerStats(unittest.IsolatedAsyncioTestCase):
    async def test_stats_follow_the_work_and_count_the_refusals(self):
        scheduler = WorkScheduler(1, 1)
        gate = asyncio.Event()

        async def work():
            await gate.wait()

        scheduler.submit("a", work)
        scheduler.submit("b", work, ordered=False)
        await asyncio.sleep(0.01)
        with self.assertRaises(SchedulerFullError):
            scheduler.submit("c", work, ordered=False)
        stats = scheduler.stats()
        self.assertEqual((stats["max_concurrency"], stats["free_slots"], stats["active"], stats["pending"], stats["queued"], stats["refused"]),
                         (1, 0, 1, 2, 1, 1))
        self.assertEqual(stats["ordered_keys"], ["a"])
        gate.set()
        await scheduler.drain()
        stats = scheduler.stats()
        self.assertEqual((stats["free_slots"], stats["pending"], stats["ordered_chains"], stats["locks"]), (1, 0, 0, 0))


class DumpOnDemand(unittest.IsolatedAsyncioTestCase):
    async def test_the_dump_names_what_a_task_waits_for(self):
        scheduler = WorkScheduler(2, 4)
        gate = asyncio.Event()

        async def stuck():
            await gate.wait()

        scheduler.submit("stuck-key", stuck)
        await asyncio.sleep(0.01)
        with tempfile.TemporaryDirectory() as directory:
            path = diagnostic.dump_tasks(os.path.join(directory, "tasks.txt"), extra=[lambda: "windows: all idle"])
            text = open(path, encoding="utf-8").read()
        self.assertIn("slots free 1 of 2", text)
        self.assertIn("windows: all idle", text)
        self.assertIn("stuck-key", text)
        self.assertIn("in stuck", text)
        self.assertIn("parked on", text)
        gate.set()
        await scheduler.drain()


if __name__ == "__main__":
    unittest.main()
