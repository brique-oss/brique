"""Dry run against the REAL provider adapters of the llm context: the request pilot.activate builds
must be accepted by every adapter, and its schema must satisfy strict structured-output rules.
No network, no API key, no cost: only the adapters' request translation is exercised."""
import importlib.util
import json
import os
import sys
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
AGENT_CORE = os.path.dirname(os.path.dirname(HERE))
sys.path.insert(0, AGENT_CORE)
LLM_DIR = os.path.normpath(os.path.join(AGENT_CORE, "..", "..", "..", "llm", "code", "llm_llm"))

import pilot_activate  # noqa: E402
from substrate import schemas  # noqa: E402

PROVIDERS = ("openai", "anthropic", "mistral", "scaleway")


def adapter(name):
    if LLM_DIR not in sys.path:                  # the wrapper runs from its own directory: its siblings (keepalive) resolve there
        sys.path.insert(0, LLM_DIR)
    path = os.path.join(LLM_DIR, f"infer_{name}.py")
    spec = importlib.util.spec_from_file_location(f"infer_{name}", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module._adapt_request


def strict_violations(node, path="$", optional_ok=False):
    """Rules of strict structured outputs shared by the providers (closed, typed, fully required)."""
    out = []
    if not isinstance(node, dict):
        return [f"{path}: not an object"]
    if "anyOf" in node:
        for i, branch in enumerate(node["anyOf"]):
            out += strict_violations(branch, f"{path}.anyOf[{i}]", optional_ok)
        return out
    t = node.get("type")
    if t is None and "enum" not in node:
        out.append(f"{path}: no type")
    types = t if isinstance(t, list) else [t]
    if isinstance(t, list):
        out.append(f"{path}: union type {t} (nullability must use anyOf: some providers refuse it)")
    if "enum" in node:             # the lesson of the first real call: enum values must match the declared type
        pytypes = {"string": str, "integer": int, "boolean": bool}
        for value in node["enum"]:
            if not any(isinstance(value, pytypes.get(x, ())) for x in types):
                out.append(f"{path}: enum value {value!r} does not match declared type {t}")
    if "object" in types:
        props = node.get("properties")
        if props is None:
            out.append(f"{path}: open object (no properties)")
        else:
            if node.get("additionalProperties") is not False:
                out.append(f"{path}: additionalProperties is not false")
            if not optional_ok and sorted(node.get("required", [])) != sorted(props):
                out.append(f"{path}: not every property is required")
            if optional_ok and not set(node.get("required", [])) <= set(props):
                out.append(f"{path}: required names an unknown property")
            for k, v in props.items():
                out += strict_violations(v, f"{path}.{k}", optional_ok)
    if "array" in types:
        if "items" not in node:
            out.append(f"{path}: array without items")
        else:
            out += strict_violations(node["items"], f"{path}[]", optional_ok)
    return out


class ProviderRequests(unittest.TestCase):
    def request(self, provider="openai"):
        pilot = {"prompt": "SCENARIO-DRIVEN TEST PILOT", "model": {"provider": provider, "model": "some-model"},
                 "parameters": {"max_output_tokens": 1024, "temperature": 0}, "rules": ["be exact"],
                 "known_capacities": [{"ref": "/root/agentic/test/test.echo", "description": "echo"}]}
        continuity = {"id": "c1", "initial_intention": "goal", "current_intention": "goal"}
        view = {"projection": {"messages": [], "definition": {}}}
        current = {"id": "m1", "seq": 1, "from": "u", "to": "c1", "kind": "user", "content": "hi", "correlation": {}, "created_at": "now"}
        return pilot_activate.build_request(pilot, continuity, view, current, None)

    def test_every_provider_adapter_accepts_the_request(self):
        for name in PROVIDERS:
            body = adapter(name)(self.request(name))
            self.assertIn("pilot", json.dumps(body).lower(), name)
            text = json.dumps(body)
            self.assertIn("json_schema", text, name)

    def test_the_schema_satisfies_strict_structured_output_rules(self):
        self.assertEqual(strict_violations(self.request("openai")["output"]["schema"]), [])
        self.assertEqual(strict_violations(self.request("anthropic")["output"]["schema"], optional_ok=True), [])

    def test_anthropic_gets_no_union_typed_parameter_far_below_its_limit_of_16(self):
        # lesson of the second real call: 55 nullable/anyOf parameters were refused (limit: 16 unions)
        schema = self.request("anthropic")["output"]["schema"]
        self.assertEqual(schemas.count_unions(schema), 0)
        self.assertGreater(schemas.count_unions(self.request("openai")["output"]["schema"]), 16)   # why the style differs

    def test_anthropic_schema_stays_under_its_optional_and_union_limits(self):
        # lessons of the real calls: "too many parameters with union types (limit 16)", then
        # "too many optional parameters (limit 24)": the model-facing schema must satisfy both with margin
        def optionals(node):
            total = 0
            if isinstance(node, dict):
                if node.get("type") == "object" and "properties" in node:
                    total += len(set(node["properties"]) - set(node.get("required", [])))
                total += sum(optionals(v) for v in node.values())
            elif isinstance(node, list):
                total += sum(optionals(v) for v in node)
            return total
        schema = self.request("anthropic")["output"]["schema"]
        self.assertLessEqual(optionals(schema), 16)
        self.assertLessEqual(schemas.count_unions(schema), 8)

    def test_values_managed_by_agentic_core_are_not_asked_of_the_model(self):
        wait_group = self.request("anthropic")["output"]["schema"]["properties"]["wait_group"]["properties"]
        self.assertEqual(sorted(wait_group), ["condition", "expected", "n"])           # id, received and status are assigned
        # the output without them is completed then valid
        raw = {"intentions": [{"id": "i1", "target_capacity": "/a/b", "parameters": {}, "await_response": True}],
               "wait_group": {"condition": "all", "expected": [{"type": "intention", "ref": "i1"}]}}
        out = pilot_activate.validate_output(pilot_activate.normalize_output({}, raw, set()), set())   # the family is assigned by normalize_output
        self.assertEqual((out["wait_group"]["status"], out["wait_group"]["received"]), ("waiting", []))
        self.assertTrue(out["wait_group"]["id"])

    def test_projection_and_correlation_travel_as_text_and_come_back_as_objects(self):
        answer = {"projection_request": {"projection": "{\"include_kinds\": [\"user\"], \"limit\": 5}", "mode": "merge"}}
        output = pilot_activate.validate_output(schemas.decode_provider_output("pilot_output", answer))
        self.assertEqual(output["projection_request"]["projection"], {"include_kinds": ["user"], "limit": 5})
        answer = {"messages": [{"to": "p", "kind": "continuity", "content": "x", "correlation": "{\"continuity_id\": \"k\"}"}]}
        output = pilot_activate.validate_output(schemas.decode_provider_output("pilot_output", answer))
        self.assertEqual(output["messages"][0]["correlation"], {"continuity_id": "k"})
        # a delegated continuity is expected under its lineage id without the model spelling the correlation
        out = pilot_activate.validate_output({"wait_group": {"condition": "all", "expected": [{"type": "continuity", "ref": "k"}]}})
        self.assertEqual(out["wait_group"]["expected"][0]["correlation"], {"continuity_id": "k"})

    def test_the_stable_system_prefix_is_marked_for_caching_and_only_anthropic_acts_on_it(self):
        request = self.request("anthropic")
        self.assertIs(request["messages"][0]["cache"], True)
        self.assertNotIn("cache", request["messages"][1])               # the volatile context is never cached
        body = adapter("anthropic")(request)
        self.assertEqual(body["system"][0]["cache_control"], {"type": "ephemeral"})
        for name in ("openai", "mistral", "scaleway"):                  # the hint never reaches a provider without explicit caching
            self.assertNotIn("cache", json.dumps(adapter(name)(self.request(name))))

    def test_a_message_of_the_conversation_can_end_the_cached_prefix_too(self):
        def request(name):
            return {"provider": name, "model": "m", "parameters": {"max_output_tokens": 50},
                    "messages": [{"role": "system", "content": "frame", "cache": True},
                                 {"role": "user", "content": "first"}, {"role": "assistant", "content": "answer"},
                                 {"role": "user", "content": "last of the history", "cache": True}, {"role": "user", "content": "what is new"}]}
        body = adapter("anthropic")(request("anthropic"))
        self.assertEqual(body["system"][0]["cache_control"], {"type": "ephemeral"})
        by_text = {m["content"] if isinstance(m["content"], str) else m["content"][0]["text"]: m for m in body["messages"]}
        self.assertEqual(by_text["last of the history"]["content"], [{"type": "text", "text": "last of the history", "cache_control": {"type": "ephemeral"}}])
        for plain in ("first", "answer", "what is new"):                  # only the marked message becomes a block with cache_control
            self.assertIsInstance(by_text[plain]["content"], str)
        self.assertEqual([m["role"] for m in body["messages"]], ["user", "assistant", "user", "user"])    # the order and the roles are kept
        for name in ("openai", "mistral", "scaleway"):                     # a provider without explicit caching ignores the hint
            self.assertNotIn("cache", json.dumps(adapter(name)(request(name))))

    def test_the_stable_capacities_live_in_the_cached_prefix_not_in_the_volatile_context(self):
        request = self.request("anthropic")
        system, volatile = request["messages"][0]["content"], request["messages"][1]["content"]
        self.assertIn("/root/agentic/test/test.echo", system)            # in the prefix that is marked for caching
        self.assertNotIn("test.echo", volatile)                           # and not paid again with every conversation turn
        self.assertNotIn("available_capacities", json.loads(volatile))

    def test_the_guide_matches_the_schema_style(self):
        self.assertIn("Omit every field you do not use", self.request("anthropic")["messages"][0]["content"])
        self.assertIn("Use null for every field you do not use", self.request("openai")["messages"][0]["content"])
        # a recorded provider forwarding to anthropic is judged by its upstream provider
        pilot = {"model": {"provider": "recorded", "model": "m"}, "parameters": {"upstream_provider": "anthropic"}}
        self.assertEqual(pilot_activate.schema_style(pilot), "optional")

    def test_the_schema_is_compact(self):
        # it travels with every inference: keep it small and free of prose
        self.assertLess(len(json.dumps(self.request()["output"]["schema"])), 8000)

    def test_a_provider_answer_round_trips_to_the_canonical_pilot_output(self):
        answer = {"intention_update": None, "projection_request": None, "messages": None,
                  "intentions": [{"id": "i1", "target_capacity": "/root/agentic/test/test.slow", "target_type": None,
                                  "parameters": "{\"gate\": \"g\", \"value\": 3}", "await_response": True}],
                  "wait_group": {"id": None, "condition": "any", "n": None, "status": None, "received": None,
                                 "expected": [{"type": "intention", "ref": "i1", "correlation": None}]}}
        decoded = schemas.decode_provider_output("pilot_output", answer)
        output = pilot_activate.validate_output(pilot_activate.normalize_output({}, decoded, set()), set())
        self.assertEqual(output["intentions"][0]["parameters"], {"gate": "g", "value": 3})
        self.assertNotIn("projection_request", output)
        self.assertEqual(output["wait_group"]["expected"][0]["correlation"], {"intention_id": "i1"})

    def test_a_textual_answer_is_read_tolerantly(self):
        wrapped = "Here you go:\n```json\n{\"messages\": [{\"to\": \"u\", \"kind\": \"user\", \"content\": \"x\"}]}\n```"
        self.assertEqual(pilot_activate.extract_json(wrapped)["messages"][0]["to"], "u")
        self.assertEqual(pilot_activate.extract_json("{\"a\": 1}"), {"a": 1})
        self.assertEqual(pilot_activate.extract_json("no json at all"), "no json at all")

    def test_the_canonical_form_is_unchanged_by_decoding(self):
        canonical = {"intentions": [{"id": "i1", "target_capacity": "/a/b", "parameters": {"k": 1}, "await_response": False}]}
        self.assertEqual(schemas.decode_provider_output("pilot_output", canonical), canonical)


if __name__ == "__main__":
    unittest.main()
