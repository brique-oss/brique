# Copyright 2026 Nicolas Cassan
# Licensed under the Apache License, Version 2.0.

import asyncio
import unittest

from wrapper.correlator import AwaitCorrelator


class AwaitCorrelatorTests(unittest.IsolatedAsyncioTestCase):
    async def test_running_keeps_correlation_and_resets_timeout(self) -> None:
        correlator = AwaitCorrelator()
        pending = await correlator.register("i-running")

        async def feed() -> None:
            await asyncio.sleep(0.04)
            self.assertTrue(
                await correlator.resolve(
                    {"response": {"intention_id": "i-running", "status": "running"}}
                )
            )
            await asyncio.sleep(0.04)
            self.assertTrue(
                await correlator.resolve(
                    {
                        "response": {
                            "intention_id": "i-running",
                            "status": "ok",
                            "payload": {"done": True},
                        }
                    }
                )
            )

        task = asyncio.create_task(feed())
        response = await pending.wait(0.06)
        await task

        self.assertEqual(response["response"]["status"], "ok")
        self.assertTrue(response["response"]["payload"]["done"])
        self.assertNotIn("i-running", correlator._pending)


if __name__ == "__main__":
    unittest.main()

