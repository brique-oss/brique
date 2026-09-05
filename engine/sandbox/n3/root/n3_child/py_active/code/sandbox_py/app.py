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


def _coerce_bool(value: object) -> bool:
    if isinstance(value, bool):
        return value
    if isinstance(value, (int, float)):
        return value != 0
    if isinstance(value, bytes):
        text = value.decode("utf-8", errors="ignore").strip().lower()
        return text not in {"", "0", "false", "off", "no"}
    if isinstance(value, str):
        text = value.strip().lower()
        return text not in {"", "0", "false", "off", "no"}
    return bool(value)


class ActiveComponent:
    def __init__(self) -> None:
        self.enabled = False
        self.tick = 0
        self.last_event = "idle"
        self._shutdown = asyncio.Event()
        self._task: asyncio.Task | None = None

    async def start(self) -> None:
        if self._task is None:
            self._task = asyncio.create_task(self._run())

    async def stop(self) -> None:
        self._shutdown.set()
        if self._task is not None:
            await self._task
            self._task = None

    async def _run(self) -> None:
        while not self._shutdown.is_set():
            if self.enabled:
                self.tick += 1
                self.last_event = f"tick:{self.tick}"
            await asyncio.sleep(0.05)

    # <brique:capacity name="active.enable">
    def enable(self, params: dict) -> dict:
        self.enabled = True
        self.last_event = "enabled"
        return {"ok": True, "enabled": self.enabled, "tick": self.tick}
    # </brique:capacity>

    # <brique:capacity name="active.disable">
    def disable(self, params: dict) -> dict:
        self.enabled = False
        self.last_event = "disabled"
        return {"ok": True, "enabled": self.enabled, "tick": self.tick}
    # </brique:capacity>

    # <brique:capacity name="active.snapshot">
    def snapshot(self, params: dict) -> dict:
        return self.read_state()
    # </brique:capacity>

    # <brique:matter name="active_enabled">
    def read_enabled(self) -> bool:
        return self.enabled

    def write_enabled(self, value: object) -> dict:
        self.enabled = _coerce_bool(value)
        self.last_event = "enabled" if self.enabled else "disabled"
        return {"ok": True, "enabled": self.enabled}
    # </brique:matter>

    # <brique:matter name="active_state">
    def read_state(self) -> dict:
        return {
            "enabled": self.enabled,
            "tick": self.tick,
            "last_event": self.last_event,
        }
    # </brique:matter>
