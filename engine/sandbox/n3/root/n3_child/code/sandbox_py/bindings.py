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

import importlib.util
from pathlib import Path

from app import SandboxCapabilities, SandboxMatterState, echo_capacity


def _load_module(label: str, path: Path):
    spec = importlib.util.spec_from_file_location(label, path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"failed to load module from {path}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


async def build_bindings(runtime):
    capabilities = SandboxCapabilities(runtime.wrapper_name)
    matters = SandboxMatterState(runtime.wrapper_name)

    py_api_app = _load_module(
        "sandbox_py_py_api_app",
        runtime.ctx_dir / "py_api" / "code" / runtime.wrapper_name / "app.py",
    )
    py_active_app = _load_module(
        "sandbox_py_py_active_app",
        runtime.ctx_dir / "py_active" / "code" / runtime.wrapper_name / "app.py",
    )

    api_state = py_api_app.ApiState()
    active_component = py_active_app.ActiveComponent()
    await active_component.start()

    async def emit_fire(params: dict) -> dict:
        target_context = str(params.get("target_context", "/remote/target")).strip() or "/remote/target"
        target_cap = str(params.get("target_cap", "remote.accept")).strip() or "remote.accept"
        target_type = str(params.get("target_type", "user")).strip() or "user"
        outbound_params = params.get("outbound_params")
        if not isinstance(outbound_params, dict):
            outbound_params = {
                "payload": params.get("payload", "fire"),
            }
        intention_id = await runtime.outbound.emit_intention(
            to_context=target_context,
            to_cap=target_cap,
            to_type=target_type,
            from_context=runtime.ctx_id,
            from_cap="sandbox.emit.fire",
            from_type="execution",
            params=outbound_params,
            await_response=False,
        )
        return {
            "ok": True,
            "emitted": True,
            "outbound_intention_id": intention_id,
            "target_context": target_context,
        }

    async def emit_await(params: dict) -> dict:
        target_context = str(params.get("target_context", "/remote/target")).strip() or "/remote/target"
        target_cap = str(params.get("target_cap", "remote.reply")).strip() or "remote.reply"
        target_type = str(params.get("target_type", "user")).strip() or "user"
        timeout_s = params.get("timeout_s")
        outbound_params = params.get("outbound_params")
        if not isinstance(outbound_params, dict):
            outbound_params = {
                "payload": params.get("payload", "await"),
            }
        response = await runtime.outbound.emit_and_wait(
            to_context=target_context,
            to_cap=target_cap,
            to_type=target_type,
            from_context=runtime.ctx_id,
            from_cap="sandbox.emit.await",
            from_type="execution",
            params=outbound_params,
            timeout_s=float(timeout_s) if isinstance(timeout_s, (int, float)) else None,
        )
        payload = response.get("response", {}).get("payload", {})
        status = response.get("response", {}).get("status", "")
        return {
            "ok": True,
            "awaited": True,
            "remote_status": status,
            "remote_payload": payload,
        }

    return {
        "capacities": {
            ("", "sandbox.echo"): echo_capacity,
            ("", "sandbox.class.echo"): capabilities.class_echo,
            ("", "sandbox.emit.fire"): emit_fire,
            ("", "sandbox.emit.await"): emit_await,
            ("py_api", "api.echo"): py_api_app.api_echo,
            ("py_api", "api.combine"): py_api_app.api_combine,
            ("py_active", "active.enable"): active_component.enable,
            ("py_active", "active.disable"): active_component.disable,
            ("py_active", "active.snapshot"): active_component.snapshot,
        },
        "matters": {
            ("", "m_wrapper"): {
                "read": matters.read_inline,
                "write": matters.write_inline,
            },
            ("", "m_wrapper_global"): {
                "read": matters.read_global,
                "write": matters.write_global,
            },
            ("", "m_wrapper_property"): {
                "read": lambda: matters.property_value,
                "write": matters.set_property_value,
            },
            ("py_api", "api_cache"): {
                "read": api_state.read_cache,
                "write": api_state.write_cache,
            },
            ("py_active", "active_enabled"): {
                "read": active_component.read_enabled,
                "write": active_component.write_enabled,
            },
            ("py_active", "active_state"): {
                "read": active_component.read_state,
            },
        },
        "shutdown": [
            active_component.stop,
        ],
    }
