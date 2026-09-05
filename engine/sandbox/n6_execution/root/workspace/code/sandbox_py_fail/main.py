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
import json
import os
from pathlib import Path

from wrapper._ws_stdlib import StdlibWebSocketClient
from wrapper.util import load_json, new_id, utc_now_rfc3339


def boundary_url() -> str:
    ctx_dir = Path(os.environ["BRIQUE_CTX_DIR"]).resolve()
    wrapper_name = os.environ["BRIQUE_WRAPPER_NAME"]
    context_doc = load_json(ctx_dir / "context.json")
    engine_cfg = context_doc.get("brique", {}).get("engine_config", {})
    comm_cfg = engine_cfg.get("communication", {})
    for iface in comm_cfg.get("interfaces", []):
        if not isinstance(iface, dict):
            continue
        if iface.get("type") != "wrapper" or iface.get("name") != wrapper_name:
            continue
        cfg = iface.get("config", {}) if isinstance(iface.get("config"), dict) else {}
        path = str(cfg.get("path", "/ws")).strip() or "/ws"
        base_url = os.environ.get("BRIQUE_WS_URL", "").strip().rstrip("/")
        if base_url:
            return f"{base_url}{path}"
        addr = str(cfg.get("addr", "")).strip() or os.environ.get("BRIQUE_WS_ADDR", "").strip()
        if not addr:
            raise RuntimeError("shared websocket listener addr not provided")
        host = "127.0.0.1"
        if addr.startswith(":"):
            port = addr[1:]
        else:
            host_part, _, port_part = addr.rpartition(":")
            host = host_part or host
            port = port_part or "8080"
            if host in {"0.0.0.0", ""}:
                host = "127.0.0.1"
        return f"ws://{host}:{port}{path}"
    raise RuntimeError(f"wrapper websocket interface not found for {wrapper_name}")


async def main() -> int:
    ws = StdlibWebSocketClient(boundary_url())
    await ws.connect()
    message = {
        "kind": "intention",
        "ts": utc_now_rfc3339(),
        "intention": {
            "intention_id": new_id(),
            "await_response": False,
            "to": {
                "context": os.environ.get("BRIQUE_CTX_ID", ""),
                "cap": "wrapper_failed",
                "type": "execution",
            },
            "from": {
                "context": "",
                "cap": "",
                "type": "execution",
            },
            "identity": {
                "id": os.environ.get("BRIQUE_WRAPPER_NAME", ""),
                "kind": "wrapper",
            },
            "params": {
                "wrapper": os.environ.get("BRIQUE_WRAPPER_NAME", ""),
                "error_text": "simulated wrapper failure",
            },
            "correlation": {
                "root_intention_id": "",
                "parent_intention_id": "",
            },
        },
    }
    await ws.send_text(json.dumps(message, separators=(",", ":"), ensure_ascii=True))
    await asyncio.sleep(0.05)
    await ws.close()
    return 0


if __name__ == "__main__":
    raise SystemExit(asyncio.run(main()))
