# Copyright 2026 Nicolas Cassan
# Licensed under the Apache License, Version 2.0.

import asyncio
import json
import os
import sys
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))

from wrapper import busy  # noqa: E402
from wrapper.outbound import OutboundIntentionAPI  # noqa: E402

VECTORS = json.loads((ROOT.parents[1] / "vectors" / "busy_retry.json").read_text(encoding="utf-8"))
BUSY = {"status": "error", "error": {"code": "refused", "details": {"reason": "busy"}}}


class BusyVectors(unittest.TestCase):
    def test_is_busy(self) -> None:
        for case in VECTORS["is_busy"]:
            self.assertEqual(busy.is_busy(case["response"]), case["busy"], case["name"])

    def test_pause_bounds(self) -> None:
        for case in VECTORS["pause"]:
            low = busy.busy_pause_s(case["attempt"], rnd=lambda: 0.0) * 1000
            high = busy.busy_pause_s(case["attempt"], rnd=lambda: 1.0) * 1000
            self.assertAlmostEqual(low, case["min_ms"], msg=str(case))
            self.assertAlmostEqual(high, case["max_ms"], msg=str(case))

    def test_settings(self) -> None:
        for case in VECTORS["settings"]:
            with mock.patch.dict(os.environ, case["env"], clear=False):
                if not case["env"]:
                    os.environ.pop("BRIQUE_BUSY_ATTEMPTS", None)
                self.assertEqual(busy.busy_attempts(), case["attempts"], str(case))


class _Runtime:
    def __init__(self) -> None:
        self.scheduler = None


class BusyRetry(unittest.IsolatedAsyncioTestCase):
    def _api(self, answers: list) -> tuple[OutboundIntentionAPI, list]:
        api = OutboundIntentionAPI(_Runtime())
        sent: list = []

        async def once(**kwargs):
            sent.append(kwargs)
            answer = answers.pop(0)
            if isinstance(answer, BaseException):
                raise answer
            return {"response": answer}

        api._emit_and_wait_once = once  # type: ignore[method-assign]
        return api, sent

    async def test_retries_until_it_runs(self) -> None:
        api, sent = self._api([BUSY, BUSY, {"status": "ok", "payload": {"x": 1}}])
        with mock.patch.dict(os.environ, {"BRIQUE_BUSY_BASE_MS": "0"}):
            message = await api.emit_and_wait(to_context="c", to_cap="k", to_type="user")
        self.assertEqual(message["response"]["status"], "ok")
        self.assertEqual(len(sent), 3)

    async def test_gives_the_last_refusal_after_the_attempts(self) -> None:
        api, sent = self._api([BUSY] * 3)
        with mock.patch.dict(os.environ, {"BRIQUE_BUSY_BASE_MS": "0", "BRIQUE_BUSY_ATTEMPTS": "3"}):
            message = await api.emit_and_wait(to_context="c", to_cap="k", to_type="user")
        self.assertTrue(busy.is_busy(message["response"]))
        self.assertEqual(len(sent), 3)

    async def test_other_errors_and_timeouts_are_not_retried(self) -> None:
        other = {"status": "error", "error": {"details": {"reason": "invalid"}}}
        api, sent = self._api([other])
        await api.emit_and_wait(to_context="c", to_cap="k", to_type="user")
        self.assertEqual(len(sent), 1)
        api, sent = self._api([asyncio.TimeoutError()])
        with self.assertRaises(asyncio.TimeoutError):
            await api.emit_and_wait(to_context="c", to_cap="k", to_type="user")
        self.assertEqual(len(sent), 1)


if __name__ == "__main__":
    unittest.main()
