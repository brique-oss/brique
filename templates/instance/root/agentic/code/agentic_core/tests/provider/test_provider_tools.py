"""Native tools in the canonical LLM contract (MCP realization plan, WP6).

The Anthropic adapter, qualified on real requests (root/mcp/document/provider_qualification.md), maps tools,
tool calls, tool results and opaque provider blocks both ways; the three other adapters refuse native tools
explicitly. The provider shapes used here are those the qualification probes recorded. No network, no key.
"""
import copy
import importlib.util
import os
import sys
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
AGENT_CORE = os.path.dirname(os.path.dirname(HERE))
sys.path.insert(0, AGENT_CORE)
LLM_DIR = os.path.normpath(os.path.join(AGENT_CORE, "..", "..", "..", "llm", "code", "llm_llm"))

import pilot_activate  # noqa: E402,F401  (puts the wrapper library on the path, as the adapters need it)


def module(name):
    if LLM_DIR not in sys.path:
        sys.path.insert(0, LLM_DIR)
    spec = importlib.util.spec_from_file_location(name, os.path.join(LLM_DIR, f"{name}.py"))
    loaded = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(loaded)
    return loaded


anthropic = module("infer_anthropic")
tokens = module("tokens_count")
from wrapper.calls import CapacityCallError  # noqa: E402  (on the path once the adapters are loaded)

SCHEMA = {"type": "object", "properties": {"answer": {"type": "string"}}, "required": ["answer"], "additionalProperties": False}
TOOL = {"name": "crm__customer_read", "description": "Read a customer.",
        "input_schema": {"type": "object", "properties": {"customer_id": {"type": "string"}}, "required": ["customer_id"]}}
THINKING = {"type": "thinking", "thinking": "I need the customer.", "signature": "sig=="}


def request(messages, **extra):
    base = {"provider": "anthropic", "model": "claude-haiku-4-5", "parameters": {"max_output_tokens": 512},
            "output": {"format": "json_schema", "schema": SCHEMA}, "tools": [TOOL], "messages": messages}
    base.update(extra)
    return base


def turn_with_call(*ids):
    return {"role": "assistant", "content": [{"type": "provider_block", "provider": "anthropic", "block": THINKING}]
            + [{"type": "tool_call", "id": i, "name": "crm__customer_read", "arguments": {"customer_id": "c" + i}} for i in ids]}


def results(*ids, error=False):
    return [{"type": "tool_result", "call_id": i, "content": "customer " + i, "is_error": error} for i in ids]


class AnthropicRequest(unittest.TestCase):

    def test_tools_calls_results_and_provider_blocks_take_the_messages_api_form(self):
        body = anthropic._adapt_request(request([
            {"role": "system", "content": "FRAME", "cache": True},
            {"role": "user", "content": "find customer 1"},
            turn_with_call("t1"),
            {"role": "user", "content": results("t1", error=True) + [{"type": "text", "text": "go on"}], "cache": True},
        ]))
        self.assertEqual(body["tools"], [TOOL])
        self.assertNotIn("tool_choice", body)
        self.assertEqual(body["output_config"], {"format": {"type": "json_schema", "schema": SCHEMA}})
        assistant, last = body["messages"][1], body["messages"][2]
        self.assertEqual(assistant["content"][0], THINKING)                       # returned unchanged
        self.assertEqual(assistant["content"][1], {"type": "tool_use", "id": "t1", "name": "crm__customer_read", "input": {"customer_id": "ct1"}})
        self.assertEqual(last["content"][0], {"type": "tool_result", "tool_use_id": "t1", "content": "customer t1", "is_error": True})
        self.assertEqual(last["content"][1], {"type": "text", "text": "go on", "cache_control": {"type": "ephemeral"}})

    def test_tool_choice_none_keeps_the_tools_and_the_history(self):
        body = anthropic._adapt_request(request([{"role": "user", "content": "x"}, turn_with_call("t1"),
                                                  {"role": "user", "content": results("t1")}], tool_choice="none"))
        self.assertEqual(body["tool_choice"], {"type": "none"})
        self.assertEqual(body["tools"], [TOOL])
        with self.assertRaises(ValueError):
            anthropic._adapt_request(request([{"role": "user", "content": "x"}], tool_choice="any"))

    def test_a_history_with_tool_blocks_and_no_tools_is_accepted(self):
        body = anthropic._adapt_request(request([{"role": "user", "content": "x"}, turn_with_call("t1"),
                                                  {"role": "user", "content": results("t1")}], tools=[]))
        self.assertNotIn("tools", body)
        self.assertEqual(body["messages"][1]["content"][1]["type"], "tool_use")

    def test_results_may_be_split_and_reordered(self):
        anthropic._adapt_request(request([{"role": "user", "content": "x"}, turn_with_call("a", "b"),
                                          {"role": "user", "content": results("b")}, {"role": "user", "content": results("a")}]))

    def test_the_pairing_rule_is_enforced_before_anything_is_sent(self):
        cases = {
            "a result is missing": [turn_with_call("a", "b"), {"role": "user", "content": results("a")}],
            "text before the result": [turn_with_call("a"), {"role": "user", "content": [{"type": "text", "text": "x"}] + results("a")}],
            "unknown call": [turn_with_call("a"), {"role": "user", "content": results("a", "z")}],
            "answered twice": [turn_with_call("a"), {"role": "user", "content": results("a")}, {"role": "user", "content": results("a")}],
            "not immediately": [turn_with_call("a"), {"role": "user", "content": "plain text"}],
            "no result at the end": [turn_with_call("a")],
            "duplicate call ids": [turn_with_call("a", "a"), {"role": "user", "content": results("a")}],
        }
        for name, messages in cases.items():
            with self.subTest(name), self.assertRaises(ValueError):
                anthropic._adapt_request(request([{"role": "user", "content": "x"}] + messages))

    def test_malformed_tools_and_foreign_blocks_are_refused(self):
        for tools in ([{"name": "a"}], [TOOL, TOOL], "x"):
            with self.subTest(tools=tools), self.assertRaises(ValueError):
                anthropic._adapt_request(request([{"role": "user", "content": "x"}], tools=tools))
        foreign = {"role": "assistant", "content": [{"type": "provider_block", "provider": "openai", "block": {}}]}
        with self.assertRaises(ValueError):
            anthropic._adapt_request(request([{"role": "user", "content": "x"}, foreign, {"role": "user", "content": "y"}]))

    def test_the_token_count_includes_the_tools(self):
        body = tokens._body_of(anthropic._adapt_request(request([{"role": "user", "content": "x"}], tool_choice="none")))
        self.assertEqual(body["tools"], [TOOL])
        self.assertEqual(body["tool_choice"], {"type": "none"})
        self.assertNotIn("max_tokens", body)


class AnthropicResponse(unittest.TestCase):

    def normalize(self, content, stop_reason="end_turn", output_format="json_schema"):
        payload = {"content": content, "stop_reason": stop_reason, "model": "m", "usage": {"input_tokens": 10, "output_tokens": 3}}
        return anthropic._normalize_response(payload, 5, output_format, True)

    def test_a_tool_call_is_captured_with_the_exact_turn_to_send_back(self):
        out = self.normalize([THINKING, {"type": "tool_use", "id": "toolu_1", "name": "crm__customer_read", "input": {"customer_id": "1"}},
                              {"type": "tool_use", "id": "toolu_2", "name": "crm__customer_read", "input": {"customer_id": "2"}}], "tool_use")
        output = out["output"]
        self.assertIsNone(output["content"])
        self.assertEqual([c["id"] for c in output["tool_calls"]], ["toolu_1", "toolu_2"])
        self.assertEqual(output["tool_calls"][0]["arguments"], {"customer_id": "1"})
        self.assertEqual(out["finish_reason"], "tool_use")
        # the turn goes back unchanged: same ids, thinking block untouched
        body = anthropic._adapt_request(request([{"role": "user", "content": "x"},
                                                  {"role": "assistant", "content": output["assistant_content"]},
                                                  {"role": "user", "content": results("toolu_1", "toolu_2")}]))
        sent = body["messages"][1]["content"]
        self.assertEqual(sent[0], THINKING)
        self.assertEqual([b["id"] for b in sent[1:]], ["toolu_1", "toolu_2"])

    def test_the_final_answer_is_the_structured_content(self):
        out = self.normalize([{"type": "text", "text": '{"answer":"done"}'}])
        self.assertEqual(out["output"]["content"], {"answer": "done"})
        self.assertNotIn("tool_calls", out["output"])
        self.assertEqual(out["usage"]["total_tokens"], 13)

    def test_an_answer_with_neither_text_nor_call_is_an_error_never_none(self):
        for content in ([], [THINKING]):
            with self.subTest(content=content), self.assertRaises(CapacityCallError) as caught:
                self.normalize(content)
            self.assertEqual(caught.exception.details["reason"], "empty_output")

    def test_an_unsupported_block_and_a_truncated_call_are_errors(self):
        with self.assertRaises(CapacityCallError) as caught:
            self.normalize([{"type": "server_tool_use", "id": "x"}])
        self.assertEqual(caught.exception.details["reason"], "unsupported_block")
        with self.assertRaises(CapacityCallError) as caught:
            self.normalize([{"type": "tool_use", "id": "t", "name": "n", "input": {}}], "max_tokens")
        self.assertEqual(caught.exception.details["reason"], "output_truncated")

    def test_without_tools_the_former_normalization_is_kept(self):
        payload = {"content": [], "stop_reason": "end_turn", "model": "m", "usage": {}}
        self.assertIsNone(anthropic._normalize_response(copy.deepcopy(payload), 1, "json_schema")["output"]["content"])


class UnqualifiedProviders(unittest.TestCase):

    def test_they_refuse_native_tools_explicitly(self):
        history = [{"role": "user", "content": "x"}, turn_with_call("a"), {"role": "user", "content": results("a")}]
        for provider in ("openai", "mistral", "scaleway"):
            adapter = module(f"infer_{provider}")
            for req in (request([{"role": "user", "content": "x"}]), request(history, tools=[])):
                with self.subTest(provider=provider), self.assertRaises(CapacityCallError) as caught:
                    adapter._adapt_request(dict(req, provider=provider))
                self.assertEqual(caught.exception.details["reason"], "tools_not_supported")
            adapter._adapt_request({"provider": provider, "model": "m", "parameters": {"max_output_tokens": 8},
                                    "messages": [{"role": "user", "content": "x"}], "output": {"format": "text"}})


if __name__ == "__main__":
    unittest.main()
