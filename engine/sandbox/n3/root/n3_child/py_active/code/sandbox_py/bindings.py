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


def _load_local_app():
    app_path = Path(__file__).with_name("app.py")
    spec = importlib.util.spec_from_file_location("sandbox_py_py_active_app", app_path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"failed to load app module from {app_path}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def _child_name() -> str:
    return Path(__file__).resolve().parents[2].name


async def build_bindings(runtime):
    module = _load_local_app()
    ctx_id = runtime.child_context_id(_child_name())
    component = module.ActiveComponent()
    await component.start()
    return {
        "capacities": {
            (ctx_id, "active.enable"): component.enable,
            (ctx_id, "active.disable"): component.disable,
            (ctx_id, "active.snapshot"): component.snapshot,
        },
        "matters": {
            (ctx_id, "active_enabled"): {
                "read": component.read_enabled,
                "write": component.write_enabled,
            },
            (ctx_id, "active_state"): {
                "read": component.read_state,
            },
        },
        "shutdown": [
            component.stop,
        ],
    }
