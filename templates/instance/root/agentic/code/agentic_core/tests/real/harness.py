"""Harness of the real-LLM tests.

A real model drives a real pilot (real_pilot, provider "recorded" -> Anthropic) over the real engine,
with the deterministic fake shop world as tools. Nothing here asserts on the model's wording: a run is
judged on observable facts (which capacities were invoked with which parameters, what the final
message contains, how many inferences it took).
"""
from __future__ import annotations

import json
import os
import re
import time
from typing import Any, Callable, Dict, List, Optional

from ..integration.driver import AGENTIC, TEST, Driver, DriverError

REAL = os.environ.get("BRIQUE_REAL_LLM") == "1"
SKIP_REASON = "real-LLM tests spend tokens: set BRIQUE_REAL_LLM=1 (the engine, the agentic and test contexts must be running)"
REAL_CALL_SHARDS = ["real_calls"] + [f"real_calls_{i}" for i in range(1, 8)]
PILOT_ID = "real_pilot"
PILOT_REF = {"context": TEST, "matter_id": PILOT_ID}
RUNS = int(os.environ.get("BRIQUE_REAL_RUNS", "1"))
QUIET_TIMEOUT = float(os.environ.get("BRIQUE_REAL_TIMEOUT", "120"))
REPORT_PATH = os.environ.get("BRIQUE_REAL_REPORT", os.path.join(os.path.dirname(os.path.abspath(__file__)), "last_report.json"))


class RealDriver(Driver):
    async def reset_world(self) -> None:           # the model is real: no script, only fresh records
        for shard in REAL_CALL_SHARDS:
            await self.write(TEST, shard, {"calls": []})
        await self.write(TEST, "tool_calls", {"calls": []})
        await self.write(TEST, "gates", {"open": []})

    async def create(self, cid: str, *, intention: Any = "Assist the customer of the online shop.", parent: Optional[str] = None,
                     pilot_ref: Optional[Dict[str, Any]] = None) -> None:
        await self.call(AGENTIC, "continuity.create", "user", {"continuity_id": cid, "pilot_ref": pilot_ref or PILOT_REF, "initial_intention": intention})
        self.created.append(cid)

    async def real_calls(self) -> List[Dict[str, Any]]:
        calls: List[Dict[str, Any]] = []
        for shard in REAL_CALL_SHARDS:                                   # the journal goes by slices (see infer.recorded); n is renumbered here
            calls.extend((await self.content(TEST, shard)).get("calls", []))
        for number, call in enumerate(calls, 1):
            call["n"] = number
        return calls


def pilot_texts(journal: List[Dict[str, Any]]) -> List[str]:
    out = []
    for m in journal:
        if m["kind"] != "pilot":
            continue
        content = m.get("content")
        if isinstance(content, dict):
            content = content.get("content", content)
        out.append(content if isinstance(content, str) else json.dumps(content, ensure_ascii=False))
    return out


def contains_any(text: str, needles: List[str]) -> bool:
    low = text.lower()
    return any(n.lower() in low for n in needles)


class Run:
    """What one run of a scenario produced, and the checks it was judged on."""

    def __init__(self, scenario: str, index: int) -> None:
        self.scenario, self.index = scenario, index
        self.checks: Dict[str, bool] = {}
        self.facts: Dict[str, Any] = {}

    def check(self, name: str, ok: Any) -> None:
        self.checks[name] = bool(ok)

    @property
    def passed(self) -> bool:
        return all(self.checks.values())

    def as_dict(self) -> Dict[str, Any]:
        return {"scenario": self.scenario, "run": self.index, "passed": self.passed, "checks": self.checks, "facts": self.facts}


async def measure(d: RealDriver, run: Run, cid: str, *, first_call_index: int = 0) -> None:
    """Facts common to every scenario: inferences, tokens, validation errors, tool calls, final state."""
    calls = (await d.real_calls())[first_call_index:]
    journal = await d.journal(cid)
    tools = await d.tool_calls()
    rt = await d.runtime(cid)
    errors = [m for m in journal if m["kind"] == "error"]
    usage = [c.get("usage") or {} for c in calls]
    run.facts.update({
        "inferences": len(calls),
        "provider_errors": [c["error"] for c in calls if c.get("error")],
        "input_tokens": sum(u.get("input_tokens", 0) for u in usage),
        "output_tokens": sum(u.get("output_tokens", 0) for u in usage),
        "elapsed_ms": sum(c.get("elapsed_ms", 0) for c in calls),
        "validation_errors": [m["content"] for m in errors if isinstance(m.get("content"), dict) and m["content"].get("event") == "validation_error"],
        "error_messages": len(errors),
        "tool_calls": [{"tool": t["tool"], "params": t["params"]} for t in tools if t["state"] == "done"],
        "pilot_texts": pilot_texts(journal),
        "kinds": [m["kind"] for m in journal if not (m["kind"] == "event" and (m.get("content") or {}).get("event") == "inference_measured")],
        "steps": [m["content"].get("step") for m in journal if m["kind"] == "prompt"],
        "runtime": {"status": rt.get("status"), "wait_groups": rt.get("active_wait_groups")},
        "models": sorted({(c.get("response") or {}).get("resolved", {}).get("model") for c in calls if c.get("response")} - {None}),
    })
    run.check("quiet_and_idle", rt.get("status") == "idle" and not rt.get("active_wait_groups"))
    run.check("no_provider_error", not run.facts["provider_errors"])
    run.check("first_try_valid", not run.facts["validation_errors"])


def summarize(runs: List[Run]) -> Dict[str, Any]:
    by: Dict[str, List[Run]] = {}
    for r in runs:
        by.setdefault(r.scenario, []).append(r)
    out = {}
    for name, rs in by.items():
        checks = sorted({c for r in rs for c in r.checks})
        out[name] = {
            "runs": len(rs), "passed": sum(1 for r in rs if r.passed),
            "checks": {c: f"{sum(1 for r in rs if r.checks.get(c))}/{len(rs)}" for c in checks},
            "inferences": [r.facts.get("inferences") for r in rs],
            "tokens": sum(r.facts.get("input_tokens", 0) + r.facts.get("output_tokens", 0) for r in rs),
        }
    return out


def write_report(runs: List[Run]) -> None:
    body = {"at": time.strftime("%Y-%m-%dT%H:%M:%S"), "summary": summarize(runs), "runs": [r.as_dict() for r in runs]}
    with open(REPORT_PATH, "w", encoding="utf-8") as fh:
        json.dump(body, fh, ensure_ascii=False, indent=1)
