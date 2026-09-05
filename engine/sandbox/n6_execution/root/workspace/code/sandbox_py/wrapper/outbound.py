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

from .constants import KIND_INTENTION, TYPE_EXECUTION
from .util import new_id, utc_now_rfc3339


class OutboundIntentionAPI:
    def __init__(self, runtime: "WrapperRuntime") -> None:
        self.runtime = runtime

    async def emit_intention(
        self,
        *,
        to_context: str,
        to_cap: str,
        to_type: str,
        from_context: str = "",
        from_cap: str = "",
        from_type: str = TYPE_EXECUTION,
        params: dict[str, Any] | None = None,
        await_response: bool = False,
        correlation: dict[str, Any] | None = None,
    ) -> str:
        intention_id = new_id()
        message = {
            "kind": KIND_INTENTION,
            "ts": utc_now_rfc3339(),
            "intention": {
                "intention_id": intention_id,
                "await_response": await_response,
                "to": {
                    "context": to_context,
                    "cap": to_cap,
                    "type": to_type,
                },
                "from": {
                    "context": from_context,
                    "cap": from_cap,
                    "type": from_type,
                },
                "identity": self.runtime.wrapper_identity(),
                "params": params or {},
                "correlation": correlation or {
                    "root_intention_id": "",
                    "parent_intention_id": "",
                },
            },
        }
        await self.runtime.transport.send(message)
        return intention_id

    async def emit_and_wait(
        self,
        *,
        to_context: str,
        to_cap: str,
        to_type: str,
        from_context: str = "",
        from_cap: str = "",
        from_type: str = TYPE_EXECUTION,
        params: dict[str, Any] | None = None,
        timeout_s: float | None = None,
        correlation: dict[str, Any] | None = None,
    ) -> dict[str, Any]:
        intention_id = new_id()
        future = await self.runtime.correlator.register(intention_id)
        message = {
            "kind": KIND_INTENTION,
            "ts": utc_now_rfc3339(),
            "intention": {
                "intention_id": intention_id,
                "await_response": True,
                "to": {
                    "context": to_context,
                    "cap": to_cap,
                    "type": to_type,
                },
                "from": {
                    "context": from_context,
                    "cap": from_cap,
                    "type": from_type,
                },
                "identity": self.runtime.wrapper_identity(),
                "params": params or {},
                "correlation": correlation or {
                    "root_intention_id": "",
                    "parent_intention_id": "",
                },
            },
        }
        await self.runtime.transport.send(message)
        if timeout_s is None:
            return await future
        return await asyncio.wait_for(future, timeout=timeout_s)
