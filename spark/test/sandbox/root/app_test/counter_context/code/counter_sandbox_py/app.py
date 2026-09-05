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

import json
from pathlib import Path
from typing import Any


class CounterSandboxService:
    def __init__(self, ctx_dir: Path) -> None:
        self.data_path = ctx_dir / "matter" / "counter_value" / "data.bin"

    def increment(self, _params: dict[str, Any]) -> dict[str, int]:
        current = self._read()["value"]
        return self._write(current + 1)

    def reset(self, _params: dict[str, Any]) -> dict[str, int]:
        return self._write(0)

    def read_counter(self, _params: dict[str, Any]) -> dict[str, int]:
        return self._read()

    def write_counter(self, value: Any) -> dict[str, int]:
        if not isinstance(value, (int, float)):
            value = 0
        return self._write(int(value))

    def _read(self) -> dict[str, int]:
        raw = json.loads(self.data_path.read_text(encoding="utf-8"))
        value = raw.get("value", 0)
        if not isinstance(value, (int, float)):
            value = 0
        return {"value": int(value)}

    def _write(self, value: int) -> dict[str, int]:
        payload = {"value": value}
        self.data_path.write_text(json.dumps(payload, separators=(",", ":")) + "\n", encoding="utf-8")
        return payload
