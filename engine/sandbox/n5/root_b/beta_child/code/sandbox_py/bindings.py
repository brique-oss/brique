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

from app import N5WrapperService


def build_bindings(runtime):
    service = N5WrapperService()

    def _parse_local_context(context: str) -> str:
        context = str(context).strip()
        if context == runtime.ctx_id:
            return ""
        prefix = runtime.ctx_id.rstrip("/") + "/"
        if context.startswith(prefix):
            return context[len(prefix) :]
        return context.strip("/")

    async def remote_state(params: dict) -> dict:
        to_context = str(params.get("to_context", "")).strip()
        response = await runtime.outbound.emit_and_wait(
            to_context=to_context,
            to_cap="read.state",
            to_type="reflexive",
            from_context="",
            from_cap="sandbox.remote.state",
            from_type="execution",
            params={"include": ["context"]},
        )
        remote = response.get("response", {})
        return {
          "ok": True,
          "remote_status": remote.get("status", ""),
          "remote_from": remote.get("from", {}),
          "remote_payload": remote.get("payload", {}),
        }

    async def remote_echo(params: dict) -> dict:
        to_context = str(params.get("to_context", "")).strip()
        message = str(params.get("message", "")).strip()
        response = await runtime.outbound.emit_and_wait(
            to_context=to_context,
            to_cap="sandbox.echo",
            to_type="user",
            from_context="",
            from_cap="sandbox.remote.echo",
            from_type="execution",
            params={"message": message},
        )
        remote = response.get("response", {})
        return {
          "ok": True,
          "remote_status": remote.get("status", ""),
          "remote_from": remote.get("from", {}),
          "remote_payload": remote.get("payload", {}),
        }

    async def inspect_resolved_inputs(params: dict) -> dict:
        received = dict(params)
        matters = received.get("@matter", {}) if isinstance(received.get("@matter"), dict) else {}
        resolved = {}
        primary = matters.get("primary") if isinstance(matters.get("primary"), dict) else None
        if primary is not None:
            local_ctx = _parse_local_context(primary.get("context", ""))
            resolved["primary"] = await runtime.matter_manager.read_matter(local_ctx, str(primary.get("id", "")).strip())
        secondary = matters.get("secondary") if isinstance(matters.get("secondary"), dict) else None
        if secondary is not None:
            response = await runtime.outbound.emit_and_wait(
                to_context=str(secondary.get("context", "")).strip(),
                to_cap="matter.read",
                to_type="matter",
                from_context="",
                from_cap="sandbox.inspect.resolved.inputs",
                from_type="execution",
                params={
                    "matter_id": str(secondary.get("id", "")).strip(),
                    "read_mode": "functional|brique",
                },
            )
            resolved["secondary"] = response.get("response", {}).get("payload", {})
        return {
            "received_params": received,
            "resolved_matters": resolved,
            "ctx_id": runtime.ctx_id,
            "wrapper_name": runtime.wrapper_name,
        }

    async def reflect_local_meaning(params: dict) -> dict:
        name = str(params.get("name", "sandbox.echo")).strip() or "sandbox.echo"
        response = await runtime.outbound.emit_and_wait(
            to_context=runtime.ctx_id,
            to_cap="read.meaning",
            to_type="reflexive",
            from_context="",
            from_cap="sandbox.reflect.local.meaning",
            from_type="execution",
            params={
                "input": [
                    {
                        "element_kind": "capacity",
                        "element_name": name,
                        "sections": ["objective", "functional"],
                    }
                ]
            },
        )
        remote = response.get("response", {})
        payload = remote.get("payload", {}) if isinstance(remote.get("payload"), dict) else {}
        rows = payload.get("result", []) if isinstance(payload.get("result"), list) else []
        first = rows[0] if rows and isinstance(rows[0], dict) else {}
        desc = first.get("desc", {}) if isinstance(first.get("desc"), dict) else {}
        if not desc and isinstance(first.get("descriptor"), dict):
            desc = first.get("descriptor", {})
        objective = desc.get("objective", {}) if isinstance(desc.get("objective"), dict) else {}
        functional = desc.get("functional", {}) if isinstance(desc.get("functional"), dict) else {}
        return {
            "item_ok": bool(first.get("ok", False)),
            "requested_name": name,
            "objective_name": objective.get("name", ""),
            "objective_description": objective.get("description", ""),
            "functional_role": functional.get("#root", {}).get("role", "") if isinstance(functional.get("#root"), dict) else "",
            "raw_item": first,
            "ctx_id": runtime.ctx_id,
            "wrapper_name": runtime.wrapper_name,
        }

    async def reflect_remote_query(params: dict) -> dict:
        to_context = str(params.get("to_context", "")).strip()
        ctx_id = str(params.get("ctx_id", "")).strip()
        name = str(params.get("name", "sandbox.echo")).strip() or "sandbox.echo"
        query_path = str(params.get("query_path", "objective.name")).strip() or "objective.name"
        query_value = params.get("query_value", name)
        rebuild = await runtime.outbound.emit_and_wait(
            to_context=to_context,
            to_cap="meaning.rebuild",
            to_type="reflexive",
            from_context="",
            from_cap="sandbox.reflect.remote.query",
            from_type="execution",
            params={"mode": "full"},
        )
        rebuild_resp = rebuild.get("response", {})
        query = await runtime.outbound.emit_and_wait(
            to_context=to_context,
            to_cap="meaning.query",
            to_type="reflexive",
            from_context="",
            from_cap="sandbox.reflect.remote.query",
            from_type="execution",
            params={
                "ctx_id": ctx_id,
                "element_kind": "capacity",
                "filters": [
                    {
                        "path": query_path,
                        "op": "EQ",
                        "value": query_value,
                    }
                ],
            },
        )
        query_resp = query.get("response", {})
        payload = query_resp.get("payload", {}) if isinstance(query_resp.get("payload"), dict) else {}
        rows = payload.get("result", []) if isinstance(payload.get("result"), list) else []
        first = rows[0] if rows and isinstance(rows[0], dict) else {}
        return {
            "rebuild_status": rebuild_resp.get("status", ""),
            "query_status": query_resp.get("status", ""),
            "row_count": len(rows),
            "queried_ctx": ctx_id,
            "first_name": first.get("name", ""),
            "ctx_id": runtime.ctx_id,
            "wrapper_name": runtime.wrapper_name,
        }

    async def reflect_mixed_summary(params: dict) -> dict:
        local_name = str(params.get("local_name", "sandbox.echo")).strip() or "sandbox.echo"
        remote_name = str(params.get("remote_name", "sandbox.echo")).strip() or "sandbox.echo"
        remote_root = str(params.get("to_context", "")).strip()
        remote_ctx = str(params.get("ctx_id", "")).strip()

        local = await runtime.outbound.emit_and_wait(
            to_context=runtime.ctx_id,
            to_cap="read.meaning",
            to_type="reflexive",
            from_context="",
            from_cap="sandbox.reflect.mixed.summary",
            from_type="execution",
            params={
                "input": [
                    {
                        "element_kind": "capacity",
                        "element_name": local_name,
                        "sections": ["objective", "functional"],
                    }
                ]
            },
        )
        local_payload = local.get("response", {}).get("payload", {})
        local_rows = local_payload.get("result", []) if isinstance(local_payload, dict) else []
        local_first = local_rows[0] if local_rows and isinstance(local_rows[0], dict) else {}
        local_desc = local_first.get("desc", {}) if isinstance(local_first.get("desc"), dict) else {}
        if not local_desc and isinstance(local_first.get("descriptor"), dict):
            local_desc = local_first.get("descriptor", {})
        local_objective = local_desc.get("objective", {}) if isinstance(local_desc.get("objective"), dict) else {}
        local_functional = local_desc.get("functional", {}) if isinstance(local_desc.get("functional"), dict) else {}

        rebuild = await runtime.outbound.emit_and_wait(
            to_context=remote_root,
            to_cap="meaning.rebuild",
            to_type="reflexive",
            from_context="",
            from_cap="sandbox.reflect.mixed.summary",
            from_type="execution",
            params={"mode": "full"},
        )
        query = await runtime.outbound.emit_and_wait(
            to_context=remote_root,
            to_cap="meaning.query",
            to_type="reflexive",
            from_context="",
            from_cap="sandbox.reflect.mixed.summary",
            from_type="execution",
            params={
                "ctx_id": remote_ctx,
                "element_kind": "capacity",
                "filters": [
                    {
                        "path": "objective.name",
                        "op": "EQ",
                        "value": remote_name,
                    }
                ],
            },
        )
        query_resp = query.get("response", {})
        query_payload = query_resp.get("payload", {}) if isinstance(query_resp.get("payload"), dict) else {}
        query_rows = query_payload.get("result", []) if isinstance(query_payload.get("result"), list) else []
        query_first = query_rows[0] if query_rows and isinstance(query_rows[0], dict) else {}
        rebuild_resp = rebuild.get("response", {})

        return {
            "local_name": local_objective.get("name", ""),
            "local_role": local_functional.get("#root", {}).get("role", "") if isinstance(local_functional.get("#root"), dict) else "",
            "remote_name": query_first.get("name", ""),
            "remote_row_count": len(query_rows),
            "remote_rebuild_status": rebuild_resp.get("status", ""),
            "remote_query_status": query_resp.get("status", ""),
            "summary": f"{local_objective.get('name', '')}:{query_first.get('name', '')}:{remote_ctx}",
            "ctx_id": runtime.ctx_id,
            "wrapper_name": runtime.wrapper_name,
        }

    async def trace_user(params: dict) -> dict:
        user_text = str(params.get("user_text", "")).strip()
        reason_code = str(params.get("reason_code", "")).strip()
        source_intention_id = str(params.get("intention_id", "")).strip()
        to_context = str(params.get("to_context", runtime.ctx_id)).strip() or runtime.ctx_id
        response = await runtime.outbound.emit_and_wait(
            to_context=to_context,
            to_cap="trace.user",
            to_type="trace",
            from_context="",
            from_cap="sandbox.trace.user",
            from_type="execution",
            params={
                "trace_kind": "user",
                "user_text": user_text,
                "reason_code": reason_code,
                "intention_id": source_intention_id,
            },
        )
        remote = response.get("response", {})
        payload = remote.get("payload", {}) if isinstance(remote.get("payload"), dict) else {}
        return {
            "trace_status": remote.get("status", ""),
            "accepted": bool(payload.get("accepted", False)),
            "trace_kind": payload.get("trace_kind", ""),
            "user_text": user_text,
            "reason_code": reason_code,
            "target_context": to_context,
            "ctx_id": runtime.ctx_id,
            "wrapper_name": runtime.wrapper_name,
        }

    async def inspect_remote_trace(params: dict) -> dict:
        to_context = str(params.get("to_context", "")).strip()
        response = await runtime.outbound.emit_and_wait(
            to_context=to_context,
            to_cap="sandbox.inspect.local.trace",
            to_type="user",
            from_context="",
            from_cap="sandbox.inspect.remote.trace",
            from_type="execution",
            params=dict(params),
        )
        remote = response.get("response", {})
        payload = remote.get("payload", {}) if isinstance(remote.get("payload"), dict) else {}
        return {
            "trace_status": remote.get("status", ""),
            "event_count": int(payload.get("event_count", 0) or 0),
            "found": bool(payload.get("found", False)),
            "first_reason_code": payload.get("first_reason_code", ""),
            "first_user_text": payload.get("first_user_text", ""),
            "first_trace_kind": payload.get("first_trace_kind", ""),
            "target_context": to_context,
            "ctx_id": runtime.ctx_id,
            "wrapper_name": runtime.wrapper_name,
        }

    async def inspect_local_trace(params: dict) -> dict:
        to_context = runtime.ctx_id
        reason_code = str(params.get("reason_code", "")).strip()
        limit = int(params.get("limit", 20) or 20)
        from_ts_ns = int(params.get("from_ts_ns", 0) or 0)
        to_ts_ns = int(params.get("to_ts_ns", 0) or 0)
        outbound_params = {
            "mode": "events",
            "limit": limit,
            "offset": 0,
            "order_by": "ts_ns DESC",
            "filters": {
                "trace_kind": "user",
                "reason_code": reason_code,
            },
        }
        if from_ts_ns > 0 and to_ts_ns > 0:
            outbound_params["time_range"] = {
                "from_ts_ns": from_ts_ns,
                "to_ts_ns": to_ts_ns,
            }
        response = await runtime.outbound.emit_and_wait(
            to_context=to_context,
            to_cap="trace.inspect",
            to_type="reflexive",
            from_context="",
            from_cap="sandbox.inspect.local.trace",
            from_type="execution",
            params=outbound_params,
        )
        remote = response.get("response", {})
        payload = remote.get("payload", {}) if isinstance(remote.get("payload"), dict) else {}
        events = payload.get("events", []) if isinstance(payload.get("events"), list) else []
        first = events[0] if events and isinstance(events[0], dict) else {}
        return {
            "trace_status": remote.get("status", ""),
            "event_count": len(events),
            "found": len(events) > 0,
            "first_reason_code": first.get("reason_code", ""),
            "first_user_text": first.get("user_text", ""),
            "first_trace_kind": first.get("trace_kind", ""),
            "target_context": to_context,
            "ctx_id": runtime.ctx_id,
            "wrapper_name": runtime.wrapper_name,
        }

    return {
        "capacities": {
            ("", "sandbox.echo"): service.echo,
            ("", "sandbox.build.record"): service.build_record,
            ("", "sandbox.consume.record"): service.consume_record,
            ("", "sandbox.build.batch"): service.build_batch,
            ("", "sandbox.inspect.inputs"): service.inspect_inputs,
            ("", "sandbox.inspect.resolved.inputs"): inspect_resolved_inputs,
            ("", "sandbox.build.matter_refs"): service.build_matter_refs,
            ("", "sandbox.reflect.local.meaning"): reflect_local_meaning,
            ("", "sandbox.reflect.remote.query"): reflect_remote_query,
            ("", "sandbox.reflect.mixed.summary"): reflect_mixed_summary,
            ("", "sandbox.trace.user"): trace_user,
            ("", "sandbox.inspect.local.trace"): inspect_local_trace,
            ("", "sandbox.inspect.remote.trace"): inspect_remote_trace,
            ("", "sandbox.sleep.echo"): service.sleep_echo,
            ("", "sandbox.remote.state"): remote_state,
            ("", "sandbox.remote.echo"): remote_echo,
        },
        "matters": {
            ("", "wrapper_note"): {
                "read": service.read_wrapper_note,
                "write": service.write_wrapper_note,
                "notify_on_write": True,
            }
        },
        "shutdown": [],
    }
