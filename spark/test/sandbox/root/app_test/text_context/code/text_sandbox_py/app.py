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


class TextSandboxService:
    def __init__(self, ctx_dir: Path) -> None:
        self.data_path = ctx_dir / "matter" / "text_buffer" / "data.bin"

    def set_text(self, params: dict[str, Any]) -> dict[str, str]:
        value = str(params.get("value", ""))
        return self._write(value)

    def uppercase_text(self, _params: dict[str, Any]) -> dict[str, str]:
        current = self._read()["value"]
        return self._write(current.upper())

    def append_text(self, params: dict[str, Any]) -> dict[str, str]:
        current = self._read()["value"]
        return self._write(current + str(params.get("value", "")))

    def read_text(self, _params: dict[str, Any]) -> dict[str, str]:
        return self._read()

    def write_text(self, value: Any) -> dict[str, str]:
        return self._write(str(value))

    def _read(self) -> dict[str, str]:
        raw = json.loads(self.data_path.read_text(encoding="utf-8"))
        return {"value": str(raw.get("value", ""))}

    def _write(self, value: str) -> dict[str, str]:
        payload = {"value": value}
        self.data_path.write_text(json.dumps(payload, separators=(",", ":")) + "\n", encoding="utf-8")
        return payload
