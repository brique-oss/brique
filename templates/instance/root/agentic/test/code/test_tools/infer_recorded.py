from __future__ import annotations

import json
import sys
import time

from support import EngineError, Port, now

CAP = "infer.recorded"
RECORD = "real_calls"
SHARDS = [RECORD] + [f"{RECORD}_{i}" for i in range(1, 8)]     # a Matter read inline stops at 256 KiB: the journal goes by slices
SHARD_LIMIT = 120_000                                          # bytes; one entry is about 20 KB, so a slice never nears the inline limit
TIMEOUT_S = 180.0


# <brique:capacity name="infer.recorded">
async def infer_recorded(runtime, params: dict) -> dict:
    """Forwards the canonical request to the real provider named by request.upstream_provider (default
    anthropic) and records what was sent and received. The pilot sees an ordinary provider answer."""
    port = Port(runtime, CAP)
    request = dict(await params.value("request") or {})        # a large request arrives as a transit reference
    upstream = str((request.get("parameters") or {}).get("upstream_provider") or "anthropic")
    forwarded = dict(request, provider=upstream)
    forwarded["parameters"] = {k: v for k, v in (request.get("parameters") or {}).items() if k != "upstream_provider"}
    forwarded["requirements"] = dict(request.get("requirements") or {}, provider=upstream)

    started = time.monotonic()
    answer, error = None, None
    try:
        answer = await port.call("infer", {"request": forwarded, "provider": upstream}, to_type="user", context="/root/llm", timeout_s=TIMEOUT_S)
    except EngineError as exc:
        error = str(exc)
    elapsed_ms = int((time.monotonic() - started) * 1000)

    response = (answer or {}).get("response") or {}
    entry = {
        "at": now(), "model": forwarded.get("model"), "provider": upstream, "elapsed_ms": elapsed_ms,
        "request": forwarded, "response": response or None, "error": error,
        "usage": response.get("usage") or {}, "finish_reason": response.get("finish_reason"),
    }

    def append(f):
        calls = f.setdefault("calls", [])
        if len(json.dumps(f, ensure_ascii=False)) > SHARD_LIMIT:
            return None, None                                   # this slice is full: the next one takes the entry
        entry["n"] = len(calls) + 1
        calls.append(entry)
        return f, entry["n"]
    try:
        for shard in SHARDS:
            if await port.update(shard, append) is not None:
                break
        else:
            raise RuntimeError("every slice of the real_calls journal is full")
    except Exception as exc:                                    # recording is instrumentation: the answer already paid for is never lost
        print(f"infer.recorded: the call could not be recorded: {exc}", file=sys.stderr, flush=True)
    if error is not None:
        raise RuntimeError(error)
    return answer
# </brique:capacity>
