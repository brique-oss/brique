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


class SandboxResponseError(Exception):
    brique_error = True

    def __init__(self, *, origin: str, code: str, message: str, details: dict[str, Any]) -> None:
        super().__init__(message)
        self.origin = origin
        self.code = code
        self.message = message
        self.details = details


class InspectorSandboxService:
    def echo(self, params: dict[str, Any]) -> dict[str, Any]:
        return dict(params)

    async def delayed_echo(self, params: dict[str, Any]) -> dict[str, Any]:
        delay_ms = params.get("delay_ms", 500)
        if not isinstance(delay_ms, (int, float)):
            delay_ms = 500
        await asyncio.sleep(max(0, float(delay_ms)) / 1000)
        payload = dict(params)
        payload["delay_ms"] = delay_ms
        return payload

    def error_test(self, _params: dict[str, Any]) -> dict[str, Any]:
        raise SandboxResponseError(
            origin="inspector_context",
            code="TEST_ERROR",
            message="Intentional sandbox error",
            details={"sandbox": True},
        )
