# Copyright 2026 Nicolas Cassan
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

from __future__ import annotations

import asyncio
from typing import Any


class _Cancelled:
    def __init__(self, reason: str) -> None:
        self.reason = reason


class PendingResponse:
    def __init__(self) -> None:
        self._queue: asyncio.Queue[dict[str, Any] | _Cancelled] = asyncio.Queue()

    def deliver(self, response: dict[str, Any]) -> None:
        self._queue.put_nowait(response)

    def cancel(self, reason: str) -> None:
        self._queue.put_nowait(_Cancelled(reason))

    async def wait(self, timeout_s: float | None = None) -> dict[str, Any]:
        while True:
            if timeout_s is None:
                item = await self._queue.get()
            else:
                # A new wait is armed after every running response, making the
                # timeout an inactivity budget rather than a total duration.
                item = await asyncio.wait_for(self._queue.get(), timeout=timeout_s)
            if isinstance(item, _Cancelled):
                raise asyncio.CancelledError(item.reason)
            body = item.get("response", {}) if isinstance(item.get("response"), dict) else {}
            if body.get("status") == "running":
                continue
            return item


class AwaitCorrelator:
    def __init__(self) -> None:
        self._pending: dict[str, PendingResponse] = {}
        self._lock = asyncio.Lock()

    async def register(self, intention_id: str) -> PendingResponse:
        pending = PendingResponse()
        async with self._lock:
            self._pending[intention_id] = pending
        return pending

    async def resolve(self, response: dict[str, Any]) -> bool:
        body = response.get("response", {}) if isinstance(response.get("response"), dict) else {}
        intention_id = str(body.get("intention_id", "")).strip()
        if not intention_id:
            return False
        running = body.get("status") == "running"
        async with self._lock:
            pending = self._pending.get(intention_id)
            if pending is not None and not running:
                self._pending.pop(intention_id, None)
        if pending is None:
            return False
        pending.deliver(response)
        return True

    async def unregister(self, intention_id: str, pending: PendingResponse) -> None:
        async with self._lock:
            if self._pending.get(intention_id) is pending:
                self._pending.pop(intention_id, None)

    async def cancel_all(self, reason: str) -> None:
        async with self._lock:
            pending = list(self._pending.values())
            self._pending.clear()
        for waiter in pending:
            waiter.cancel(reason)
