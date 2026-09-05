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

from pathlib import Path

from app import PythonExecutionService


def build_bindings(runtime):
    service = PythonExecutionService()

    async def call_cpp(params: dict) -> dict:
        message = str(params.get("message", "hello-cross-wrapper"))
        response = await runtime.outbound.emit_and_wait(
            to_context=runtime.ctx_id,
            to_cap="sandbox.cpp.echo",
            to_type="user",
            from_context="",
            from_cap="sandbox.py.call.cpp",
            from_type="execution",
            params={
                "message": message,
            },
        )
        remote = response.get("response", {})
        return {
            "ok": True,
            "remote_status": remote.get("status", ""),
            "remote_from": remote.get("from", {}),
            "remote_payload": remote.get("payload", {}),
        }

    async def remote_ops(params: dict) -> dict:
        root_context = str(params.get("root_context", "/root")).strip() or "/root"
        remote_context = str(params.get("remote_context", "/root/remote_catalog")).strip() or "/root/remote_catalog"
        matter_id = str(params.get("matter_id", "m_remote")).strip() or "m_remote"
        document_name = str(params.get("document_name", "remote_note")).strip() or "remote_note"
        marker = str(params.get("marker", "patched-by-wrapper-remote-ops")).strip() or "patched-by-wrapper-remote-ops"

        remote_descriptor = runtime.ctx_dir.parent / "remote_catalog" / "document" / f"{document_name}.json"
        service.patch_remote_document_description(Path(remote_descriptor), marker)

        update = await runtime.outbound.emit_and_wait(
            to_context=root_context,
            to_cap="meaning.update",
            to_type="reflexive",
            from_context="",
            from_cap="sandbox.py.remote.ops",
            from_type="execution",
            params={
                "elements": [
                    {
                        "ctx_id": remote_context,
                        "element_kind": "document",
                        "name": document_name,
                    }
                ]
            },
        )
        query = await runtime.outbound.emit_and_wait(
            to_context=root_context,
            to_cap="meaning.query",
            to_type="reflexive",
            from_context="",
            from_cap="sandbox.py.remote.ops",
            from_type="execution",
            params={
                "ctx_id": remote_context,
                "element_kind": "document",
                "filters": [
                    {
                        "path": "objective.description",
                        "op": "EQ",
                        "value": marker,
                    }
                ],
                "limit": 10,
                "offset": 0,
            },
        )
        matter = await runtime.outbound.emit_and_wait(
            to_context=remote_context,
            to_cap="matter.read",
            to_type="matter",
            from_context="",
            from_cap="sandbox.py.remote.ops",
            from_type="execution",
            params={
                "matter_id": matter_id,
                "read_mode": "functional|brique",
            },
        )

        query_payload = query.get("response", {}).get("payload", {})
        matter_payload = matter.get("response", {}).get("payload", {})
        return {
            "ok": True,
            "marker": marker,
            "remote_context": remote_context,
            "meaning_update_status": update.get("response", {}).get("status", ""),
            "meaning_update_payload": update.get("response", {}).get("payload", {}),
            "meaning_query_status": query.get("response", {}).get("status", ""),
            "meaning_query_payload": query_payload,
            "matter_read_status": matter.get("response", {}).get("status", ""),
            "matter_read_payload": matter_payload,
        }

    return {
        "capacities": {
            ("", "sandbox.py.echo"): service.echo,
            ("", "sandbox.py.runtime.info"): service.runtime_info,
            ("", "sandbox.py.call.cpp"): call_cpp,
            ("", "sandbox.py.remote.ops"): remote_ops,
        },
        "matters": {},
        "shutdown": [],
    }
