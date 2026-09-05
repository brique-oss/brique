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


WRAPPER_GLOBAL_TEXT = "sandbox-global-wrapper-data"


# <brique:capacity name="sandbox.echo">
def echo_capacity(params: dict) -> dict:
    message = str(params.get("message", ""))
    return {
        "echo": message,
        "handled_by": "echo_capacity",
    }
# </brique:capacity>


class SandboxCapabilities:
    def __init__(self, wrapper_name: str) -> None:
        self.wrapper_name = wrapper_name

    # <brique:capacity name="sandbox.class.echo">
    def class_echo(self, params: dict) -> dict:
        message = str(params.get("message", ""))
        return {
            "echo": message,
            "handled_by": "SandboxCapabilities.class_echo",
            "wrapper_name": self.wrapper_name,
        }
    # </brique:capacity>


class SandboxMatterState:
    def __init__(self, wrapper_name: str) -> None:
        self.wrapper_name = wrapper_name
        self.inline_payload = f"getter:m_wrapper:{wrapper_name}"
        self.property_payload = f"class-property:{wrapper_name}"
        self.global_payload = WRAPPER_GLOBAL_TEXT

    # <brique:matter name="m_wrapper">
    def read_inline(self) -> str:
        return self.inline_payload

    def write_inline(self, value: object) -> dict:
        self.inline_payload = str(value)
        return {"ok": True, "value": self.inline_payload}
    # </brique:matter>

    # <brique:matter name="m_wrapper_global">
    def read_global(self) -> str:
        return self.global_payload

    def write_global(self, value: object) -> dict:
        self.global_payload = str(value)
        return {"ok": True, "value": self.global_payload}
    # </brique:matter>

    # <brique:matter name="m_wrapper_property">
    @property
    def property_value(self) -> str:
        return self.property_payload

    def set_property_value(self, value: object) -> dict:
        self.property_payload = str(value)
        return {"ok": True, "value": self.property_payload}
    # </brique:matter>
