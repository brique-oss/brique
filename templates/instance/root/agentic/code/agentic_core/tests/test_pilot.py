import asyncio
import copy
import json
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from fake_engine import PILOT_ID, make_engine  # noqa: E402

import pilot_activate, mailbox_send, continuity_create, mailbox_release  # noqa: E402
from substrate import liveness  # noqa: E402
from substrate.ids import matter_ids  # noqa: E402

pilot_activate.POLL_S = 0.01
PILOT = {"context": "/root/agentic", "matter_id": PILOT_ID}


class ScriptedLLM:
    """Replies to every inference with the next scripted output (or callable(request) -> output)."""
    def __init__(self, *outputs):
        self.outputs, self.requests = list(outputs), []

    async def __call__(self, port, params):
        request = params["request"]
        self.requests.append(request)
        out = self.outputs.pop(0) if len(self.outputs) > 1 else self.outputs[0]
        out = out(request) if callable(out) else out
        return {"response": {"output": {"format": "json", "content": out}}}


def say(text="ok"):
    return {"messages": [{"to": "user:1", "kind": "user", "content": text}]}


class Base(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.e = make_engine()
        self.p = self.e.port("test")
        await continuity_create.run(self.p, {"continuity_id": "c1", "pilot_ref": PILOT, "initial_intention": "goal"})

    def llm(self, *outputs):
        self.l = ScriptedLLM(*outputs)
        self.e.caps["/root/llm/infer"] = self.l
        return self.l

    def target(self, name, fn):
        self.e.caps[name] = fn

    async def send(self, mid="m1", content="hello", kind="user", cid="c1"):
        return await mailbox_send.run(self.p, {"continuity_id": cid, "message": {"id": mid, "from": "user:1", "kind": kind, "content": content}})

    async def settle(self):
        for _ in range(400):
            await self.e.drain()
            rt = await self.runtime()
            if rt["status"] == "idle" and not [t for t in self.e.tasks if not t.done()]:
                return
            await asyncio.sleep(0.005)

    async def runtime(self, cid="c1"):
        return (await self.p.matter_read(matter_ids(cid).runtime)).content

    async def journal(self, cid="c1"):
        """The lines of the exchange: the cognitive frame, first line of every journal, is left out."""
        return copy.deepcopy([m for m in self.e.matters[matter_ids(cid).journal]["lines"] if m["kind"] != "framework"])

    async def entries(self, cid="c1"):
        return (await self.p.matter_read(matter_ids(cid).mailbox)).content["entries"]

    def events(self, journal):
        return [m["content"].get("event") for m in journal if isinstance(m["content"], dict) and m["content"].get("event")]


class Basics(Base):
    async def test_message_wakes_pilot_which_answers_acks_and_returns_idle(self):
        self.llm(say("hi"))
        await self.send()
        await self.settle()
        self.e.raise_task_errors()
        rt = await self.runtime()
        self.assertEqual((rt["status"], rt["run_id"], rt["current_message_id"]), ("idle", None, None))
        self.assertEqual(await self.entries(), [])
        kinds = [m["kind"] for m in await self.journal()]
        self.assertEqual(kinds, ["user", "event", "completion", "pilot"])             # the measure of the context, the decision, then its effect
        measure = (await self.journal())[1]["content"]
        self.assertEqual((measure["event"], measure["message_id"]), ("inference_measured", "m1"))
        # the LLM received the stable system, then the journal as a conversation, then what is new, with the current message explicit
        sent = self.l.requests[0]["messages"]
        self.assertEqual([m["role"] for m in sent], ["system", "user", "user"])
        self.assertIn("Initial intention:", sent[0]["content"])
        body = json.loads(sent[-1]["content"])
        self.assertEqual(list(body), ["continuity_id", "current_intention", "current_message"])
        self.assertEqual(body["current_message"]["id"], "m1")
        self.assertEqual(self.l.requests[0]["output"]["format"], "json_schema")

    async def test_messages_are_processed_one_at_a_time_in_order(self):
        order = []
        self.llm(lambda req: (order.append(json.loads(req["messages"][-1]["content"])["current_message"]["id"]), say())[1])
        for i in range(3):
            await self.send(f"m{i}")
        await self.settle(); self.e.raise_task_errors()
        self.assertEqual(order, ["m0", "m1", "m2"])
        self.assertEqual(await self.entries(), [])

    async def test_concurrent_activations_create_a_single_run(self):
        self.llm(say())
        await self.send("a"); await self.send("b")
        await asyncio.gather(*[pilot_activate.run(self.e.port("x"), {"continuity_id": "c1"}) for _ in range(5)])
        await self.settle(); self.e.raise_task_errors()
        self.assertEqual(len(self.l.requests), 2)               # each message treated exactly once
        self.assertEqual(await self.entries(), [])

    async def test_pilot_message_to_another_continuity_goes_through_its_mailbox(self):
        await continuity_create.run(self.p, {"continuity_id": "c2", "pilot_ref": PILOT, "initial_intention": "other"})
        self.llm(lambda req: {"messages": [{"to": "c2", "kind": "continuity", "content": "ping"}]} if "m1" in req["messages"][-1]["content"] and "from c1" not in req["messages"][-1]["content"] and json.loads(req["messages"][-1]["content"])["current_message"]["from"] == "user:1" else say())
        await self.send("m1")
        await self.settle(); self.e.raise_task_errors()
        j2 = await self.journal("c2")
        self.assertEqual(j2[0]["kind"], "continuity")                 # arrived through the mailbox of c2
        self.assertEqual(j2[0]["correlation"]["continuity_id"], "c1")
        self.assertEqual(await self.entries("c2"), [])                # and c2's own pilot treated it

    async def test_intention_update_and_fire_and_forget(self):
        calls = []
        async def tool(port, params): calls.append(params); return {"ok": True}
        self.target("/root/tools/do", tool)
        self.llm({"intention_update": "new goal", "intentions": [{"id": "i1", "target_capacity": "/root/tools/do", "parameters": {"x": 1}, "await_response": False}]})
        await self.send()
        await self.settle(); self.e.raise_task_errors()
        c = (await self.p.matter_read("continuity_c1")).content
        self.assertEqual((c["initial_intention"], c["current_intention"]), ("goal", "new goal"))
        self.assertEqual(calls, [{"x": 1}])
        self.assertEqual(await self.entries(), [])           # no response, no waiting, message acked
        self.assertIn("capacity_intention", [m["kind"] for m in await self.journal()])


class Waiting(Base):
    def waits(self, *refs, condition="all", n=None):
        group = {"condition": condition, "expected": [{"type": "intention", "ref": r} for r in refs]}
        if n: group["n"] = n
        return {"wait_group": group, "intentions": [{"id": r, "target_capacity": f"/root/tools/{r}", "parameters": {"r": r}, "await_response": True} for r in refs]}

    async def test_all_condition_resumes_with_wait_result_then_completes(self):
        async def tool(port, params):
            await asyncio.sleep(0.02); return {"value": params["r"]}
        for r in ("a", "b"): self.target(f"/root/tools/{r}", tool)
        llm = self.llm(self.waits("a", "b"), say("done"))
        await self.send()
        await self.settle(); self.e.raise_task_errors()
        self.assertEqual(len(llm.requests), 2)
        stim = json.loads(llm.requests[1]["messages"][-1]["content"])["active_stimulus"]
        self.assertTrue(stim.pop("wait_group_id"))                                                       # what makes each resumption a different text
        self.assertEqual(stim, {"kind": "active_wait_result", "outcome": "satisfied", "received": 2, "expected": 2})     # and only what the model needs
        answers = [json.loads(m["content"]) for m in llm.requests[1]["messages"] if m["role"] == "user" and m["content"].startswith('{"intention_id"')]
        self.assertEqual(sorted(a["intention_id"] for a in answers), ["a", "b"])           # each answer says which intention it answers
        rt = await self.runtime()
        self.assertEqual((rt["status"], rt["active_wait_groups"]), ("idle", []))
        self.assertEqual(await self.entries(), [])
        j = await self.journal()
        self.assertEqual(sorted(m["kind"] for m in j), ["capacity_intention", "capacity_intention", "capacity_response", "capacity_response", "completion", "completion", "event", "event", "event", "event", "pilot", "user"])
        self.assertEqual(self.events(j).count("wait_group_created"), 1)
        self.assertEqual(self.events(j).count("wait_group_completed"), 1)

    async def test_the_runtime_really_reports_waiting(self):
        gate = asyncio.Event()
        async def tool(port, params): await gate.wait(); return {}
        self.target("/root/tools/a", tool)
        self.llm(self.waits("a"), say())
        await self.send()
        for _ in range(100):
            await asyncio.sleep(0.01)
            if (await self.runtime())["status"] == "waiting": break
        rt = await self.runtime()
        self.assertEqual(rt["status"], "waiting"); self.assertTrue(rt["run_id"]); self.assertEqual(rt["current_message_id"], "m1")
        self.assertEqual([e["status"] for e in await self.entries()], ["claimed"])
        gate.set()
        await self.settle(); self.e.raise_task_errors()
        self.assertEqual((await self.runtime())["status"], "idle")

    async def test_any_condition_does_not_wait_for_the_slow_one_and_late_result_is_only_history(self):
        gate = asyncio.Event()
        async def fast(port, params): return {"v": 1}
        async def slow(port, params): await gate.wait(); return {"v": 2}
        self.target("/root/tools/f", fast); self.target("/root/tools/s", slow)
        llm = self.llm(self.waits("f", "s", condition="any"), say("done"))
        await self.send()
        for _ in range(200):
            await asyncio.sleep(0.01)
            if len(llm.requests) == 2 and (await self.runtime())["status"] == "idle": break
        self.assertEqual(len(llm.requests), 2)
        gate.set(); await self.settle(); self.e.raise_task_errors()
        self.assertEqual(len(llm.requests), 2)               # the late result triggers no new inference
        j = await self.journal()
        self.assertEqual(len([m for m in j if m["kind"] == "capacity_response"]), 2)
        late = [m for m in j if m["kind"] == "capacity_response" and m["correlation"]["intention_id"] == "s"][0]
        entries = await self.entries()
        self.assertEqual([e["message_id"] for e in entries], [late["id"]] if entries else [])   # late = normal message, or already consumed

    async def test_error_response_is_captured_with_outcome_error(self):
        async def boom(port, params): raise RuntimeError("tool failed")
        self.target("/root/tools/a", boom)
        llm = self.llm(self.waits("a", condition="first_success"), say())
        await self.send()
        await self.settle(); self.e.raise_task_errors()
        stim = json.loads(llm.requests[1]["messages"][-1]["content"])["active_stimulus"]
        self.assertEqual((stim["outcome"], stim["received"], stim["expected"]), ("no_success", 1, 1))
        answer = json.loads(next(m["content"] for m in llm.requests[1]["messages"] if m["content"].startswith('{"intention_id":"a"')))
        self.assertEqual(answer["response"]["status"], "error")                              # the failure is read in the answer itself

    async def test_wait_group_exists_before_the_intentions_are_invoked(self):
        seen = {}
        async def tool(port, params):
            seen["groups"] = [g["id"] for g in (await port.matter_read("runtime_c1")).content["active_wait_groups"]]
            return {}
        self.target("/root/tools/a", tool)
        self.llm(self.waits("a"), say())
        await self.send(); await self.settle(); self.e.raise_task_errors()
        self.assertEqual(len(seen["groups"]), 1)


class Validation(Base):
    async def test_invalid_then_corrected(self):
        llm = self.llm({"bogus": 1}, say("fixed"))
        await self.send(); await self.settle(); self.e.raise_task_errors()
        self.assertEqual(len(llm.requests), 2)
        self.assertIn("validation_error", llm.requests[1]["messages"][-1]["content"])
        self.assertEqual(self.events(await self.journal()).count("validation_error"), 1)
        self.assertEqual(await self.entries(), [])

    async def test_invalid_twice_fails_the_treatment_and_releases_without_loop(self):
        llm = self.llm({"bogus": 1})
        await self.send(); await self.settle(); self.e.raise_task_errors()
        # one correction per treatment, and a message whose treatment fails is retried until it is parked: no endless loop
        self.assertEqual(len(llm.requests), 2 * mailbox_release.MAX_ATTEMPTS)
        rt = await self.runtime()
        self.assertEqual((rt["status"], rt["current_message_id"]), ("idle", None))
        self.assertEqual([(e["status"]) for e in await self.entries()], ["failed"])     # parked, not rolled back
        self.assertIn("treatment_failed", self.events(await self.journal()))

    async def test_wait_group_on_fire_and_forget_intention_is_invalid(self):
        bad = {"wait_group": {"condition": "all", "expected": [{"type": "intention", "ref": "i"}]},
               "intentions": [{"id": "i", "target_capacity": "/root/tools/x", "parameters": {}, "await_response": False}]}
        llm = self.llm(bad, say())
        await self.send(); await self.settle(); self.e.raise_task_errors()
        self.assertEqual(len(llm.requests), 2)
        self.assertIn("await_response false", llm.requests[1]["messages"][-1]["content"])

    async def test_projection_request_excludes_effects_and_gives_a_new_view(self):
        bad = {"projection_request": {"projection": {"limit": 1}}, "messages": [{"to": "u", "kind": "user", "content": "x"}]}
        llm = self.llm(bad, {"projection_request": {"projection": {"limit": 1}, "mode": "replace"}}, say())
        await self.send(); await self.settle(); self.e.raise_task_errors()
        self.assertEqual(len(llm.requests), 3)
        ids = [i for i in llm.requests[2]["metadata"]["projection_ids"] if not i.startswith("framework_")]       # the frame is pinned: always there
        self.assertEqual(len(ids), 1)                                                                          # exactly the requested view
        self.assertIn("projection_result", self.events(await self.journal()))

    async def test_execution_failure_goes_back_to_cognition_without_replay(self):
        calls = []
        async def ok(port, params): calls.append(1); return {}
        self.target("/root/tools/ok", ok)
        self.e.bad_refs.add("/root/tools/bad")
        out = {"intentions": [{"id": "i1", "target_capacity": "/root/tools/ok", "parameters": {}, "await_response": False},
                              {"id": "i2", "target_capacity": "/root/tools/bad", "parameters": {}, "await_response": False},
                              {"id": "i3", "target_capacity": "/root/tools/ok", "parameters": {}, "await_response": False}]}
        llm = self.llm(out, say("recovered"))
        await self.send(); await self.settle(); self.e.raise_task_errors()
        stim = json.loads(llm.requests[1]["messages"][-1]["content"])["active_stimulus"]
        self.assertEqual((stim["kind"], stim["failed"], stim["succeeded"], stim["remaining"]), ("execution_result", "intention:i2", ["intention:i1"], ["intention:i3"]))
        self.assertEqual(calls, [1])                           # i1 not replayed, i3 never attempted
        self.assertEqual(await self.entries(), [])


class Controls(Base):
    async def test_deferred_control_is_applied_and_acked_at_a_safe_point(self):
        gate = asyncio.Event()
        async def slow(port, params): await gate.wait(); return {}
        self.target("/root/tools/a", slow)
        self.llm({"wait_group": {"condition": "all", "expected": [{"type": "intention", "ref": "a"}]},
                  "intentions": [{"id": "a", "target_capacity": "/root/tools/a", "parameters": {}, "await_response": True}]}, say())
        await self.send()
        for _ in range(100):
            await asyncio.sleep(0.01)
            if (await self.runtime())["status"] == "waiting": break
        r = await mailbox_send.run(self.p, {"continuity_id": "c1", "message": {"id": "k1", "from": "user:1", "kind": "control", "content": {"action": "set_intention", "intention": "pivot"}}})
        self.assertEqual((r["priority"], r["status"]), ("control", "pending"))
        for _ in range(100):
            await asyncio.sleep(0.01)
            if (await self.p.matter_read("continuity_c1")).content["current_intention"] == "pivot": break
        self.assertEqual((await self.p.matter_read("continuity_c1")).content["current_intention"], "pivot")
        self.assertNotIn("k1", [e["message_id"] for e in await self.entries()])        # acked, never re-processed
        gate.set(); await self.settle(); self.e.raise_task_errors()

    async def test_suspend_while_waiting_then_resume_continues_the_same_chain(self):
        gate = asyncio.Event()
        async def slow(port, params): await gate.wait(); return {}
        self.target("/root/tools/a", slow)
        llm = self.llm({"wait_group": {"condition": "all", "expected": [{"type": "intention", "ref": "a"}]},
                        "intentions": [{"id": "a", "target_capacity": "/root/tools/a", "parameters": {}, "await_response": True}]}, say("after resume"))
        await self.send()
        for _ in range(100):
            await asyncio.sleep(0.01)
            if (await self.runtime())["status"] == "waiting": break
        await mailbox_send.run(self.p, {"continuity_id": "c1", "message": {"id": "k1", "from": "u", "kind": "control", "content": {"action": "suspend"}}})
        for _ in range(100):
            await asyncio.sleep(0.01)
            if (await self.runtime())["status"] == "suspended": break
        rt = await self.runtime()
        self.assertEqual((rt["status"], rt["run_id"]), ("suspended", None))
        self.assertEqual(len(rt["active_wait_groups"]), 1)                            # the durable wait survives
        gate.set(); await asyncio.sleep(0.05); await self.e.drain()                   # Response arrives while suspended
        self.assertEqual(len(llm.requests), 1)                                        # no cognition while suspended
        await mailbox_send.run(self.p, {"continuity_id": "c1", "message": {"id": "k2", "from": "u", "kind": "control", "content": {"action": "resume"}}})
        await self.settle(); self.e.raise_task_errors()
        self.assertEqual(len(llm.requests), 2)
        self.assertEqual(json.loads(llm.requests[1]["messages"][-1]["content"])["active_stimulus"]["kind"], "active_wait_result")
        self.assertEqual(await self.entries(), [])

    async def test_delete_control_at_a_safe_point_is_terminal(self):
        gate = asyncio.Event()
        async def slow(port, params): await gate.wait(); return {}
        self.target("/root/tools/a", slow)
        self.llm({"wait_group": {"condition": "all", "expected": [{"type": "intention", "ref": "a"}]},
                  "intentions": [{"id": "a", "target_capacity": "/root/tools/a", "parameters": {}, "await_response": True}]})
        await self.send()
        for _ in range(100):
            await asyncio.sleep(0.01)
            if (await self.runtime())["status"] == "waiting": break
        await mailbox_send.run(self.p, {"continuity_id": "c1", "message": {"id": "k1", "from": "u", "kind": "control", "content": {"action": "delete"}}})
        for _ in range(100):
            await asyncio.sleep(0.01)
            if "continuity_c1" not in self.e.matters: break
        self.assertEqual([m for m in self.e.matters if m.endswith("_c1")], [])
        gate.set(); await self.e.drain(); self.e.raise_task_errors()                   # late Response finds nothing, no recreation
        self.assertEqual([m for m in self.e.matters if m.endswith("_c1")], [])


class RaceAndRecovery(Base):
    async def test_release_and_recheck_catches_a_message_arriving_while_going_idle(self):
        self.llm(say())
        original = pilot_activate._publish_idle
        state = {"done": False}

        async def racing(ctx):
            result = await original(ctx)
            if result and not state["done"]:
                state["done"] = True
                # a send that read "running" just before idle was published: it did not wake anything
                await mailbox_send.run(ctx.port, {"continuity_id": "c1", "message": {"id": "late", "from": "u", "kind": "user", "content": "late"}})
                self.e.calls[:] = [c for c in self.e.calls if c != "fire:/root/agentic/pilot.activate"]
            return result
        pilot_activate._publish_idle = racing
        try:
            await self.send("m1")
            await self.settle(); self.e.raise_task_errors()
        finally:
            pilot_activate._publish_idle = original
        self.assertEqual(len(self.l.requests), 2)             # the late message was treated: no lost wake-up
        self.assertEqual(await self.entries(), [])

    async def test_orphan_run_is_taken_over_and_its_claim_transferred(self):
        self.llm(say("recovered"))
        ids = matter_ids("c1")
        await self.send("m1")
        await self.settle()                                    # m1 done
        await self.send("m2")
        liveness._live.clear()
        # fabricate a crash: a dead run holds m2 mid-chain
        v = await self.p.matter_read(ids.runtime)
        await self.p.matter_write(ids.runtime, content=dict(v.content, status="running", run_id="DEAD", current_message_id="m2"), expected_revision=v.revision)
        mb = await self.p.matter_read(ids.mailbox)
        entries = [dict(e, status="claimed", claimed_by="DEAD") if e["message_id"] == "m2" else e for e in mb.content["entries"]]
        await self.p.matter_write(ids.mailbox, content=dict(mb.content, entries=entries), expected_revision=mb.revision)
        await self.e.drain()
        await pilot_activate.run(self.e.port("x"), {"continuity_id": "c1"})
        await self.settle(); self.e.raise_task_errors()
        rt = await self.runtime()
        self.assertEqual((rt["status"], rt["run_id"], rt["current_message_id"]), ("idle", None, None))
        self.assertEqual(await self.entries(), [])
        self.assertIn("run_recovered", self.events(await self.journal()))

    async def test_a_live_run_is_never_taken_over(self):
        gate = asyncio.Event()
        async def slow(port, params): await gate.wait(); return {}
        self.target("/root/tools/a", slow)
        self.llm({"wait_group": {"condition": "all", "expected": [{"type": "intention", "ref": "a"}]},
                  "intentions": [{"id": "a", "target_capacity": "/root/tools/a", "parameters": {}, "await_response": True}]}, say())
        await self.send()
        for _ in range(100):
            await asyncio.sleep(0.01)
            if (await self.runtime())["status"] == "waiting": break
        run_id = (await self.runtime())["run_id"]
        r = await pilot_activate.run(self.e.port("x"), {"continuity_id": "c1", "wake": True})      # a wake answers at once
        self.assertEqual((r["status"], r["run_id"]), ("waiting", run_id))
        # an explicit activation is an ordinary awaited capacity: it waits for the run that owns the continuity
        explicit = asyncio.ensure_future(pilot_activate.run(self.e.port("x"), {"continuity_id": "c1"}))
        await asyncio.sleep(0.1)
        self.assertFalse(explicit.done())
        gate.set()
        done = await asyncio.wait_for(explicit, 5)
        self.assertEqual(done["status"], "idle")
        await self.settle(); self.e.raise_task_errors()

    async def test_takeover_resolves_awaits_lost_with_the_dead_process_without_replay(self):
        calls = []
        async def tool(port, params): calls.append(1); return {}
        self.target("/root/tools/a", tool)
        llm = self.llm(say())
        ids = matter_ids("c1")
        await self.send("m1")
        await self.settle()
        await self.send("m2")
        liveness._live.clear()
        group = {"id": "g1", "condition": "all", "status": "waiting", "received": [],
                 "expected": [{"type": "intention", "ref": "lost1", "correlation": {"intention_id": "lost1"}}]}
        v = await self.p.matter_read(ids.runtime)
        await self.p.matter_write(ids.runtime, content=dict(v.content, status="waiting", run_id="DEAD", current_message_id="m2", active_wait_groups=[group]), expected_revision=v.revision)
        mb = await self.p.matter_read(ids.mailbox)
        entries = [dict(e, status="claimed", claimed_by="DEAD") if e["message_id"] == "m2" else e for e in mb.content["entries"]]
        await self.p.matter_write(ids.mailbox, content=dict(mb.content, entries=entries), expected_revision=mb.revision)
        await pilot_activate.run(self.e.port("x"), {"continuity_id": "c1"})
        await self.settle(); self.e.raise_task_errors()
        j = await self.journal()
        lost = [m for m in j if m["kind"] == "capacity_response"]
        self.assertEqual(len(lost), 1)
        self.assertTrue(lost[0]["content"]["unresolved"])
        self.assertEqual(lost[0]["content"]["response"]["error"]["code"], "unresolved")
        self.assertEqual(calls, [])                                   # nothing replayed
        stim = json.loads(llm.requests[-1]["messages"][-1]["content"])["active_stimulus"]
        self.assertEqual((stim["kind"], stim["received"], stim["expected"]), ("active_wait_result", 1, 1))
        answer = json.loads(next(m["content"] for m in llm.requests[-1]["messages"] if m["content"].startswith('{"intention_id":"lost1"')))
        self.assertEqual(answer["response"]["error"]["code"], "unresolved")
        self.assertEqual((await self.runtime())["status"], "idle")
        self.assertEqual(await self.entries(), [])

    async def test_a_runaway_chain_is_stopped_explicitly(self):
        llm = self.llm({"projection_request": {"projection": {"limit": 1}}})
        await self.send()
        await self.settle(); self.e.raise_task_errors()
        self.assertEqual(len(llm.requests), pilot_activate.MAX_CHAIN_STEPS * mailbox_release.MAX_ATTEMPTS)     # bounded per treatment, the message retried then parked
        self.assertIn("treatment_failed", self.events(await self.journal()))
        self.assertEqual((await self.runtime())["status"], "idle")
        self.assertEqual([e["status"] for e in await self.entries()], ["failed"])


if __name__ == "__main__":
    unittest.main()
