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
from typing import Any

from .constants import MODE_WRAPPER
from .transit import Params, TransitTarget
from .util import invoke_callable, normalize_inline_payload

# Holds the intention currently being executed by CapacityExecutor, scoped
# per asyncio Task (each Task created by _schedule_raw_message gets its own
# copy on creation, so concurrent capacity executions never see each other's
# intention) — read by WrapperRuntime.notify_running() so hosted code can
# call it with no argument, staying agnostic of the message envelope per
# Wrapper.md §6/§9.
current_intention: contextvars.ContextVar[dict[str, Any] | None] = contextvars.ContextVar(
    "current_intention", default=None
)

# Set while a direct (in-process) call waits for its callee. `notify_running` touches it, which
# renews the call's inactivity budget exactly as a `running` response renews an engine-routed one.
current_activity: contextvars.ContextVar[asyncio.Event | None] = contextvars.ContextVar(
    "current_activity", default=None
)

# Results of the calls made during the current capacity execution (Wrapper.md §17.3): the executor
# releases the transit references that nobody consumed when the execution ends.
current_scope: contextvars.ContextVar[list[Any] | None] = contextvars.ContextVar(
    "current_scope", default=None
)

current_binding_stack: contextvars.ContextVar[tuple[tuple[str, str], ...]] = contextvars.ContextVar(
    "current_binding_stack", default=()
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
        self._binding_locks: dict[tuple[str, str], asyncio.Lock] = {}

    async def execute_capacity(self, context_id: str, name: str, intention: dict[str, Any]) -> dict[str, Any]:
        binding = self.runtime.registry.resolve_capacity(context_id, name)
        if binding is None:
            raise WrapperBindingNotFoundError(f"unknown wrapper capacity {name}")
        params = intention.get("params", {}) if isinstance(intention.get("params"), dict) else {}
        params = dict(params)
        matters = self._normalize_matters(intention.get("matters"))
        if matters:
            params["@matter"] = matters
        # Params is a dict: code written against the dictionary is unaffected.
        # Hosted code opts in to resolution with `await params.value(name)` (Wrapper.md §17.3).
        params = Params(params, self.runtime)
        scope: list[Params] = []
        token = current_intention.set(intention)
        scope_token = current_scope.set(scope)
        try:
            result = await self._invoke_binding((context_id, name), binding.symbol, params)
            result = self._normalize_result(result)
            if self.runtime.transit.emit_returns:
                result = await self._emit_result(result, intention)
            return result
        finally:
            await params.release_unconsumed_async()
            for held in scope:
                await held.release_unconsumed_async()
            current_scope.reset(scope_token)
            current_intention.reset(token)

    async def execute_direct(
        self, context_id: str, name: str, params: dict[str, Any], timeout_s: float | None = None
    ) -> dict[str, Any]:
        """Call a capacity bound in this wrapper as a plain function call (Wrapper.md §17.4).

        Parameters are passed as they are, objects included. The caller's intention stays the
        current one, so `notify_running` keeps acknowledging the caller's own call. `timeout_s`
        is an inactivity budget, renewed by `notify_running`, like the engine-routed path.
        """
        binding = self.runtime.registry.resolve_capacity(context_id, name)
        if binding is None:
            raise WrapperBindingNotFoundError(f"unknown wrapper capacity {name}")
        call_params = Params(dict(params), self.runtime)
        scope: list[Params] = []
        activity = asyncio.Event()
        scope_token = current_scope.set(scope)
        activity_token = current_activity.set(activity)
        try:
            # the task copies the context now: it sees this scope and this activity event
            task = asyncio.ensure_future(self._invoke_binding((context_id, name), binding.symbol, call_params))
        finally:
            current_activity.reset(activity_token)
            current_scope.reset(scope_token)
        try:
            try:
                result = await self._wait(task, activity, timeout_s)
            except BaseException:
                if not task.done():
                    task.cancel()
                # Cancellation is cooperative. Wait for the callee's cleanup to
                # finish before releasing the parameters it may still inspect.
                await asyncio.gather(task, return_exceptions=True)
                raise
        finally:
            await call_params.release_unconsumed_async()
            for held in scope:
                await held.release_unconsumed_async()
        return self._normalize_result(result)

    @staticmethod
    async def _wait(task: "asyncio.Future[Any]", activity: asyncio.Event, timeout_s: float | None) -> Any:
        if timeout_s is None:
            return await task
        while True:
            done, _ = await asyncio.wait({task}, timeout=timeout_s)
            if done:
                return task.result()
            if activity.is_set():
                activity.clear()
                continue
            task.cancel()
            raise asyncio.TimeoutError()

    @staticmethod
    def _normalize_result(result: Any) -> dict[str, Any]:
        if result is None:
            return {"ok": True}
        if isinstance(result, dict):
            return result
        return {"result": result}

    async def _emit_result(self, result: dict[str, Any], intention: dict[str, Any]) -> dict[str, Any]:
        """Choose the form of each top-level return field for the caller (chart B)."""
        origin = intention.get("from", {}) if isinstance(intention.get("from"), dict) else {}
        target = TransitTarget(
            context=str(origin.get("context", "")),
            cap=str(origin.get("cap", "")),
            type=str(origin.get("type", "")),
            same_wrapper=self.runtime.is_local_caller(intention),
        )
        emitted: dict[str, Any] = {}
        for key, value in result.items():
            emitted[key] = await self.runtime.transit.emit_async(value, target=target)
        return emitted

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
        return await invoke_callable(symbol, *args)

    async def _invoke_binding(self, key: tuple[str, str], symbol: Any, *args: Any) -> Any:
        stack = current_binding_stack.get()
        if key in stack:
            return await self._invoke(symbol, *args)
        lock = self._binding_locks.setdefault(key, asyncio.Lock())
        async with lock:
            token = current_binding_stack.set((*stack, key))
            try:
                return await self._invoke(symbol, *args)
            finally:
                current_binding_stack.reset(token)


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
        notified = 0
        notification_failed = False
        manager_notifies = binding.notify_on_write and not getattr(
            binding.write_symbol, "_brique_notifies_on_write", False
        )
        if manager_notifies:
            try:
                if binding.event_value is not None:
                    notify_value = await invoke_callable(
                        binding.event_value, args=(value,), kwargs={}, result=result
                    )
                elif isinstance(result, dict) and "value" in result:
                    notify_value = result.get("value")
                else:
                    notify_value = value if result is None else result
                notified = await self.runtime.notify_matter_written(context_id, name, value=notify_value)
            except Exception as exc:
                notification_failed = True
                logger = getattr(self.runtime, "log_exception", None)
                if callable(logger):
                    logger("matter_notification_failed", exc, context=context_id, matter=name)
        if result is None:
            payload = {
                "ok": True,
                "matter_id": name,
                "substance_mode": MODE_WRAPPER,
            }
        elif isinstance(result, dict):
            payload = dict(result)
            payload.setdefault("matter_id", name)
            payload.setdefault("substance_mode", MODE_WRAPPER)
        else:
            payload = {
                "ok": True,
                "matter_id": name,
                "substance_mode": MODE_WRAPPER,
                "value": result,
            }
        if manager_notifies:
            payload["notified"] = notified
            if notification_failed:
                payload["notification_failed"] = True
        return payload

    async def _invoke(self, symbol, *args):
        return await invoke_callable(symbol, *args)
