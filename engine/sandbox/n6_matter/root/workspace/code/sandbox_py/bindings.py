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

from app import WrapperMatterState
from wrapper.matter import build_matter_binding


def build_bindings(runtime):
    state = WrapperMatterState()
    matter_entry = {
        "read": state.read_payload,
        "write": state.write_payload,
        "notify_on_write": True,
    }
    matter_binding = build_matter_binding(runtime, context_id="", matter_name="m_wrapper", entry=matter_entry)

    async def touch_wrapper(params: dict) -> dict:
        value = str(params.get("value", "wrapper:touched"))
        assert matter_binding.write_symbol is not None
        return await matter_binding.write_symbol(value)

    return {
        "capacities": {
            ("", "wrapper.ping"): state.ping,
            ("", "wrapper.touch"): touch_wrapper,
        },
        "matters": {
            ("", "m_wrapper"): matter_entry,
        },
        "shutdown": [],
    }
