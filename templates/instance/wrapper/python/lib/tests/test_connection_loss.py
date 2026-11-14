# Copyright 2026 Nicolas Cassan
# Licensed under the Apache License, Version 2.0.
"""When the connection to the engine is gone, nothing can answer the calls that wait for a Response: the wrapper frees them at once.

Before, `run` waited for all the work in flight to finish BEFORE it cancelled the waits for a Response, and that work was stuck on those very waits: the
process stayed alive, silent and unreachable, until each wait ran out (60 s for a call to the engine, 600 s for an awaited intention), and the engine, which
only starts a wrapper when none is running, found it "running" for minutes. See agentic_core_concurrency_model.md in the agentic context.
"""
import asyncio
import time
import unittest
from types import SimpleNamespace

from wrapper.correlator import AwaitCorrelator
from wrapper.runtime import WrapperRuntime
from wrapper.scheduler import WorkScheduler


class Transport:
    """A connection that the engine closes when `lose` is set."""

    def __init__(self):
        self.lose = asyncio.Event()
        self.sent = []

    async def send(self, message):
        self.sent.append(message)

    async def receive_loop(self, handler):
        await self.lose.wait()

    async def close(self):
        pass


class Runtime(WrapperRuntime):
    def __init__(self, transport):
        self.transport = transport
        self.stop_requested = asyncio.Event()
        self.scheduler = WorkScheduler(4, 16)
        self.correlator = AwaitCorrelator()
        self.inflight = set()
        self.registry = SimpleNamespace(shutdown_hooks=[])

    async def load(self):
        pass

    def wrapper_ready_message(self):
        return {"kind": "intention", "intention": {"to": {"cap": "wrapper_ready"}}}

    def log_exception(self, *args, **kwargs):
        pass


class ConnectionLoss(unittest.IsolatedAsyncioTestCase):
    async def test_work_waiting_for_a_response_is_freed_when_the_connection_is_lost(self):
        transport = Transport()
        runtime = Runtime(transport)
        outcome = []

        async def waiting_for_an_answer():
            pending = await runtime.correlator.register("call-1")
            try:
                await pending.wait(timeout_s=300)           # a long wait: the answer cannot come any more
                outcome.append("answered")
            except asyncio.CancelledError:
                outcome.append("freed")
                raise
        runtime.scheduler.submit("waiter", waiting_for_an_answer)
        task = asyncio.ensure_future(runtime.run())
        await asyncio.sleep(0.05)
        started = time.monotonic()
        transport.lose.set()
        await asyncio.wait_for(task, 3)                      # before: it waited for the 300 s of the call
        self.assertLess(time.monotonic() - started, 1.5)
        self.assertEqual(outcome, ["freed"])

    async def test_a_wait_begun_after_the_connection_is_lost_fails_at_once(self):
        correlator = AwaitCorrelator()
        await correlator.cancel_all("connection closed")
        with self.assertRaises(asyncio.CancelledError):
            await correlator.register("late")

    async def test_work_that_does_not_wait_for_an_answer_is_still_finished(self):
        transport = Transport()
        runtime = Runtime(transport)
        done = []

        async def finishing():
            await asyncio.sleep(0.2)
            done.append("done")
        runtime.scheduler.submit("finisher", finishing)
        task = asyncio.ensure_future(runtime.run())
        await asyncio.sleep(0.05)
        transport.lose.set()
        await asyncio.wait_for(task, 3)
        self.assertEqual(done, ["done"])                     # freeing the waits does not cut the work that can end by itself
