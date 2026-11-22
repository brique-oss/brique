import asyncio
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from fake_engine import PILOT_ID, make_engine  # noqa: E402

from substrate.errors import ConsistencyError, InvalidRequest, NotFound, Refused  # noqa: E402
from substrate.ids import matter_ids  # noqa: E402
from substrate import inference_window, liveness  # noqa: E402
import continuity_create, continuity_delete, mailbox_ack, mailbox_read, mailbox_release, mailbox_send  # noqa: E402

PILOT = {"context": "/root/agentic", "matter_id": PILOT_ID}


def msg(i, kind="user", content="hello", **kw):
    return dict({"id": i, "from": "user:1", "kind": kind, "content": content}, **kw)


class Base(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        # state shared by the whole process: a test must not inherit the reservations or liveness of the previous one (each test has its own event loop)
        liveness._live.clear()
        liveness._events.clear()
        inference_window._windows.clear()
        self.e = make_engine(pilot=False)
        self.p = self.e.port("test")

    async def create(self, cid="c1", **kw):
        return await continuity_create.run(self.p, dict({"continuity_id": cid, "pilot_ref": PILOT, "initial_intention": "goal"}, **kw))

    def journal(self, cid):
        """The lines of the exchange: the cognitive frame, first line of every journal, is left out (seq 1: the first message is seq 2)."""
        return [m for m in self.e.matters[matter_ids(cid).journal]["lines"] if m["kind"] != "framework"]

    async def state(self, cid, which):
        return (await self.p.matter_read(getattr(matter_ids(cid), which))).content

    async def acquire(self, cid="c1", run_id="R1"):
        """Hand the continuity to a run, without pilot.activate."""
        ids = matter_ids(cid)
        v = await self.p.matter_read(ids.runtime)
        f = dict(v.content, status="running", run_id=run_id)
        await self.p.matter_write(ids.runtime, content=f, expected_revision=v.revision)


class CreateDelete(Base):
    async def test_create_builds_three_matters_and_structure(self):
        r = await self.create()
        self.assertTrue(r["created"])
        self.assertEqual((await self.state("c1", "runtime"))["status"], "idle")
        self.assertEqual((await self.state("c1", "mailbox"))["entries"], [])
        c = await self.state("c1", "continuity")
        self.assertEqual(c["current_intention"], c["initial_intention"])
        self.assertEqual(len(self.e.structures["continuities"]["functional"]["items"]), 1)
        self.assertEqual(self.e.matters["continuity_c1"]["functional"], {"format": "json", "schema_ref": "/root/agentic/schema/continuity_data"})
        self.assertNotIn("schema_ref", self.e.matters["continuity_c1"]["data"])    # the content is the substance only

    async def test_create_is_idempotent_and_detects_contradiction(self):
        await self.create()
        again = await self.create()
        self.assertFalse(again["created"])
        self.assertEqual(len(self.e.structures["continuities"]["functional"]["items"]), 1)
        with self.assertRaises(ConsistencyError):
            await self.create(initial_intention="another goal")

    async def test_create_completes_an_interrupted_creation(self):
        await self.create()
        del self.e.matters["runtime_c1"]
        del self.e.structures["continuities"]["functional"]["items"][:]
        r = await self.create()
        self.assertTrue(r["created"])
        self.assertIn("runtime_c1", self.e.matters)
        self.assertEqual(len(self.e.structures["continuities"]["functional"]["items"]), 1)

    async def test_parent_child_relation_and_unknown_parent(self):
        await self.create("p")
        await self.create("k", parent_id="p")
        self.assertEqual((await self.state("p", "continuity"))["children"], ["k"])
        await self.create("k", parent_id="p")   # idempotent
        self.assertEqual((await self.state("p", "continuity"))["children"], ["k"])
        with self.assertRaises(NotFound):
            await self.create("z", parent_id="ghost")

    async def test_delete_without_cascade_detaches_children(self):
        await self.create("p"); await self.create("k", parent_id="p")
        r = await continuity_delete.run(self.p, {"continuity_id": "p"})
        self.assertTrue(r["deleted"]); self.assertEqual(r["detached_children"], ["k"])
        self.assertNotIn("continuity_p", self.e.matters)
        self.assertNotIn("parent_id", self.e.matters["continuity_k"]["data"])

    async def test_delete_cascade_and_parent_cleanup(self):
        await self.create("p"); await self.create("k", parent_id="p"); await self.create("g", parent_id="k")
        r = await continuity_delete.run(self.p, {"continuity_id": "k", "cascade": True})
        self.assertEqual(sorted(r["deleted_children"]), ["g"])
        self.assertEqual((await self.state("p", "continuity"))["children"], [])
        for m in ("continuity_k", "mailbox_k", "runtime_k", "continuity_g"):
            self.assertNotIn(m, self.e.matters)
        self.assertEqual(len(self.e.structures["continuities"]["functional"]["items"]), 1)   # only p remains

    async def test_delete_is_idempotent_and_finishes_partial(self):
        await self.create("c1")
        del self.e.matters["runtime_c1"]            # interrupted deletion state
        r = await continuity_delete.run(self.p, {"continuity_id": "c1"})
        self.assertTrue(r["deleted"])
        again = await continuity_delete.run(self.p, {"continuity_id": "c1"})
        self.assertFalse(again["deleted"])
        self.assertEqual([m for m in self.e.matters if m.endswith("_c1")], [])


class Mailbox(Base):
    async def asyncSetUp(self):
        await super().asyncSetUp()
        await self.create("c1")

    async def send(self, m, cid="c1"):
        return await mailbox_send.run(self.p, {"continuity_id": cid, "message": m})

    async def test_send_normal_appends_reserves_and_wakes(self):
        r = await self.send(msg("m1"))
        self.assertEqual((r["seq"], r["priority"], r["status"], r["wake"]), (2, "normal", "pending", True))
        self.assertEqual([e["message_id"] for e in (await self.state("c1", "mailbox"))["entries"]], ["m1"])
        self.assertIn("fire:/root/agentic/pilot.activate", self.e.calls)

    async def test_seq_is_max_plus_one_and_never_revision(self):
        for i in range(1, 4):
            r = await self.send(msg(f"m{i}"))
            self.assertEqual(r["seq"], i + 1)                      # the cognitive frame is line 1

    async def test_concurrent_sends_get_distinct_contiguous_seq(self):
        results = await asyncio.gather(*[self.send(msg(f"m{i}")) for i in range(12)])
        self.assertEqual(sorted(r["seq"] for r in results), list(range(2, 14)))
        self.assertEqual(len((await self.state("c1", "mailbox"))["entries"]), 12)

    async def test_concurrent_sends_without_lock_still_safe_via_revision(self):
        # bypass the local lock: expected_revision alone must protect the sequence
        from substrate import locks
        from contextlib import asynccontextmanager

        @asynccontextmanager
        async def no_lock(*ids, yielding=None):
            yield
        original = locks.critical
        import substrate.cas as cas, mailbox_send as ms
        cas.critical = no_lock
        try:
            results = await asyncio.gather(*[self.send(msg(f"x{i}")) for i in range(10)])
        finally:
            cas.critical = original
        self.assertEqual(sorted(r["seq"] for r in results), list(range(2, 12)))
        self.assertGreater(self.e.conflicts, 0)

    async def test_idempotent_retry_and_consumed_reinsertion(self):
        await self.send(msg("m1"))
        again = await self.send(msg("m1"))
        self.assertEqual(again["seq"], 2)
        self.assertEqual(len(self.journal("c1")), 1)
        await self.acquire(); got = await mailbox_read.run(self.p, {"continuity_id": "c1", "run_id": "R1", "priority": "normal"})
        await mailbox_ack.run(self.p, {"continuity_id": "c1", "message_id": got["message_id"], "run_id": "R1"})
        with self.assertRaises(Refused):
            await self.send(msg("m1"))
        self.assertEqual(len(self.journal("c1")), 1)

    async def test_orphan_reservation_is_inert_and_resumed_by_retry(self):
        ids = matter_ids("c1")
        v = await self.p.matter_read(ids.mailbox)
        await self.p.matter_write(ids.mailbox, content=dict(v.content, entries=[{"message_id": "m9", "priority": "normal", "status": "pending"}]), expected_revision=v.revision)
        await self.acquire()
        empty = await mailbox_read.run(self.p, {"continuity_id": "c1", "run_id": "R1", "priority": "normal"})
        self.assertIsNone(empty["message_id"])           # no durable message: never claimable
        r = await self.send(msg("m9"))                   # the retry resumes the reservation
        self.assertEqual(r["seq"], 2)
        self.assertEqual(len((await self.state("c1", "mailbox"))["entries"]), 1)

    async def test_read_priority_then_seq_and_ownership(self):
        await self.send(msg("n1")); await self.send(msg("n2"))
        await self.acquire()
        with self.assertRaises(Refused):
            await mailbox_read.run(self.p, {"continuity_id": "c1", "run_id": "OTHER", "priority": "normal"})
        with self.assertRaises(InvalidRequest):
            await mailbox_read.run(self.p, {"continuity_id": "c1", "run_id": "R1", "priority": "expected"})
        a = await mailbox_read.run(self.p, {"continuity_id": "c1", "run_id": "R1", "priority": "normal"})
        b = await mailbox_read.run(self.p, {"continuity_id": "c1", "run_id": "R1", "priority": "normal"})
        c = await mailbox_read.run(self.p, {"continuity_id": "c1", "run_id": "R1", "priority": "normal"})
        self.assertEqual((a["message_id"], b["message_id"], c["message_id"]), ("n1", "n2", None))

    async def test_concurrent_reads_never_claim_twice(self):
        for i in range(6): await self.send(msg(f"n{i}"))
        await self.acquire()
        got = await asyncio.gather(*[mailbox_read.run(self.p, {"continuity_id": "c1", "run_id": "R1", "priority": "normal"}) for _ in range(6)])
        ids = [g["message_id"] for g in got]
        self.assertEqual(sorted(ids), [f"n{i}" for i in range(6)])

    async def test_ack_release_rules(self):
        await self.send(msg("m1")); await self.acquire()
        with self.assertRaises(Refused):
            await mailbox_ack.run(self.p, {"continuity_id": "c1", "message_id": "m1", "run_id": "R1"})   # pending
        await mailbox_read.run(self.p, {"continuity_id": "c1", "run_id": "R1", "priority": "normal"})
        with self.assertRaises(Refused):
            await mailbox_ack.run(self.p, {"continuity_id": "c1", "message_id": "m1", "run_id": "R2"})
        await mailbox_release.run(self.p, {"continuity_id": "c1", "message_id": "m1", "run_id": "R1"})
        e = (await self.state("c1", "mailbox"))["entries"][0]
        self.assertEqual(e["status"], "pending"); self.assertNotIn("claimed_by", e)
        await mailbox_read.run(self.p, {"continuity_id": "c1", "run_id": "R1", "priority": "normal"})
        await mailbox_ack.run(self.p, {"continuity_id": "c1", "message_id": "m1", "run_id": "R1"})
        self.assertEqual((await self.state("c1", "mailbox"))["entries"], [])
        self.assertEqual(len(self.journal("c1")), 1)   # history untouched
        with self.assertRaises(NotFound):
            await mailbox_ack.run(self.p, {"continuity_id": "c1", "message_id": "m1", "run_id": "R1"})

    async def test_validation(self):
        with self.assertRaises(InvalidRequest): await self.send(msg("a", kind="nope"))
        with self.assertRaises(InvalidRequest): await self.send({"id": "a", "kind": "user", "content": "x"})   # no from
        with self.assertRaises(InvalidRequest): await self.send(msg("a", to="other"))
        with self.assertRaises(InvalidRequest): await self.send(msg("a", kind="control", content={"action": "explode"}))
        with self.assertRaises(ConsistencyError): await mailbox_send.run(self.p, {"continuity_id": "ghost", "message": msg("a")})

    async def test_control_immediate_when_idle_removes_entry_keeps_message(self):
        r = await self.send(msg("k1", kind="control", content={"action": "set_intention", "intention": "new goal"}))
        self.assertEqual(r["status"], "consumed")
        self.assertEqual((await self.state("c1", "continuity"))["current_intention"], "new goal")
        self.assertEqual((await self.state("c1", "continuity"))["initial_intention"], "goal")
        self.assertEqual((await self.state("c1", "mailbox"))["entries"], [])
        self.assertEqual(len(self.journal("c1")), 1)

    async def test_suspend_then_resume_then_wake(self):
        await self.send(msg("k1", kind="control", content={"action": "suspend"}))
        self.assertEqual((await self.state("c1", "runtime"))["status"], "suspended")
        r = await self.send(msg("m1"))
        self.assertFalse(r["wake"])                          # suspended: no automatic wake
        self.e.calls.clear()
        r = await self.send(msg("k2", kind="control", content={"action": "resume"}))
        self.assertEqual((await self.state("c1", "runtime"))["status"], "idle")
        self.assertTrue(r["wake"]); self.assertIn("fire:/root/agentic/pilot.activate", self.e.calls)

    async def test_control_deferred_while_running(self):
        await self.acquire()
        r = await self.send(msg("k1", kind="control", content={"action": "suspend"}))
        self.assertEqual((r["priority"], r["status"]), ("control", "pending"))
        self.assertEqual((await self.state("c1", "runtime"))["status"], "running")
        got = await mailbox_read.run(self.p, {"continuity_id": "c1", "run_id": "R1", "priority": "control"})
        self.assertEqual(got["message_id"], "k1")

    async def test_delete_control_is_terminal(self):
        r = await self.send(msg("k1", kind="control", content={"action": "delete"}))
        self.assertEqual(r["status"], "deleted")
        self.assertEqual([m for m in self.e.matters if m.endswith("_c1")], [])


if __name__ == "__main__":
    unittest.main()
