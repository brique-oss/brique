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
import json
from typing import Any, Awaitable, Callable

from ._ws_stdlib import StdlibWebSocketClient
from .constants import CONTROL_MESSAGE_LIMIT_BYTES

try:
    import websockets
except Exception as exc:  # pragma: no cover - import failure is runtime-only
    websockets = None
    IMPORT_ERROR = exc
else:
    IMPORT_ERROR = None


class TransportClient:
    def __init__(self, url: str, connect_attempts: int = 5) -> None:
        self.url = url
        self.connect_attempts = connect_attempts
        self._ws = None
        self._send_lock = asyncio.Lock()

    async def connect(self) -> None:
        delay = 0.1
        for attempt in range(1, self.connect_attempts + 1):
            try:
                if IMPORT_ERROR is None:
                    self._ws = await websockets.connect(self.url, max_size=CONTROL_MESSAGE_LIMIT_BYTES)
                    return
                fallback = StdlibWebSocketClient(self.url)
                await fallback.connect()
                self._ws = fallback
                return
            except Exception as exc:
                if attempt >= self.connect_attempts or not self._is_connection_refused(exc):
                    raise
                await asyncio.sleep(delay)
                delay = min(delay * 2, 1.0)

    @staticmethod
    def _is_connection_refused(exc: Exception) -> bool:
        if isinstance(exc, OSError) and getattr(exc, "errno", None) in {61, 111}:
            return True
        text = str(exc).lower()
        return "connect call failed" in text or "connection refused" in text

    async def send(self, message: dict[str, Any]) -> None:
        if self._ws is None:
            raise RuntimeError("transport is not connected")
        async with self._send_lock:
            payload = await asyncio.to_thread(
                json.dumps, message, separators=(",", ":"), ensure_ascii=True, allow_nan=False
            )
            size = len(payload.encode("utf-8"))
            if size > CONTROL_MESSAGE_LIMIT_BYTES:
                raise ValueError(
                    f"control message is {size} bytes, limit is {CONTROL_MESSAGE_LIMIT_BYTES}; "
                    "store large payloads in Matter substance"
                )
            if IMPORT_ERROR is None:
                await self._ws.send(payload)
                return
            await self._ws.send_text(payload)

    async def receive_loop(self, handler: Callable[[str], Awaitable[None]]) -> None:
        if self._ws is None:
            raise RuntimeError("transport is not connected")
        try:
            while True:
                raw = await self._ws.recv() if IMPORT_ERROR is None else await self._ws.recv_text()
                if isinstance(raw, str):
                    await handler(raw)
        except Exception:
            return

    async def close(self) -> None:
        if self._ws is not None:
            await self._ws.close()
            self._ws = None
