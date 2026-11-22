import asyncio
import json
import unittest

from ..real import token_profile as tp


def call(messages, *, input_tokens, cached=0, output_tokens=40, out=None, cid="c1"):
    return {"request": {"messages": messages, "output": {"format": "json_schema", "schema": {"type": "object", "properties": {"x": {"type": "string"}}}},
                        "metadata": {"continuity_id": cid}},
            "usage": {"input_tokens": input_tokens, "cached_input_tokens": cached, "output_tokens": output_tokens},
            "response": {"output": {"content": out if out is not None else {"messages": [{"content": "bonjour"}], "wait_group": {"condition": "all"}}}}}


FRAME = {"role": "system", "content": "F" * 400}
STATE = {"role": "user", "content": json.dumps({"current_intention": "aider", "context_pressure": {"ratio": 0.2}})}
ASK = {"role": "assistant", "content": json.dumps({"intentions": [{"id": "i1", "target_capacity": "/root/meaning.query", "target_type": "reflexive", "parameters": "{}", "await_response": True}]})}
OK = {"role": "user", "content": json.dumps({"intention_id": "i1", "response": {"status": "ok", "payload": {"brique": {"kind": "matter"}, "result": ["aaaa" * 20]}}})}
BAD = {"role": "user", "content": json.dumps({"intention_id": "i2", "response": {"status": "error", "error": {"code": "invalid"}}})}


def fake_count(request):
    """A counter of a known law: one token per 4 characters of every text, 7 tokens of fixed overhead per request."""
    async def count(req):
        text = "".join(m["content"] if isinstance(m["content"], str) else json.dumps(m["content"]) for m in req["messages"])
        schema = json.dumps((req.get("output") or {}).get("schema", "")) if req.get("output") else ""
        return 7 + (len(text) + len(schema)) // 4
    return count


def run(coro):
    loop = asyncio.new_event_loop()
    try:
        return loop.run_until_complete(coro)
    finally:
        loop.close()


class TokenProfile(unittest.TestCase):
    def test_every_part_has_one_category_and_the_message_is_covered(self):
        targets = {"i1": "/root/meaning.query"}
        for message, categories in [
            (FRAME, {"framework"}), (STATE, {"state_block"}),
            ({"role": "user", "content": "Summary of the previous continuity:\nx"}, {"summary"}),
            ({"role": "user", "content": "Bonjour"}, {"user_message"}),
            ({"role": "user", "content": json.dumps({"validation_error": ["x"]})}, {"correction"}),
            ({"role": "user", "content": json.dumps({"current_intention": "a", "active_stimulus": {"kind": "compaction", "instruction": "x"}})}, {"compaction_instruction"}),
            ({"role": "user", "content": json.dumps({"from": "continuity:a", "content": "hi"})}, {"continuity_message"}),
            (OK, {"tool_payload", "tool_envelope"}), (BAD, {"tool_error"}),
            (ASK, {"decision_content", "decision_structure"}),
        ]:
            parts = tp.classify(message, targets)
            self.assertEqual({p["category"] for p in parts}, categories, message)
            self.assertEqual(sum(p["chars"] for p in parts), len(message["content"]), message)

    def test_the_system_text_is_split_in_its_parts_and_they_add_up(self):
        frame = "FRAME " * 50
        text = "\n\n".join([frame, "Prompt du pilote.", "Rules:\n- a\n- b", "Reply with one JSON object matching the schema. Omit.",
                            "Available capacities (target_capacity = ref):\n[]", "Never invoke (refused before any effect): x",
                            "You may invoke only: y", "Initial intention:\n\"aider\""])
        parts = tp.split_system(text, frame)
        self.assertEqual([p["category"] for p in parts], ["framework", "pilot_prompt", "pilot_rules", "output_guide", "known_capacities", "policy", "policy", "initial_intention"])
        self.assertEqual(sum(len(p["text"]) for p in parts), len(text))
        plan = tp.split_system(frame + "\n\nExamine les commandes.\n\n[1,2]", frame)
        self.assertEqual([p["category"] for p in plan], ["framework", "step_prompt"])
        self.assertEqual([p["category"] for p in tp.split_system(text)][0], "framework")      # frame text unknown: everything before a marker

    def test_tool_results_are_attributed_to_the_capacity_asked(self):
        parts = tp.classify(OK, tp.intention_targets([ASK]))
        self.assertEqual({p["capacity"] for p in parts}, {"/root/meaning.query"})
        payload = next(p for p in parts if p["category"] == "tool_payload")
        self.assertLess(payload["chars"], len(OK["content"]))
        self.assertNotIn("brique", json.dumps(tp.strip_engine(json.loads(OK["content"])["response"]["payload"])))

    def test_estimate_adds_up_to_the_billed_input_and_the_cache_is_a_prefix(self):
        report = tp.profile([call([FRAME, STATE], input_tokens=1000, cached=300)])
        self.assertFalse(report["exact"])
        self.assertEqual(report["counted_input"], 1000)
        segments = tp.segments_of(call([FRAME, STATE], input_tokens=1000)["request"])
        tp.allocate(segments, 1000, 300, None)
        flags = [s["cached"] > 0 for s in segments]
        self.assertEqual(flags, sorted(flags, reverse=True))                      # cached segments come first, with no gap
        self.assertEqual(sum(s["cached"] for s in segments), 300)

    def test_exact_counts_are_per_distinct_text_and_the_parts_of_a_message_add_up(self):
        calls = [call([FRAME, STATE, ASK, OK], input_tokens=900), call([FRAME, STATE, ASK, OK, BAD], input_tokens=1000)]
        counted = []

        async def count(req):
            counted.append(req)
            return await fake_count(None)(req)
        exact = run(tp.count_units(calls, None, count))
        distinct = {s["unit_key"] for c in calls for s in tp.segments_of(c["request"])}
        self.assertEqual(set(exact), distinct)
        self.assertEqual(len(counted), len(distinct) + 1)                           # once per distinct text, plus the base request
        report = tp.profile(calls, None, exact)
        self.assertTrue(report["exact"])
        first = report["per_inference"][0]
        message = next(s for s in tp.segments_of(calls[0]["request"]) if s["category"] == "tool_payload")
        parts = [s for s in tp.segments_of(calls[0]["request"]) if s["unit_key"] == message["unit_key"]]
        tp.allocate(parts, 0, 0, exact)
        self.assertEqual(sum(p["tokens"] for p in parts), exact[message["unit_key"]])      # invariant 2
        self.assertEqual(first["counted_input"], sum(s["tokens"] for s in tp.segments_of(calls[0]["request"]) and
                                                      _allocated(calls[0], exact)))

    def test_fixed_cost_is_the_same_for_each_inference_and_replay_is_counted_in_tokens(self):
        calls = [call([FRAME, STATE], input_tokens=500), call([FRAME, STATE, ASK, OK], input_tokens=800)]
        report = tp.profile(calls, None, run(tp.count_units(calls, None, fake_count(None))))
        self.assertGreater(report["fixed_per_inference"]["total"], 0)
        self.assertIn("framework", report["fixed_per_inference"]["by_element"])
        self.assertEqual(report["history"]["sent"], report["history"]["new"] + report["history"]["replayed"])
        self.assertGreater(report["history"]["replayed"], 0)                         # the state block of the second inference was already sent
        for name, row in report["by_category"].items():
            self.assertEqual(row["new"] + row["replayed"], row["tokens"], name)
        other = tp.profile([call([FRAME], input_tokens=500, cid="a"), call([FRAME], input_tokens=500, cid="b")])
        self.assertEqual(other["by_category"]["framework"]["replayed"], 0)         # another continuity: its own frame is new

    def test_per_turn_cost_gives_occurrences_and_tokens_per_occurrence(self):
        calls = [call([FRAME, STATE, ASK, OK, BAD], input_tokens=900)]
        report = tp.profile(calls, None, run(tp.count_units(calls, None, fake_count(None))))
        for name in ("state_block", "tool_envelope", "tool_error", "decision_structure"):
            row = report["per_turn"][name]
            self.assertEqual(row["occurrences"], 1)
            self.assertEqual(row["per_occurrence"], row["tokens"])

    def test_the_gap_to_the_billed_input_is_always_shown_and_unknown_is_not_hidden(self):
        calls = [call([FRAME, STATE, {"role": "user", "content": "Where is my order?"}, ASK, OK, BAD], input_tokens=900)]
        report = tp.profile(calls, None, run(tp.count_units(calls, None, fake_count(None))))
        self.assertEqual(report["per_inference"][0]["gap"], report["billed_input"] - report["counted_input"])
        self.assertNotIn("unknown", report["by_category"])
        self.assertGreater(report["by_capacity"]["/root/meaning.query"]["tokens"], 0)
        self.assertIn("gap", tp.render(report))

    def test_compaction_is_a_phase_and_a_summary_output_is_content(self):
        compaction = {"role": "user", "content": json.dumps({"current_intention": "a", "active_stimulus": {"kind": "compaction", "instruction": "x"}})}
        report = tp.profile([call([FRAME, compaction], input_tokens=400, out={"succession": {"summary": "summary"}}, output_tokens=100)])
        self.assertEqual(set(report["by_phase"]), {"compaction"})
        self.assertEqual((report["output_content"], report["output_structure"]), (100, 0))

    def test_an_output_of_a_plan_step_is_content_as_a_whole(self):
        report = tp.profile([call([FRAME], input_tokens=300, output_tokens=60, out={"commande_active": "CMD-1042", "constats": ["a"]})])
        self.assertEqual((report["output_content"], report["output_structure"]), (60, 0))

    def test_calls_without_request_or_usage_are_skipped(self):
        self.assertEqual(tp.profile([{"request": {}, "usage": {}}, {"error": "x"}])["inferences"], 0)


def _allocated(c, exact):
    segments = tp.segments_of(c["request"])
    tp.allocate(segments, c["usage"]["input_tokens"], 0, exact)
    return segments


if __name__ == "__main__":
    unittest.main()
