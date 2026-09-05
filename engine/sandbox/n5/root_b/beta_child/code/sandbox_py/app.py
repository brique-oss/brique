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

import os
from asyncio import sleep


class N5WrapperService:
    def __init__(self) -> None:
        self.wrapper_note = "seed-wrapper-note"

    def echo(self, params: dict) -> dict:
        message = str(params.get("message", ""))
        return {
            "echo": message,
            "handled_by": "sandbox.echo",
            "ctx_id": os.environ.get("BRIQUE_CTX_ID", ""),
            "wrapper_name": os.environ.get("BRIQUE_WRAPPER_NAME", ""),
        }

    def build_record(self, params: dict) -> dict:
        message = str(params.get("message", "")).strip()
        target_id = str(params.get("target_id", "")).strip()
        prefix = str(params.get("prefix", "built")).strip()
        ctx_id = os.environ.get("BRIQUE_CTX_ID", "")
        wrapper_name = os.environ.get("BRIQUE_WRAPPER_NAME", "")
        return {
            "record": {
                "message": message,
                "target_id": target_id,
                "composed": f"{prefix}:{message}",
                "ctx_id": ctx_id,
                "wrapper_name": wrapper_name,
            }
        }

    def consume_record(self, params: dict) -> dict:
        message = str(params.get("message", "")).strip()
        source_ctx = str(params.get("source_ctx", "")).strip()
        revision = str(params.get("revision", "")).strip()
        return {
            "summary": f"{source_ctx}:{message}:{revision}",
            "handled_by": "sandbox.consume.record",
            "ctx_id": os.environ.get("BRIQUE_CTX_ID", ""),
            "wrapper_name": os.environ.get("BRIQUE_WRAPPER_NAME", ""),
        }

    def build_batch(self, params: dict) -> dict:
        prefix = str(params.get("prefix", "item")).strip()
        count = int(params.get("count", 0) or 0)
        if count < 0:
            count = 0
        items = []
        for i in range(count):
            items.append(
                {
                    "message": f"{prefix}-{i}",
                    "index": i,
                }
            )
        return {
            "items": items,
            "count": count,
        }

    def inspect_inputs(self, params: dict) -> dict:
        return {
            "received_params": params,
            "ctx_id": os.environ.get("BRIQUE_CTX_ID", ""),
            "wrapper_name": os.environ.get("BRIQUE_WRAPPER_NAME", ""),
        }

    def build_matter_refs(self, params: dict) -> dict:
        local_matter_id = str(params.get("local_matter_id", "m_chain_remote")).strip() or "m_chain_remote"
        remote_matter_ref = str(params.get("remote_matter_ref", "")).strip()
        ctx_id = os.environ.get("BRIQUE_CTX_ID", "")
        refs = {
            "local_ref": f"{ctx_id}/{local_matter_id}",
        }
        if remote_matter_ref:
            refs["remote_ref"] = remote_matter_ref
        return refs

    def read_wrapper_note(self) -> str:
        return self.wrapper_note

    def write_wrapper_note(self, value: object) -> dict:
        self.wrapper_note = str(value)
        return {
            "ok": True,
            "value": self.wrapper_note,
        }

    async def sleep_echo(self, params: dict) -> dict:
        delay_ms = int(params.get("delay_ms", 0) or 0)
        if delay_ms > 0:
            await sleep(delay_ms / 1000.0)
        return self.echo(params)
