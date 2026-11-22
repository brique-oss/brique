import unittest

from .harness import CapacityContract, covers, entry, message
from substrate import locks
from substrate.errors import ConcurrencyError, ConsistencyError, EngineError, InvalidRequest, Refused
import continuity_delete


class ContinuityDeleteContract(CapacityContract):
    CAP, MODULE = "continuity.delete", continuity_delete

    async def asyncSetUp(self):
        await super().asyncSetUp()
        # tree: p -> k -> g, and p -> s ; plus an unrelated continuity u that received a message from k
        self.w.full("p", children=["k", "s"])
        self.w.full("k", parent="p", children=["g"])
        self.w.full("g", parent="k")
        self.w.full("s", parent="p")
        self.w.full("u", messages=[message(1, "from-k", content="hello from k", **{"from": "continuity:k"})])
        self.e.caps["/root/agentic/continuity.delete"] = continuity_delete.run     # cascade recursion only

    async def delete(self, cid="k", **kw):
        return await self.call(continuity_id=cid, **kw)

    def gone(self, cid):
        return [m for m in self.e.matters if m.endswith(f"_{cid}")] == []

    def structure_ids(self):
        return sorted(i["matter_id"] for i in self.e.structures["continuities"]["functional"]["items"])

    async def test_payload_matches_the_declared_outputs(self):
        out = await self.delete("g")
        self.assertPayloadMatchesContract(out)
        self.assertEqual((out["deleted"], out["cascade"], out["detached_children"], out["deleted_children"]), (True, False, [], []))

    async def test_continuity_id_is_required(self):
        with self.assertRaises(InvalidRequest):
            await self.call()

    @covers("A deleted continuity leaves no owned mailbox or runtime behind.", "Deletion never creates a closed or archived continuity state.",
            "The Matter IDs of a continuity are derived from continuity_id")
    async def test_every_owned_matter_disappears(self):
        await self.delete("g")
        self.assertTrue(self.gone("g"))
        self.assertEqual(sorted(m for m in self.e.matters if "g" == m.split("_", 1)[-1]), [])

    @covers("A deleted continuity leaves no parent children reference behind.", "A deleted continuity leaves no Structure reference behind.")
    async def test_parent_and_structure_are_cleaned(self):
        await self.delete("s")
        self.assertEqual(self.w.state("continuity_p")["children"], ["k"])
        self.assertNotIn("continuity_s", self.structure_ids())

    @covers("A deleted continuity leaves no reading cursor behind")
    async def test_the_reading_cursors_of_a_deleted_continuity_go_with_it_and_only_those(self):
        """Without it the Matter of the cursors grew with every conversation ever created and deleted (241 cursors, none of a conversation that still existed)."""
        self.w.put("reading_cursors", "reading", {"cursors": [
            {"reader": "user:1", "lineage_id": "p", "seq": 9}, {"reader": "someone:2", "lineage_id": "p", "seq": 4},
            {"reader": "user:1", "lineage_id": "k", "seq": 3}, {"reader": "user:1", "lineage_id": "u", "seq": 7}]})
        await self.call(continuity_id="p", cascade=True)                                    # p, k, g, s go
        left = [(c["reader"], c["lineage_id"], c["seq"]) for c in self.w.state("reading_cursors")["cursors"]]
        self.assertEqual(left, [("user:1", "u", 7)])                                        # every reader's cursor on a deleted one, none of the others
        before = self.e.snapshot()
        await self.call(continuity_id="p", cascade=True)                                    # again: nothing left to do, nothing written
        self.assertEqual(self.e.snapshot(), before)

    async def test_deleting_without_a_cursors_matter_is_fine(self):
        self.assertNotIn("reading_cursors", self.e.matters)
        out = await self.call(continuity_id="s")
        self.assertTrue(out["deleted"])
        self.assertNotIn("reading_cursors", self.e.matters)                                 # and the Matter is not created for it

    @covers("cascade defaults to false.", "Without cascade, children survive and their parent_id becomes empty.")
    async def test_without_cascade_children_survive_detached(self):
        out = await self.delete("k")
        self.assertEqual(out["detached_children"], ["g"])
        self.assertFalse(self.gone("g"))
        self.assertNotIn("parent_id", self.w.state("continuity_g"))
        self.assertIn("continuity_g", self.structure_ids())

    @covers("With cascade, every descendant follows the same deletion semantics.",
            "A deleted continuity is always removed from the canonical continuities Structure, and the same rule applies recursively")
    async def test_cascade_deletes_the_whole_subtree_and_its_structure_references(self):
        out = await self.delete("p", cascade=True)
        self.assertEqual(sorted(out["deleted_children"]), ["g", "k", "s"])
        for cid in ("p", "k", "g", "s"):
            self.assertTrue(self.gone(cid), cid)
        self.assertEqual(self.structure_ids(), ["continuity_u"])

    @covers("Messages already stored in another continuity are never rewritten or deleted.",
            "Deleting a continuity never rolls back already materialized external effects.")
    async def test_history_elsewhere_is_never_rewritten(self):
        before = self.w.journal("u")
        await self.delete("k", cascade=True)
        self.assertEqual(self.w.journal("u"), before)
        self.assertEqual(self.e.calls.count("/root/agentic/continuity.delete"), 1)      # only the cascade recursion, no external effect

    @covers("A late external result never recreates a deleted continuity.", "A continuity already fully deleted yields an idempotent success")
    async def test_deleting_again_is_a_success_without_effect(self):
        await self.delete("g")
        snapshot = self.e.snapshot()
        again = await self.delete("g")
        self.assertEqual((again["deleted"], again["deleted_children"], again["detached_children"]), (False, [], []))
        self.assertUnchanged(snapshot)

    @covers("Deletion is a recoverable multi-step protocol executed in this order",
            "Deleting the runtime before the mailbox and the continuity prevents any new activation")
    async def test_steps_run_in_order_and_runtime_goes_first(self):
        await self.delete("k")
        log = [(kind, mid) for kind, mid, *_ in self.e.log]
        self.assertEqual(log, [("write", "continuity_g"), ("write", "continuity_p"), ("structure_patch", "continuities"),
                               ("delete", "runtime_k"), ("delete", "mailbox_k"), ("delete", "journal_k"), ("delete", "continuity_k")])

    @covers("Writes to a parent or child Matter and to the canonical continuities Structure carry the same expected_revision guard")
    async def test_relation_writes_are_guarded(self):
        await self.delete("k")
        for kind, mid, guard, how in self.e.log:
            if kind in ("write", "structure_patch"):
                self.assertIsNotNone(guard, (kind, mid))
        self.assertEqual({how for kind, mid, g, how in self.e.log if kind == "write"}, {"data"})

    @covers("Each step treats an already absent or already cleaned target as an idempotent success when the remaining state is consistent;",
            "Each Matter mutation is individually atomic; the operation as a whole is a recoverable multi-Matter operation",
            "A partial deletion must remain explicitly detectable and recoverable rather than being reported as complete.")
    async def test_an_interrupted_deletion_is_detectable_and_completed_by_a_rerun(self):
        self.e.fail_delete.add("mailbox_g")
        with self.assertRaises(EngineError):
            await self.delete("g")
        self.assertIn("continuity_g", self.e.matters)           # the continuity goes last: still there, so detectable
        self.assertNotIn("runtime_g", self.e.matters)
        self.e.fail_delete.clear()
        out = await self.delete("g")
        self.assertTrue(out["deleted"])
        self.assertTrue(self.gone("g"))

    @covers("Locks held simultaneously are acquired in deterministic continuity_id order;")
    async def test_no_parent_lock_is_held_around_the_recursive_child_call(self):
        seen = []

        async def spy(port, params):
            seen.append(("p" in locks._locks and locks._locks["p"][0].locked()))
            return await continuity_delete.run(port, params)
        self.e.caps["/root/agentic/continuity.delete"] = spy
        await self.delete("p", cascade=True)
        self.assertTrue(seen and not any(seen))

    @covers("Fails if the target continuity bundle is internally inconsistent.")
    async def test_an_incoherent_bundle_is_refused_without_change(self):
        self.e.matters["mailbox_g"]["data"]["continuity_id"] = "someone-else"
        before = self.e.snapshot()
        with self.assertRaises(ConsistencyError):
            await self.delete("g")
        self.assertUnchanged(before)

    @covers("Fails if a required relation cleanup cannot be completed coherently.")
    async def test_a_failing_relation_cleanup_stops_before_any_owned_matter_is_removed(self):
        self.e.fail_structure_patch = True
        with self.assertRaises(EngineError):
            await self.delete("g")
        for matter in ("continuity_g", "mailbox_g", "runtime_g"):
            self.assertIn(matter, self.e.matters)

    @covers("Fails explicitly with a concurrency failure when the bounded retries")
    async def test_exhausted_revision_conflicts_fail_explicitly(self):
        self.e.always_conflict.add("continuity_p")
        with self.assertRaises(ConcurrencyError):
            await self.delete("k")
        self.assertIn("continuity_k", self.e.matters)


    # --- lineage and spilled contents -------------------------------------------------------------------
    def lineage(self):
        """c1 -> c1_g2 -> c1_g3 (active), c1 and c1_g2 archived."""
        self.w.full("c1", successor_id="c1_g2")
        self.w.full("c1_g2", predecessor_id="c1", successor_id="c1_g3")
        self.w.full("c1_g3", predecessor_id="c1_g2")
        for cid, succ, pred, gen in (("c1", "c1_g2", None, 1), ("c1_g2", "c1_g3", "c1", 2), ("c1_g3", None, "c1_g2", 3)):
            st = self.w.state(f"continuity_{cid}")
            st.update(lineage_id="c1", generation=gen)
            if succ:
                st.update(status="archived", successor_id=succ)
            if pred:
                st["predecessor_id"] = pred
            self.e.matters[f"continuity_{cid}"]["data"] = st

    @covers("A continuity that belongs to a lineage of successions is deleted only with cascade, which removes the whole lineage, newest generation first.",
            "Fails when the continuity belongs to a lineage of successions and cascade is not requested.")
    async def test_a_lineage_is_deleted_whole_or_not_at_all(self):
        self.lineage()
        before = self.e.snapshot()
        for member in ("c1", "c1_g2", "c1_g3"):
            with self.assertRaises(Refused):
                await self.delete(member)
            self.assertEqual(self.e.snapshot(), before)
        out = await self.delete("c1_g2", cascade=True)             # from any member
        self.assertTrue(out["deleted"])
        for member in ("c1", "c1_g2", "c1_g3"):
            self.assertTrue(self.gone(member), member)
        self.assertEqual([m for m in self.structure_ids() if "c1" in m], [])

    async def test_the_journal_and_the_in_memory_continuity_go_with_the_continuity(self):
        from substrate import continuity_store
        self.w.full("d", messages=[message(1, "a"), message(2, "b")])
        await continuity_store.get(self.p, "d")                            # the object is built from the journal
        self.assertIn("d", continuity_store._home(self.p)["objects"])
        await self.delete("d")
        self.assertNotIn("journal_d", self.e.matters)
        self.assertNotIn("d", continuity_store._home(self.p)["objects"])    # evicted with the Matters
        self.assertFalse([m for m in self.e.matters if m.startswith("content_")])    # there is no overflow Matter any more


if __name__ == "__main__":
    unittest.main()
