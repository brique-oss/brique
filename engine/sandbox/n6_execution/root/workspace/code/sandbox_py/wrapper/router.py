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

import json
from typing import Any

from .constants import (
    CAP_MATTER_READ,
    CAP_MATTER_SUBSCRIBE,
    CAP_MATTER_UNSUBSCRIBE,
    CAP_MATTER_WRITE,
    CAP_WRAPPER_STOP,
    KIND_INTENTION,
    KIND_RESPONSE,
    STATUS_ERROR,
    STATUS_OK,
    TYPE_EXECUTION,
    TYPE_MATTER,
    TYPE_USER,
)
from .util import new_id, utc_now_rfc3339


class MessageRouter:
    def __init__(self, runtime: "WrapperRuntime") -> None:
        self.runtime = runtime

    async def route_incoming_raw(self, raw: str) -> None:
        try:
            message = json.loads(raw)
        except json.JSONDecodeError:
            return
        if not isinstance(message, dict):
            return
        kind = message.get("kind")
        if kind == KIND_RESPONSE:
            await self.route_response(message)
            return
        if kind != KIND_INTENTION:
            return
        intention = message.get("intention", {})
        if not isinstance(intention, dict):
            return
        await self.route_intention(intention)

    async def route_response(self, message: dict[str, Any]) -> None:
        await self.runtime.correlator.resolve(message)

    async def route_intention(self, intention: dict[str, Any]) -> None:
        to = intention.get("to", {})
        if not isinstance(to, dict):
            return
        msg_type = str(to.get("type", "")).strip()
        cap_name = str(to.get("cap", "")).strip()
        if msg_type == TYPE_EXECUTION:
            await self.route_control(intention, cap_name)
            return
        if msg_type == TYPE_USER:
            await self.route_capacity(intention, cap_name)
            return
        if msg_type == TYPE_MATTER:
            await self.route_matter(intention, cap_name)

    async def route_control(self, intention: dict[str, Any], cap_name: str) -> None:
        if cap_name == CAP_WRAPPER_STOP:
            await self.runtime.request_stop()

    async def route_capacity(self, intention: dict[str, Any], cap_name: str) -> None:
        target_context = self.runtime.target_context(intention)
        try:
            payload = await self.runtime.capacity_executor.execute_capacity(target_context, cap_name, intention)
            await self.runtime.transport.send(self.ok_response(intention, payload))
        except LookupError as exc:
            await self.runtime.transport.send(
                self.error_response(intention, "not_found", str(exc), {"reason": "unknown_cap"})
            )
        except Exception as exc:  # pragma: no cover - runtime failure path
            await self.runtime.transport.send(
                self.error_response(intention, "internal", str(exc), {"reason": "wrapper_capacity_failed"})
            )

    async def route_matter(self, intention: dict[str, Any], cap_name: str) -> None:
        params = intention.get("params", {}) if isinstance(intention.get("params"), dict) else {}
        matter_name = str(params.get("matter_id", "")).strip()
        target_context = self.runtime.target_context(intention)
        try:
            if cap_name == CAP_MATTER_READ:
                payload = await self.runtime.matter_manager.read_matter(target_context, matter_name)
                await self.runtime.transport.send(self.ok_response(intention, payload))
                return
            if cap_name == CAP_MATTER_WRITE:
                value = self.runtime.extract_write_value(intention)
                payload = await self.runtime.matter_manager.write_matter(target_context, matter_name, value)
                await self.runtime.transport.send(self.ok_response(intention, payload))
                return
            if cap_name == CAP_MATTER_SUBSCRIBE:
                sub_id = str(params.get("sub_id", "")).strip() or f"sub_{new_id()}"
                from_addr = intention.get("from", {}) if isinstance(intention.get("from"), dict) else {}
                self.runtime.subscriptions[sub_id] = {
                    "matter_id": matter_name,
                    "context": target_context,
                    "target": {
                        "context": str(from_addr.get("context", "")).strip(),
                        "cap": str(from_addr.get("cap", "")).strip(),
                        "type": str(from_addr.get("type", "")).strip(),
                    },
                }
                await self.runtime.transport.send(
                    self.ok_response(
                        intention,
                        {"ok": True, "matter_id": matter_name, "sub_id": sub_id},
                    )
                )
                return
            if cap_name == CAP_MATTER_UNSUBSCRIBE:
                sub_id = str(params.get("sub_id", "")).strip()
                if sub_id:
                    self.runtime.subscriptions.pop(sub_id, None)
                await self.runtime.transport.send(
                    self.ok_response(
                        intention,
                        {"ok": True, "matter_id": matter_name, "sub_id": sub_id},
                    )
                )
                return
            await self.runtime.transport.send(
                self.error_response(
                    intention,
                    "invalid",
                    f"unsupported wrapper matter cap {cap_name}",
                    {"reason": "unsupported_matter_cap"},
                )
            )
        except LookupError as exc:
            await self.runtime.transport.send(
                self.error_response(intention, "not_found", str(exc), {"reason": "unknown_matter"})
            )
        except Exception as exc:  # pragma: no cover - runtime failure path
            await self.runtime.transport.send(
                self.error_response(intention, "internal", str(exc), {"reason": "wrapper_matter_failed"})
            )

    def ok_response(self, intention: dict[str, Any], payload: dict[str, Any]) -> dict[str, Any]:
        return {
            "kind": KIND_RESPONSE,
            "ts": utc_now_rfc3339(),
            "response": {
                "intention_id": intention.get("intention_id", ""),
                "to": intention.get("from", {}),
                "from": {
                    "context": self.runtime.response_from_context(intention),
                    "cap": intention.get("to", {}).get("cap", ""),
                    "type": intention.get("to", {}).get("type", ""),
                },
                "identity": self.runtime.wrapper_identity(),
                "status": STATUS_OK,
                "payload": payload,
            },
        }

    def error_response(
        self,
        intention: dict[str, Any],
        code: str,
        message: str,
        details: dict[str, Any],
    ) -> dict[str, Any]:
        return {
            "kind": KIND_RESPONSE,
            "ts": utc_now_rfc3339(),
            "response": {
                "intention_id": intention.get("intention_id", ""),
                "to": intention.get("from", {}),
                "from": {
                    "context": self.runtime.response_from_context(intention),
                    "cap": intention.get("to", {}).get("cap", ""),
                    "type": intention.get("to", {}).get("type", ""),
                },
                "identity": self.runtime.wrapper_identity(),
                "status": STATUS_ERROR,
                "error": {
                    "origin": "wrapper",
                    "code": code,
                    "message": message,
                    "details": details,
                },
            },
        }
