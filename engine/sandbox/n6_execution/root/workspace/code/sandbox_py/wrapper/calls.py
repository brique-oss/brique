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

"""Calling a capacity from hosted code (Wrapper.md §17.3, §17.4).

``call`` does three things so that hosted code does not have to:

* it chooses the form of each parameter (``emit``, chart B) for the callee;
* it chooses the path (chart C): through the engine, or a direct function call;
* it releases what it posted when the callee was not found or refused the call.

Semantic Matters are never read here: they travel as explicit references and
the capacity handles them. A transient Matter created by the transit layer is
instead an internal, single-consumer carrier and is cleaned up here on a
definitive delivery failure.
"""

from __future__ import annotations

import asyncio
from typing import Any

from .constants import STATUS_ERROR, TYPE_USER
from .execution import current_intention, current_scope
from .transit import MatterRef, Params, TransitError, TransitTarget, reference_kind


class CapacityCallError(Exception):
    """A capacity answered an engine-routed call with an error response."""

    def __init__(self, code: str, message: str, details: dict[str, Any] | None = None) -> None:
        super().__init__(message)
        self.code = code
        self.details = details or {}


class CapacityCaller:
    def __init__(self, runtime: Any) -> None:
        self.runtime = runtime

    def _relative_target(self, to_context: str) -> str | None:
        """Express a target context relative to the wrapper root (None when outside it)."""
        context = str(to_context).strip()
        if context.startswith("/"):
            return self.runtime.relative_context(context)
        return context.strip("/")

    def is_local(self, to_context: str, to_cap: str, to_type: str = TYPE_USER) -> bool:
        """True when the target is a capacity bound in this wrapper."""
        if to_type != TYPE_USER:
            return False
        relative = self._relative_target(to_context)
        return relative is not None and self.runtime.registry.resolve_capacity(relative, to_cap) is not None

    async def call(
        self,
        *,
        to: dict[str, Any] | None = None,
        to_context: str = "",
        to_cap: str = "",
        params: dict[str, Any] | None = None,
        to_type: str = TYPE_USER,
        await_response: bool = True,
        direct: bool = False,
        timeout_s: float | None = None,
        type_tags: dict[str, str] | None = None,
    ) -> Params | None:
        """Call an address with ordinary values as parameters.

        ``to`` is the canonical address form. The legacy split fields remain accepted.
        The runtime derives the transit representation from that address.
        """
        if to is not None:
            if not isinstance(to, dict):
                raise TransitError("invalid_reference", "to must be an address object")
            to_context = str(to.get("context", ""))
            to_cap = str(to.get("cap", ""))
            to_type = str(to.get("type", TYPE_USER)) or TYPE_USER
        if not to_cap.strip():
            raise TransitError("invalid_reference", "destination address has no capacity")
        local = self.is_local(to_context, to_cap, to_type)
        if direct:
            if not local:
                raise TransitError("unsupported", "a direct call requires a capacity bound in this wrapper")
            relative = self._relative_target(to_context)
            result = await self.runtime.capacity_executor.execute_direct(
                relative or "", to_cap, dict(params or {}), timeout_s
            )
            return self._hold(Params(result, self.runtime))

        intention = current_intention.get()
        if intention is None and await_response:
            raise TransitError("unsupported", "a call that awaits a response needs a running capacity")
        target = TransitTarget(to_context, to_cap, to_type, same_wrapper=local)
        emitted, posted, matters = await self._emit_params(params or {}, target, type_tags or {})
        from_context = self.runtime.target_context(intention) if intention else ""
        from_cap = str(intention.get("to", {}).get("cap", "")).strip() if intention else ""
        correlation = self._child_correlation(intention)
        try:
            if not await_response:
                await self.runtime.outbound.emit_intention(
                    to_context=to_context,
                    to_cap=to_cap,
                    to_type=to_type,
                    from_context=from_context,
                    from_cap=from_cap,
                    params=emitted,
                    matters=matters,
                    await_response=False,
                    correlation=correlation,
                )
                return None
            message = await self.runtime.outbound.emit_and_wait(
                to_context=to_context,
                to_cap=to_cap,
                to_type=to_type,
                from_context=from_context,
                from_cap=from_cap,
                params=emitted,
                matters=matters,
                timeout_s=timeout_s,
                correlation=correlation,
            )
        except asyncio.TimeoutError:
            # The callee may still be running and about to take what was posted: do not release.
            raise
        except BaseException:
            await self._release(posted)
            raise
        body = message.get("response", {}) if isinstance(message.get("response"), dict) else {}
        if str(body.get("status", "")) == STATUS_ERROR:
            # Not found, refused, failed: whatever it consumed is gone, the rest is released here.
            await self._release(posted)
            error = body.get("error") if isinstance(body.get("error"), dict) else {}
            raise CapacityCallError(
                str(error.get("code", "internal")),
                str(error.get("message", "capacity call failed")),
                error.get("details") if isinstance(error.get("details"), dict) else {},
            )
        payload = body.get("payload") if isinstance(body.get("payload"), dict) else {}
        return self._hold(Params(payload, self.runtime))

    @staticmethod
    def _child_correlation(intention: dict[str, Any] | None) -> dict[str, str]:
        if not intention:
            return {"root_intention_id": "", "parent_intention_id": ""}
        current_id = str(intention.get("intention_id", "")).strip()
        inherited = intention.get("correlation", {})
        inherited = inherited if isinstance(inherited, dict) else {}
        root = str(inherited.get("root_intention_id", "")).strip() or current_id
        return {"root_intention_id": root, "parent_intention_id": current_id}

    async def _emit_params(
        self, params: dict[str, Any], target: TransitTarget, type_tags: dict[str, str]
    ) -> tuple[dict[str, Any], list[Any], list[dict[str, Any]]]:
        emitted: dict[str, Any] = {}
        posted: list[Any] = []
        matters: list[dict[str, Any]] = []
        try:
            for name, value in params.items():
                if isinstance(value, MatterRef):
                    context = value.context.strip()
                    matter_id = value.id.strip()
                    if not context or not matter_id:
                        raise TransitError("invalid_reference", f"matter parameter {name!r} is incomplete")
                    matter = {"context": context, "id": matter_id, "repr": name}
                    if value.mode:
                        matter["mode"] = value.mode
                    matters.append(matter)
                    continue
                out = await self.runtime.transit.emit_async(
                    value,
                    target=target,
                    type_tag=type_tags.get(name, ""),
                )
                if out is not value and reference_kind(out) is not None:
                    posted.append(out)
                emitted[name] = out
        except BaseException:
            await self._release(posted)
            raise
        return emitted, posted, matters

    async def _release(self, posted: list[Any]) -> None:
        for ref in posted:
            if reference_kind(ref) == "matter":
                await self.runtime.transit_matters.discard(ref)
            else:
                self.runtime.transit.release(ref)

    def _hold(self, result: Params) -> Params:
        """Keep the result of a call so its unconsumed references are released when the
        capacity that made the call ends."""
        scope = current_scope.get()
        if scope is not None:
            scope.append(result)
        return result
