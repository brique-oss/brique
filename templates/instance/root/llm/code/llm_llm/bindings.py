from __future__ import annotations

import functools
import importlib.util
from pathlib import Path
from typing import Any

THIS_DIR = Path(__file__).resolve().parent

# The provider API keys are secrets of Brique Sec, never files of the instance: a value in the security folder of the machine
# (`brique-sec secret add anthropic`), a right in the signed policy (`brique-sec secret allow anthropic llm_llm`), a need in context.json
# (run.secrets). The engine puts the value in os.environ when it launches the wrapper, so every provider capacity below reads its credential
# from os.environ, never from a value written in this file or in context.json.


def _load(rel_path: str, symbol: str):
    p = THIS_DIR / rel_path
    spec = importlib.util.spec_from_file_location(p.stem, p)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return getattr(module, symbol)


infer_openai = _load("infer_openai.py", "infer_openai")
infer_anthropic = _load("infer_anthropic.py", "infer_anthropic")
infer_mistral = _load("infer_mistral.py", "infer_mistral")
infer_scaleway = _load("infer_scaleway.py", "infer_scaleway")
tokens_count = _load("tokens_count.py", "tokens_count")


def recorded(capacity):
    """An unexpected failure reaches the caller as a generic one: its real reason is kept in the working directory of the wrapper."""
    import json, os, time, traceback

    @functools.wraps(capacity)
    async def run(runtime, params):
        try:
            return await capacity(runtime, params)
        except Exception as exc:
            try:
                line = {"at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), "capacity": getattr(capacity, "__name__", "capacity"),
                        "type": type(exc).__name__, "error": str(exc)[:2000], "traceback": traceback.format_exc()[-1500:]}
                with open(os.path.join(os.environ.get("BRIQUE_WORKDIR") or ".", "unexpected_errors.jsonl"), "a", encoding="utf-8") as handle:
                    handle.write(json.dumps(line, ensure_ascii=False) + "\n")
            except Exception:
                pass
            raise
    return run


def build_bindings(runtime) -> dict[str, Any]:
    bind = lambda capacity, rt: functools.partial(recorded(capacity), rt)        # noqa: E731  each capacity receives the runtime first
    return {
        "capacities": {
            ("", "infer.openai"): bind(infer_openai, runtime),
            ("", "infer.anthropic"): bind(infer_anthropic, runtime),
            ("", "infer.mistral"): bind(infer_mistral, runtime),
            ("", "infer.scaleway"): bind(infer_scaleway, runtime),
            ("", "tokens.count"): bind(tokens_count, runtime),
        },
        "matters": {},
        "shutdown": [],
    }
