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

try:
    import websockets
except Exception as exc:  # pragma: no cover - import failure is runtime-only
    websockets = None
    IMPORT_ERROR = exc
else:
    IMPORT_ERROR = None


class TransportClient:
    def __init__(self, url: str) -> None:
        self.url = url
        self._ws = None
        self._send_lock = asyncio.Lock()

    async def connect(self) -> None:
        if IMPORT_ERROR is None:
            self._ws = await websockets.connect(self.url)
            return
        fallback = StdlibWebSocketClient(self.url)
        await fallback.connect()
        self._ws = fallback

    async def send(self, message: dict[str, Any]) -> None:
        if self._ws is None:
            raise RuntimeError("transport is not connected")
        async with self._send_lock:
            payload = json.dumps(message, separators=(",", ":"), ensure_ascii=True)
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
