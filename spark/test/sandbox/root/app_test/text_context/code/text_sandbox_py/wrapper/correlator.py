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


class AwaitCorrelator:
    def __init__(self) -> None:
        self._pending: dict[str, asyncio.Future[dict[str, Any]]] = {}
        self._lock = asyncio.Lock()

    async def register(self, intention_id: str) -> asyncio.Future[dict[str, Any]]:
        future: asyncio.Future[dict[str, Any]] = asyncio.get_running_loop().create_future()
        async with self._lock:
            self._pending[intention_id] = future
        return future

    async def resolve(self, response: dict[str, Any]) -> bool:
        body = response.get("response", {}) if isinstance(response.get("response"), dict) else {}
        intention_id = str(body.get("intention_id", "")).strip()
        if not intention_id:
            return False
        async with self._lock:
            future = self._pending.pop(intention_id, None)
        if future is None or future.done():
            return False
        future.set_result(response)
        return True

    async def cancel_all(self, reason: str) -> None:
        async with self._lock:
            pending = list(self._pending.values())
            self._pending.clear()
        for future in pending:
            if not future.done():
                future.cancel(reason)
