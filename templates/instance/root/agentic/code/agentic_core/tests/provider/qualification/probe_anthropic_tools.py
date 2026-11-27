"""Qualification probes of Anthropic native tool use, for the MCP realization (plan WP0.2).

Not a unit test: it sends about thirty small requests to the real API and records what the provider accepts and refuses. It needs
the operator's approval to spend tokens and takes the key from the environment (ANTHROPIC_API_KEY), never from an argument. Nothing
secret is printed or recorded: only status codes, error messages, stop reasons, and block types.

    ANTHROPIC_API_KEY=... python3 probe_anthropic_tools.py <results.json>

The answers feed root/mcp/document/provider_qualification.md.
"""
from __future__ import annotations

import json
import os
import sys
from pathlib import Path

import httpx

sys.path.insert(0, str(Path(__file__).resolve().parents[3]))      # agentic_core: to build the real pilot_output schema
from substrate.schemas import provider_schema                      # noqa: E402

URL = "https://api.anthropic.com/v1/messages"
MODEL = os.environ.get("PROBE_MODEL", "claude-haiku-4-5-20251001")
KEY = os.environ.get("ANTHROPIC_API_KEY", "")

WEATHER = {"name": "get_weather", "description": "Current weather of a city.",
           "input_schema": {"type": "object", "properties": {"city": {"type": "string"}}, "required": ["city"]}}
PILOT_OUTPUT = provider_schema("pilot_output", style="optional", describe_depth=0)      # the schema a Matter pilot sends to Anthropic
RESULTS = []


def post(body):
    headers = {"x-api-key": KEY, "anthropic-version": "2023-06-01", "content-type": "application/json"}
    try:
        response = httpx.post(URL, headers=headers, json=body, timeout=90)
    except Exception as exc:                                       # a transport failure is recorded, not hidden
        return {"status": None, "error": f"{type(exc).__name__}"}
    data = response.json() if response.headers.get("content-type", "").startswith("application/json") else {}
    out = {"status": response.status_code}
    if response.status_code >= 400:
        error = data.get("error") or {}
        out["error"] = f"{error.get('type')}: {str(error.get('message'))[:240]}"
        return out
    blocks = data.get("content") or []
    out.update(stop_reason=data.get("stop_reason"), blocks=[b.get("type") for b in blocks],
               tool_uses=[{"id": b["id"], "name": b["name"], "input": b["input"]} for b in blocks if b.get("type") == "tool_use"],
               text=" ".join(b.get("text", "") for b in blocks if b.get("type") == "text")[:160],
               usage={k: (data.get("usage") or {}).get(k) for k in ("input_tokens", "output_tokens")})
    out["_content"] = blocks                                       # kept in memory for the next turn, removed before saving
    return out


def probe(pid, question, body):
    result = post(dict({"model": MODEL, "max_tokens": 300}, **body))
    record = {"id": pid, "question": question, "result": {k: v for k, v in result.items() if not k.startswith("_")}}
    RESULTS.append(record)
    summary = result.get("error") or f"{result.get('stop_reason')} {result.get('blocks')}"
    print(f"{pid:4} {str(result.get('status')):5} {summary[:150]}")
    return result


def user(text):
    return {"role": "user", "content": text}


def tool_result(tool_use_id, text, **extra):
    return dict({"type": "tool_result", "tool_use_id": tool_use_id, "content": text}, **extra)


def main(out_path):
    if not KEY:
        sys.exit("ANTHROPIC_API_KEY is not set")
    ask_paris = [user("What is the weather in Paris? Use the tool.")]

    # A: tools together with the strict structured output of a Matter pilot
    a1 = probe("A1", "tools + pilot_output json_schema, prompt that needs the tool: tool call or JSON text?",
               {"tools": [WEATHER], "output_config": {"format": {"type": "json_schema", "schema": PILOT_OUTPUT}}, "messages": ask_paris})
    probe("A2", "tools + pilot_output json_schema, prompt that does not need a tool: final JSON?",
          {"tools": [WEATHER], "output_config": {"format": {"type": "json_schema", "schema": PILOT_OUTPUT}}, "messages": [user("Reply with an empty decision. Do not use any tool.")]})
    probe("A3", "tool with strict:true + pilot_output json_schema: accepted? (union-parameter budget shared)",
          {"tools": [dict(WEATHER, strict=True, input_schema={"type": "object", "properties": {"city": {"type": "string"}}, "required": ["city"], "additionalProperties": False})],
           "output_config": {"format": {"type": "json_schema", "schema": PILOT_OUTPUT}}, "messages": ask_paris})

    # B: a history that holds tool blocks, with and without the tools parameter
    first = probe("B0", "plain turn that returns a tool call (no output schema)", {"tools": [WEATHER], "messages": ask_paris})
    tu = (first.get("tool_uses") or [{}])[0]
    if tu.get("id"):
        history = ask_paris + [{"role": "assistant", "content": first["_content"]}, {"role": "user", "content": [tool_result(tu["id"], "Sunny, 21 C")]}]
        probe("B1", "history with tool_use and tool_result, tools parameter ABSENT", {"messages": history})
        probe("B2", "same history with tools present (control)", {"tools": [WEATHER], "messages": history})
        probe("F1", "tool_choice none with tools and a history of tool blocks", {"tools": [WEATHER], "tool_choice": {"type": "none"}, "messages": history})
        probe("G1", "history names a tool that is no longer in tools (projection changed)",
              {"tools": [dict(WEATHER, name="get_time")], "messages": history})
        probe("H3", "user text BEFORE the tool_result in the same user message",
              {"tools": [WEATHER], "messages": ask_paris + [{"role": "assistant", "content": first["_content"]},
                                                           {"role": "user", "content": [{"type": "text", "text": "Thanks."}, tool_result(tu["id"], "Sunny, 21 C")]}]})
        probe("H4", "tool_result with an unknown tool_use_id",
              {"tools": [WEATHER], "messages": ask_paris + [{"role": "assistant", "content": first["_content"]},
                                                           {"role": "user", "content": [tool_result("toolu_unknown", "x")]}]})
        probe("H5", "tool_result flagged is_error with a bounded message",
              {"tools": [WEATHER], "messages": ask_paris + [{"role": "assistant", "content": first["_content"]},
                                                           {"role": "user", "content": [tool_result(tu["id"], "not_executed: the call was refused before transmission", is_error=True)]}]})

    # C: tool names
    names = {"dot": "customer.read", "double_underscore": "crm__customer_read", "len64": "a" * 64, "len65": "a" * 65,
             "len128": "a" * 128, "len129": "a" * 129, "accent": "météo", "space": "get weather"}
    for label, name in names.items():
        probe("C-" + label, f"tool name {label!r} ({len(name)} chars)", {"max_tokens": 16, "tools": [dict(WEATHER, name=name)], "messages": [user("Hi")]})

    # D: parallel calls and the shape of the answers
    two = probe("D1", "two independent cities: parallel tool_use blocks in one assistant turn?",
                {"tools": [WEATHER], "messages": [user("Get the weather in Paris and in Tokyo. Call the tool for both cities in the same turn.")]})
    uses = two.get("tool_uses") or []
    if len(uses) >= 2:
        base = [user("Get the weather in Paris and in Tokyo. Call the tool for both cities in the same turn."), {"role": "assistant", "content": two["_content"]}]
        a, b = uses[0]["id"], uses[1]["id"]
        probe("D2", "both results in one user message, in REVERSED order", {"tools": [WEATHER], "messages": base + [{"role": "user", "content": [tool_result(b, "Rain"), tool_result(a, "Sun")]}]})
        probe("D3", "only ONE of the two results provided", {"tools": [WEATHER], "messages": base + [{"role": "user", "content": [tool_result(a, "Sun")]}]})
        probe("D4", "the same tool_use_id answered twice", {"tools": [WEATHER], "messages": base + [{"role": "user", "content": [tool_result(a, "Sun"), tool_result(a, "Sun"), tool_result(b, "Rain")]}]})
        probe("D5", "results in two consecutive user messages", {"tools": [WEATHER], "messages": base + [{"role": "user", "content": [tool_result(a, "Sun")]}, {"role": "user", "content": [tool_result(b, "Rain")]}]})

    # E: extended thinking with tools
    thinking = {"type": "enabled", "budget_tokens": 1024}
    t1 = probe("E1", "extended thinking + tools: thinking block then tool_use?", {"max_tokens": 2048, "thinking": thinking, "tools": [WEATHER], "messages": ask_paris})
    tt = (t1.get("tool_uses") or [{}])[0]
    if tt.get("id"):
        blocks = t1["_content"]
        without = [b for b in blocks if b.get("type") not in ("thinking", "redacted_thinking")]
        probe("E2", "continue with the thinking block returned unmodified", {"max_tokens": 2048, "thinking": thinking, "tools": [WEATHER],
              "messages": ask_paris + [{"role": "assistant", "content": blocks}, {"role": "user", "content": [tool_result(tt["id"], "Sunny, 21 C")]}]})
        probe("E3", "continue WITHOUT the thinking block", {"max_tokens": 2048, "thinking": thinking, "tools": [WEATHER],
              "messages": ask_paris + [{"role": "assistant", "content": without}, {"role": "user", "content": [tool_result(tt["id"], "Sunny, 21 C")]}]})
    probe("E4", "extended thinking + tools + pilot_output json_schema together",
          {"max_tokens": 2048, "thinking": thinking, "tools": [WEATHER], "output_config": {"format": {"type": "json_schema", "schema": PILOT_OUTPUT}}, "messages": ask_paris})
    probe("E5", "tool_choice none + extended thinking", {"max_tokens": 2048, "thinking": thinking, "tools": [WEATHER], "tool_choice": {"type": "none"}, "messages": ask_paris})

    # A1 follow-up: the turn after a tool call under the structured output constraint
    if (a1.get("tool_uses") or [{}])[0].get("id"):
        use = a1["tool_uses"][0]
        probe("A4", "after a tool call under pilot_output json_schema: result turn returns the final JSON?",
              {"tools": [WEATHER], "output_config": {"format": {"type": "json_schema", "schema": PILOT_OUTPUT}},
               "messages": ask_paris + [{"role": "assistant", "content": a1["_content"]}, {"role": "user", "content": [tool_result(use["id"], "Sunny, 21 C")]}]})

    Path(out_path).write_text(json.dumps({"model": MODEL, "results": RESULTS}, indent=1, ensure_ascii=False), encoding="utf-8")
    print("saved", out_path)


if __name__ == "__main__":
    if len(sys.argv) != 2:
        sys.exit("usage: probe_anthropic_tools.py <results.json>")
    main(sys.argv[1])
