"""Non-regression oracle of the provider adapters (MCP realization plan, WP6.0).

Before the adapters learn native tools, this records exactly what each of the four adapters produces today for a corpus of canonical
requests (the requests that pilot.activate and pilot.infer really build, and the edge cases of each option), and how each normalizes
typical provider answers. A request without tools must keep producing the same bytes after the tool work: the comparison is made on
the JSON text, so a change of key order is a change. An error is part of the behavior and is recorded too.

No network, no key. To record the fixtures again after a deliberate change, run with BRIQUE_RECORD_PROVIDER_FIXTURES=1 and review the
diff of fixtures/provider_adapters.json.
"""
import copy
import importlib.util
import json
import os
import sys
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
AGENT_CORE = os.path.dirname(os.path.dirname(HERE))
sys.path.insert(0, AGENT_CORE)
LLM_DIR = os.path.normpath(os.path.join(AGENT_CORE, "..", "..", "..", "llm", "code", "llm_llm"))
FIXTURES = os.path.join(HERE, "fixtures", "provider_adapters.json")
RECORD = os.environ.get("BRIQUE_RECORD_PROVIDER_FIXTURES") == "1"

import pilot_activate  # noqa: E402

PROVIDERS = ("openai", "anthropic", "mistral", "scaleway")


def module(name):
    if LLM_DIR not in sys.path:
        sys.path.insert(0, LLM_DIR)
    path = os.path.join(LLM_DIR, f"{name}.py")
    spec = importlib.util.spec_from_file_location(name, path)
    loaded = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(loaded)
    return loaded


def text(value):
    """The JSON text of a value, in its own key order: what the HTTP client sends."""
    return json.dumps(value, ensure_ascii=False)


def outcome(function, *args):
    try:
        return {"ok": text(function(*copy.deepcopy(args)))}
    except Exception as exc:                      # an error is a behavior of the adapter
        return {"error": f"{type(exc).__name__}: {exc}"}


def line(seq, kind, content, **extra):
    return dict({"id": f"l{seq}", "seq": seq, "kind": kind, "content": content, "from": "x", "to": "c1", "correlation": {}}, **extra)


def pilot_of(provider):
    return {"prompt": "FIXTURE PILOT", "model": {"provider": provider, "model": "some-model"},
            "parameters": {"max_output_tokens": 1024, "temperature": 0}, "rules": ["be exact"],
            "known_capacities": [{"ref": "/root/agentic/test/test.echo", "description": "echo"}],
            "allowed_capacities": ["/root/agentic/test/*"], "forbidden_capacities": ["/root/llm/*"]}


CONTINUITY = {"id": "c1", "initial_intention": "goal", "current_intention": "goal"}
CURRENT = line(2, "user", "hello")
HISTORY = {"projection": {"definition": {}, "messages": [
    line(1, "framework", "FRAME TEXT"),
    CURRENT,
    line(3, "completion", {"step": "decision", "output": {"intentions": [{"id": "i1", "target_capacity": "/root/agentic/test/test.echo"}]}}),
    line(4, "capacity_intention", {"id": "i1", "target_capacity": "/root/agentic/test/test.echo", "parameters": {"x": 1}, "await_response": True}),
    line(5, "capacity_response", {"response": {"status": "ok", "payload": {"y": 2}}}, correlation={"intention_id": "i1"}),
    line(6, "continuity", {"note": "from another continuity"}, **{"from": "c0"}),
    line(7, "summary", "previous summary"),
    line(8, "event", {"event": "inference_measured"}),
]}}


def canonical_requests():
    """name -> (canonical request, applies to every provider)."""
    out = {}
    for provider in PROVIDERS:
        pilot = pilot_of(provider)
        out[f"matter_first_{provider}"] = pilot_activate.build_request(pilot, CONTINUITY, {"projection": {"messages": [], "definition": {}}}, CURRENT, None)
        out[f"matter_history_{provider}"] = pilot_activate.build_request(
            pilot, CONTINUITY, HISTORY, CURRENT, {"kind": "active_wait_result", "wait_group_id": "g1", "outcome": "satisfied", "received": 1, "expected": 1},
            user_messages=["m9"])
        out[f"matter_correction_{provider}"] = pilot_activate.build_request(pilot, CONTINUITY, HISTORY, CURRENT, None, correction={"errors": ["bad field"]})
        out[f"compaction_{provider}"] = pilot_activate.build_request(pilot, CONTINUITY, HISTORY, None, {"kind": "compaction", "instruction": pilot_activate.COMPACTION_GUIDE})
    schema = {"type": "object", "properties": {"answer": {"type": "string"}}, "required": ["answer"], "additionalProperties": False}
    base = {"provider": "x", "model": "some-model", "parameters": {"max_output_tokens": 256}, "requirements": {}}
    out["step_with_projection"] = dict(base, messages=[{"role": "system", "content": "FRAME", "cache": True}, {"role": "user", "content": "old"},
                                                       {"role": "assistant", "content": "{\"answer\":\"a\"}", "cache": True}, {"role": "user", "content": "NEW"}],
                                       output={"format": "json_schema", "schema": schema})
    out["step_plain"] = dict(base, messages=[{"role": "system", "content": "FRAME\n\nPROMPT", "cache": True}, {"role": "user", "content": "{\"step\":\"s\"}"}],
                             output={"format": "json_schema", "schema": schema})
    out["text_all_parameters"] = dict(base, messages=[{"role": "user", "content": "hi"}],
                                      parameters={"max_output_tokens": 64, "temperature": 0.5, "top_p": 0.9, "stop": ["END"]}, output={"format": "text"})
    out["text_no_output"] = dict(base, messages=[{"role": "system", "content": "S"}, {"role": "user", "content": "hi"}])
    out["reasoning_requirement"] = dict(base, messages=[{"role": "user", "content": "hi"}], requirements={"reasoning": "low"}, output={"format": "text"})
    out["json_schemaless"] = dict(base, messages=[{"role": "user", "content": "hi"}], output={"format": "json"})
    out["no_max_tokens"] = dict(base, messages=[{"role": "user", "content": "hi"}], parameters={}, output={"format": "text"})
    out["unknown_role"] = dict(base, messages=[{"role": "tool", "content": "x"}], output={"format": "text"})
    out["unknown_format"] = dict(base, messages=[{"role": "user", "content": "hi"}], output={"format": "xml"})
    return out


RESPONSES = {
    "anthropic": [
        ("json", {"content": [{"type": "text", "text": "{\"answer\":\"a\"}"}], "stop_reason": "end_turn", "model": "m",
                  "usage": {"input_tokens": 10, "output_tokens": 3, "cache_read_input_tokens": 100, "cache_creation_input_tokens": 5}}, "json_schema"),
        ("text", {"content": [{"type": "text", "text": "hello "}, {"type": "text", "text": "world"}], "stop_reason": "end_turn", "model": "m", "usage": {}}, "text"),
        ("truncated_json", {"content": [{"type": "text", "text": "{\"answer\":"}], "stop_reason": "max_tokens", "model": "m", "usage": {}}, "json_schema"),
        ("empty", {"content": [], "stop_reason": "end_turn", "model": "m", "usage": {}}, "json_schema"),
    ],
    "openai": [
        ("json", {"status": "completed", "model": "m", "output": [{"type": "message", "content": [{"type": "output_text", "text": "{\"answer\":\"a\"}"}]}],
                  "usage": {"input_tokens": 10, "output_tokens": 3, "total_tokens": 13}}, "json_schema"),
        ("incomplete", {"status": "incomplete", "incomplete_details": {"reason": "max_output_tokens"}, "model": "m",
                        "output": [{"type": "message", "content": [{"type": "output_text", "text": "partial"}]}], "usage": {}}, "text"),
        ("failed", {"status": "failed", "model": "m", "output": [], "usage": {}}, "text"),
    ],
    "mistral": [
        ("json", {"model": "m", "choices": [{"message": {"content": "{\"answer\":\"a\"}"}, "finish_reason": "stop"}], "usage": {"prompt_tokens": 10, "completion_tokens": 3}}, "json_schema"),
        ("chunks", {"model": "m", "choices": [{"message": {"content": [{"type": "text", "text": "hel"}, {"type": "text", "text": "lo"}]}, "finish_reason": "stop"}], "usage": {}}, "text"),
    ],
    "scaleway": [
        ("json", {"model": "m", "choices": [{"message": {"content": "{\"answer\":\"a\"}"}, "finish_reason": "stop"}], "usage": {"prompt_tokens": 10, "completion_tokens": 3}}, "json_schema"),
        ("text", {"model": "m", "choices": [{"message": {"content": "hello"}, "finish_reason": "length"}], "usage": {}}, "text"),
    ],
}


def observe():
    observed = {"requests": {}, "responses": {}, "token_count_bodies": {}}
    adapters = {p: module(f"infer_{p}") for p in PROVIDERS}
    tokens = module("tokens_count")
    for name, request in canonical_requests().items():
        for provider in PROVIDERS:
            if name.rsplit("_", 1)[-1] in PROVIDERS and not name.endswith("_" + provider):
                continue                          # a Matter pilot request is built for its own provider
            observed["requests"][f"{name}@{provider}"] = outcome(adapters[provider]._adapt_request, request)
        anthropic = observed["requests"].get(f"{name}@anthropic")
        if anthropic and "ok" in anthropic:
            observed["token_count_bodies"][name] = {"ok": text(tokens._body_of(json.loads(anthropic["ok"])))}
    for provider, cases in RESPONSES.items():
        for label, payload, output_format in cases:
            observed["responses"][f"{label}@{provider}"] = outcome(adapters[provider]._normalize_response, payload, 1234, output_format)
    return observed


class ProviderAdapterFixtures(unittest.TestCase):
    """The adapters produce, for every case of the corpus, exactly what they produced when the fixtures were recorded."""

    @classmethod
    def setUpClass(cls):
        cls.observed = observe()
        if RECORD:
            os.makedirs(os.path.dirname(FIXTURES), exist_ok=True)
            with open(FIXTURES, "w", encoding="utf-8") as handle:
                json.dump(cls.observed, handle, ensure_ascii=False, indent=1, sort_keys=True)
                handle.write("\n")
        with open(FIXTURES, encoding="utf-8") as handle:
            cls.expected = json.load(handle)

    def compare(self, section):
        expected, observed = self.expected[section], self.observed[section]
        self.assertEqual(sorted(expected), sorted(observed), f"{section}: the corpus changed")
        for case in expected:
            with self.subTest(section=section, case=case):
                self.assertEqual(observed[case], expected[case])

    def test_provider_requests_are_unchanged(self):
        self.compare("requests")

    def test_response_normalization_is_unchanged(self):
        self.compare("responses")

    def test_token_count_bodies_are_unchanged(self):
        self.compare("token_count_bodies")

    def test_the_corpus_covers_every_provider(self):
        for provider in PROVIDERS:
            self.assertTrue(any(case.endswith("@" + provider) for case in self.expected["requests"]), provider)
            self.assertTrue(any(case.endswith("@" + provider) for case in self.expected["responses"]), provider)
        self.assertTrue(any("ok" in v for v in self.expected["requests"].values()))
        self.assertTrue(any("error" in v for v in self.expected["requests"].values()))


if __name__ == "__main__":
    unittest.main()
