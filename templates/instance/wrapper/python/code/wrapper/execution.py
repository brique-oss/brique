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
import contextvars
import inspect
from typing import Any

from .constants import MODE_WRAPPER
from .util import normalize_inline_payload

# Holds the intention currently being executed by CapacityExecutor, scoped
# per asyncio Task (each Task created by _schedule_raw_message gets its own
# copy on creation, so concurrent capacity executions never see each other's
# intention) — read by WrapperRuntime.notify_running() so hosted code can
# call it with no argument, staying agnostic of the message envelope per
# Wrapper.md §6/§9.
current_intention: contextvars.ContextVar[dict[str, Any] | None] = contextvars.ContextVar(
    "current_intention", default=None
)


class WrapperBindingNotFoundError(Exception):
    """Raised only when a capacity/matter binding is absent from the registry.

    Deliberately not a LookupError subclass: hosted binding code is free to
    raise KeyError/IndexError (both LookupError subclasses) for its own
    reasons, and callers must be able to tell "binding does not exist" apart
    from "binding code failed" without those cases colliding.
    """


class CapacityExecutor:
    def __init__(self, runtime: "WrapperRuntime") -> None:
        self.runtime = runtime

    async def execute_capacity(self, context_id: str, name: str, intention: dict[str, Any]) -> dict[str, Any]:
        binding = self.runtime.registry.resolve_capacity(context_id, name)
        if binding is None:
            raise WrapperBindingNotFoundError(f"unknown wrapper capacity {name}")
        params = intention.get("params", {}) if isinstance(intention.get("params"), dict) else {}
        params = dict(params)
        matters = self._normalize_matters(intention.get("matters"))
        if matters:
            params["@matter"] = matters
        token = current_intention.set(intention)
        try:
            result = await self._invoke(binding.symbol, params)
        finally:
            current_intention.reset(token)
        if result is None:
            return {"ok": True}
        if isinstance(result, dict):
            return result
        return {"result": result}

    def _normalize_matters(self, raw: Any) -> dict[str, Any]:
        if not isinstance(raw, list):
            return {}
        out: dict[str, Any] = {}
        for item in raw:
            if not isinstance(item, dict):
                continue
            context = str(item.get("context", "")).strip()
            matter_id = str(item.get("id", "")).strip()
            repr_name = str(item.get("repr", "")).strip() or matter_id
            mode = str(item.get("mode", "")).strip()
            if not context or not matter_id or not repr_name:
                continue
            entry = {
                "ref": f"{context.rstrip('/')}/{matter_id}",
                "context": context,
                "id": matter_id,
            }
            if mode:
                entry["mode"] = mode
            out[repr_name] = entry
        return out

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
            raise WrapperBindingNotFoundError(f"unknown wrapper matter {name}")
        async with binding.lock:
            value = await self._invoke(binding.read_symbol)
        return normalize_inline_payload(value)

    async def write_matter(self, context_id: str, name: str, value: Any) -> dict[str, Any]:
        binding = self.runtime.registry.resolve_matter(context_id, name)
        if binding is None or binding.write_symbol is None:
            raise WrapperBindingNotFoundError(f"unknown wrapper matter {name}")
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
