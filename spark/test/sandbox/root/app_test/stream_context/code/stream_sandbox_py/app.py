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

import asyncio
import urllib.request
from typing import Any


class StreamSandboxService:
    async def generate_stream(
        self,
        params: dict[str, Any],
        runtime: Any,
    ) -> dict[str, Any]:
        chunk_count = int(params.get("chunk_count", 5))
        chunk_size_bytes = int(params.get("chunk_size_bytes", 1024))

        # Open an HTTP upload lease for stream_payload
        write_resp = await runtime.outbound.emit_and_wait(
            to_context="",
            to_cap="matter.write",
            to_type="matter",
            params={
                "matter_id": "stream_payload",
                "http_data": True,
            },
        )

        payload = write_resp.get("payload", {})
        http_data = payload.get("http_data", {})
        upload_url = http_data.get("url")
        token = http_data.get("token")

        if not upload_url or not token:
            raise RuntimeError(f"No HTTP upload lease in response: {payload}")

        # Build the full payload in memory and upload it
        total_bytes = chunk_count * chunk_size_bytes
        body = bytes([i % 256 for i in range(chunk_size_bytes)]) * chunk_count

        req = urllib.request.Request(
            upload_url,
            data=body,
            method="PUT",
            headers={
                "Authorization": f"Bearer {token}",
                "Content-Type": "application/octet-stream",
                "Content-Length": str(total_bytes),
            },
        )

        await asyncio.to_thread(_do_request, req)

        return {
            "ok": True,
            "matter_id": "stream_payload",
            "total_bytes": total_bytes,
        }


def _do_request(req: urllib.request.Request) -> None:
    with urllib.request.urlopen(req) as resp:
        resp.read()
