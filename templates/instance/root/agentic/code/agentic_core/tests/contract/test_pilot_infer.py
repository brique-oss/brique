import asyncio
import json
import unittest

from .harness import CapacityContract, covers, entry, message
from substrate.errors import ConcurrencyError, ConsistencyError, InvalidRequest, NotFound, Refused
import continuity_project, pilot_infer
from substrate import continuity_store, inference_window

SCHEMA = {"type": "object", "properties": {"verdict": {"type": "string", "enum": ["ok", "risk"]}, "why": {"type": "string"}},
          "required": ["verdict", "why"], "additionalProperties": False}
MODEL = {"provider": "fake", "model": "m", "parameters": {"max_output_tokens": 100}}


class PilotInferContract(CapacityContract):
    CAP, MODULE = "pilot.infer", pilot_infer

    async def asyncSetUp(self):
        await super().asyncSetUp()
        self.requests, self.answer = [], {"verdict": "ok", "why": "fine"}

        async def llm(port, params):
            self.requests.append(params["request"])
            out = self.answer(params["request"]) if callable(self.answer) else self.answer
            return {"response": {"output": {"format": "json", "content": out}, "usage": {"input_tokens": 321, "output_tokens": 9}}}
        self.e.caps["/root/llm/infer"] = llm
        self.e.caps["/root/agentic/continuity.project"] = continuity_project.run       # the optional view of the journal
        self.w.full("c1", messages=[message(1, "m1", content="hello")], entries=[entry("m1", status="claimed", claimed_by="R1")],
                    status="running", run_id="R1", current="m1")

    async def infer(self, **extra):
        return await self.call(**dict({"continuity_id": "c1", "run_id": "R1", "step": "analyse", "instructions": "Analyse the order.",
                                       "output_schema": SCHEMA, "model": MODEL}, **extra))

    def journal_kinds(self):
        return [m["kind"] for m in self.w.journal("c1")]

    async def test_payload_matches_the_declared_outputs(self):
        out = await self.infer()
        self.assertPayloadMatchesContract(out)
        self.assertEqual((out["output"], out["step"], out["replayed"], out["usage"]["input_tokens"]), ({"verdict": "ok", "why": "fine"}, "analyse", False, 321))

    @covers("Fails when a required input is missing or malformed.")
    async def test_every_required_input_is_checked(self):
        for name, bad in (("run_id", None), ("step", ""), ("instructions", " "), ("output_schema", None), ("model", {"provider": "x"})):
            before = self.e.snapshot()
            with self.assertRaises(InvalidRequest):
                await self.infer(**{name: bad})
            self.assertEqual(self.e.snapshot(), before)
            self.assertEqual(self.requests, [])

    async def test_the_system_text_is_marked_as_a_cacheable_prefix(self):
        await self.infer()
        system = self.requests[0]["messages"][0]
        self.assertEqual((system["role"], system.get("cache")), ("system", True))        # the provider adapter turns it into a cache_control
        self.assertNotIn("cache", self.requests[0]["messages"][1])                      # the body of the step is what changes: never marked

    # --- the cognitive frame: with the continuity (a projection) always, without it (no projection) only when asked -------------------
    def with_frame(self):
        self.w.full("c1", messages=[message(1, "framework_c1", kind="framework", content={"text": "THE FRAME", "source": "s"}), message(2, "m1", content="hello")],
                    entries=[entry("m1", status="claimed", claimed_by="R1")], status="running", run_id="R1", current="m1")

    async def test_a_step_without_projection_has_no_frame_unless_it_asks(self):
        self.with_frame()
        await self.infer()
        self.assertEqual(self.requests[0]["messages"][0]["content"], "Analyse the order.")                 # prompts only, as before
        await self.infer(step="other", framework=False)
        self.assertEqual(self.requests[1]["messages"][0]["content"], "Analyse the order.")
        await self.infer(step="framed", framework=True)
        self.assertEqual(self.requests[2]["messages"][0]["content"], "THE FRAME\n\nAnalyse the order.")   # the frame first, then the prompt
        self.assertNotIn("continuity_projection", self.requests[2]["messages"][1]["content"])             # still no view of the journal
        recorded = [m["content"]["instructions"] for m in self.w.journal("c1") if m["kind"] == "prompt"]
        self.assertEqual(recorded, ["Analyse the order."] * 3)                                           # the history keeps the prompt: the frame is its own line

    async def test_a_step_with_a_projection_is_the_frame_then_the_conversation_then_the_prompt(self):
        self.with_frame()
        await self.infer(projection={"include_kinds": ["user"]})
        sent = self.requests[0]["messages"]
        self.assertEqual([(m["role"], m["content"]) for m in sent],
                         [("system", "THE FRAME"), ("user", "hello"), ("user", "Analyse the order.")])    # the history first, the new prompt last
        self.assertEqual([m.get("cache") for m in sent], [True, True, None])                              # the frame and the history end stable prefixes; the prompt is new
        self.assertEqual(self.requests[0]["metadata"], {"continuity_id": "c1", "step": "analyse"})        # who asked, for whoever observes the request

    async def test_what_a_step_sent_is_the_start_of_what_the_next_step_sends(self):
        """The journal is the conversation: the next step shows the earlier prompt, its answer and the new prompt after them, and the
        part already sent is the same bytes, which is what the provider's cache reads."""
        self.with_frame()
        shown = {"include_kinds": ["user", "prompt", "completion"]}
        await self.infer(step="first", instructions="First question.", projection=shown)
        await self.infer(step="second", instructions="Second question.", projection=shown)
        first, second = ([(m["role"], m["content"]) for m in r["messages"]] for r in self.requests)
        self.assertEqual(first, [("system", "THE FRAME"), ("user", "hello"), ("user", "First question.")])
        self.assertEqual(second[:3], first[:2] + [("user", "First question.")])                          # the same start, the old prompt now history
        self.assertEqual(second[3], ("assistant", '{"verdict":"ok","why":"fine"}'))                      # its answer
        self.assertEqual(second[4:], [("user", "Second question.")])                                    # and only the new prompt is new
        self.assertEqual([m.get("cache") for m in self.requests[1]["messages"]], [True, None, None, True, None])   # the marker moved to the end of the history

    async def test_an_interrupted_step_asked_again_does_not_say_its_own_prompt_twice(self):
        """The prompt of the step is in the journal before the model answers; if the process dies there, the step is asked again and
        the view of the journal now holds that prompt: it must be the new prompt only, not also a message of the history."""
        shown = {"include_kinds": ["user", "prompt", "completion"]}
        calls = []

        def flaky(request):
            calls.append(request)
            if len(calls) == 1:
                raise RuntimeError("the process died here")
            return {"verdict": "ok", "why": "fine"}
        self.answer = flaky
        with self.assertRaises(Exception):
            await self.infer(step="s", instructions="The question.", projection=shown)
        self.assertEqual(sum(1 for m in self.w.journal("c1") if m["kind"] == "prompt"), 1)                # recorded, no completion
        await self.infer(step="s", instructions="The question.", projection=shown)
        self.assertEqual([m["content"] for m in calls[1]["messages"]], ["hello", "The question."])        # said once, as the new prompt
        self.assertEqual(sum(1 for m in self.w.journal("c1") if m["kind"] == "prompt"), 1)                # and still one line in the journal

    async def test_a_continuity_without_a_frame_sends_no_system_message(self):
        await self.infer(projection={"include_kinds": ["user"]})
        self.assertEqual([m["role"] for m in self.requests[0]["messages"]], ["user", "user"])

    async def test_the_frame_flag_is_a_boolean_and_needs_a_frame_to_give(self):
        self.with_frame()
        before = self.e.snapshot()
        with self.assertRaises(InvalidRequest):
            await self.infer(framework="yes")
        self.assertEqual((self.e.snapshot(), self.requests), (before, []))
        self.w.full("c1", messages=[message(1, "m1", content="hello")], entries=[entry("m1", status="claimed", claimed_by="R1")],
                    status="running", run_id="R1", current="m1")                                          # a continuity without a frame line
        with self.assertRaises(ConsistencyError):
            await self.infer(framework=True)
        self.assertEqual(self.requests, [])

    @covers("Fails when the continuity does not exist.")
    async def test_an_unknown_continuity_fails(self):
        with self.assertRaises(NotFound):
            await self.infer(continuity_id="ghost")

    @covers("Only the run that owns the continuity may infer for it.", "Fails when run_id is not the run owning the continuity.")
    async def test_only_the_owning_run_may_infer(self):
        before = self.e.snapshot()
        with self.assertRaises(Refused):
            await self.infer(run_id="R2")
        self.w.runtime("c1", "idle")
        with self.assertRaises(Refused):
            await self.infer()
        self.assertEqual(self.requests, [])

    @covers("The prompt is recorded before the model is asked and the completion after its output is validated;")
    async def test_prompt_then_completion_with_deterministic_ids(self):
        seen = []
        original = self.answer

        def spy(request):
            seen.append(self.journal_kinds())                       # what the journal holds when the model is asked
            return original
        self.answer = spy
        await self.infer()
        self.assertEqual(seen, [["user", "prompt"]])
        journal = self.w.journal("c1")
        prompt, completion = journal[1], journal[2]
        self.assertEqual((prompt["kind"], completion["kind"]), ("prompt", "completion"))
        self.assertEqual(prompt["content"]["step"], "analyse")
        self.assertEqual(prompt["content"]["instructions"], "Analyse the order.")
        self.assertEqual(completion["content"]["output"], {"verdict": "ok", "why": "fine"})
        self.assertEqual(completion["correlation"]["causation_id"], prompt["id"])
        self.assertTrue(prompt["id"].startswith("prompt_c1_") and completion["id"].startswith("completion_c1_"))

    @covers("A step already completed in the journal is returned without calling the model and adds nothing to the journal.",
            "A step is identified by its message, name, prompt, output schema and model: the same step is the same inference.")
    async def test_a_completed_step_is_replayed_for_free(self):
        first = await self.infer()
        before = self.e.snapshot()
        again = await self.infer()
        self.assertEqual((again["replayed"], again["output"]), (True, first["output"]))
        self.assertEqual(len(self.requests), 1)                       # the model was asked once
        self.assertEqual(self.e.snapshot(), before)
        await self.infer(instructions="Analyse the other order.")      # another prompt is another step
        self.assertEqual(len(self.requests), 2)
        await self.infer(step="summary")                               # another name too
        self.assertEqual(len(self.requests), 3)

    PILOT_CTX, TEST_CTX = "/root/agentic/pilot", "/root/agentic/test"

    def lay_texts(self):
        self.e.texts[(self.PILOT_CTX, "tone")] = "Answer like a pilot.\n"                # a Matter keeps its substance as is, final newline included
        self.e.texts[(self.TEST_CTX, "shape")] = '{"not": "parsed", "kept": "raw"}'      # even when it looks like JSON

    @staticmethod
    def matters(*refs):
        """The Matters of the prompt, bound under prompt_matters as the wrapper receives them (intention.matters)."""
        return {"@matter": {"prompt_matters": [{"context": c, "id": i} for c, i in refs]}}

    def system_prompt(self, request):
        return request["messages"][0]["content"]                     # the assembled prompt, and nothing added to it

    @covers("A prompt given as lists is assembled as is:")
    async def test_lists_are_assembled_in_the_order_asked_with_a_blank_line(self):
        self.lay_texts()
        slot = self.matters((self.PILOT_CTX, "tone"), (self.TEST_CTX, "shape"))
        raw = ["Answer like a pilot.\n", '{"not": "parsed", "kept": "raw"}']
        await self.infer(instructions=None, prompt_parts=["First.", "Third."], prompt_order="matters_first", **slot)
        self.assertEqual(self.system_prompt(self.requests[-1]), "\n\n".join(raw + ["First.", "Third."]))     # Matters, then texts
        await self.infer(instructions=None, prompt_parts=["First.", "Third."], prompt_order="parts_first", **slot)
        self.assertEqual(self.system_prompt(self.requests[-1]), "\n\n".join(["First.", "Third."] + raw))     # texts, then Matters
        await self.infer(instructions=None, **slot)                                                           # one list needs no order
        self.assertEqual(self.system_prompt(self.requests[-1]), "\n\n".join(raw))
        await self.infer(instructions=None, prompt_parts=["Alone.", "Twice."])
        self.assertEqual(self.system_prompt(self.requests[-1]), "Alone.\n\nTwice.")
        prompts = [m["content"]["instructions"] for m in self.w.journal("c1") if m["kind"] == "prompt"]
        self.assertEqual(prompts[0], "\n\n".join(raw + ["First.", "Third."]))                                # the journal keeps the text sent
        self.assertNotIn("prompt_parts", json.dumps(self.w.journal("c1")))
        self.assertEqual([x for x in self.e.log if x[1] in ("tone", "shape")], [])                           # the Matters are only read

    @covers("A prompt given as lists is assembled as is:")
    async def test_any_json_value_is_a_part_as_compact_json(self):
        data = {"b": 1, "a": [1, "é"]}
        await self.infer(instructions=None, prompt_parts=["Analyse.", data, 7, True, [1, 2]])
        self.assertEqual(self.system_prompt(self.requests[-1]), 'Analyse.\n\n{"a":[1,"é"],"b":1}\n\n7\n\ntrue\n\n[1,2]')   # keys in order, text as is
        again = await self.infer(instructions=None, prompt_parts=["Analyse.", {"a": [1, "é"], "b": 1}, 7, True, [1, 2]])
        self.assertTrue(again["replayed"])                                           # the same value in another key order is the same text, the same step
        self.assertEqual(len(self.requests), 1)

    @covers("The prompt of a step is the text that was sent, however it was given:")
    async def test_the_step_is_the_text_sent_so_a_changed_matter_is_a_new_step(self):
        self.lay_texts()
        slot = self.matters((self.PILOT_CTX, "tone"))
        kw = dict(instructions=None, prompt_parts=["Be brief."], prompt_order="parts_first", **slot)
        first = await self.infer(**kw)
        again = await self.infer(**kw)
        self.assertEqual((first["replayed"], again["replayed"], len(self.requests)), (False, True, 1))     # same text: the same step
        self.e.texts[(self.PILOT_CTX, "tone")] = "Answer like a poet.\n"
        changed = await self.infer(**kw)
        self.assertFalse(changed["replayed"])
        self.assertEqual(len(self.requests), 2)                                                          # a changed Matter: a new inference
        prompts = [m["content"]["instructions"] for m in self.w.journal("c1") if m["kind"] == "prompt"]
        self.assertEqual(prompts, ["Be brief.\n\nAnswer like a pilot.\n", "Be brief.\n\nAnswer like a poet.\n"])   # the history keeps both
        via_text = await self.infer(instructions="Be brief.\n\nAnswer like a poet.\n")                 # the same text given as plain instructions
        self.assertTrue(via_text["replayed"])
        self.assertEqual(len(self.requests), 2)

    @covers("Fails when the prompt is given twice or not at all,")
    async def test_a_malformed_or_missing_prompt_is_refused_before_any_effect(self):
        self.lay_texts()
        self.e.texts[(self.PILOT_CTX, "blank")] = "  \n"
        tone = self.matters((self.PILOT_CTX, "tone"))
        bad = [
            dict(instructions="x", prompt_parts=["y"]),                                              # instructions and texts
            dict(instructions="x", **tone),                                                          # instructions and Matters
            dict(instructions=None),                                                                 # neither
            dict(instructions=None, prompt_parts=[]),                                                # an empty list
            dict(instructions=None, prompt_parts="text"),                                            # not a list
            dict(instructions=None, prompt_parts=["a", None]),                                       # null is not a part
            dict(instructions=None, prompt_parts=["a", " "]),                                        # an empty text
            dict(prompt_order="matters_first", input={"order": "A100"}),                             # input is gone: the data is a part of the prompt
            dict(instructions=None, prompt_parts=["a"], input={"order": "A100"}),
            dict(instructions=None, prompt_parts=["a"], prompt_order="sideways", **tone),            # an unknown order
            dict(instructions=None, prompt_parts=["a"], **tone),                                     # both lists, no order
            dict(instructions=None, **{"@matter": {"prompt_matters": [{"context": "/root/agentic/pilot"}]}}),                # no id
            dict(instructions=None, **{"@matter": {"prompt_matters": [{"context": "pilot", "id": "tone"}]}}),                # relative context
            dict(instructions=None, **self.matters((self.PILOT_CTX, "tone"), (self.PILOT_CTX, "blank"))),                    # an empty Matter
        ]
        for extra in bad:
            before = self.e.snapshot()
            with self.assertRaises(InvalidRequest, msg=str(extra)[:90]):
                await self.infer(**extra)
            self.assertEqual(self.e.snapshot(), before)                                              # nothing recorded
            self.assertEqual(self.requests, [])                                                      # the model was not asked
        before = self.e.snapshot()
        with self.assertRaises(NotFound):                                                            # a Matter that does not exist
            await self.infer(instructions=None, **self.matters((self.PILOT_CTX, "tone"), (self.PILOT_CTX, "ghost")))
        self.assertEqual(self.e.snapshot(), before)
        self.assertEqual(self.requests, [])

    @covers("The output is validated against output_schema; an invalid output is refused and recorded as an error, never as a completion.",
            "Fails when the model output does not conform to output_schema.")
    async def test_an_invalid_output_is_refused_and_never_a_completion(self):
        self.answer = {"verdict": "maybe", "why": 3}
        with self.assertRaises(InvalidRequest):
            await self.infer()
        kinds = self.journal_kinds()
        self.assertNotIn("completion", kinds)
        self.assertIn("error", kinds)

    @covers("Fails when the model provider fails; nothing is recorded as completed.")
    async def test_a_provider_failure_records_no_completion(self):
        async def broken(port, params):
            raise RuntimeError("provider down")
        self.e.caps["/root/llm/infer"] = broken
        with self.assertRaises(Exception):
            await self.infer()
        self.assertNotIn("completion", self.journal_kinds())
        self.e.caps["/root/llm/infer"] = lambda port, params: _ok()      # a retry reuses the recorded prompt
        out = await self.infer()
        self.assertEqual(self.journal_kinds().count("prompt"), 1)
        self.assertEqual(out["output"]["verdict"], "ok")

    @covers("The number of prompts of one message is bounded; beyond the bound the step is refused.",
            "Fails when the message already used its allowed inferences.")
    async def test_the_prompts_of_a_message_are_bounded(self):
        await self.infer(step="s1", max_inferences=2)
        await self.infer(step="s2", max_inferences=2)
        before = self.e.snapshot()
        with self.assertRaises(Refused):
            await self.infer(step="s3", max_inferences=2)
        self.assertEqual(self.e.snapshot(), before)
        self.assertEqual((await self.infer(step="s1", max_inferences=2))["replayed"], True)      # a replay is not a new inference

    @covers("An output_schema must be sendable to a strict structured-output provider (closed objects, bounded optional and union parameters); otherwise it is refused before any model call.",
            "Fails when output_schema cannot be sent to a structured-output provider.")
    async def test_an_unsendable_schema_is_refused_before_any_call(self):
        open_object = {"type": "object", "properties": {"a": {"type": "string"}}}
        wide = {"type": "object", "properties": {f"f{i}": {"type": "string"} for i in range(30)}, "required": [], "additionalProperties": False}
        for bad in (open_object, wide, {"type": "array"}):
            with self.assertRaises(InvalidRequest):
                await self.infer(output_schema=bad)
        self.assertEqual(self.requests, [])

    @covers("Without projection the step sees only its prompt; with projection it also sees the requested view of the journal.")
    async def test_the_step_sees_the_journal_only_on_request(self):
        await self.infer()
        body = json.loads(self.requests[-1]["messages"][1]["content"])
        self.assertEqual(sorted(body), ["continuity_id", "step"])
        await self.infer(step="with-view", projection={"include_kinds": ["user"]})
        sent = [(m["role"], m["content"]) for m in self.requests[-1]["messages"]]
        self.assertEqual(sent, [("user", "hello"), ("user", "Analyse the order.")])                       # the journal as a conversation, then the prompt

    @covers("The inference_measured event is written only by a step whose context contains the projection.")
    async def test_the_context_measure_follows_only_steps_that_saw_the_journal(self):
        def measures():
            return [m["content"] for m in self.w.journal("c1") if m["kind"] == "event" and m["content"].get("event") == "inference_measured"]
        await self.infer()
        self.assertEqual(measures(), [])                                           # a step that did not see the journal measures nothing
        await self.infer(step="with-view", projection={})
        self.assertEqual(measures(), [{"event": "inference_measured", "input_tokens": 321, "message_id": "m1", "output_tokens": 9}])
        self.assertNotIn("context_tokens", self.w.rt("c1"))                        # the measure is a fact of the journal, not a state of runtime

    @covers("When the provider reports the usage, the inference_measured event also carries the breakdown, each field only if known:")
    async def test_the_measure_carries_the_usage_breakdown_each_field_only_if_known(self):
        def measures():
            return [m["content"] for m in self.w.journal("c1") if m["kind"] == "event" and m["content"].get("event") == "inference_measured"]

        async def llm(port, params):
            return {"response": {"output": {"format": "json", "content": self.answer},
                                 "usage": {"input_tokens": 1000, "cached_input_tokens": 700, "cache_creation_input_tokens": 200, "output_tokens": 30},
                                 "resolved": {"provider": "fake", "model": "m-2"}, "duration_ms": 1234}}
        self.e.caps["/root/llm/infer"] = llm
        await self.infer(projection={})
        self.assertEqual(measures()[-1], {"event": "inference_measured", "input_tokens": 1000, "message_id": "m1", "cache_read_tokens": 700,
                                          "cache_creation_tokens": 200, "uncached_input_tokens": 100, "output_tokens": 30,
                                          "model": "fake/m-2", "duration_ms": 1234})

        async def no_cache(port, params):                  # a provider that says nothing of the cache: no cache field, no uncached claim
            return {"response": {"output": {"format": "json", "content": self.answer}, "usage": {"input_tokens": 50, "output_tokens": 4}}}
        self.e.caps["/root/llm/infer"] = no_cache
        await self.infer(step="other", projection={})
        self.assertEqual(measures()[-1], {"event": "inference_measured", "input_tokens": 50, "message_id": "m1", "output_tokens": 4})

    @covers("A content is written as is in its journal line, whatever its size, with no carrying by reference;")
    async def test_large_contents_are_written_as_is_and_returned_whole(self):
        big = {"order": "x" * 300_000}                                              # above the inline limit of the engine: no threshold, no reference
        out = await self.infer(instructions=None, prompt_parts=["Analyse.", big])
        prompt = [m for m in self.w.journal("c1") if m["kind"] == "prompt"][0]
        self.assertEqual(prompt["content"]["instructions"], "Analyse.\n\n" + json.dumps(big, separators=(",", ":")))
        self.assertEqual(out["output"], {"verdict": "ok", "why": "fine"})
        wide = {"verdict": "ok", "why": "y" * 300_000}
        self.answer = wide
        out = await self.infer(step="long-answer")
        self.assertEqual(out["output"], wide)
        again = await self.infer(step="long-answer")
        self.assertEqual((again["replayed"], again["output"]), (True, wide))      # a replay reads it back from the journal
        self.assertFalse([m for m in self.e.matters if m.startswith("content_")])    # no overflow Matter

    @covers("The request sent to the model is a transit value, never written to the journal;")
    async def test_the_request_to_the_model_is_never_written(self):
        await self.infer(projection={})
        written = json.dumps(self.w.journal("c1")) + json.dumps({k: v for k, v in self.e.matters.items() if k != "pilot_default"})
        # the request is the assembled prompt (which the journal does record, as the prompt of the step) and the context of the
        # step, which it never does
        self.assertEqual(self.requests[0]["messages"][-1]["content"], "Analyse the order.")
        self.assertNotIn("continuity_projection", written)                    # nor a view assembled for it
        prompt = [m for m in self.w.journal("c1") if m["kind"] == "prompt"][0]
        self.assertEqual(sorted(prompt["content"]), ["instructions", "model", "step"])             # the journal keeps the step, as is

    @covers("Prompt and completion are history only: they have no mailbox entry and never wake the continuity.")
    async def test_prompt_and_completion_never_reach_the_mailbox(self):
        before = self.w.entries("c1")
        await self.infer()
        self.assertEqual(self.w.entries("c1"), before)
        self.assertEqual([c for c in self.e.calls if c.startswith("fire:")], [])

    @covers("Fails explicitly with a concurrency failure when the bounded retries on revision conflict are exhausted.")
    async def test_exhausted_conflicts_fail_explicitly(self):
        self.e.always_line_conflict.add("journal_c1")
        with self.assertRaises(ConcurrencyError):
            await self.infer()

    @covers("From the reading of the projection to the writing of the completion the step holds the inference window")
    async def test_the_step_holds_the_window_from_its_prompt_to_its_completion(self):
        gate, called = asyncio.Event(), asyncio.Event()

        async def slow(port, params):
            called.set()
            await gate.wait()
            return await _ok()
        self.e.caps["/root/llm/infer"] = slow
        task = asyncio.ensure_future(self.infer())
        await called.wait()
        outsider = asyncio.ensure_future(continuity_store.append(self.p, "c1", [{"kind": "event", "content": {"event": "late"}}]))   # the answer of a tool, say
        await asyncio.sleep(0.05)
        self.assertFalse(outsider.done())
        self.assertEqual(self.journal_kinds(), ["user", "prompt"])                         # the prompt is written; nothing may come between it and its completion
        gate.set()
        await task
        await outsider
        self.assertEqual(self.journal_kinds(), ["user", "prompt", "completion", "event"])
        # a replayed step takes no window: it waits for nobody
        held, release = asyncio.Event(), asyncio.Event()

        async def other():
            async with inference_window.hold("c1"):
                held.set()
                await release.wait()
        busy = asyncio.ensure_future(other())
        await held.wait()
        replay = await asyncio.wait_for(self.infer(), 1)
        self.assertTrue(replay["replayed"])
        release.set()
        await busy


async def _ok():
    return {"response": {"output": {"format": "json", "content": {"verdict": "ok", "why": "fine"}}, "usage": {"input_tokens": 1}}}


if __name__ == "__main__":
    unittest.main()
