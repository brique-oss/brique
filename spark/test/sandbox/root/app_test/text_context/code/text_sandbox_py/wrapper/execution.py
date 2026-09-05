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
import inspect
from typing import Any

from .constants import MODE_WRAPPER
from .util import normalize_inline_payload


class CapacityExecutor:
    def __init__(self, runtime: "WrapperRuntime") -> None:
        self.runtime = runtime

    async def execute_capacity(self, context_id: str, name: str, intention: dict[str, Any]) -> dict[str, Any]:
        binding = self.runtime.registry.resolve_capacity(context_id, name)
        if binding is None:
            raise LookupError(f"unknown wrapper capacity {name}")
        params = intention.get("params", {}) if isinstance(intention.get("params"), dict) else {}
        result = await self._invoke(binding.symbol, params)
        if result is None:
            return {"ok": True}
        if isinstance(result, dict):
            return result
        return {"result": result}

    async def _invoke(self, symbol, *args):
        call_symbol = getattr(symbol, "__call__", None)
        if inspect.iscoroutinefunction(symbol) or inspect.iscoroutinefunction(call_symbol):
            return await symbol(*args)
        return await asyncio.to_thread(symbol, *args)


class MatterManager:
    def __init__(self, runtime: "WrapperRuntime") -> None:
        self.runtime = runtime

    async def read_matter(self, context_id: str, name: str) -> dict[str, Any]:
        binding = self.runtime.registry.resolve_matter(context_id, name)
        if binding is None or binding.read_symbol is None:
            raise LookupError(f"unknown wrapper matter {name}")
        async with binding.lock:
            value = await self._invoke(binding.read_symbol)
        return normalize_inline_payload(value)

    async def write_matter(self, context_id: str, name: str, value: Any) -> dict[str, Any]:
        binding = self.runtime.registry.resolve_matter(context_id, name)
        if binding is None or binding.write_symbol is None:
            raise LookupError(f"unknown wrapper matter {name}")
        async with binding.lock:
            result = await self._invoke(binding.write_symbol, value)
        if result is None:
            return {
                "ok": True,
                "matter_id": name,
                "substance_mode": MODE_WRAPPER,
            }
        if isinstance(result, dict):
            return result
        return {
            "ok": True,
            "matter_id": name,
            "substance_mode": MODE_WRAPPER,
            "value": result,
        }

    async def _invoke(self, symbol, *args):
        call_symbol = getattr(symbol, "__call__", None)
        if inspect.iscoroutinefunction(symbol) or inspect.iscoroutinefunction(call_symbol):
            return await symbol(*args)
        return await asyncio.to_thread(symbol, *args)
