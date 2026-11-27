import asyncio
import json
import unittest

from .harness import PILOT_ID, CapacityContract, covers, entry, message
from .mcp_scenarios import McpScenarios
from fake_engine import make_engine
from substrate import continuity_store, inference_window, liveness, locks
from substrate.errors import ConcurrencyError, ConsistencyError, InvalidRequest
import mailbox_send, pilot_activate, pilot_infer

pilot_activate.POLL_S = 0.01


def wrap(output):
    return {"response": {"output": {"format": "json", "content": output}}}


def say(text="ok"):
    return {"messages": [{"to": "user:1", "kind": "user", "content": text}]}


def awaited(ref, target="/root/tools/echo", **kw):
    return {"id": ref, "target_capacity": target, "parameters": {"r": ref}, "await_response": True, **kw}


class NormalizeOutput(unittest.TestCase):
    """What is mechanical is the code's: identifiers and families are written by the runtime, not by the model."""
    PILOT = {"known_capacities": [{"ref": "/root/tools/echo", "target_type": "user"}, {"ref": "/ctx/special", "target_type": "execution"}]}

    def test_an_intention_without_identifier_gets_a_free_one(self):
        out = pilot_activate.normalize_output(self.PILOT, {"intentions": [
            {"target_capacity": "/root/tools/echo", "parameters": {}, "await_response": True},
            {"id": "i1", "target_capacity": "/root/tools/echo", "parameters": {}, "await_response": True},
            {"target_capacity": "/root/tools/echo", "parameters": {}, "await_response": True}]}, used_ids={"i2"})
        self.assertEqual([i["id"] for i in out["intentions"]], ["i3", "i1", "i4"])               # unique, and not the ones already taken

    def test_a_reused_identifier_is_renamed_and_the_wait_group_follows(self):
        out = pilot_activate.normalize_output(self.PILOT, {"intentions": [
            {"id": "x", "target_capacity": "/root/tools/echo", "parameters": {}, "await_response": True}],
            "wait_group": {"condition": "all", "expected": [{"type": "intention", "ref": "x"}]}}, used_ids={"x"})
        self.assertEqual(out["intentions"][0]["id"], "x_2")
        self.assertEqual(out["wait_group"]["expected"][0]["ref"], "x_2")
        repaired = pilot_activate.validate_output(out, {"x"})                                    # and the result is valid
        self.assertEqual(repaired["wait_group"]["expected"][0]["correlation"], {"intention_id": "x_2"})

    def test_a_duplicate_inside_one_output_is_renamed_too(self):
        out = pilot_activate.normalize_output(self.PILOT, {"intentions": [
            {"id": "a", "target_capacity": "/root/tools/echo", "parameters": {}, "await_response": False},
            {"id": "a", "target_capacity": "/root/tools/echo", "parameters": {}, "await_response": False}]})
        self.assertEqual([i["id"] for i in out["intentions"]], ["a", "a_2"])

    def test_the_family_is_written_when_the_code_knows_it(self):
        def family(ref, written=None):
            intention = {"id": "z", "target_capacity": ref, "parameters": {}, "await_response": True}
            if written:
                intention["target_type"] = written
            return pilot_activate.normalize_output(self.PILOT, {"intentions": [intention]})["intentions"][0].get("target_type")
        self.assertEqual(family("/root/vocabulary.query"), "reflexive")
        self.assertEqual(family("/root/read.capacity"), "reflexive")
        self.assertEqual(family("/ctx/sub/read.meaning"), "reflexive")                           # an engine capacity of any context
        self.assertEqual(family("/ctx/sub/matter.read"), "matter")
        self.assertEqual(family("/ctx/wrapper.restart"), "execution")
        self.assertEqual(family("/root/context.restart"), "control")
        self.assertEqual(family("/ctx/special"), "execution")                                    # declared by the pilot_definition
        self.assertEqual(family("/root/tools/echo"), "user")
        self.assertEqual(family("/root/matter.read", "user"), "matter")                          # a wrong family is corrected
        self.assertEqual(family("/ctx/unknown.capacity"), "user")                              # unknown to the code: the architect's, user
        self.assertEqual(family("/ctx/unknown.capacity", "matter"), "matter")

    def test_only_a_correction_is_reported_not_what_the_model_cannot_write(self):
        repairs = []
        pilot_activate.normalize_output(self.PILOT, {"intentions": [
            {"id": "x", "target_capacity": "/root/vocabulary.query", "parameters": {}, "await_response": True, "target_type": "user"},
            {"target_capacity": "/ctx/matter.read", "parameters": {}, "await_response": True},
            {"id": "x", "target_capacity": "/root/tools/echo", "parameters": {}, "await_response": True}]}, {"x"}, repairs)
        self.assertEqual(sorted(r["kind"] for r in repairs), ["family_corrected", "id_renamed", "id_renamed"])      # an id written and taken, a family written wrong
        corrected = next(r for r in repairs if r["kind"] == "family_corrected")
        self.assertEqual((corrected["wrote"], corrected["set"]), ("user", "reflexive"))
        clean = []
        pilot_activate.normalize_output(self.PILOT, {"intentions": [
            {"target_capacity": "/root/tools/echo", "parameters": {}, "await_response": True},
            {"target_capacity": "/root/read.capacity", "parameters": {}, "await_response": True}]}, set(), clean)
        self.assertEqual(clean, [])                                                                  # a model that writes neither costs nothing to report

    def test_a_reference_that_designates_no_intention_is_dropped_and_the_default_applies(self):
        out = pilot_activate.normalize_output(self.PILOT, {"intentions": [
            {"target_capacity": "/root/tools/echo", "parameters": {}, "await_response": True},
            {"target_capacity": "/root/tools/echo", "parameters": {}, "await_response": True}],
            "wait_group": {"condition": "all", "expected": [{"type": "intention", "ref": "0"}, {"type": "intention", "ref": "2"},
                                                            {"type": "continuity", "ref": "kid"}]}}, None, (repairs := []))
        self.assertEqual(out["wait_group"]["expected"], [{"type": "continuity", "ref": "kid"}])         # a delegated continuity is kept
        self.assertEqual(sorted(r["ref"] for r in repairs if r["kind"] == "expected_dropped"), ["0", "2"])
        only = pilot_activate.normalize_output(self.PILOT, {"intentions": [
            {"target_capacity": "/root/tools/echo", "parameters": {}, "await_response": True}],
            "wait_group": {"condition": "all", "expected": [{"type": "intention", "ref": "0"}]}})
        valid = pilot_activate.validate_output(only)
        self.assertEqual([e["ref"] for e in valid["wait_group"]["expected"]], [only["intentions"][0]["id"]])      # the default: every awaited intention
        named = pilot_activate.normalize_output(self.PILOT, {"intentions": [
            {"id": "a", "target_capacity": "/root/tools/echo", "parameters": {}, "await_response": True}],
            "wait_group": {"condition": "all", "expected": [{"type": "intention", "ref": "a"}]}})
        self.assertEqual(named["wait_group"]["expected"][0]["ref"], "a")                              # a scripted pilot that names its ids is still understood

    def test_a_succession_next_to_other_fields_is_made_alone(self):
        work = {"intentions": [{"target_capacity": "/root/tools/echo", "parameters": {}, "await_response": True}], "wait_group": {"condition": "all"},
                "messages": [{"to": "user:1", "kind": "user", "content": "x"}]}
        repairs = []
        kept = pilot_activate.normalize_output(self.PILOT, dict(work, succession={"summary": "where it stands"}, intention_update="g"), None, repairs)
        self.assertEqual(sorted(kept), ["intention_update", "succession"])                                 # the succession stays, the rest is set aside
        self.assertEqual(repairs[0]["kept"], "succession")
        self.assertEqual(sorted(repairs[0]["dropped"]), ["intentions", "messages", "wait_group"])
        self.assertEqual(pilot_activate.validate_output(kept)["succession"], {"summary": "where it stands"})     # and it is valid now
        repairs = []
        worked = pilot_activate.normalize_output(self.PILOT, dict(work, succession={"summary": ""}), None, repairs)
        self.assertNotIn("succession", worked)                                                              # no summary: it meant to work
        self.assertEqual(len(worked["intentions"]), 1)
        self.assertEqual(repairs[0]["kept"], "work")
        alone = {"succession": {"summary": "x"}}
        self.assertEqual(pilot_activate.normalize_output(self.PILOT, alone, None, (clean := [])), alone)    # alone, nothing to repair
        self.assertEqual(clean, [])

    def test_a_non_decision_is_left_alone(self):
        self.assertEqual(pilot_activate.normalize_output(self.PILOT, {"succession": {"summary": "x"}}), {"succession": {"summary": "x"}})
        self.assertEqual(pilot_activate.normalize_output(self.PILOT, "text"), "text")


class PilotActivateContract(McpScenarios, CapacityContract):
    """pilot.activate is exercised with its collaborators (mailbox.*, continuity.project) wired in;
    the LLM and the tools it addresses are scripted."""
    CAP, MODULE = "pilot.activate", pilot_activate

    def successor_state(self):
        """The successor as the tests read it: its header plus the lines of its journal under "messages"."""
        lines = self.w.lines("c1_g2")
        return dict(self.w.state("continuity_c1_g2"), messages=[m for m in lines if m["kind"] != "framework"],
                    frame=[m for m in lines if m["kind"] == "framework"])

    async def asyncSetUp(self):
        await super().asyncSetUp()
        self.e = make_engine(with_capacities=True, pilot=False)
        self.p = self.e.port(self.CAP)
        from .harness import World
        self.w = World(self.e)
        self.requests, self.script, self.tool_calls = [], [wrap(say())], []

        async def llm(port, params):
            self.requests.append(params["request"])
            out = self.script.pop(0) if len(self.script) > 1 else self.script[0]
            return out(params["request"]) if callable(out) else out
        self.e.caps["/root/llm/infer"] = llm

        async def echo(port, params):
            self.tool_calls.append(params)
            return {"ok": True}
        self.e.caps["/root/tools/echo"] = echo
        self.w.full("c1", messages=[message(1, "m1", content="hello")], entries=[entry("m1")])

    async def activate(self, cid="c1"):
        out = await self.call(continuity_id=cid)
        await self.e.drain()
        return out

    def rt(self):
        return self.w.rt("c1")

    def measures(self, cid="c1"):
        """The context measures of the journal: the inference_measured events, in order."""
        return [m["content"] for m in self.w.journal(cid) if m["kind"] == "event" and m["content"].get("event") == "inference_measured"]

    def measured(self, cid, tokens, line):
        return message(line, f"measure{line}", kind="event", content={"event": "inference_measured", "input_tokens": tokens, "message_id": "m1"})

    def events(self):
        return [m["content"].get("event") for m in self.w.journal("c1") if isinstance(m["content"], dict)]

    # --- inputs and outputs --------------------------------------------------------------------------
    async def test_payload_matches_the_declared_outputs(self):
        out = await self.activate()
        self.assertPayloadMatchesContract(out)
        self.assertEqual((out["continuity_id"], out["status"], out["current_message_id"]), ("c1", "idle", None))
        self.assertTrue(out["run_id"])

    async def test_continuity_id_is_required(self):
        with self.assertRaises(InvalidRequest):
            await self.call()

    # --- invariants --------------------------------------------------------------------------------
    @covers("At most one cognitive run owns a continuity at any instant.",
            "Uniqueness of the cognitive run is guaranteed durably by runtime.status, run_id and expected_revision")
    async def test_concurrent_activations_run_one_cognitive_chain(self):
        await asyncio.gather(*[self.call(continuity_id="c1") for _ in range(6)])
        await self.e.drain()
        self.assertEqual(len(self.requests), 1)                       # one message, one inference
        self.assertEqual(self.w.entries("c1"), [])
        # the guarantee is durable: a takeover needs the run to be gone, a live one is never taken
        self.w.runtime("c1", "running", "R1", None)
        liveness.mark_live("c1", "R1")
        out = await self.call(continuity_id="c1", wake=True)
        self.assertEqual((out["status"], out["run_id"]), ("running", "R1"))
        liveness.drop_live("c1", "R1")

    @covers("initial_intention is immutable.", "current_intention changes only through an explicit traced update.")
    async def test_only_current_intention_changes_and_the_change_is_traced(self):
        self.script = [wrap({"intention_update": "new direction", "messages": say()["messages"]})]
        await self.activate()
        c = self.w.state("continuity_c1")
        self.assertEqual((c["initial_intention"], c["current_intention"]), ("goal", "new direction"))
        self.assertIn("intention_update", self.events())

    @covers("A durable message is immutable once appended.", "The journal remains the durable cognitive history.")
    async def test_history_only_grows(self):
        before = self.w.journal("c1")
        await self.activate()
        after = self.w.journal("c1")
        self.assertEqual(after[:len(before)], before)                  # the prefix is byte-identical
        self.assertGreater(len(after), len(before))
        self.assertEqual(sorted(m["seq"] for m in after), list(range(1, len(after) + 1)))

    @covers("A pilot output is validated before any requested effect begins.",
            "Fails the current treatment if both the initial pilot output and its single correction are invalid.")
    async def test_an_invalid_output_has_no_effect_and_the_treatment_fails_after_one_correction(self):
        bad = wrap({"intentions": [awaited("i1")], "intention_update": "sneaky", "wait_group": {"condition": "n_of_m"}})       # n_of_m with no n: invalid whatever the model knows of identifiers
        self.script = [bad]
        await self.activate()
        self.assertEqual(len(self.requests), 6)                        # three attempts of one inference + one correction, no more
        self.assertEqual(self.tool_calls, [])                           # no intention was emitted
        self.assertEqual(self.w.state("continuity_c1")["current_intention"], "goal")      # nor the update applied
        self.assertEqual(self.rt()["active_wait_groups"], [])
        self.assertIn("treatment_failed", self.events())
        self.assertEqual([e["status"] for e in self.w.entries("c1")], ["failed"])          # released each time, not rolled back, parked on the third
        invalid = [m["content"] for m in self.w.journal("c1") if isinstance(m["content"], dict) and m["content"].get("event") == "validation_error"]
        self.assertTrue(invalid and all("n_of_m" in e["output"] for e in invalid))          # the invalid output, as the model wrote it, is kept for the diagnosis

    @covers("Requested effects are applied in deterministic order.")
    async def test_effects_are_applied_in_the_documented_order(self):
        out = {"intention_update": "g2", "wait_group": {"condition": "all", "expected": [{"type": "intention", "ref": "w1"}]},
               "intentions": [awaited("w1"), {"id": "f1", "target_capacity": "/root/tools/echo", "parameters": {}, "await_response": False}],
               "messages": [{"to": "user:1", "kind": "user", "content": "sent last"}]}
        self.script = [wrap(out), wrap(say("done"))]
        await self.activate()
        kinds = []
        for m in self.w.journal("c1"):
            c = m["content"]
            if isinstance(c, dict) and c.get("event") in ("intention_update", "wait_group_created", "message_out"):
                kinds.append(c["event"])
            elif m["kind"] == "capacity_intention":
                kinds.append("intention:" + c["id"])
        self.assertEqual(kinds[:5], ["intention_update", "wait_group_created", "intention:w1", "intention:f1", "message_out"])

    @covers("An already successful effect is never implicitly replayed.", "An already emitted intention is never implicitly re-emitted.",
            "Does not silently replay uncertain effects after interruption or recovery.", "Does not roll back already materialized effects.")
    async def test_a_recovered_run_never_replays_what_was_emitted(self):
        # a previous run emitted i1 (trace in the journal, tool called once) then died holding m1
        self.script = [wrap({"intentions": [{"id": "f1", "target_capacity": "/root/tools/echo", "parameters": {}, "await_response": False}]}), wrap(say("after"))]
        await self.activate()
        self.assertEqual(len(self.tool_calls), 1)
        self.w.full("c1", messages=self.w.journal("c1") + [message(len(self.w.journal("c1")) + 1, "m2")], entries=[entry("m2", status="claimed", claimed_by="DEAD")],
                    status="running", run_id="DEAD", current="m2", structure=False)
        self.script = [wrap(say("recovered"))]
        await self.activate()
        self.assertEqual(len(self.tool_calls), 1)                         # f1 was not emitted a second time
        self.assertIn("run_recovered", self.events())
        self.assertEqual(self.w.entries("c1"), [])
        self.assertTrue(any(m["kind"] == "capacity_intention" for m in self.w.journal("c1")))   # effects stay real in the history

    @covers("A takeover does not answer again a message whose chain already ended")
    async def test_a_takeover_does_not_answer_again_a_message_whose_chain_already_ended(self):
        """S44, deterministic: the dead run wrote the last message of the chain and died before acknowledging the message."""
        answered = message(2, "out1", kind="pilot", content={"event": "message_out", "kind": "user", "content": "first answer", "ends_chain": True}, to="user:1",
                           correlation={"causation_id": "m1"})
        self.w.full("c1", messages=[message(1, "m1"), answered], entries=[entry("m1", status="claimed", claimed_by="DEAD")],
                    status="running", run_id="DEAD", current="m1", structure=False)
        self.script = [wrap(say("second answer"))]
        await self.activate()
        said = [m["content"]["content"] for m in self.w.journal("c1") if m["kind"] == "pilot"]
        self.assertEqual(said, ["first answer"])                                          # not answered again
        self.assertEqual(self.requests, [])                                               # and the model was not even asked
        self.assertEqual(self.w.entries("c1"), [])                                        # the message is acknowledged
        recovered = [m["content"] for m in self.w.journal("c1") if isinstance(m["content"], dict) and m["content"].get("event") == "run_recovered"]
        self.assertEqual([(r["message_id"], r.get("already_answered")) for r in recovered], [("m1", True)])
        self.assertIsNone(self.rt()["current_message_id"])

    async def test_a_chain_interrupted_before_its_last_message_is_presented_again(self):
        """No proof of an ended chain (a message in the middle of it, or a message of another one): the takeover presents the message again, which may answer
        it twice but never loses the end of the chain."""
        for name, line in (("mid-chain", message(2, "out1", kind="pilot", content={"event": "message_out", "kind": "user", "content": "partial"}, to="user:1",
                                                correlation={"causation_id": "m1"})),
                           ("another message's", message(2, "out1", kind="pilot", content={"event": "message_out", "kind": "user", "content": "other", "ends_chain": True},
                                                       to="user:1", correlation={"causation_id": "m0"}))):
            self.w.full("c1", messages=[message(1, "m1"), line], entries=[entry("m1", status="claimed", claimed_by="DEAD")],
                        status="running", run_id="DEAD", current="m1", structure=False)
            self.requests.clear()
            self.script = [wrap(say("the rest"))]
            await self.activate()
            self.assertEqual(len(self.requests), 1, name)                                  # presented again
            self.assertEqual([m["content"]["content"] for m in self.w.journal("c1") if m["kind"] == "pilot"][-1], "the rest", name)
            self.e.matters.pop("journal_c1", None)

    @covers("Every message the pilot writes carries the message it answers")
    async def test_the_messages_of_the_pilot_carry_their_cause_and_the_last_of_a_final_step_ends_the_chain(self):
        self.script = [wrap({"intentions": [awaited("t1")], "wait_group": {"condition": "all"},
                             "messages": [{"to": "user:1", "kind": "user", "content": "working"}]}),
                       wrap({"messages": [{"to": "user:1", "kind": "user", "content": "one"}, {"to": "user:1", "kind": "user", "content": "two"}]})]
        await self.activate()
        await self.e.drain()
        out = [(m["content"]["content"], m["content"].get("ends_chain"), (m.get("correlation") or {}).get("causation_id"))
               for m in self.w.journal("c1") if m["kind"] == "pilot"]
        causes = {c for _text, _end, c in out}
        self.assertEqual(causes, {"m1"})                                                   # all answer the message in progress
        self.assertEqual([(text, end) for text, end, _c in out], [("working", None), ("one", None), ("two", True)])   # only the last of the final step ends the chain

    @covers("Stops requested-effect application on the first execution failure.")
    async def test_the_first_failing_effect_stops_the_rest(self):
        self.e.bad_refs.add("/root/tools/bad")
        out = {"intentions": [{"id": "a", "target_capacity": "/root/tools/echo", "parameters": {}, "await_response": False},
                              {"id": "b", "target_capacity": "/root/tools/bad", "parameters": {}, "await_response": False},
                              {"id": "c", "target_capacity": "/root/tools/echo", "parameters": {}, "await_response": False}],
               "messages": [{"to": "user:1", "kind": "user", "content": "never"}]}
        self.script = [wrap(out), wrap(say("replanned"))]
        await self.activate()
        stimulus = json.loads(self.requests[1]["messages"][-1]["content"])["active_stimulus"]
        self.assertEqual((stimulus["succeeded"], stimulus["failed"], stimulus["remaining"]), (["intention:a"], "intention:b", ["intention:c", "message:0"]))
        self.assertEqual(len(self.tool_calls), 1)
        self.assertNotIn("never", [m["content"] for m in self.w.journal("c1")])

    @covers("Waiting state is explicit in runtime.active_wait_groups.")
    async def test_waiting_is_explicit_and_durable(self):
        gate = asyncio.Event()

        async def slow(port, params):
            await gate.wait()
            return {}
        self.e.caps["/root/tools/slow"] = slow
        self.script = [wrap({"wait_group": {"condition": "all", "expected": [{"type": "intention", "ref": "w"}]},
                             "intentions": [awaited("w", "/root/tools/slow")]}), wrap(say())]
        task = asyncio.ensure_future(self.call(continuity_id="c1"))
        for _ in range(100):
            await asyncio.sleep(0.01)
            if self.rt()["status"] == "waiting":
                break
        rt = self.rt()
        self.assertEqual(rt["status"], "waiting")
        self.assertEqual([(g["id"] is not None, g["status"], g["condition"]) for g in rt["active_wait_groups"]], [(True, "waiting", "all")])
        self.assertEqual(rt["current_message_id"], "m1")
        gate.set()
        await task
        await self.e.drain()
        self.assertEqual(self.rt()["active_wait_groups"], [])

    @covers("The wrapper owns orchestration but no hidden persistent cognitive state.")
    async def test_no_state_is_kept_between_runs(self):
        await self.activate()
        self.assertEqual(liveness._live, {})
        self.assertEqual(locks._locks, {})
        self.assertEqual(pilot_activate._background, set())
        rt = self.rt()
        self.assertEqual((rt["status"], rt["run_id"], rt["current_message_id"], rt["active_wait_groups"]), ("idle", None, None, []))

    @covers("The Matters of the continuity are resolved from continuity_id by the convention")
    async def test_only_the_derived_matters_are_written(self):
        self.w.full("c2", messages=[message(1, "z1")], entries=[entry("z1")])
        await self.activate("c1")
        touched = {m for k, m, *_ in self.e.log}
        self.assertTrue(touched <= {"continuity_c1", "journal_c1", "mailbox_c1", "runtime_c1"}, touched)
        self.assertEqual([e["message_id"] for e in self.w.entries("c2")], ["z1"])

    @covers("Ownership transitions of the runtime (idle to running, running to idle, waiting and suspension changes)")
    async def test_every_runtime_write_is_guarded_by_expected_revision(self):
        await self.activate()
        runtime_writes = self.e.writes("runtime_c1")
        self.assertGreaterEqual(len(runtime_writes), 3)               # idle->running, current message, running->idle
        self.assertTrue(all(w[2] is not None for w in runtime_writes))
        self.assertTrue(all(w[2] is not None for w in self.e.log if w[0] == "write"))

    # --- failures -----------------------------------------------------------------------------------
    @covers("Fails if required Matters or pilot_definition are incoherent.")
    async def test_missing_matters_or_pilot_fail_without_ownership(self):
        for gone in ("runtime_c1", "mailbox_c1", PILOT_ID):
            self.w.full("c1", messages=[message(1, "m1")], entries=[entry("m1")], structure=False)
            self.e.matters.setdefault(PILOT_ID, {"data": {}, "functional": {}, "brique": {"revision": 1}})
            saved = self.e.matters.pop(gone)
            before = self.e.snapshot()
            with self.assertRaises(ConsistencyError):
                await self.call(continuity_id="c1")
            self.assertUnchanged(before)
            self.e.matters[gone] = saved

    @covers("Fails if exclusive ownership cannot be established consistently.", "Fails explicitly with a concurrency failure when the bounded retries")
    async def test_ownership_that_cannot_be_acquired_fails_explicitly(self):
        self.e.always_conflict.add("runtime_c1")
        with self.assertRaises(ConcurrencyError):
            await self.call(continuity_id="c1")
        self.assertEqual(self.requests, [])                            # no cognition without ownership
        self.assertEqual(self.rt()["status"], "idle")

    async def test_a_suspended_continuity_is_not_activated(self):
        self.w.runtime("c1", "suspended")
        out = await self.call(continuity_id="c1")
        self.assertEqual(out["status"], "suspended")
        self.assertEqual(self.requests, [])


    # --- succession, compaction pressure and contents by reference -----------------------------------------
    def limits(self, **limits):
        self.e.matters[PILOT_ID]["data"]["limits"] = limits

    def with_usage(self, output, tokens):
        return {"response": {"output": {"format": "json", "content": output}, "usage": {"input_tokens": tokens}}}

    async def test_the_activation_measure_carries_the_breakdown_and_the_window_of_the_pilot_ref(self):
        self.script = [{"response": {"output": {"format": "json", "content": say()},
                                     "usage": {"input_tokens": 900, "cached_input_tokens": 800, "cache_creation_input_tokens": 50, "output_tokens": 12},
                                     "resolved": {"provider": "fake", "model": "m"}, "duration_ms": 77}}]
        await self.activate()
        measure = self.measures()[-1]
        self.assertEqual({k: measure.get(k) for k in ("input_tokens", "cache_read_tokens", "cache_creation_tokens", "uncached_input_tokens",
                                                       "output_tokens", "model", "duration_ms")},
                         {"input_tokens": 900, "cache_read_tokens": 800, "cache_creation_tokens": 50, "uncached_input_tokens": 50,
                          "output_tokens": 12, "model": "fake/m", "duration_ms": 77})

    @covers("An archived continuity is never activated.")
    async def test_an_archived_continuity_is_never_activated(self):
        state = self.w.state("continuity_c1")
        state.update(status="archived", successor_id="c1_g2")
        self.e.matters["continuity_c1"]["data"] = state
        before = self.e.snapshot()
        out = await self.activate()
        self.assertEqual((out["status"], out["run_id"]), ("archived", None))
        self.assertEqual(self.requests, [])
        self.assertEqual(self.e.snapshot(), before)

    @covers("The size of the context sent by the last inference (input_tokens reported by the provider")
    async def test_the_context_size_is_a_journal_event_with_the_provider_measure_or_an_estimation(self):
        self.script = [self.with_usage(say(), 1234)]
        await self.activate()
        self.assertEqual(self.measures(), [{"event": "inference_measured", "input_tokens": 1234, "message_id": "m1"}])
        self.assertNotIn("context_tokens", self.rt())                       # a measure of the journal, not a state of runtime
        self.w.full("c2", messages=[message(1, "m1", content="hello")], entries=[entry("m1")])
        self.script = [wrap(say())]                      # no usage reported: estimated from the request, never absent
        await self.call(continuity_id="c2")
        await self.e.drain()
        measure = self.measures("c2")[0]
        self.assertGreater(measure["input_tokens"], 0)
        self.assertTrue(measure["estimated"])
        kinds = [m["kind"] for m in self.w.journal("c1")]
        self.assertLess(kinds.index("event"), kinds.index("pilot"))         # measured before the decision is applied

    @covers("When the pilot declares limits.context_tokens, the inference context carries context_pressure from the advised level.")
    async def test_the_model_of_a_pilot_definition_never_sees_the_pressure_of_the_context(self):
        """Compacting is a measured threshold, so code: a model shown the advised level succeeded too early, and each successor began again."""
        self.limits(context_tokens=1000)
        self.script = [self.with_usage(say(), 300)]
        await self.activate()
        self.w.full("c1", messages=[message(1, "m1"), message(2, "m2")], entries=[entry("m2")],
                    status="idle", run_id=None)
        self.w.lay_journal("c1", [message(1, "m1"), message(2, "m2"), self.measured("c1", 700, 3)])        # the last measure: 0.7 of the window, advised
        self.script = [self.with_usage(say(), 700)]
        await self.activate()
        news = json.loads(self.requests[-1]["messages"][-1]["content"])
        self.assertNotIn("context_pressure", news)                                                         # advised: nothing for the model
        self.assertNotIn("if_you_succeed", json.dumps(self.requests[-1]["messages"]))
        self.assertNotIn("continuity_c1_g2", self.e.matters)                                               # and no succession by itself below the required level

    @covers("At the required level, at a safe point with normal work pending, a compaction inference produces a summary and the continuity succeeds before the next message is processed; a failed compaction leaves the continuity unchanged and is traced.",
            "The compaction inference is one more turn of the conversation")
    async def test_compaction_at_the_safe_point_then_succession(self):
        self.limits(context_tokens=1000)
        self.w.full("c1", messages=[message(1, "m1", content="first"), message(2, "m2", content="second")], entries=[entry("m1"), entry("m2")])

        def answer(request):
            if "too full" in request["messages"][-1]["content"]:
                return wrap({"succession": {"summary": "They said first; now second."}})
            return self.with_usage(say("answered"), 900)             # 0.9 of the window: required after the first message
        self.script = [answer]
        self.e.caps["/root/agentic/pilot.activate"] = pilot_activate.run      # the successor is woken by the transfer
        await self.activate()
        await self.e.drain()
        source, successor = self.w.state("continuity_c1"), self.successor_state()
        self.assertEqual((source["status"], source["successor_id"]), ("archived", "c1_g2"))
        self.assertEqual(successor["messages"][0]["kind"], "summary")
        self.assertEqual(successor["messages"][0]["content"], "They said first; now second.")
        self.assertIn("m2", [m["id"] for m in successor["messages"]])          # the pending message moved with the work
        self.assertNotIn("m2", [e["message_id"] for e in self.w.entries("c1")])
        self.assertEqual([m["content"]["content"] for m in successor["messages"] if m["kind"] == "pilot"], ["answered"])
        compactions = [r for r in self.requests if "too full" in r["messages"][-1]["content"]]
        self.assertEqual(len(compactions), 1)
        normal = next(r for r in self.requests if r not in compactions)
        self.assertEqual(compactions[0]["messages"][0], normal["messages"][0])                  # the same system text, the cache prefix
        self.assertEqual(compactions[0]["output"], normal["output"])                              # the same schema: changing it would drop the cache
        self.assertNotIn("too full", compactions[0]["messages"][0]["content"])                   # what is asked is the last message, never the start

    async def test_a_failed_compaction_is_not_asked_again_by_the_same_run(self):
        self.limits(context_tokens=1000)
        self.w.full("c1", messages=[message(1, "m1"), message(2, "m2"), message(3, "m3")], entries=[entry("m1"), entry("m2"), entry("m3")])
        self.script = [lambda r: wrap(say("no summary here")) if "too full" in r["messages"][-1]["content"] else self.with_usage(say(), 900)]
        await self.activate()
        self.assertEqual(len([r for r in self.requests if "too full" in r["messages"][-1]["content"]]), 1)      # three messages, one attempt
        self.assertEqual(self.w.entries("c1"), [])

    async def test_a_failed_compaction_leaves_the_continuity_unchanged_and_is_traced(self):
        self.limits(context_tokens=1000)
        self.w.full("c1", messages=[message(1, "m1"), message(2, "m2")], entries=[entry("m1"), entry("m2")])
        self.script = [lambda r: wrap(say("no summary here")) if "too full" in r["messages"][-1]["content"] else self.with_usage(say(), 900)]
        await self.activate()
        self.assertEqual(self.w.state("continuity_c1")["status"], "active")
        self.assertIn("compaction_failed", self.events())
        self.assertEqual(self.w.entries("c1"), [])                      # both messages were processed normally

    # --- the policy of a pilot: code, before any effect ---------------------------------------------------------------------
    def policy(self, **patterns):
        self.e.matters[PILOT_ID]["data"].update(patterns)

    @covers("A pilot policy is applied by the code before any effect")
    async def test_a_forbidden_target_refuses_the_whole_output_before_any_effect(self):
        self.policy(forbidden_capacities=["/root/tools/bad*"])
        self.script = [wrap({"intentions": [awaited("a"), awaited("b", "/root/tools/bad.thing")], "wait_group": {"condition": "all"},
                             "intention_update": "sneaky", "messages": [{"to": "user:1", "kind": "user", "content": "never said"}]}), wrap(say("ok"))]
        await self.activate()
        self.assertEqual(self.tool_calls, [])                                             # not even the allowed one
        self.assertEqual(self.w.state("continuity_c1")["current_intention"], "goal")      # nor the update
        self.assertEqual(self.rt()["active_wait_groups"], [])                             # nor the wait
        self.assertEqual([m["kind"] for m in self.w.journal("c1") if m["kind"] in ("capacity_intention", "pilot")], ["pilot"])    # only the later "ok"
        stimulus = json.loads(self.requests[1]["messages"][-1]["content"])["active_stimulus"]
        self.assertEqual((stimulus["kind"], stimulus["failed"], stimulus["succeeded"]), ("execution_result", "intention:b", []))
        self.assertEqual(stimulus["remaining"], ["intention:a", "message:0"])
        self.assertIn("forbidden by the pilot policy", stimulus["error"])
        self.assertIn("intentions_refused", self.events())
        self.assertIn("Never invoke (refused before any effect): /root/tools/bad*", self.requests[0]["messages"][0]["content"])   # and the model was told

    @covers("A pilot that asks three times in a row for what its policy refuses fails its treatment")
    async def test_a_pilot_that_keeps_asking_for_what_is_forbidden_fails_after_three_refusals(self):
        self.policy(forbidden_capacities=["/root/tools/*"])
        self.script = [wrap({"intentions": [awaited("a")], "wait_group": {"condition": "all"}})]       # the same wish, whatever it is told
        await self.activate()
        self.assertEqual(len(self.requests), 9)                                                       # three refusals per attempt, three attempts: not fifty
        self.assertEqual(self.events().count("intentions_refused"), 9)
        self.assertIn("treatment_failed", self.events())
        self.assertEqual(self.tool_calls, [])
        self.assertEqual(next(e for e in self.w.entries("c1") if e["message_id"] == "m1")["attempts"], 3)    # and each counts as a failed attempt

    async def test_the_policy_of_a_capacity_pilot_lives_in_its_pilot_ref(self):
        seen = []

        async def plan(port, params):
            seen.append(params.get("stimulus"))
            if params.get("stimulus") is None:
                return {"intentions": [awaited("a", "/root/tools/echo")], "wait_group": {"condition": "all"}}
            return {"messages": [{"to": "user:1", "kind": "user", "content": "went on"}]}
        self.capacity_pilot(plan, policy={"allowed_capacities": ["/root/tools/other"]})
        await self.activate()
        self.assertEqual(self.tool_calls, [])                                             # echo is not among the allowed ones
        self.assertEqual(seen[1]["kind"], "execution_result")                              # the plan is told, as for a pilot_definition

    @covers("forbidden_capacities wins over allowed_capacities")
    def test_forbidden_wins_and_the_contract_readers_stay_allowed(self):
        from substrate.policy import refusal
        allowed = ["/root/tools/echo"]
        self.assertIsNone(refusal(allowed, [], "/root/tools/echo"))
        self.assertIsNotNone(refusal(allowed, [], "/root/tools/other"))                    # present: only these
        self.assertIsNone(refusal(allowed, [], "/root/read.capacity"))                     # the readers agentic exposes
        self.assertIsNone(refusal(allowed, [], "/root/shop/read.meaning"))
        self.assertIsNotNone(refusal(allowed, ["/root/read.capacity"], "/root/read.capacity"))      # unless forbidden names them
        self.assertIsNotNone(refusal(["/root/tools/*"], ["/root/tools/bad*"], "/root/tools/bad.one"))   # forbidden wins
        self.assertIsNone(refusal([], [], "/root/anything/goes"))                          # no policy: open

    @covers("A pilot policy may also name contexts")
    def test_a_context_covers_its_capacities_and_the_contexts_nested_in_it(self):
        from substrate.policy import refusal
        self.assertIsNotNone(refusal([], [], "/root/music/album.enrich", [], ["/root/music"]))                  # a capacity of the context
        self.assertIsNotNone(refusal([], [], "/root/music/streaming/encode", [], ["/root/music"]))              # of a context nested in it
        self.assertIsNone(refusal([], [], "/root/musical/x", [], ["/root/music"]))                              # not a neighbour with the same beginning
        self.assertIsNone(refusal([], [], "/root/shop/x", [], ["/root/music"]))
        self.assertIsNone(refusal(["/root/tools/echo"], [], "/root/shop/lookup", ["/root/shop"], []))           # a listed context is enough…
        self.assertIsNone(refusal(["/root/tools/echo"], [], "/root/tools/echo", ["/root/shop"], []))            # …as is a listed capacity
        self.assertIsNotNone(refusal(["/root/tools/echo"], [], "/root/other/x", ["/root/shop"], []))            # nothing else
        self.assertIsNotNone(refusal([], ["/root/shop/lookup"], "/root/shop/lookup", ["/root/shop"], []))       # a forbidden capacity beats an allowed context
        self.assertIsNotNone(refusal([], [], "/root/shop/lookup", ["/root/shop"], ["/root/shop"]))              # and a forbidden context an allowed one
        self.assertIsNone(refusal(["/root/tools/echo"], [], "/root/read.capacity", [], []))                     # the readers stay allowed

    async def test_a_forbidden_context_refuses_the_output_and_the_model_is_told(self):
        self.policy(forbidden_contexts=["/root/tools"])
        self.script = [wrap({"intentions": [awaited("a")], "wait_group": {"condition": "all"}}), wrap(say("ok"))]
        await self.activate()
        self.assertEqual(self.tool_calls, [])
        stimulus = json.loads(self.requests[1]["messages"][-1]["content"])["active_stimulus"]
        self.assertIn("context /root/tools", stimulus["error"])
        self.assertIn("anything in /root/tools", self.requests[0]["messages"][0]["content"])

    @covers("A target that is not a canonical absolute reference is refused by the policy.")
    def test_a_target_that_is_not_canonical_is_refused(self):
        from substrate.policy import refusal
        for ref in ("/root/./edit.delete", "/root//edit.delete", "/root/tools/../edit.delete", "root/edit.delete", "/root/edit.delete/", None):
            self.assertIsNotNone(refusal([], [], ref), ref)

    # --- a wait that nothing answers ends -----------------------------------------------------------------------------------------
    @covers("A wait group whose expected answers do not come within the wait delay")
    async def test_a_wait_that_nothing_answers_ends_with_error_answers(self):
        from unittest import mock
        self.script = [wrap({"wait_group": {"condition": "all", "expected": [{"type": "continuity", "ref": "child1"}]}}), wrap(say("decided"))]
        with mock.patch.object(pilot_activate, "WAIT_TIMEOUT_S", 0.05):
            await self.activate()
        self.assertEqual(len(self.requests), 2)                                           # the pilot decided after the wait
        timeout = next(m for m in self.w.journal("c1") if m["id"] == "timeout_c1_child1")
        self.assertEqual(timeout["content"]["error"]["code"], "timeout")
        stimulus = json.loads(self.requests[1]["messages"][-1]["content"])["active_stimulus"]
        self.assertEqual((stimulus["kind"], stimulus["received"], stimulus["expected"]), ("active_wait_result", 1, 1))
        self.assertEqual(self.rt()["active_wait_groups"], [])

    async def test_an_intention_that_answers_after_the_wait_delay_is_a_late_result(self):
        from unittest import mock

        async def slow(port, params):
            await asyncio.sleep(0.3)
            return {"ok": True}
        self.e.caps["/root/tools/slow"] = slow
        self.script = [wrap({"intentions": [awaited("i1", "/root/tools/slow")], "wait_group": {"condition": "all"}}), wrap(say("decided"))]
        with mock.patch.object(pilot_activate, "WAIT_TIMEOUT_S", 0.05):
            await self.activate()
        self.assertEqual(len(self.requests), 2)                                           # the late real answer opened no inference
        ids = [m["id"] for m in self.w.journal("c1") if m["kind"] == "capacity_response"]
        self.assertEqual(sorted(ids), ["response_c1_i1", "timeout_c1_i1"])                # both are in the history

    # --- recovery, pressure of a Capacity pilot, notification ---------------------------------------------------------------------
    @covers("The stimulus of a recovery states what the journal knows of the interrupted chain")
    async def test_the_recovery_stimulus_says_what_the_journal_knows(self):
        intention = lambda seq, ref, wait: message(seq, f"intention_c1_{ref}", kind="capacity_intention", content={"id": ref, "target_capacity": "/root/tools/echo", "parameters": {}, "await_response": wait})
        self.w.full("c1", messages=[message(1, "m1"), intention(2, "i1", True), message(3, "r1", kind="capacity_response", content={"response": {"status": "ok"}}, correlation={"intention_id": "i1"}),
                                    intention(4, "i2", False), intention(5, "i3", True)],
                    entries=[entry("m1", status="claimed", claimed_by="DEAD")], status="running", run_id="DEAD", current="m1")
        await self.activate()
        stimulus = json.loads(self.requests[0]["messages"][-1]["content"])["active_stimulus"]
        self.assertEqual((stimulus["kind"], stimulus["message_id"]), ("recovery", "m1"))
        self.assertEqual(stimulus["emitted"], [{"id": "i1", "await_response": True, "answered": True}, {"id": "i2", "await_response": False, "answered": False},
                                               {"id": "i3", "await_response": True, "answered": False}])
        self.assertTrue(stimulus["run_id"])
        self.assertEqual(self.tool_calls, [])                                               # and nothing was replayed

    @covers("A Capacity pilot has no pilot_definition and no model of its own")
    async def test_a_capacity_pilot_is_given_the_pressure_but_never_compacted(self):
        self.w.full("c1", messages=[message(1, "m1"), message(2, "m2"), self.measured("c1", 900, 3)], entries=[entry("m1"), entry("m2")])
        seen = []

        async def plan(port, params):
            seen.append(params.get("context_pressure"))
            return {"messages": [{"to": "user:1", "kind": "user", "content": "ok"}]}
        self.capacity_pilot(plan, limits={"context_tokens": 1000})
        await self.activate()
        self.assertEqual((seen[0]["level"], seen[0]["window"], seen[0]["tokens"]), ("required", 1000, 900))     # the plan is told…
        self.assertEqual(self.w.state("continuity_c1")["status"], "active")                                         # …and decides: nothing is compacted for it
        seen.clear()
        self.capacity_pilot(plan)                                                                                   # no window declared: no pressure given
        self.w.full("c1", messages=[message(1, "m1"), self.measured("c1", 900, 2)], entries=[entry("m1")])
        self.capacity_pilot(plan)
        await self.activate()
        self.assertEqual(seen, [None])

    @covers("A waiting run is woken by notification")
    async def test_a_waiting_run_is_woken_by_notification_not_by_the_poll(self):
        from unittest import mock

        async def slow(port, params):
            await asyncio.sleep(0.05)
            return {"ok": True}
        self.e.caps["/root/tools/echo"] = slow
        self.script = [wrap({"intentions": [awaited("i1")], "wait_group": {"condition": "all"}}), wrap(say("done"))]
        with mock.patch.object(pilot_activate, "POLL_S", 30.0):             # were the poll the way a wait ends, this would last half a minute
            await asyncio.wait_for(self.activate(), 10)
        self.assertEqual(len(self.requests), 2)

    @covers("The family of a capacity listed in known_capacities is the one the pilot_definition declares")
    async def test_the_family_of_a_known_capacity_is_the_one_the_definition_declares(self):
        self.policy(known_capacities=[{"ref": "/root/tools/echo", "description": "Echo."}, {"ref": "/root/engine/read.x", "target_type": "reflexive", "description": "A reader."}])
        self.script = [wrap({"intentions": [dict(awaited("a"), target_type="matter"), dict(awaited("b", "/root/engine/read.x"), target_type="user"),
                                            dict(awaited("c", "/root/elsewhere/do"), target_type="matter")], "wait_group": {"condition": "all"}})]
        await self.activate()
        system = self.requests[0]["messages"][0]["content"]
        listed = json.loads(system.split("Available capacities (target_capacity = ref):\n", 1)[1].split("\n", 1)[0])
        self.assertEqual([(c["ref"], c["target_type"]) for c in listed], [("/root/tools/echo", "user"), ("/root/engine/read.x", "reflexive")])    # the model sees the family
        fixed = pilot_activate.declare_families(self.e.matters[PILOT_ID]["data"], {"intentions": [dict(awaited("a"), target_type="matter"), dict(awaited("b", "/root/engine/read.x"), target_type="user"),
                                                                                              dict(awaited("c", "/root/elsewhere/do"), target_type="matter")]})
        self.assertEqual([i["target_type"] for i in fixed["intentions"]], ["user", "reflexive", "matter"])      # the architect's word wins; an unknown target is left as written

    @covers("The decision of a pilot_definition")
    async def test_the_decision_is_recorded_before_its_effects_and_is_what_the_model_reads_back(self):
        self.script = [wrap({"intentions": [awaited("i1")], "wait_group": {"condition": "all"}, "intention_update": "a new goal"}), wrap(say("done"))]
        await self.activate()
        journal = self.w.journal("c1")
        decisions = [m for m in journal if m["kind"] == "completion"]
        self.assertEqual([m["content"]["step"] for m in decisions], ["decision", "decision"])
        first = decisions[0]["content"]["output"]
        self.assertEqual(first, {"intentions": [dict(awaited("i1"), target_type="user")], "wait_group": {"condition": "all"}, "intention_update": "a new goal"})   # as the model wrote it, with what the code wrote:
        self.assertNotIn("id", first["wait_group"])                                                                                     # nothing the code adds
        effect = next(m["seq"] for m in journal if m["kind"] == "capacity_intention")
        self.assertLess(decisions[0]["seq"], effect)                                                                                    # before any effect
        history = [m for m in self.requests[1]["messages"][1:-1]]
        said = [m for m in history if m["role"] == "assistant"]
        self.assertEqual(len(said), 1)                                                                                                  # its decision, once…
        self.assertEqual(json.loads(said[0]["content"])["intention_update"], "a new goal")                                              # …with what it had decided
        self.assertNotIn('"target_capacity":"/root/tools/echo","await_response"', json.dumps(history))                                 # not also the lines of its effects

    # --- a message that keeps failing is parked -------------------------------------------------------------------------------------
    @covers("A treatment that fails is traced and its entry released with one more attempt")
    async def test_a_message_that_keeps_failing_is_parked_and_the_run_goes_on(self):
        from substrate.errors import Refused
        self.w.full("c1", messages=[message(1, "m1"), message(2, "m2")], entries=[entry("m1"), entry("m2")])

        def answer(request):
            if json.loads(request["messages"][-1]["content"])["current_message"]["id"] == "m1":
                raise Refused("provider down")
            return wrap(say("answered m2"))
        self.script = [answer]
        await self.activate()                                                                     # one wake: three attempts at once, then the next message
        entries = {e["message_id"]: e for e in self.w.entries("c1")}
        self.assertEqual((entries["m1"]["status"], entries["m1"]["attempts"]), ("failed", 3))      # tried again at once, parked on the third failure…
        self.assertNotIn("m2", entries)                                                            # …and the run went on: m2 was answered
        self.assertEqual([m["content"]["content"] for m in self.w.journal("c1") if m["kind"] == "pilot"], ["answered m2"])
        self.assertEqual(len([e for e in self.events() if e == "treatment_failed"]), 3)
        self.assertIn("message_parked", self.events())
        self.assertEqual(self.rt()["status"], "idle")

    async def test_a_parked_message_takes_its_wait_groups_with_it(self):
        """A group satisfied but never consumed must not become the stimulus of the next message, nor block the compaction."""
        from substrate.errors import Refused
        self.w.full("c1", messages=[message(1, "m1")], entries=[entry("m1")])
        state = {"calls": 0}

        def answer(request):
            state["calls"] += 1
            if state["calls"] == 1:
                return wrap({"intentions": [awaited("i1")], "wait_group": {"condition": "all"}})       # the group, answered by the tool
            raise Refused("provider down")                                                            # m1 fails after its group is satisfied
        self.script = [answer]
        for _ in range(3):
            await self.activate()
        entries = {e["message_id"]: e for e in self.w.entries("c1")}
        self.assertEqual(entries["m1"]["status"], "failed")
        self.assertEqual(self.rt()["active_wait_groups"], [])
        abandoned = [m["content"] for m in self.w.journal("c1") if m["kind"] == "event" and m["content"].get("event") == "wait_groups_abandoned"]
        self.assertEqual(len(abandoned), 1)
        self.assertEqual(abandoned[0]["message_id"], "m1")
        self.assertEqual(len(abandoned[0]["wait_group_ids"]), 1)
        self.assertEqual([e for e in self.w.entries("c1") if e["priority"] == "expected"], [])      # the answer the group captured is acknowledged, not left claimed

    async def test_a_message_claimed_by_a_dead_run_that_never_started_it_is_put_back_and_answered(self):
        """The window of a hard kill between the claim of a message and the record that it is being treated."""
        self.w.full("c1", messages=[message(1, "m1")], entries=[entry("m1", status="claimed", claimed_by="run_dead")],
                    status="running", run_id="run_dead", current=None)
        self.script = [wrap(say("answered m1"))]
        await self.activate()
        self.assertEqual([m["content"]["content"] for m in self.w.journal("c1") if m["kind"] == "pilot"], ["answered m1"])   # not left claimed forever
        self.assertEqual(self.w.entries("c1"), [])
        self.assertEqual(self.rt()["status"], "idle")

    async def test_the_answers_a_dead_run_had_claimed_for_a_group_are_still_adopted(self):
        self.w.full("c1", messages=[message(1, "m1")], entries=[entry("m1", status="claimed", claimed_by="run_dead"),
                                                                  entry("response_x", priority="expected", status="claimed", claimed_by="run_dead")],
                    status="running", run_id="run_dead", current="m1")
        self.script = [wrap(say("answered"))]
        await self.activate()
        self.assertNotIn("run_dead", [e.get("claimed_by") for e in self.w.entries("c1")])                  # none is left to a dead run

    @covers("Each answer of a capacity in the conversation says which intention it answers")
    async def test_the_answer_of_an_intention_says_which_intention_it_answers(self):
        self.script = [wrap({"intentions": [awaited("i1")], "wait_group": {"condition": "all"}}), wrap(say("done"))]
        await self.activate()
        answers = [m["content"] for m in self.requests[1]["messages"] if m["role"] == "user" and m["content"].startswith('{"intention_id"')]
        self.assertEqual(len(answers), 1)
        self.assertEqual(json.loads(answers[0])["intention_id"], "i1")

    # --- failures are said, never lost in silence --------------------------------------------------------------------
    @covers('A Response that mailbox.send does not take is offered again')
    async def test_a_response_that_mailbox_send_does_not_take_at_first_is_offered_again(self):
        from unittest import mock
        from substrate.errors import ConcurrencyError
        import mailbox_send
        attempts = []

        async def flaky(port, params):
            if params["message"]["kind"] == "capacity_response":
                attempts.append(1)
                if len(attempts) < 3:
                    raise ConcurrencyError("conflict")
            return await mailbox_send.run(port, params)
        self.e.caps["/root/agentic/mailbox.send"] = flaky
        self.script = [wrap({"intentions": [awaited("i1")], "wait_group": {"condition": "all"}}), wrap(say("done"))]
        with mock.patch.object(pilot_activate, "DELIVERY_BACKOFF_S", 0):
            await self.activate()
        self.assertEqual(len(attempts), 3)                                    # twice refused, then taken
        self.assertEqual(len(self.requests), 2)                               # so the wait ended and the pilot went on
        self.assertEqual(self.rt()["active_wait_groups"], [])

    async def test_a_response_that_cannot_be_delivered_is_traced_not_lost_in_silence(self):
        from unittest import mock
        from substrate.errors import ConcurrencyError
        from substrate.ids import matter_ids

        async def broken(port, params):
            raise ConcurrencyError("conflict")
        self.e.caps["/root/agentic/mailbox.send"] = broken
        ctx = pilot_activate.Ctx(self.p, "c1", matter_ids("c1"), {})
        with mock.patch.object(pilot_activate, "DELIVERY_BACKOFF_S", 0):
            await pilot_activate._awaited(ctx, {"id": "i1", "target_capacity": "/root/tools/echo", "parameters": {}}, "user")
        self.assertIn("response_delivery_failed", self.events())

    @covers('A not-found that does not come from the disappearance of the continuity')
    async def test_something_not_found_by_the_treatment_is_a_failure_not_a_deletion(self):
        from substrate.errors import NotFound

        async def no_provider(port, params):
            raise NotFound("no such provider")
        self.e.caps["/root/llm/infer"] = no_provider
        out = await self.activate()
        self.assertEqual(out["status"], "idle")                                # not "deleted": the continuity is there
        self.assertIn("treatment_failed", self.events())                       # and the failure is said
        self.assertEqual([e["status"] for e in self.w.entries("c1")], ["failed"])
        self.assertEqual(self.rt()["status"], "idle")

    @covers('The control set_pilot takes effect from the next inference of the same run.')
    async def test_set_pilot_changes_who_decides_for_the_rest_of_the_run(self):
        import copy
        second = copy.deepcopy(self.e.matters[PILOT_ID])
        second["data"].update(id="pilot_second", prompt="SECOND PILOT")
        self.e.matters["pilot_second"] = second
        self.w.full("c1", messages=[message(1, "m1", content="hello"),
                                    message(2, "ctl", kind="control", content={"action": "set_pilot", "pilot_ref": {"context": "/root/agentic", "matter_id": "pilot_second"}})],
                    entries=[entry("m1"), entry("ctl", priority="control")])
        await self.activate()
        self.assertEqual(len(self.requests), 1)
        self.assertIn("SECOND PILOT", self.requests[0]["messages"][0]["content"])        # the control was applied before m1: m1 is the new pilot's

    @covers("A pilot output carrying succession is exclusive with intentions, wait_group, messages and projection_request; the message in progress is released so that the successor processes it.")
    async def test_a_succession_decision_hands_the_message_in_progress_to_the_successor(self):
        with self.assertRaises(InvalidRequest):
            pilot_activate.validate_output({"succession": {"summary": "s"}, "messages": [{"to": "u", "kind": "user", "content": "x"}]})
        with self.assertRaises(InvalidRequest):
            pilot_activate.validate_output({"succession": {"summary": ""}})
        self.script = [wrap({"succession": {"summary": "S"}}), wrap(say("continued"))]     # c1 decides, the successor answers
        self.e.caps["/root/agentic/pilot.activate"] = pilot_activate.run
        await self.activate()
        await self.e.drain()
        source = self.w.state("continuity_c1")
        self.assertEqual(source["status"], "archived")
        self.assertEqual(self.w.entries("c1"), [])
        self.assertEqual(self.w.rt("c1")["status"], "idle")
        successor = self.successor_state()
        self.assertEqual(successor["messages"][0]["content"], "S")
        self.assertIn("m1", [m["id"] for m in successor["messages"]])          # m1 is answered by the successor, not lost
        self.assertEqual([m["content"]["content"] for m in successor["messages"] if m["kind"] == "pilot"], ["continued"])

    @covers("A Response is written as is in its capacity_response line with its status preserved, whatever its size")
    async def test_a_large_response_is_written_as_is(self):
        blob = {"blob": "x" * 300_000}                                       # no threshold, no reference, no overflow Matter
        async def big(port, params):
            return blob
        self.e.caps["/root/tools/big"] = big
        self.script = [wrap({"intentions": [awaited("i1", "/root/tools/big")],
                             "wait_group": {"condition": "all", "expected": [{"type": "intention", "ref": "i1"}]}}), wrap(say("done"))]
        await self.activate()
        await self.e.drain()
        response = [m for m in self.w.journal("c1") if m["kind"] == "capacity_response"][0]["content"]["response"]
        self.assertEqual((response["status"], response["payload"]), ("ok", blob))
        self.assertFalse([m for m in self.e.matters if m.startswith("content_")])

    @covers("The request sent to the model and the projection view are transit values")
    async def test_the_request_and_the_view_are_transit_values(self):
        await self.activate()
        sent = json.dumps(self.requests[0])
        marker = self.requests[0]["messages"][0]["content"][:60]             # the stable system text of the request
        written = json.dumps(self.w.journal("c1")) + json.dumps({k: v for k, v in self.e.matters.items() if k != "pilot_default"})
        self.assertNotIn(marker, written)                                    # the request is never written to the journal or to a Matter
        self.assertNotIn("continuity_projection", written)                   # nor a view built for it
        self.assertIn("m1", sent)                                            # the journal reached the model, as the conversation

    async def test_the_cognitive_frame_opens_the_system_text_and_is_not_repeated_in_the_view(self):
        self.w.full("c1", messages=[message(1, "framework_c1", kind="framework", content={"text": "THE COGNITIVE FRAME", "source": "s"}),
                                    message(2, "m1")], entries=[entry("m1")])
        await self.activate()
        system = self.requests[0]["messages"][0]["content"]
        self.assertTrue(system.startswith("THE COGNITIVE FRAME\n\n"))                       # first, before the prompt of the pilot
        self.assertEqual(system.count("THE COGNITIVE FRAME"), 1)
        rest = json.dumps(self.requests[0]["messages"][1:])
        self.assertNotIn("THE COGNITIVE FRAME", rest)                                        # the conversation does not repeat it
        self.assertNotIn("framework", rest)

    async def test_the_request_is_the_stable_system_then_the_conversation_then_what_is_new(self):
        self.w.full("c1", messages=[message(1, "framework_c1", kind="framework", content={"text": "FRAME", "source": "s"}),
                                    message(2, "m0", content="earlier question"), message(3, "a0", kind="completion", content={"step": "decision", "output": {"messages": [{"to": "user:1", "kind": "user", "content": "earlier answer"}]}}),
                                    message(4, "m1", content="the question now")], entries=[entry("m1")])
        await self.activate()
        sent = self.requests[0]["messages"]
        self.assertEqual([m["role"] for m in sent], ["system", "user", "assistant", "user", "user"])
        self.assertEqual([m["content"] for m in sent[1:4]], ["earlier question", '{"messages":[{"content":"earlier answer","kind":"user","to":"user:1"}]}', "the question now"])    # the journal, in order
        news = json.loads(sent[4]["content"])                                                                        # and only what is new
        self.assertEqual(sorted(news), ["continuity_id", "current_intention", "current_message"])
        self.assertEqual(news["current_message"]["id"], "m1")
        self.assertEqual([m.get("cache") for m in sent], [True, None, None, True, None])        # the stable system and the end of the history end cacheable prefixes
        self.assertIn("Initial intention:", sent[0]["content"])                                  # immutable: in the stable part
        self.assertEqual(self.requests[0]["metadata"]["projection_ids"], ["framework_c1", "m0", "a0", "m1"])

    @covers('A request to the model ends with one message that holds what is new')
    async def test_the_news_designates_the_current_message_when_the_history_holds_it(self):
        self.w.full("c1", messages=[message(1, "framework_c1", kind="framework", content={"text": "FRAME", "source": "s"}),
                                    message(2, "m1", content="the question now")], entries=[entry("m1")])
        await self.activate()
        request = self.requests[0]
        news = json.loads(request["messages"][-1]["content"])
        self.assertEqual(news["current_message"], {"id": "m1", "kind": "user", "from": "user:1"})       # who and what, not the content again
        self.assertEqual(news["continuity_id"], "c1")                                                      # a pilot knows who it is: it cannot delegate without it
        self.assertEqual(json.dumps(request["messages"]).count("the question now"), 1)                    # once, in the history
        self.assertEqual(request["metadata"]["current_message"]["content"], "the question now")           # whoever observes the request has it whole

    async def test_the_news_carries_the_whole_current_message_when_the_history_does_not_hold_it(self):
        self.e.matters[PILOT_ID]["data"]["default_projection"] = {"include_kinds": ["pilot"]}             # a view without the user lines
        self.w.full("c1", messages=[message(1, "framework_c1", kind="framework", content={"text": "FRAME", "source": "s"}),
                                    message(2, "m1", content="the question now")], entries=[entry("m1")])
        await self.activate()
        news = json.loads(self.requests[0]["messages"][-1]["content"])
        self.assertEqual(news["current_message"]["content"], "the question now")                         # otherwise the model would never see it

    async def test_the_projection_of_a_pilot_definition_leaves_out_the_lines_of_effects(self):
        """The decision already says its effects: those lines are neither sent nor counted by a budget."""
        self.w.full("c1", messages=[
            message(1, "framework_c1", kind="framework", content={"text": "FRAME", "source": "s"}),
            message(2, "m1", content="the question"),
            message(3, "intention_c1_a", kind="capacity_intention", content={"id": "a", "target_capacity": "/root/tools/echo", "parameters": {}, "await_response": False}),
            message(4, "out1", kind="pilot", content={"event": "message_out", "kind": "pilot", "content": "said"})], entries=[entry("m1")])
        await self.activate()
        ids = self.requests[0]["metadata"]["projection_ids"]
        self.assertIn("m1", ids)
        self.assertNotIn("intention_c1_a", ids)
        self.assertNotIn("out1", ids)

    async def test_the_runtime_writes_identifiers_and_families_and_the_journal_keeps_them(self):
        self.script = [wrap({"intentions": [{"target_capacity": "/root/tools/echo", "parameters": {"q": 1}, "await_response": True},
                                            {"id": "i1", "target_capacity": "/root/tools/echo", "parameters": {"q": 2}, "await_response": True}],
                             "wait_group": {"condition": "all"}}), wrap(say("done"))]
        await self.activate()
        self.assertEqual(len(self.tool_calls), 2)                                                          # both ran: no validation error, no correction
        events = [m["content"].get("event") for m in self.w.journal("c1") if m["kind"] == "error"]
        self.assertEqual(events, [])
        decision = next(m for m in self.w.journal("c1") if m["kind"] == "completion")["content"]["output"]
        self.assertEqual([i["id"] for i in decision["intentions"]], ["i2", "i1"])                         # what the model reads back is what the answers carry
        answers = [m["correlation"]["intention_id"] for m in self.w.journal("c1") if m["kind"] == "capacity_response"]
        self.assertCountEqual(answers, ["i2", "i1"])
        repaired = [m["content"] for m in self.w.journal("c1") if m["kind"] == "event" and m["content"].get("event") == "output_repaired"]
        self.assertEqual(repaired, [])                                                                    # what the model cannot write is filled, never reported as a repair

    @covers('A wait group that lists no expected item waits for the intentions')
    def test_a_wait_group_waits_by_default_for_the_awaited_intentions_of_its_output(self):
        awaited = {"id": "a", "target_capacity": "/root/x", "parameters": {}, "await_response": True}
        fired = {"id": "b", "target_capacity": "/root/x", "parameters": {}, "await_response": False}
        validate = lambda o: pilot_activate.validate_output(pilot_activate.normalize_output({}, o))      # the runtime writes the family before it validates
        out = validate({"intentions": [awaited, fired], "wait_group": {"condition": "all"}})
        self.assertEqual(out["wait_group"]["expected"], [{"type": "intention", "ref": "a", "correlation": {"intention_id": "a"}}])
        listed = validate({"intentions": [awaited, dict(awaited, id="c")], "wait_group": {
            "condition": "all", "expected": [{"type": "intention", "ref": "c"}]}})
        self.assertEqual([i["ref"] for i in listed["wait_group"]["expected"]], ["c"])                      # what the model lists is kept
        with self.assertRaises(InvalidRequest):                                                           # nothing to wait for
            validate({"intentions": [fired], "wait_group": {"condition": "all"}})

    @covers('The stimulus of a resumption after a wait states its outcome')
    def test_two_waits_that_ended_alike_are_two_different_stimuli(self):
        """pilot.infer memoizes a step by the text of its prompt: a resumption must never read like the previous one."""
        def group(gid):
            return {"id": gid, "condition": "all", "status": "satisfied", "expected": [{"ref": "a"}, {"ref": "b"}],
                    "received": [{"expected_ref": "a", "message_id": "x", "outcome": "success"}, {"expected_ref": "b", "message_id": "y", "outcome": "success"}]}
        first, second = pilot_activate._wait_result(group("wg1")), pilot_activate._wait_result(group("wg2"))
        self.assertNotEqual(json.dumps(first, sort_keys=True), json.dumps(second, sort_keys=True))
        self.assertEqual((first["received"], first["expected"]), (second["received"], second["expected"]))

    async def test_the_next_inference_starts_with_what_the_previous_one_sent(self):
        self.script = [wrap(say("first")), wrap(say("second"))]
        self.w.full("c1", messages=[message(1, "framework_c1", kind="framework", content={"text": "FRAME", "source": "s"}),
                                    message(2, "m1", content="one"), message(3, "m2", content="two")], entries=[entry("m1"), entry("m2")])
        await self.activate()
        first, second = ([(m["role"], m["content"]) for m in r["messages"][:-1]] for r in self.requests[:2])    # without the part that is new
        self.assertGreater(len(second), len(first))
        self.assertEqual(second[:len(first) - 1], first[:-1])                                     # the same start, byte for byte
        self.assertEqual(first[-1], ("user", "two"))                                              # the history of the first ended with m2…
        self.assertIn(("assistant", '{"messages":[{"content":"first","kind":"user","to":"user:1"}]}'), second)     # …and the decision it took, as it wrote it, is now part of the second

    async def test_the_pilot_definition_is_read_in_the_context_its_pilot_ref_names(self):
        state = self.w.state("continuity_c1")
        state["pilot_ref"] = {"context": "/root/shop", "matter_id": PILOT_ID}
        self.e.matters["continuity_c1"]["data"] = state
        await self.activate()
        self.assertIn((PILOT_ID, "/root/shop"), self.e.matter_contexts)              # asked there, not in agentic
        self.assertEqual(len(self.requests), 1)

    async def test_no_capacity_is_added_to_the_ones_the_pilot_lists(self):
        await self.activate()
        system = self.requests[0]["messages"][0]["content"]
        self.assertNotIn("/root/read.capacity", system)                                    # the cognitive frame gives the two calls, once
        self.assertNotIn("/root/read.meaning", system)

    async def test_a_pilot_that_lists_read_capacity_itself_keeps_its_own_entry(self):
        state = self.e.matters[PILOT_ID]
        definition = json.loads(state["data"]) if isinstance(state["data"], str) else state["data"]
        definition["known_capacities"] = [{"ref": "/root/read.capacity", "description": "OWN WORDS"}]
        state["data"] = json.dumps(definition) if isinstance(state["data"], str) else definition
        await self.activate()
        system = self.requests[0]["messages"][0]["content"]
        self.assertEqual(system.count('"/root/read.capacity"'), 1)
        self.assertIn("OWN WORDS", system)

    @covers("A message sent to another continuity carries correlation.continuity_id equal to the lineage_id of the sender unless the pilot set it.")
    async def test_delegation_answers_carry_the_lineage_id(self):
        self.w.full("p", messages=[], entries=[])
        self.w.full("c1", messages=[message(1, "m1")], entries=[entry("m1")], lineage_id="origin", generation=3, predecessor_id="c1_g2")
        self.script = [wrap({"messages": [{"to": "p", "kind": "continuity", "content": "answer"}]})]
        await self.activate()
        await self.e.drain()
        received = [m for m in self.w.journal("p") if m["kind"] == "continuity"][0]
        self.assertEqual(received["correlation"]["continuity_id"], "origin")       # the id the parent created, whatever the generation


    # --- Capacity pilot: a plan decides, its prompts go through pilot.infer ---------------------------------------
    SCHEMA = {"type": "object", "properties": {"verdict": {"type": "string"}}, "required": ["verdict"], "additionalProperties": False}
    MODEL = {"provider": "fake", "model": "m", "parameters": {"max_output_tokens": 50}}

    def capacity_pilot(self, plan, *, limits=None, policy=None):
        """c1 is piloted by the Capacity /root/shop/plan, whose body is `plan(port, params)`."""
        ref = {"context": "/root/shop", "capacity": "plan"}
        if limits:
            ref["limits"] = limits
        ref.update(policy or {})
        self.e.caps["/root/shop/plan"] = plan
        self.e.caps["/root/agentic/pilot.infer"] = pilot_infer.run
        state = self.w.state("continuity_c1")
        state["pilot_ref"] = ref
        self.e.matters["continuity_c1"]["data"] = state

    @covers("A Capacity pilot is invoked at each turn with the continuity, the run, the current message and the stimulus; the pilot_output it returns is validated and applied like the one of a pilot_definition.",
            "The actions of a Capacity pilot leave no trace in the journal; only its prompts and completions, recorded by pilot.infer, do.")
    async def test_a_capacity_pilot_decides_with_deterministic_actions_and_recorded_prompts(self):
        seen = []

        async def plan(port, params):
            seen.append(params)
            await port.call_capacity("/root/tools/echo", {"order": "A100"})            # a deterministic action: no trace
            step = await port.call_capacity("/root/agentic/pilot.infer", {
                "continuity_id": params["continuity_id"], "run_id": params["run_id"], "step": "analyse",
                "prompt_parts": ["Analyse.", {"order": "A100"}], "output_schema": self.SCHEMA, "model": self.MODEL})
            return {"messages": [{"to": "user:1", "kind": "user", "content": f"verdict: {step['output']['verdict']}"}]}
        self.capacity_pilot(plan)
        self.script = [wrap({"verdict": "late"})]
        out = await self.activate()
        self.assertEqual(out["status"], "idle")
        self.assertEqual(len(seen), 1)
        self.assertEqual((seen[0]["continuity_id"], seen[0]["message"]["id"], "stimulus" in seen[0]), ("c1", "m1", False))
        self.assertTrue(seen[0]["run_id"])
        journal = self.w.journal("c1")
        self.assertEqual([m["kind"] for m in journal], ["user", "prompt", "completion", "pilot"])     # the action left nothing
        self.assertEqual(journal[3]["content"]["content"], "verdict: late")
        self.assertEqual(self.tool_calls, [{"order": "A100"}])
        self.assertEqual(self.w.entries("c1"), [])

    @covers("A wait group of a Capacity pilot resumes by invoking the Capacity again with the stimulus: the plan holds no state.")
    async def test_a_capacity_pilot_resumes_with_the_stimulus(self):
        calls = []

        async def plan(port, params):
            calls.append(params.get("stimulus"))
            if params.get("stimulus") is None:
                return {"intentions": [awaited("i1")], "wait_group": {"condition": "all", "expected": [{"type": "intention", "ref": "i1"}]}}
            return {"messages": [{"to": "user:1", "kind": "user", "content": "done"}]}
        self.capacity_pilot(plan)
        await self.activate()
        self.assertEqual(len(calls), 2)
        self.assertIsNone(calls[0])
        self.assertEqual((calls[1]["kind"], calls[1]["outcome"]), ("active_wait_result", "satisfied"))
        self.assertEqual(self.w.rt("c1")["active_wait_groups"], [])
        self.assertEqual([m["content"]["content"] for m in self.w.journal("c1") if m["kind"] == "pilot"], ["done"])

    async def test_a_capacity_pilot_output_with_json_text_values_is_decoded_like_a_matter_pilot_s(self):
        """A plan whose step asks the model for a strict schema gets its free values (parameters, content) as JSON text; the
        output of the plan is decoded exactly as a pilot_definition's, so the capacity is called with a real object."""
        seen = []

        async def plan(port, params):
            seen.append(params.get("stimulus"))
            if params.get("stimulus") is None:
                return {"intentions": [{"id": "i1", "target_capacity": "/root/tools/echo", "await_response": True,
                                        "parameters": '{"r": "i1", "filters": [{"path": "objective.name", "op": "LIKE"}]}'}],
                        "wait_group": {"condition": "all", "expected": [{"type": "intention", "ref": "i1"}]}}
            return {"messages": [{"to": "user:1", "kind": "user", "content": "plain words, not json"}]}
        self.capacity_pilot(plan)
        await self.activate()
        self.assertEqual(self.tool_calls, [{"r": "i1", "filters": [{"path": "objective.name", "op": "LIKE"}]}])   # an object, not a string
        self.assertEqual([m["content"]["content"] for m in self.w.journal("c1") if m["kind"] == "pilot"], ["plain words, not json"])

    @covers("An invalid output of a Capacity pilot is never corrected by a second inference: the treatment fails explicitly and the claim is released.")
    async def test_an_invalid_plan_output_fails_the_treatment_without_a_correction(self):
        calls = []

        async def plan(port, params):
            calls.append(1)
            return {"bogus": True}
        self.capacity_pilot(plan)
        await self.activate()
        self.assertEqual(len(calls), 3)                          # once per attempt, never asked again within one
        self.assertEqual(self.requests, [])                      # and no model was called for a correction
        self.assertIn("pilot_output_invalid", self.events())
        self.assertIn("treatment_failed", self.events())
        self.assertEqual([e["status"] for e in self.w.entries("c1")], ["failed"])       # released each time, never rolled back, parked on the third

    @covers("A Capacity pilot has no pilot_definition and no model of its own")
    async def test_a_capacity_pilot_has_no_automatic_compaction(self):
        self.w.full("c1", messages=[message(1, "m1"), message(2, "m2"), self.measured("c1", 10 ** 9, 3)], entries=[entry("m1"), entry("m2")])   # an absurd measure changes nothing
        asked = []

        async def plan(port, params):
            asked.append(params["message"]["id"])
            return {"messages": [{"to": "user:1", "kind": "user", "content": "ok"}]}
        self.capacity_pilot(plan)
        await self.activate()
        self.assertEqual(asked, ["m1", "m2"])
        self.assertEqual(self.w.state("continuity_c1")["status"], "active")

    @covers("Fails the treatment when the Capacity pilot referenced by pilot_ref does not exist.")
    async def test_a_missing_capacity_pilot_fails_the_treatment_not_the_continuity(self):
        self.capacity_pilot(lambda port, params: None)
        del self.e.caps["/root/shop/plan"]
        out = await self.activate()
        self.assertEqual(out["status"], "idle")                  # the continuity is alive, the treatment failed explicitly
        self.assertIn("treatment_failed", self.events())
        self.assertEqual([e["status"] for e in self.w.entries("c1")], ["failed"])               # three attempts in the run, then parked


    # --- an activation is an ordinary awaited capacity ---------------------------------------------------------------
    @covers("An activation of a continuity that another live run owns waits for that run to end and answers with the state the continuity is left in, like any awaited Capacity; an activation marked wake answers at once.")
    async def test_an_explicit_activation_waits_for_the_run_that_owns_the_continuity(self):
        gate = asyncio.Event()

        async def slow(port, params):
            await gate.wait()
            return {"ok": True}
        self.e.caps["/root/tools/echo"] = slow
        self.script = [wrap({"intentions": [awaited("i1")], "wait_group": {"condition": "all", "expected": [{"type": "intention", "ref": "i1"}]}}),
                       wrap(say("finished"))]
        owner = asyncio.ensure_future(self.call(continuity_id="c1", wake=True))
        for _ in range(200):
            await asyncio.sleep(0.01)
            if self.rt()["status"] == "waiting":
                break
        self.assertEqual(self.rt()["status"], "waiting")
        wake = await self.call(continuity_id="c1", wake=True)
        self.assertEqual(wake["status"], "waiting")                       # a wake answers at once
        explicit = asyncio.ensure_future(self.call(continuity_id="c1"))
        await asyncio.sleep(0.1)
        self.assertFalse(explicit.done())                                  # it waits like any awaited capacity
        gate.set()
        answer = await asyncio.wait_for(explicit, 5)
        await owner
        self.assertEqual(answer["status"], "idle")                         # the state the continuity is left in
        self.assertEqual([m["content"]["content"] for m in self.w.journal("c1") if m["kind"] == "pilot"], ["finished"])
        self.assertEqual(len(self.requests), 2)                           # the waiter did not run a second chain

    @covers("A continuity cannot wait for its own activation: such an intention fails explicitly.")
    async def test_a_continuity_cannot_wait_for_its_own_activation(self):
        self.e.caps["/root/agentic/pilot.activate"] = pilot_activate.run
        self.script = [wrap({"intentions": [{"id": "self", "target_capacity": "/root/agentic/pilot.activate",
                                              "parameters": {"continuity_id": "c1"}, "await_response": True}],
                             "wait_group": {"condition": "all", "expected": [{"type": "intention", "ref": "self"}]}}),
                       wrap(say("understood"))]
        out = await asyncio.wait_for(self.activate(), 10)                  # no deadlock: the intention fails at once
        self.assertEqual(out["status"], "idle")
        self.assertIn("execution_error", self.events())
        self.assertEqual([m["content"]["content"] for m in self.w.journal("c1") if m["kind"] == "pilot"], ["understood"])


    # --- a user message in the middle of a chain: absorbed, with the inference window ------------------------------------
    def news(self, request):
        return json.loads(request["messages"][-1]["content"])

    async def waiting_on_a_slow_tool(self, *, message=None):
        """m1 asks for a slow tool and waits for it; then a message arrives. Returns (gate, task). The script answers by what the
        news says: a user_message gets a reply, a resumed wait gets the final word, anything else about m1 starts the wait."""
        gate = asyncio.Event()

        async def slow(port, params):
            await gate.wait()
            return {"done": True}
        self.e.caps["/root/tools/slow"] = slow

        def answer(request):
            news = self.news(request)
            if "user_message" in news:
                return wrap(say("to the user"))
            if (news.get("active_stimulus") or {}).get("kind") == "active_wait_result":
                return wrap(say("final"))
            if news["current_message"]["id"] == "m1":
                return wrap({"wait_group": {"condition": "all"}, "intentions": [awaited("w", "/root/tools/slow")]})
            return wrap(say("another turn"))
        self.script = [answer]
        task = asyncio.ensure_future(self.call(continuity_id="c1"))
        for _ in range(200):
            await asyncio.sleep(0.01)
            if self.rt()["status"] == "waiting":
                break
        self.assertEqual(self.rt()["status"], "waiting")
        await mailbox_send.run(self.e.port("mailbox.send"), {"continuity_id": "c1", "message": message or {"id": "u2", "from": "user:1", "kind": "user", "content": "change of plan"}})
        return gate, task

    async def until(self, condition, seconds=2.0):
        for _ in range(int(seconds / 0.01)):
            if condition():
                return True
            await asyncio.sleep(0.01)
        return False

    @covers("A user message that arrives while the chain of a pilot_definition is in progress joins that chain")
    async def test_a_user_message_joins_the_chain_in_progress_and_the_wait_groups_stay_open(self):
        gate, task = await self.waiting_on_a_slow_tool()
        self.assertTrue(await self.until(lambda: len(self.requests) >= 2))                  # the run woke while the tool is still working
        absorbing = self.news(self.requests[1])
        self.assertEqual(absorbing["user_message"], {"message_ids": ["u2"]})                # designated by its own block ...
        self.assertNotIn("active_stimulus", absorbing)                                      # ... which is not a stimulus
        self.assertEqual(absorbing["current_message"]["id"], "m1")                          # the chain is still the one of m1: u2 was never made current
        await self.until(lambda: any(m["kind"] == "pilot" for m in self.w.journal("c1")))
        self.assertEqual(self.rt()["current_message_id"], "m1")
        self.assertEqual([g["status"] for g in self.rt()["active_wait_groups"]], ["waiting"])   # the group of the tool is still open
        gate.set()
        await task
        await self.e.drain()
        resumed = self.news(self.requests[-1])
        self.assertEqual(resumed["active_stimulus"]["kind"], "active_wait_result")
        self.assertNotIn("user_message", resumed)                                           # said once, at the inference that absorbed it
        ids = [m["id"] for m in self.w.journal("c1")]
        self.assertLess(ids.index("u2"), next(i for i, m in enumerate(self.w.journal("c1")) if m["kind"] == "capacity_response"))

    @covers("While waiting, the run wakes for a pending user message")
    async def test_only_a_user_message_wakes_a_waiting_run(self):
        gate, task = await self.waiting_on_a_slow_tool(message={"id": "c9", "from": "child", "kind": "continuity", "content": "report"})
        await asyncio.sleep(0.2)
        self.assertEqual(len(self.requests), 1)                                             # a message of another kind waits for its own turn
        self.assertEqual(self.rt()["status"], "waiting")
        gate.set()
        await task
        await self.e.drain()
        self.assertEqual(self.w.entries("c1"), [])                                          # and it was treated afterwards, as a message of its own

    @covers("An absorbed message stays claimed until the end of the chain")
    async def test_an_absorbed_message_ends_with_the_chain_and_renews_its_allowance(self):
        self.limits(max_inferences_per_message=2)                                           # wait, absorb, resume = 3 inferences: only a renewal fits
        gate, task = await self.waiting_on_a_slow_tool()
        self.assertTrue(await self.until(lambda: any(m["kind"] == "pilot" for m in self.w.journal("c1"))))
        statuses = {e["message_id"]: e["status"] for e in self.w.entries("c1")}
        self.assertEqual((statuses["m1"], statuses["u2"]), ("claimed", "claimed"))          # claimed, not acknowledged, while the chain goes on
        gate.set()
        await task
        await self.e.drain()
        self.assertEqual(self.w.entries("c1"), [])                                          # acknowledged with the main message
        self.assertNotIn("treatment_failed", self.events())

    @covers("After an abrupt stop, every normal entry claimed by the dead run that is not its current message goes back to pending")
    async def test_after_an_abrupt_stop_an_absorbed_message_is_absorbed_again(self):
        self.w.full("c1", messages=[message(1, "m1"), message(2, "u2")],
                    entries=[entry("m1", status="claimed", claimed_by="DEAD"), entry("u2", status="claimed", claimed_by="DEAD")],
                    status="running", run_id="DEAD", current="m1", structure=False)
        seen = []

        def answer(request):
            seen.append(self.news(request))
            return wrap(say("ok"))
        self.script = [answer]
        await self.activate()
        self.assertEqual(seen[0]["user_message"], {"message_ids": ["u2"]})                  # it is told again, nothing is lost
        self.assertEqual(self.w.entries("c1"), [])

    async def test_waiting_for_the_continuity_of_a_launch_is_waiting_for_the_launch_and_a_failed_launch_does_not_hang_the_pilot(self):
        """The guide wrote wait_group.expected = [{type: continuity, ref: <the daughter>}] next to an awaited pilot.launch: the daughter's answer is the
        launch's Response, so the group waited for a message that never comes, for ever when the launch failed (journal of the 11 November)."""
        launch = {"id": "i1", "target_capacity": "/root/agentic/pilot.launch", "target_type": "user", "await_response": True,
                  "parameters": {"continuity_id": "sub_test_1", "pilot_ref": {"context": "/root", "matter_id": "no_such_pilot"}, "initial_intention": "test",
                                 "parent_id": "c1", "message": {"content": "task"}}}
        self.script = [wrap({"intentions": [launch], "wait_group": {"condition": "all", "expected": [{"type": "continuity", "ref": "sub_test_1"}]}}),
                       wrap(say("the launch failed, I will say so"))]
        await self.activate()
        await self.e.drain()
        group = next(m["content"]["wait_group"] for m in self.w.journal("c1") if isinstance(m["content"], dict) and m["content"].get("event") == "wait_group_created")
        self.assertEqual([(e["type"], e["ref"]) for e in group["expected"]], [("intention", "i1")])      # the launch, not the continuity
        repairs = [r for m in self.w.journal("c1") if isinstance(m["content"], dict) and m["content"].get("event") == "output_repaired" for r in m["content"]["repairs"]]
        self.assertIn("continuity_wait_is_the_launch", [r["kind"] for r in repairs])
        self.assertEqual(self.rt()["active_wait_groups"], [])                                           # the failed launch answered: the group is over
        self.assertEqual([m["content"]["content"] for m in self.w.journal("c1") if m["kind"] == "pilot"], ["the launch failed, I will say so"])

    async def test_a_wait_group_over_nothing_is_dropped_instead_of_costing_a_correction(self):
        """A model that only speaks sometimes adds a wait_group with nothing to wait for (journal of the 11 November): refusing it cost one more inference."""
        self.script = [wrap({"messages": [{"to": "user:1", "kind": "user", "content": "said"}], "wait_group": {"condition": "all", "expected": []}})]
        await self.activate()
        self.assertEqual(len(self.requests), 1)                                           # no correction was asked
        self.assertEqual([m["content"]["content"] for m in self.w.journal("c1") if m["kind"] == "pilot"], ["said"])
        repairs = [r["kind"] for m in self.w.journal("c1") if isinstance(m["content"], dict) and m["content"].get("event") == "output_repaired" for r in m["content"]["repairs"]]
        self.assertIn("empty_wait_group_dropped", repairs)
        self.assertEqual(self.rt()["active_wait_groups"], [])

    async def test_a_continuity_launched_earlier_is_still_waited_by_its_continuity(self):
        """The type continuity stays for a continuity launched earlier that reports by message: only a launch of the same output replaces it."""
        awaited_tool = {"id": "t1", "target_capacity": "/root/tools/echo", "target_type": "user", "await_response": True, "parameters": {}}
        self.script = [wrap({"intentions": [awaited_tool], "wait_group": {"condition": "all", "expected": [{"type": "continuity", "ref": "older_daughter"}]}})]
        running = asyncio.ensure_future(self.activate())                                  # the group waits for a message that this test never sends
        for _ in range(100):
            await asyncio.sleep(0.02)
            if any(isinstance(m["content"], dict) and m["content"].get("event") == "wait_group_created" for m in self.w.journal("c1")):
                break
        running.cancel()
        group = next(m["content"]["wait_group"] for m in self.w.journal("c1") if isinstance(m["content"], dict) and m["content"].get("event") == "wait_group_created")
        self.assertEqual([(e["type"], e["ref"]) for e in group["expected"]], [("continuity", "older_daughter")])

    @covers("The inference step of a pilot_definition")
    async def test_the_step_holds_the_window_for_its_inference_and_not_for_its_effects(self):
        gate, called, windows = asyncio.Event(), asyncio.Event(), []

        async def slow(port, params):
            called.set()
            await gate.wait()
            return wrap({"intentions": [{"id": "f1", "target_capacity": "/root/tools/echo", "parameters": {}, "await_response": False}]})
        self.e.caps["/root/llm/infer"] = slow

        async def echo(port, params):
            inference_window.forget_role()                                                  # as an outside writer would see the door
            windows.append(inference_window.closed_for("c1"))
            return {"ok": True}
        self.e.caps["/root/tools/echo"] = echo
        task = asyncio.ensure_future(self.call(continuity_id="c1"))
        await called.wait()
        self.assertTrue(inference_window.closed_for("c1"))                                  # the model is thinking: the door is shut to the others
        late = asyncio.ensure_future(continuity_store.append(self.p, "c1", [{"kind": "event", "content": {"event": "late"}}]))
        await asyncio.sleep(0.05)
        self.assertFalse(late.done())
        gate.set()
        await task
        await self.e.drain()
        await late
        self.assertEqual(windows, [False])                                                  # when the effect left, the window was already free
        journal = self.w.journal("c1")
        decision = next(i for i, m in enumerate(journal) if m["kind"] == "completion")
        written_late = next(i for i, m in enumerate(journal) if m["kind"] == "event" and m["content"].get("event") == "late")
        self.assertLess(decision, written_late)                                             # after the decision it had to wait for

    @covers("A window reserved for a request stays open to the writes of the run's loop")
    async def test_a_reservation_closes_the_door_to_the_tools_but_not_to_the_loop(self):
        inference_window.RESERVATION_TTL_S = 5
        async with inference_window.hold("c1") as handle:                                   # a request nobody has absorbed yet
            handle.reserve(["ghost"])
        self.script = [wrap({"wait_group": {"condition": "all"}, "intentions": [awaited("w")]}), wrap(say("done"))]
        task = asyncio.ensure_future(self.call(continuity_id="c1"))
        self.assertTrue(await self.until(lambda: self.rt()["status"] == "waiting"))
        self.assertIn("wait_group_created", self.events())                                  # the loop wrote its traces despite the reservation
        await asyncio.sleep(0.1)
        self.assertFalse(task.done())
        self.assertEqual([m for m in self.w.journal("c1") if m["kind"] == "capacity_response"], [])   # the answer of the tool is an outside writer: it waits
        inference_window.clear("c1")
        await asyncio.wait_for(task, 2)
        await self.e.drain()
        self.assertEqual(len([m for m in self.w.journal("c1") if m["kind"] == "capacity_response"]), 1)

    @covers("A Capacity pilot absorbs nothing")
    async def test_a_capacity_pilot_is_never_interrupted_and_absorbs_nothing(self):
        inference_window.RESERVATION_TTL_S = 5
        gate, calls = asyncio.Event(), []

        async def plan(port, params):
            calls.append(params["message"]["id"])
            if params["message"]["id"] == "m1":
                await gate.wait()
            return say("turn")
        self.capacity_pilot(plan)
        task = asyncio.ensure_future(self.call(continuity_id="c1"))
        self.assertTrue(await self.until(lambda: calls == ["m1"]))
        await mailbox_send.run(self.e.port("mailbox.send"), {"continuity_id": "c1", "message": {"id": "u2", "from": "user:1", "kind": "user", "content": "hello?"}})
        self.assertEqual(inference_window.reserved_ids("c1"), set())                        # nothing is reserved for a run that does not absorb
        await asyncio.sleep(0.05)
        self.assertEqual(calls, ["m1"])                                                     # the plan was neither interrupted nor shown the message
        gate.set()
        await task
        await self.e.drain()
        self.assertEqual(calls, ["m1", "u2"])                                               # the message is a turn of its own, afterwards


    # --- the policy and the launch of a pilot ------------------------------------------------------------------------------
    def launch_of(self, pilot_ref, cid):
        return {"id": f"l_{cid}", "target_capacity": "/root/agentic/pilot.launch", "await_response": True,
                "parameters": {"continuity_id": cid, "pilot_ref": pilot_ref, "initial_intention": "Do the task.", "parent_id": "c1", "message": {"content": "work"}, "wait": False}}

    @covers("An intention to pilot.launch is checked against the policy of the pilot that asks for it on the pilot it names too")
    async def test_a_launch_is_checked_against_the_pilot_it_names(self):
        self.policy(allowed_capacities=["/root/agentic/pilot.launch", f"/root/agentic/{PILOT_ID}"])      # the launcher, and one pilot
        refused = {"context": "/root/shop", "capacity": "plan"}
        self.script = [wrap({"intentions": [self.launch_of(refused, "k_refused")], "wait_group": {"condition": "all"},
                             "messages": [{"to": "user:1", "kind": "user", "content": "never said"}]}), wrap(say("ok"))]
        await self.activate()
        self.assertNotIn("continuity_k_refused", self.e.matters)                           # the pilot is not on the list: nothing was launched
        self.assertEqual(self.w.state("continuity_c1")["children"], [])
        stimulus = json.loads(self.requests[1]["messages"][-1]["content"])["active_stimulus"]
        self.assertEqual((stimulus["kind"], stimulus["failed"]), ("execution_result", "intention:l_k_refused"))
        self.assertIn("the pilot to launch /root/shop/plan", stimulus["error"])
        self.assertIn("intentions_refused", self.events())

    async def test_a_pilot_the_policy_allows_is_launched_as_a_sub_continuity(self):
        self.policy(allowed_capacities=["/root/agentic/pilot.launch", f"/root/agentic/{PILOT_ID}"])
        allowed = {"context": "/root/agentic", "matter_id": PILOT_ID}
        self.script = [wrap({"intentions": [self.launch_of(allowed, "k_allowed")], "wait_group": {"condition": "all"}}), wrap(say("ok"))]
        await self.activate()
        self.assertEqual(self.w.state("continuity_k_allowed")["parent_id"], "c1")
        self.assertEqual(self.w.state("continuity_c1")["children"], ["k_allowed"])       # the parent's header knows it; its journal says it too
        self.assertTrue(any(m["kind"] == "capacity_response" for m in self.w.journal("c1")))


if __name__ == "__main__":
    unittest.main()
