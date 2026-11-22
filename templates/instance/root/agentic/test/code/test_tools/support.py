"""Minimal access of the test tools to the Brique world (outbound intentions) and the shared
state they use: the matters of the test context. Test infrastructure only."""
from __future__ import annotations

import asyncio
import json
from datetime import datetime, timezone
from typing import Any, Callable, Dict, Optional, Tuple

CONTEXT = "/root/agentic/test"
RETRIES = 40


def now() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z")


class EngineError(Exception):
    def __init__(self, code: str, reason: str, message: str) -> None:
        super().__init__(f"{code}/{reason}: {message}")
        self.code, self.reason = code, reason


class Port:
    def __init__(self, runtime, from_cap: str) -> None:
        self.runtime, self.from_cap = runtime, from_cap

    async def call(self, cap: str, params: Dict[str, Any], *, to_type: str = "matter", context: str = CONTEXT, timeout_s: float = 30.0) -> Dict[str, Any]:
        message = await self.runtime.outbound.emit_and_wait(
            to_context=context, to_cap=cap, to_type=to_type, from_context="", from_cap=self.from_cap,
            params=params, timeout_s=timeout_s)
        body = message.get("response", {})       # the runtime correlator swallows `running`: this is the final answer
        if body.get("status") == "error":
            err = body.get("error") or {}
            raise EngineError(str(err.get("code", "")), str((err.get("details") or {}).get("reason", "")), str(err.get("message", "")))
        return body.get("payload") or {}

    async def read(self, matter_id: str) -> Tuple[Dict[str, Any], int]:
        """The content of a test Matter is its substance: one JSON document."""
        payload = await self.call("matter.read", {"matter_id": matter_id, "read_mode": "data|brique"})
        text = (payload.get("data") or {}).get("bytes")
        return (json.loads(text) if isinstance(text, str) and text.strip() else {}), int((payload.get("brique") or {}).get("revision", 0))

    async def update(self, matter_id: str, fn: Callable[[Dict[str, Any]], Tuple[Optional[Dict[str, Any]], Any]]) -> Any:
        """Read, recompute, write guarded by expected_revision; retry from a fresh read on conflict."""
        for _ in range(RETRIES):
            content, revision = await self.read(matter_id)
            new, result = fn(json.loads(json.dumps(content)))
            if new is None:
                return result
            try:
                await self.call("matter.write", {"matter_id": matter_id, "data": json.dumps(new, ensure_ascii=False, indent=1), "expected_revision": str(revision)})
            except EngineError as exc:
                if exc.reason == "revision_mismatch":
                    await asyncio.sleep(0.005)
                    continue
                raise
            return result
        raise RuntimeError(f"too many revision conflicts on {matter_id}")


async def record_tool_call(port: Port, tool: str, params: Any, state: str = "start") -> int:
    def fn(f):
        calls = f.setdefault("calls", [])
        rank = 1 + sum(1 for c in calls if c["tool"] == tool and c["state"] == "start")
        calls.append({"tool": tool, "state": state, "params": params, "call_index": rank, "at": now()})
        return f, rank
    return await port.update("tool_calls", fn)
