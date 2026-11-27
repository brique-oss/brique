"""Native tools through the real engine path /root/llm/infer, on Anthropic (MCP realization plan, WP6 gate).

One tool call, its result, the final structured answer, then a turn with tool_choice none over the same
history (what a compaction sends). Haiku, a few hundred tokens per inference. Run with BRIQUE_REAL_LLM=1.
The judgement is structural: a tool call of the declared tool, then a structured answer, then no tool call.
"""
import json
import unittest

from ..integration.driver import Driver
from .harness import REAL, SKIP_REASON

MODEL = "claude-haiku-4-5-20251001"
SCHEMA = {"type": "object", "properties": {"answer": {"type": "string"}}, "required": ["answer"], "additionalProperties": False}
TOOL = {"name": "weather__get", "description": "Current weather of a city.",
        "input_schema": {"type": "object", "properties": {"city": {"type": "string"}}, "required": ["city"]}}


def request(messages, **extra):
    out = {"provider": "anthropic", "model": MODEL, "parameters": {"max_output_tokens": 400, "temperature": 0},
           "output": {"format": "json_schema", "schema": SCHEMA}, "tools": [TOOL], "messages": messages}
    out.update(extra)
    return out


@unittest.skipUnless(REAL, SKIP_REASON)
class NativeToolsOnAnthropic(unittest.IsolatedAsyncioTestCase):

    async def infer(self, d, req):
        return (await d.call("/root/llm", "infer", "user", {"provider": "anthropic", "request": req}, timeout=120))["response"]

    async def test_a_tool_call_its_result_and_the_final_answer(self):
        async with Driver() as d:
            history = [{"role": "system", "content": "Answer with the JSON schema. Use the tool when you need data.", "cache": True},
                       {"role": "user", "content": "What is the weather in Paris now?"}]
            first = await self.infer(d, request(history))
            calls = first["output"].get("tool_calls") or []
            self.assertEqual(first["finish_reason"], "tool_use")
            self.assertTrue(calls and all(c["name"] == "weather__get" for c in calls), first["output"])
            self.assertIsNone(first["output"]["content"])

            history += [{"role": "assistant", "content": first["output"]["assistant_content"]},
                        {"role": "user", "content": [{"type": "tool_result", "call_id": c["id"], "content": "Paris: 14 C, light rain"} for c in calls]}]
            second = await self.infer(d, request(history))
            self.assertNotIn("tool_calls", second["output"])
            self.assertIsInstance(second["output"]["content"], dict)
            self.assertIn("answer", second["output"]["content"])

            third = await self.infer(d, request(history + [{"role": "assistant", "content": json.dumps(second["output"]["content"])},
                                                           {"role": "user", "content": "Summarize this exchange."}], tool_choice="none"))
            self.assertNotIn("tool_calls", third["output"])
            self.assertIsInstance(third["output"]["content"], dict)


if __name__ == "__main__":
    unittest.main()
