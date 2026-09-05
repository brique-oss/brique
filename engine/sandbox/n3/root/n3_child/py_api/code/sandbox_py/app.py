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


# <brique:capacity name="api.echo">
def api_echo(params: dict) -> dict:
    message = str(params.get("message", ""))
    return {
        "echo": message,
        "handled_by": "api_echo",
    }
# </brique:capacity>


# <brique:capacity name="api.combine">
def api_combine(params: dict) -> dict:
    return {
        "combined": f"{params.get('left', '')}:{params.get('right', '')}",
        "handled_by": "api_combine",
    }
# </brique:capacity>


class ApiState:
    def __init__(self) -> None:
        self._cache = "cold"

    @property
    def cache_value(self) -> str:
        return self._cache

    # <brique:matter name="api_cache">
    def read_cache(self) -> str:
        return self.cache_value

    def write_cache(self, value: object) -> dict:
        self._cache = str(value)
        return {
            "ok": True,
            "value": self._cache,
        }
    # </brique:matter>
