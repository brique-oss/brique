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

import os


def build_bindings(runtime):
    def reply(params: dict) -> dict:
        return {
            "echo": str(params.get("message", "")),
            "node": os.environ.get("E2E_NODE", ""),
            "wrapper": runtime.wrapper_name,
            "ctx_id": runtime.ctx_id,
        }

    async def relay(params: dict) -> dict:
        try:
            response = await runtime.outbound.emit_and_wait(
                to_context=str(params.get("to_context", "")),
                to_cap=str(params.get("to_cap", "")),
                to_type=str(params.get("to_type", "user")),
                from_context="",
                from_cap="e2e.relay",
                from_type="execution",
                params=params.get("params") or {},
            )
        except Exception as error:  # the relay reports what happened, whatever it was
            return {"node": os.environ.get("E2E_NODE", ""), "wrapper": runtime.wrapper_name,
                    "relayed_status": "exception", "relayed_error": {"message": str(error)}, "relayed_payload": {}}
        answer = response.get("response", {})
        return {
            "node": os.environ.get("E2E_NODE", ""),
            "wrapper": runtime.wrapper_name,
            "relayed_status": answer.get("status", ""),
            "relayed_error": answer.get("error") or {},
            "relayed_payload": answer.get("payload") or {},
        }

    return {
        "capacities": {("", "e2e.echo"): reply, ("", "e2e.secret"): reply, ("", "e2e.relay"): relay},
        "matters": {},
        "shutdown": [],
    }
