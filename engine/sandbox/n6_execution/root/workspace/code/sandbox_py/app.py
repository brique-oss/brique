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
import os
from pathlib import Path


class PythonExecutionService:
    # <brique:capacity name="sandbox.py.echo">
    def echo(self, params: dict) -> dict:
        message = str(params.get("message", ""))
        return {
            "echo": message,
            "handled_by": "PythonExecutionService.echo",
        }
    # </brique:capacity>

    # <brique:capacity name="sandbox.py.runtime.info">
    def runtime_info(self, _params: dict) -> dict:
        return {
            "wrapper_name": os.environ.get("BRIQUE_WRAPPER_NAME", ""),
            "ctx_id": os.environ.get("BRIQUE_CTX_ID", ""),
            "ctx_dir": os.environ.get("BRIQUE_CTX_DIR", ""),
            "wrapper_src_dir": os.environ.get("BRIQUE_WRAPPER_SRC_DIR", ""),
            "wrapper_build_dir": os.environ.get("BRIQUE_WRAPPER_BUILD_DIR", ""),
            "workdir_env": os.environ.get("BRIQUE_WORKDIR", ""),
            "cwd": str(Path.cwd()),
        }
    # </brique:capacity>

    def patch_remote_document_description(self, descriptor_path: Path, description: str) -> None:
        raw = json.loads(descriptor_path.read_text(encoding="utf-8"))
        objective = raw.get("objective")
        if not isinstance(objective, dict):
            objective = {}
        objective["description"] = description
        raw["objective"] = objective
        descriptor_path.write_text(json.dumps(raw, indent=2) + "\n", encoding="utf-8")
