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


class WrapperMatterState:
    def __init__(self) -> None:
        self.payload = "wrapper:seed"

    # <brique:capacity name="wrapper.ping">
    def ping(self, params: dict) -> dict:
        return {
            "ok": True,
            "echo": str(params.get("message", "ping")),
        }
    # </brique:capacity>

    # <brique:matter name="m_wrapper">
    def read_payload(self) -> str:
        return self.payload

    def write_payload(self, value: object) -> dict:
        self.payload = str(value)
        return {
            "ok": True,
            "matter_id": "m_wrapper",
            "substance_mode": "wrapper",
            "value": self.payload,
        }
    # </brique:matter>
