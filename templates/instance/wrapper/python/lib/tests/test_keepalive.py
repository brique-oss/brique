# Copyright 2026 Nicolas Cassan
# Licensed under the Apache License, Version 2.0.

import asyncio
import os
import unittest
from unittest import mock

from wrapper.keepalive import keepalive


class Runtime:
    def __init__(self):
        self.running = 0

    async def notify_running(self):
        self.running += 1


class Keepalive(unittest.IsolatedAsyncioTestCase):
    async def test_a_running_at_once_then_one_per_period_until_the_block_ends(self):
        runtime = Runtime()
        async with keepalive(runtime, every_s=0.02):
            self.assertEqual(runtime.running, 1)
            await asyncio.sleep(0.07)
        seen = runtime.running
        self.assertGreaterEqual(seen, 3)
        await asyncio.sleep(0.06)
        self.assertEqual(runtime.running, seen)      # the heartbeat stopped with the block

    async def test_the_period_comes_from_the_setting(self):
        runtime = Runtime()
        with mock.patch.dict(os.environ, {"BRIQUE_HEARTBEAT_MS": "50"}):
            async with keepalive(runtime):
                await asyncio.sleep(0.18)
        self.assertGreaterEqual(runtime.running, 3)

    async def test_the_heartbeat_stops_when_the_block_fails(self):
        runtime = Runtime()
        with self.assertRaises(ValueError):
            async with keepalive(runtime, every_s=0.02):
                raise ValueError("boom")
        seen = runtime.running
        await asyncio.sleep(0.06)
        self.assertEqual(runtime.running, seen)


if __name__ == "__main__":
    unittest.main()


class TheRunningResponse(unittest.TestCase):
    """The message a capacity sends to say it is still working is the shared one (vectors/running_response.json)."""

    def test_the_vectors(self):
        import json
        from pathlib import Path

        from wrapper.execution import current_delegation
        from wrapper.router import MessageRouter
        from wrapper.runtime import WrapperRuntime

        cases = json.loads((Path(__file__).resolve().parents[2].parent / "vectors" / "running_response.json").read_text(encoding="utf-8"))["cases"]
        for case in cases:
            class Stub:
                wrapper_name = case["wrapper_name"]

                def wrapper_identity(self):
                    return {"id": self.wrapper_name, "kind": "wrapper"}

                def response_from_context(self, intention):
                    return WrapperRuntime.response_from_context(self, intention)

            token = current_delegation.set("handle" if case["delegated"] else "")
            try:
                message = MessageRouter(Stub()).running_response(case["intention"])
            finally:
                current_delegation.reset(token)
            self.assertEqual(message["kind"], "response", case["name"])
            drop_empty = lambda body: {k: v for k, v in body.items() if not (k in ("identity", "payload") and v == {})}      # noqa: E731  see the vector
            self.assertEqual(drop_empty(message["response"]), drop_empty(case["response"]), case["name"])
