import json
import asyncio
import unittest

from .harness import PILOT_ID, CapacityContract, covers, entry, message
from substrate.errors import IdentityConflict, ConcurrencyError, ConsistencyError, EngineError, InvalidRequest, Refused
import continuity_delete, mailbox_send
from substrate import inference_window, liveness


def new(mid="n1", kind="user", content="hello", **kw):
    return dict({"id": mid, "from": "user:1", "kind": kind, "content": content}, **kw)


def group(gid, *refs, status="waiting", received=(), condition="all", **kw):
    return dict({"id": gid, "condition": condition, "status": status, "received": list(received),
                 "expected": [{"type": "intention", "ref": r, "correlation": {"intention_id": r}} for r in refs]}, **kw)


class MailboxSendContract(CapacityContract):
    CAP, MODULE = "mailbox.send", mailbox_send

    async def asyncSetUp(self):
        await super().asyncSetUp()
        self.e.caps["/root/agentic/mailbox.send"] = mailbox_send.run       # a redirection forwards through the capacity
        self.w.full("c1")

    async def send(self, m, cid="c1"):
        return await self.call(continuity_id=cid, message=m)

    def fires(self):
        return [c for c in self.e.calls if c == "fire:/root/agentic/pilot.activate"]

    # --- inputs and outputs -------------------------------------------------------------------
    async def test_payload_matches_the_declared_outputs(self):
        out = await self.send(new())
        self.assertPayloadMatchesContract(out)
        self.assertEqual((out["continuity_id"], out["message_id"], out["seq"], out["priority"], out["status"]), ("c1", "n1", 1, "normal", "pending"))

    async def test_every_declared_input_is_required(self):
        for params in ({"message": new()}, {"continuity_id": "c1"}):
            with self.assertRaises(InvalidRequest):
                await self.call(**params)

    # --- invariants -----------------------------------------------------------------------------
    @covers("A successfully sent new message exists in the journal.", "A successfully sent message has at most one mailbox entry.",
            "Mailbox entries never duplicate message content.", "The reservation is a pending mailbox entry; no provisional status is introduced.")
    async def test_one_durable_message_and_one_content_free_pending_entry(self):
        await self.send(new(content={"big": "payload"}))
        await self.send(new(content={"big": "payload"}))                              # a retry: the same message
        self.assertEqual([m["id"] for m in self.w.journal("c1")], ["n1"])
        entries = self.w.entries("c1")
        self.assertEqual([{k: v for k, v in e.items() if k != "message_fingerprint"} for e in entries], [{"message_id": "n1", "priority": "normal", "status": "pending"}])
        self.assertNotIn("payload", json.dumps(entries))                              # a fingerprint of the content, never the content itself

    @covers("Message seq is the line number of the message in the journal")
    async def test_seq_is_the_line_number_in_the_journal(self):
        self.w.full("c2", messages=[message(1, "a"), message(2, "b"), message(3, "c")])
        for _ in range(3):                                    # revisions advance; seq must not follow them
            v = await self.p.matter_read("runtime_c2")
            await self.p.matter_write("runtime_c2", content=v.content, expected_revision=v.revision)
        out = await self.send(new("n1", **{"seq": 999}), cid="c2")      # a caller seq is never authoritative
        self.assertEqual(out["seq"], 4)
        self.assertEqual((await self.send(new("n2"), cid="c2"))["seq"], 5)
        self.assertEqual([m["seq"] for m in self.w.journal("c2")], [1, 2, 3, 4, 5])      # seq == line number
        guards = [e[2] for e in self.e.log if e[0] == "append" and e[1] == "journal_c2"]
        self.assertEqual(guards, [3, 4])                       # each append is guarded by the number of lines already committed

    @covers("Durable messages are immutable after append.", "An idempotent retry does not append, enqueue, capture, or wake twice.")
    async def test_a_retry_changes_nothing_and_does_not_wake_again(self):
        await self.send(new("n1", content="original"))
        fires, before = len(self.fires()), self.e.snapshot()
        again = await self.send(new("n1", content="original"))                         # the same message again
        self.assertEqual(again["seq"], 1)
        self.assertEqual(self.w.journal("c1")[0]["content"], "original")
        with self.assertRaises(IdentityConflict):                                         # another content under the same id never replaces it
            await self.send(new("n1", content="TAMPERED"))
        self.assertEqual(self.w.journal("c1")[0]["content"], "original")
        self.assertEqual(len(self.w.entries("c1")), 1)
        self.assertGreaterEqual(len(self.fires()), fires)      # waking is idempotent at the activation side
        self.assertEqual(self.w.journal("c1"), self.w.journal("c1"))

    @covers("A message is classified exactly once when first accepted.")
    async def test_classification_is_fixed_at_first_acceptance(self):
        self.w.runtime("c1", "waiting", "R1", "m0", [group("g1", "i1")])
        response = new("r1", kind="capacity_response", content={"response": {"status": "ok"}}, correlation={"intention_id": "i1"})
        first = await self.send(response)
        self.assertEqual(first["priority"], "expected")
        self.w.runtime("c1", "running", "R1", "m0", [])          # the wait group is gone by the time of the retry
        liveness.mark_live("c1", "R1")
        again = await self.send(response)
        self.assertEqual((again["priority"], again["seq"]), ("expected", first["seq"]))      # never re-classified
        self.assertEqual(self.w.entries("c1")[0]["priority"], "expected")

    @covers("One incoming message can satisfy at most one active wait group.", "Fails when correlation matches more than one active wait group.")
    async def test_an_ambiguous_correlation_is_refused_without_mutation(self):
        self.w.runtime("c1", "waiting", "R1", "m0", [group("g1", "i1"), group("g2", "i1")])
        before = self.e.snapshot()
        with self.assertRaises(InvalidRequest):
            await self.send(new("r1", kind="capacity_response", content={"response": {"status": "ok"}}, correlation={"intention_id": "i1"}))
        self.assertUnchanged(before)

    @covers("Expected is contextual and exists only when an active wait group actually matches.")
    async def test_expected_only_with_an_actually_matching_active_group(self):
        self.w.runtime("c1", "waiting", "R1", "m0", [group("g1", "i1")])
        no_match = await self.send(new("r0", kind="capacity_response", content={"response": {"status": "ok"}}, correlation={"intention_id": "other"}))
        self.assertEqual(no_match["priority"], "normal")
        done = group("g9", "i7", status="completed")
        self.w.runtime("c1", "waiting", "R1", "m0", [group("g1", "i1"), done])
        completed = await self.send(new("r9", kind="capacity_response", content={"response": {"status": "ok"}}, correlation={"intention_id": "i7"}))
        self.assertEqual(completed["priority"], "normal")
        match = await self.send(new("r1", kind="capacity_response", content={"response": {"status": "ok"}}, correlation={"intention_id": "i1"}))
        self.assertEqual((match["priority"], match["status"], match["wait_group_id"], match["claimed_by"]), ("expected", "claimed", "g1", "R1"))

    @covers("An acknowledged historical message cannot be reinserted with the same id.", "Fails when a historical consumed message id is reused.")
    async def test_a_consumed_message_id_is_never_reinserted(self):
        self.w.full("c3", messages=[message(1, "old")])        # in the journal, no entry any more: consumed
        before = self.e.snapshot()
        with self.assertRaises(Refused):
            await self.send(new("old", content="x"), cid="c3")                                  # the same message again
        with self.assertRaises(IdentityConflict):
            await self.send(new("old", content="not what was sent"), cid="c3")                 # another message under the same id
        self.assertUnchanged(before)

    @covers("The identity of a message is its id and the fingerprint of its content", "Fails with an identity conflict, changing nothing")
    async def test_the_same_id_with_another_content_is_an_identity_conflict_in_every_state(self):
        original = new("k1", content={"a": 1})
        # reserved only: the reservation carries the fingerprint, the journal does not hold the message yet
        self.w.mailbox("c1", [dict(entry("k1"), message_fingerprint=mailbox_send.fingerprint_of(dict(original, correlation={})))])
        before = self.e.snapshot()
        with self.assertRaises(IdentityConflict):
            await self.send(new("k1", content={"a": 2}))
        self.assertUnchanged(before)
        await self.send(original)                                                           # the same message takes the reservation over
        self.assertEqual([m["id"] for m in self.w.journal("c1")], ["k1"])
        # accepted and still active
        before = self.e.snapshot()
        with self.assertRaises(IdentityConflict):
            await self.send(new("k1", content={"a": 2}))
        with self.assertRaises(IdentityConflict):                                           # the sender, the kind and the correlation are part of the content
            await self.send(new("k1", content={"a": 1}, **{"from": "user:2"}))
        with self.assertRaises(IdentityConflict):
            await self.send(new("k1", content={"a": 1}, correlation={"intention_id": "i"}))
        self.assertUnchanged(before)
        await self.send(dict(original, created_at="2031-01-01T00:00:00Z"))                  # created_at is not part of the identity: still a retry
        self.assertEqual(len(self.w.journal("c1")), 1)

    async def test_a_reservation_laid_before_fingerprints_existed_is_still_taken_over(self):
        self.w.mailbox("c1", [entry("old1")])                                               # no fingerprint in it
        await self.send(new("old1", content="whatever"))
        self.assertEqual([m["id"] for m in self.w.journal("c1")], ["old1"])

    @covers("pilot.activate is invoked only after persistence is coherent.", "Activation through pilot.activate is non-blocking")
    async def test_activation_is_fire_and_forget_and_after_the_commit(self):
        seen = {}

        def on_fire(engine, ref, params):
            seen["journal"] = [m["id"] for m in self.w.journal("c1")]
            seen["entries"] = [e["message_id"] for e in self.w.entries("c1")]
        self.e.on_fire = on_fire
        await self.send(new())
        self.assertEqual(seen, {"journal": ["n1"], "entries": ["n1"]})
        self.assertEqual(self.fires(), ["fire:/root/agentic/pilot.activate"])
        self.assertNotIn("/root/agentic/pilot.activate", self.e.calls)     # never awaited

    @covers("The Matters of the target continuity are resolved from continuity_id by the convention")
    async def test_only_the_derived_matters_are_touched(self):
        self.w.full("c2")
        await self.send(new(), cid="c1")
        self.assertEqual(self.w.journal("c2"), [])
        self.assertEqual({m for k, m, *_ in self.e.log}, {"mailbox_c1", "journal_c1"})

    @covers("Ordered steps: validation, idempotence decision, reservation of the mailbox entry",
            "Before the append the message does not exist and the operation may be abandoned or cleaned;")
    async def test_reservation_comes_before_the_append_commit(self):
        await self.send(new())
        self.assertEqual([m for k, m, *_ in self.e.log], ["mailbox_c1", "journal_c1"])      # reserve, then commit (the journal append)
        # interrupted between the two: the reservation exists, the message does not
        self.e.fail_append.add("journal_c1")
        before = [e["message_id"] for e in self.w.entries("c1")]
        with self.assertRaises(EngineError):
            await self.send(new("n2"))
        self.assertEqual([m["id"] for m in self.w.journal("c1")], ["n1"])        # not accepted
        self.assertEqual([e["message_id"] for e in self.w.entries("c1")], before + ["n2"])
        self.assertEqual(self.fires().__len__(), 1)                               # and nothing was woken for it

    @covers("A mailbox entry whose message_id matches no durable message in the journal is a pre-commit reservation",
            "The reservation is a pending mailbox entry")
    async def test_a_pre_commit_reservation_is_resumed_by_a_retry(self):
        self.w.mailbox("c1", [entry("n1")])
        out = await self.send(new())
        self.assertEqual(out["seq"], 1)
        self.assertEqual(len(self.w.entries("c1")), 1)
        self.assertEqual([m["id"] for m in self.w.journal("c1")], ["n1"])

    @covers("The journal is the durable stream of accepted messages;",
            "Each Matter mutation is individually atomic; the operation as a whole is a recoverable multi-Matter operation",
            "Before the append the message does not exist")
    async def test_post_commit_routing_is_repaired_on_retry(self):
        # crash after the commit: journal and (expected) entry exist, the capture never happened
        self.w.full("c4", messages=[message(1, "r1", kind="capacity_response", content={"response": {"status": "ok"}}, correlation={"intention_id": "i1"})],
                    entries=[entry("r1", "expected")], status="waiting", run_id="R1", current="m0", groups=[group("g1", "i1")])
        out = await self.send({"id": "r1", "from": "user:1", "kind": "capacity_response", "content": {"response": {"status": "ok"}},
                               "correlation": {"intention_id": "i1"}}, cid="c4")
        self.assertEqual(out["wait_group_id"], "g1")
        rt = self.w.rt("c4")
        self.assertEqual(rt["active_wait_groups"][0]["received"], [{"expected_ref": "i1", "message_id": "r1", "outcome": "success"}])
        self.assertEqual(len(self.w.journal("c4")), 1)                   # the message was not appended a second time
        again = await self.send({"id": "r1", "from": "user:1", "kind": "capacity_response", "content": {"response": {"status": "ok"}},
                                 "correlation": {"intention_id": "i1"}}, cid="c4")
        self.assertEqual(len(self.w.rt("c4")["active_wait_groups"][0]["received"]), 1)   # captured once

    # --- success statements -------------------------------------------------------------------
    async def test_expected_results_are_captured_and_satisfy_the_group(self):
        self.w.runtime("c1", "waiting", "R1", "m0", [group("g1", "i1", "i2")])
        first = await self.send(new("r1", kind="capacity_response", content={"response": {"status": "ok"}}, correlation={"intention_id": "i1"}))
        self.assertEqual(self.w.rt("c1")["status"], "waiting")            # not yet satisfied
        second = await self.send(new("r2", kind="capacity_response", content={"response": {"status": "error"}}, correlation={"intention_id": "i2"}))
        rt = self.w.rt("c1")
        self.assertEqual((rt["status"], rt["active_wait_groups"][0]["status"]), ("running", "satisfied"))
        self.assertEqual([r["outcome"] for r in rt["active_wait_groups"][0]["received"]], ["success", "error"])
        self.assertEqual(self.w.journal("c1")[-1]["content"]["response"]["status"], "error")    # detail stays in the journal

    async def test_activation_follows_the_runtime_state(self):
        await self.send(new("a"))
        self.assertEqual(len(self.fires()), 1)                            # idle: wake
        self.w.runtime("c1", "running", "R1")
        liveness.mark_live("c1", "R1")                                    # a run really executing in this process
        self.e.calls.clear()
        await self.send(new("b"))
        self.assertEqual(self.fires(), [])                                # a live run never gets a competitor
        liveness.drop_live("c1", "R1")
        await self.send(new("b2"))
        self.assertEqual(len(self.fires()), 1)                            # running but not alive here: an orphan, recovered
        self.e.calls.clear()
        self.w.runtime("c1", "suspended")
        await self.send(new("c"))
        self.assertEqual(self.fires(), [])                                # suspended stays asleep
        self.assertEqual([e["status"] for e in self.w.entries("c1")], ["pending"] * 4)

    async def test_control_semantics(self):
        r = await self.send(new("k1", kind="control", content={"action": "set_pilot", "pilot_ref": {"context": "/root/agentic", "matter_id": PILOT_ID}}))
        self.assertEqual((r["priority"], r["status"]), ("control", "consumed"))
        self.assertEqual(self.w.entries("c1"), [])                        # applied: entry removed, message kept
        self.assertEqual(len(self.w.journal("c1")), 1)
        self.w.runtime("c1", "running", "R1")
        r = await self.send(new("k2", kind="control", content={"action": "suspend"}))
        self.assertEqual((r["status"], self.w.rt("c1")["status"]), ("pending", "running"))   # deferred to a safe point

    async def test_a_delete_control_delegates_and_is_terminal(self):
        self.e.caps["/root/agentic/continuity.delete"] = continuity_delete.run
        out = await self.send(new("k", kind="control", content={"action": "delete", "cascade": True}))
        self.assertEqual(out["status"], "deleted")
        self.assertEqual([m for m in self.e.matters if m.endswith("_c1")], [])

    async def test_a_suspended_continuity_can_be_deleted(self):
        """A stopped conversation must stay deletable: a suspended continuity has no run, so nothing is in progress and the delete is applied at once."""
        self.e.caps["/root/agentic/continuity.delete"] = continuity_delete.run
        self.w.runtime("c1", "suspended")
        out = await self.send(new("k", kind="control", content={"action": "delete", "cascade": True}))
        self.assertEqual(out["status"], "deleted")
        self.assertEqual([m for m in self.e.matters if m.endswith("_c1")], [])

    async def test_the_other_controls_stay_pending_while_suspended(self):
        self.w.runtime("c1", "suspended")
        for action in ("suspend", "set_intention"):
            content = {"action": action, **({"intention": "x"} if action == "set_intention" else {})}
            out = await self.send(new(f"k_{action}", kind="control", content=content))
            self.assertEqual(out["status"], "pending", action)
        self.assertEqual(self.fires(), [])                                # and a suspended continuity is never woken by them

    async def test_a_deferred_control_wakes_a_dead_run_and_only_tells_a_live_one(self):
        """The wrapper restarted under a waiting run: the runtime still says waiting, but nobody is there to reach the safe point that applies the control."""
        self.w.runtime("c1", "waiting", "DEAD")
        out = await self.send(new("k1", kind="control", content={"action": "delete", "cascade": True}))
        self.assertEqual((out["status"], out["wake"]), ("pending", True))
        self.assertEqual(len(self.fires()), 1)                            # the orphan is taken over: its run applies the control
        self.e.calls.clear()
        self.w.runtime("c1", "waiting", "R1")
        liveness.mark_live("c1", "R1")
        out = await self.send(new("k2", kind="control", content={"action": "suspend"}))
        self.assertEqual((out["status"], out["wake"]), ("pending", False))
        self.assertEqual(self.fires(), [])                                # a live run is told, never competed with
        liveness.drop_live("c1", "R1")

    # --- failures ---------------------------------------------------------------------------------
    @covers("Fails if continuity, mailbox, or runtime are inconsistent.")
    async def test_a_missing_matter_is_an_inconsistency(self):
        for gone in ("continuity_c1", "mailbox_c1", "runtime_c1"):
            self.setUp_world = self.w.full("c1")
            del self.e.matters[gone]
            before = self.e.snapshot()
            with self.assertRaises(ConsistencyError):
                await self.send(new())
            self.assertUnchanged(before)

    @covers("Fails before mutation when incoming message data is invalid.")
    async def test_invalid_messages_fail_before_any_mutation(self):
        bad = [new(kind="nope"), {"id": "x", "kind": "user", "content": "c"}, new(content=None), new("", ), new(to="other"),
               new(correlation={"unknown": 1}), new("c", kind="control", content={"action": "explode"}), "not an object"]
        before = self.e.snapshot()
        for m in bad:
            with self.assertRaises(InvalidRequest):
                await self.send(m)
        self.assertUnchanged(before)

    @covers("Does not activate cognition when persistence or routing failed.")
    async def test_no_activation_when_persistence_failed(self):
        self.e.fail_append.add("journal_c1")
        with self.assertRaises(EngineError):
            await self.send(new())
        self.assertEqual(self.fires(), [])

    @covers("Fails explicitly with a concurrency failure when the bounded retries")
    async def test_exhausted_revision_conflicts_fail_explicitly(self):
        self.e.always_line_conflict.add("journal_c1")
        with self.assertRaises(ConcurrencyError):
            await self.send(new())
        self.assertEqual(self.fires(), [])


    # --- succession: redirection and content by reference ---------------------------------------------
    def archive(self, cid="c1", successor="c1_g2"):
        """c1 was succeeded by c1_g2 (created here, active)."""
        state = self.w.state(f"continuity_{cid}")
        state.update(status="archived", successor_id=successor)
        self.e.matters[f"continuity_{cid}"]["data"] = state
        self.w.full(successor, predecessor_id=cid)

    @covers("A message addressed to a continuity that names a successor is redirected to the active continuity of its lineage; the response carries redirected_to.")
    async def test_a_message_to_an_archived_continuity_is_redirected(self):
        self.archive()
        before = self.w.journal("c1")
        out = await self.send(new("late"))
        self.assertPayloadMatchesContract(out)
        self.assertEqual((out["continuity_id"], out["redirected_to"], out["seq"]), ("c1_g2", "c1_g2", 1))
        self.assertEqual([m["id"] for m in self.w.journal("c1_g2")], ["late"])
        self.assertEqual(self.w.journal("c1"), before)                 # the archive is never written
        self.assertEqual(self.w.entries("c1"), [])

    async def test_the_redirection_follows_the_whole_chain(self):
        self.archive()
        self.archive("c1_g2", "c1_g3")
        out = await self.send(new("far"))
        self.assertEqual(out["redirected_to"], "c1_g3")
        self.assertEqual([m["id"] for m in self.w.journal("c1_g3")], ["far"])

    @covers("A message id already accepted and consumed by an archived continuity is never reinserted in its successor.")
    async def test_a_consumed_id_is_never_reinserted_in_the_successor(self):
        self.w.full("c1", messages=[message(1, "done")], entries=[])        # accepted then consumed in c1
        self.archive()
        with self.assertRaises(Refused):
            await self.send(new("done"))
        self.assertEqual(self.w.journal("c1_g2"), [])

    @covers("A message accepted by a continuity while a succession switches its routing is forwarded to the successor, never stranded.")
    async def test_a_message_caught_by_the_switch_is_forwarded_not_stranded(self):
        self.w.full("c1_g2", predecessor_id="c1")
        original = mailbox_send._append

        async def switching(port, cid, message):
            stored = await original(port, cid, message)               # committed in c1 ...
            state = self.w.state("continuity_c1")
            state["successor_id"] = "c1_g2"                            # ... and then the succession switches the routing
            self.e.matters["continuity_c1"]["data"] = state
            return stored
        mailbox_send._append = switching
        try:
            out = await self.send(new("racing"))
        finally:
            mailbox_send._append = original
        self.assertEqual(out["redirected_to"], "c1_g2")
        self.assertIn("racing", [m["id"] for m in self.w.journal("c1_g2")])
        self.assertEqual(self.w.entries("c1"), [])                      # nothing left pending in the source

    @covers("A message is written as is in one journal line, whatever its size")
    async def test_a_large_content_is_written_as_is(self):
        big = "x" * 300_000                                       # above the inline append limit of the engine: no threshold, no reference
        out = await self.send(new("big", content=big))
        self.assertEqual(out["seq"], 1)
        self.assertEqual(self.w.journal("c1")[0]["content"], big)
        self.assertFalse([m for m in self.e.matters if m.startswith("content_")])      # no overflow Matter

    @covers("Messages of kind summary, prompt, completion, event, projection and error are history only",
            "Fails when the message kind is history only.")
    async def test_history_only_kinds_are_never_accepted_from_outside(self):
        for kind in ("summary", "prompt", "completion", "event", "projection", "error"):
            before = self.e.snapshot()
            with self.assertRaises(InvalidRequest):
                await self.send(new(f"x-{kind}", kind=kind, content="forged"))
            self.assertEqual(self.e.snapshot(), before)


    @covers("The activation requested after a message is marked wake: it answers at once and never waits for another run.")
    async def test_the_activation_it_requests_is_a_wake(self):
        fired = []
        self.e.on_fire = lambda engine, ref, params: fired.append((ref, params))
        await self.send(new("w1"))
        self.assertEqual(fired, [("/root/agentic/pilot.activate", {"continuity_id": "c1", "wake": True})])


    @covers("A wake that is refused because the callee is busy is asked again, a bounded number of times; if it is still refused, the refusal is written in the journal of the continuity as a wake_refused error line, never lost in silence.")
    async def test_a_wake_that_stays_refused_is_written_in_the_journal(self):
        from unittest import mock
        from fake_engine import FakePort
        from substrate import port as port_module
        from substrate.port import BriquePort
        busy = {"status": "error", "error": {"code": "refused", "message": "wrapper pending-work queue is full", "details": {"reason": "busy"}}}

        async def refused(port, ref, params, **kwargs):
            return busy            # the retries of the real port are covered in test_busy_retry; here the callee never takes the call
        with mock.patch.object(FakePort, "wake", BriquePort.wake), \
                mock.patch.object(FakePort, "_wake_until_taken", BriquePort._wake_until_taken, create=True), \
                mock.patch.object(FakePort, "_wake_on_its_own", BriquePort._wake_on_its_own, create=True), \
                mock.patch.object(FakePort, "call_capacity_response", refused):
            await self.send(new("w1"))
            await asyncio.gather(*list(port_module._WAKES))
        lines = [m for m in self.w.journal("c1") if m["kind"] == "error" and (m.get("content") or {}).get("event") == "wake_refused"]
        self.assertEqual(len(lines), 1)
        self.assertEqual((lines[0]["content"]["reason"], lines[0]["content"]["attempts"]), ("busy", port_module.WAKE_ATTEMPTS))
        # and the message itself was not lost: it is in the mailbox, waiting for whatever wakes the continuity next
        self.assertEqual([e["status"] for e in self.w.entries("c1")], ["pending"])

    @covers("A wake transport failure is reconciled with durable state and, if work is still idle and pending, is written as a wake_failed error with outcome unknown; it is never replayed blindly.")
    async def test_a_wake_timeout_is_visible_only_when_work_still_waits(self):
        from unittest import mock
        from fake_engine import FakePort
        from substrate import port as port_module
        from substrate.port import BriquePort

        async def timed_out(port, ref, params, **kwargs):
            raise asyncio.TimeoutError()
        with mock.patch.object(FakePort, "wake", BriquePort.wake), \
                mock.patch.object(FakePort, "_wake_until_taken", BriquePort._wake_until_taken, create=True), \
                mock.patch.object(FakePort, "_wake_on_its_own", BriquePort._wake_on_its_own, create=True), \
                mock.patch.object(FakePort, "call_capacity_response", timed_out):
            await self.send(new("w-timeout"))
            await asyncio.gather(*list(port_module._WAKES))
        lines = [m for m in self.w.journal("c1") if m["kind"] == "error" and (m.get("content") or {}).get("event") == "wake_failed"]
        self.assertEqual(len(lines), 1)
        self.assertEqual((lines[0]["content"]["reason"], lines[0]["content"]["outcome"]), ("timeout", "unknown"))
        self.assertNotIn("attempts", lines[0]["content"])

    # --- the inference window ---------------------------------------------------------------------
    async def held_window(self):
        """Another task holds the inference window of c1 (a model is thinking); returns (task, release event)."""
        held, release = asyncio.Event(), asyncio.Event()

        async def holder():
            async with inference_window.hold("c1"):
                held.set()
                await release.wait()
        task = asyncio.ensure_future(holder())
        await held.wait()
        return task, release

    @covers("The append waits for the inference window of the continuity unless the writer is its holder")
    async def test_the_append_waits_for_the_inference_window_unless_the_writer_holds_it(self):
        holder, release = await self.held_window()
        waiting = asyncio.ensure_future(self.send(new("w1")))
        await asyncio.sleep(0.05)
        self.assertFalse(waiting.done())
        self.assertEqual([m["id"] for m in self.w.journal("c1")], [])                       # nothing is written while an inference holds the window
        self.assertEqual([e["message_id"] for e in self.w.entries("c1")], ["w1"])           # the entry was reserved before the wait: a retry resumes it
        self.assertEqual(self.e.keepalives.count("mailbox.send"), 1)                       # a running acknowledgement while it waits
        release.set()
        out = await waiting
        await holder
        self.assertEqual(out["seq"], 1)                                                    # the contract of the result does not change: seq is known at return
        async with inference_window.hold("c1"):                                            # the holder itself writes without waiting
            self.assertEqual((await asyncio.wait_for(self.send(new("w2")), 1))["seq"], 2)

    @covers("Writers waiting for the inference window are served in arrival order")
    async def test_waiting_writers_are_served_in_arrival_order(self):
        holder, release = await self.held_window()
        tool = asyncio.ensure_future(self.send(new("r1", kind="capacity_response", content={"response": {"status": "ok"}}, correlation={"intention_id": "i1"})))
        await asyncio.sleep(0.02)
        user = asyncio.ensure_future(self.send(new("u1")))
        await asyncio.sleep(0.02)
        release.set()
        await asyncio.gather(tool, user)
        await holder
        self.assertEqual([m["id"] for m in self.w.journal("c1")], ["r1", "u1"])            # the answer that came first is written first

    @covers("A user message sent to a continuity whose pilot is a pilot_definition and whose run is running or waiting opens the inference window")
    async def test_a_user_request_for_a_working_run_opens_the_window_and_reserves_it(self):
        inference_window.RESERVATION_TTL_S = 5
        for status, opens in (("running", True), ("waiting", True), ("idle", False), ("suspended", False)):
            inference_window.clear("c1")
            inference_window._windows.clear()
            self.w.runtime("c1", status, "R1" if status in ("running", "waiting") else None)
            mid = f"u_{status}"
            out = await self.send(new(mid))
            self.assertGreaterEqual(out["seq"], 1)                                          # written in every case
            self.assertEqual(inference_window.reserved_ids("c1"), {mid} if opens else set(), status)
        # closed to the others while it waits for the run that absorbs it; the loop takes it over
        inference_window._windows.clear()
        self.w.runtime("c1", "running", "R1")
        await self.send(new("u1"))
        other = asyncio.ensure_future(self.send(new("r1", kind="capacity_response", content={"response": {"status": "ok"}}, correlation={"intention_id": "i1"})))
        await asyncio.sleep(0.05)
        self.assertFalse(other.done())

        async def loop():
            inference_window.enter_loop("c1")
            async with inference_window.hold("c1", adopt=["u1"]):
                pass
        await asyncio.ensure_future(loop())
        await asyncio.wait_for(other, 1)
        # a reservation nobody takes expires: the message was a normal one all along
        inference_window.RESERVATION_TTL_S = 0.05
        inference_window._windows.clear()
        await self.send(new("u2"))
        await asyncio.sleep(0.1)
        self.assertEqual(inference_window.reserved_ids("c1"), set())
        self.assertEqual(next(e for e in self.w.entries("c1") if e["message_id"] == "u2")["priority"], "normal")

    @covers("mailbox.send never refuses a user message addressed to a continuity whose pilot is a Capacity")
    async def test_a_user_message_to_a_capacity_pilot_is_accepted_and_opens_nothing(self):
        inference_window.RESERVATION_TTL_S = 5
        self.w.full("c2", pilot_ref={"context": "/root/shop", "capacity": "plan"}, status="running", run_id="R1")
        out = await self.send(new("u1"), cid="c2")
        self.assertEqual((out["seq"], out["status"]), (1, "pending"))
        self.assertEqual(inference_window.reserved_ids("c2"), set())                       # no run absorbs for a Capacity pilot: nothing to reserve


if __name__ == "__main__":
    unittest.main()
