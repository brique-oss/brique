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

from app import CounterSandboxService


def build_bindings(runtime):
    service = CounterSandboxService(runtime.ctx_dir)

    async def increment(_params):
        current = service.read_counter({})["value"]
        return await runtime.matter_manager.write_matter("", "counter_value", current + 1)

    async def reset(_params):
        return await runtime.matter_manager.write_matter("", "counter_value", 0)

    return {
        "capacities": {
            ("", "increment"): increment,
            ("", "reset"): reset,
            ("", "read_counter"): service.read_counter,
        },
        "matters": {
            ("", "counter_value"): {
                "read": lambda: service.read_counter({}),
                "write": service.write_counter,
                "notify_on_write": True,
            },
        },
        "shutdown": [],
    }
