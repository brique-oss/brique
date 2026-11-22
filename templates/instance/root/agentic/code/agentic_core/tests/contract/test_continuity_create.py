import unittest

from .harness import PILOT, PILOT_ID, CapacityContract, covers, message
from substrate.errors import ConcurrencyError, ConsistencyError, EngineError, InvalidRequest, NotFound
import continuity_create
from substrate import pilot_ref as pilot_ref_module


class ContinuityCreateContract(CapacityContract):
    CAP, MODULE = "continuity.create", continuity_create

    async def create(self, cid="c1", **kw):
        return await self.call(**dict({"continuity_id": cid, "pilot_ref": PILOT, "initial_intention": "goal"}, **kw))

    def structure_items(self):
        return self.e.structures["continuities"]["functional"]["items"]

    async def test_payload_matches_the_declared_outputs(self):
        out = await self.create()
        self.assertPayloadMatchesContract(out)
        self.assertEqual(out, {"continuity_id": "c1", "created": True})

    @covers("A valid continuity always has exactly one associated mailbox.", "A valid continuity always has exactly one associated runtime.",
            "initial_intention is present at creation.", "current_intention initially equals initial_intention.",
            "pilot_ref is present at creation.", "The journal initially holds only the cognitive frame line.",
            "The first line of the journal is the cognitive frame, copied from its Matter at creation (kind framework);", "runtime initially has status idle.",
            "runtime initially has no run_id.", "runtime initially has no current_message_id.",
            "runtime initially has no active wait groups.", "mailbox initially has no entries.",
            "Creation does not send the first mailbox message.")
    async def test_the_initial_state_of_the_four_matters(self):
        await self.create(initial_intention={"goal": "ship"})
        c, m, r = self.w.state("continuity_c1"), self.w.state("mailbox_c1"), self.w.state("runtime_c1")
        self.assertEqual(c["initial_intention"], {"goal": "ship"})
        self.assertEqual(c["current_intention"], c["initial_intention"])
        self.assertEqual(c["pilot_ref"], PILOT)
        self.assertNotIn("messages", c)                                   # the header holds no messages: they live in the journal
        self.assertEqual(c["journal_ref"], {"context": "/root/agentic", "matter_id": "journal_c1"})
        self.assertEqual([m["kind"] for m in self.w.lines("c1")], ["framework"])   # the journal opens with the cognitive frame, nothing else
        self.assertEqual((c["id"], c["children"], c["parent_id"]), ("c1", [], None))
        self.assertEqual((m["continuity_id"], m["entries"]), ("c1", []))
        self.assertEqual((r["continuity_id"], r["status"], r["run_id"], r["current_message_id"], r["active_wait_groups"]),
                         ("c1", "idle", None, None, []))

    async def test_the_first_line_is_the_cognitive_frame_copied_from_its_matter(self):
        await self.create()
        first = self.w.lines("c1")[0]
        self.assertEqual((first["kind"], first["seq"], first["id"]), ("framework", 1, "framework_c1"))
        self.assertEqual(first["content"], {"text": "FRAMEWORK TEXT", "source": "/root/agentic/prompt/pilot_framework"})
        self.e.texts[("/root/agentic/prompt", "pilot_framework")] = "CHANGED LATER"
        await self.create()                                                        # a retry: the same creation, no second line
        self.assertEqual([m["content"]["text"] for m in self.w.lines("c1")], ["FRAMEWORK TEXT"])

    async def test_a_missing_or_empty_frame_refuses_the_creation_before_any_effect(self):
        del self.e.texts[("/root/agentic/prompt", "pilot_framework")]
        with self.assertRaises(NotFound):
            await self.create()
        self.e.texts[("/root/agentic/prompt", "pilot_framework")] = "  \n"
        with self.assertRaises(InvalidRequest):
            await self.create()
        self.assertFalse([m for m in self.e.matters if m.endswith("c1")])          # nothing was created

    @covers("The Matter IDs of a continuity are derived deterministically from its continuity_id")
    async def test_matter_ids_follow_the_convention(self):
        await self.create("abc-1")
        self.assertEqual(sorted(m for m in self.e.matters if m.endswith("abc-1")), ["continuity_abc-1", "journal_abc-1", "mailbox_abc-1", "runtime_abc-1"])

    @covers("Parent and child relations are explicit and bidirectionally coherent when parent_id is provided.")
    async def test_parent_and_child_are_bidirectional(self):
        self.w.full("p")
        await self.create("k", parent_id="p")
        self.assertEqual(self.w.state("continuity_k")["parent_id"], "p")
        self.assertEqual(self.w.state("continuity_p")["children"], ["k"])

    @covers("Structure placement is independent of parent-child cognitive relation.",
            "Every new continuity is automatically referenced by the canonical continuities Structure.")
    async def test_every_continuity_is_in_the_structure_whatever_its_parentage(self):
        self.w.full("p", structure=False)
        await self.create("k", parent_id="p")
        await self.create("r")
        refs = [i["matter_id"] for i in self.structure_items()]
        self.assertEqual(sorted(refs), ["continuity_k", "continuity_r"])        # p was not added, k and r were
        self.assertEqual({i["role"] for i in self.structure_items()}, {"continuity"})

    @covers("Creation does not invoke pilot.activate.", "Does not start cognition after partial creation.")
    async def test_cognition_is_never_started(self):
        await self.create()
        self.assertEqual([c for c in self.e.calls if "pilot.activate" in c], [])
        with self.assertRaises(EngineError):
            self.e.fail_create.add("runtime_z")
            await self.create("z")
        self.assertEqual([c for c in self.e.calls if "pilot.activate" in c], [])
        self.assertEqual(self.e.tasks, [])

    @covers("Creation is a recoverable multi-step protocol executed in this order")
    async def test_steps_run_in_the_documented_order(self):
        self.w.full("p")
        await self.create("k", parent_id="p")
        order = [(kind, mid) for kind, mid, *_ in self.e.log]
        self.assertEqual(order, [("create", "journal_k"), ("create", "continuity_k"), ("create", "mailbox_k"), ("create", "runtime_k"),
                                 ("append", "journal_k"), ("write", "continuity_p"), ("structure_patch", "continuities")])

    @covers("Each step treats an already realized step in its expected state as an idempotent success;",
            "Each Matter mutation is individually atomic; the operation as a whole is a recoverable multi-Matter operation")
    async def test_rerun_completes_an_interrupted_creation(self):
        await self.create()
        del self.e.matters["runtime_c1"]                      # interruption after two Matters
        self.structure_items().clear()                        # ... and before the Structure relation
        out = await self.create()
        self.assertTrue(out["created"])
        self.assertIn("runtime_c1", self.e.matters)
        self.assertEqual(len(self.structure_items()), 1)
        again = await self.create()                          # a complete creation is an idempotent success
        self.assertFalse(again["created"])
        self.assertEqual(len(self.structure_items()), 1)

    @covers("Fails with a consistency failure when an existing Matter or relation contradicts the expected state.",
            "Fails if continuity_id designates an existing continuity whose state contradicts the requested creation;")
    async def test_a_contradictory_existing_creation_is_a_consistency_failure(self):
        await self.create()
        for kw in ({"initial_intention": "another"}, {"pilot_ref": {"context": "/root/agentic", "matter_id": "pilot_default"}, "parent_id": None}):
            if kw.get("parent_id") is None and "pilot_ref" in kw:
                kw = {"initial_intention": "goal2"}
            before = self.e.snapshot()
            with self.assertRaises(ConsistencyError):
                await self.create(**kw)
            self.assertUnchanged(before)
        self.e.matters["mailbox_c1"]["data"]["continuity_id"] = "other"
        with self.assertRaises(ConsistencyError):
            await self.create()

    @covers("Writes to the parent Matter and to the canonical continuities Structure carry the same expected_revision guard")
    async def test_relation_writes_are_guarded(self):
        self.w.full("p")
        await self.create("k", parent_id="p")
        parent_write = [e for e in self.e.log if e[:2] == ("write", "continuity_p")][0]
        self.assertIsNotNone(parent_write[2])
        self.assertEqual(parent_write[3], "data")              # one guarded write of the whole content
        structure = [e for e in self.e.log if e[0] == "structure_patch"][0]
        self.assertIsNotNone(structure[2])

    @covers("Each created Matter descriptor carries functional.format json and functional.schema_ref", "Each created Matter carries a tracked brique.revision")
    async def test_created_matters_carry_schema_ref_mode_and_revision(self):
        await self.create()
        for kind, mid in (("continuity", "continuity_c1"), ("mailbox", "mailbox_c1"), ("runtime", "runtime_c1")):
            matter = self.e.matters[mid]
            self.assertEqual(matter["functional"], {"format": "json", "schema_ref": f"/root/agentic/schema/{kind}_data"})
            self.assertEqual(matter["brique"]["substance_mode"], "brique")
            self.assertIsInstance(matter["brique"]["revision"], int)
        journal = self.e.matters["journal_c1"]                             # the journal is line-framed JSON Lines, one message per line
        self.assertEqual(journal["functional"], {"format": "jsonl", "schema_ref": "/root/agentic/schema/message"})
        self.assertEqual(journal["brique"]["substance_framing"], "lines")
        self.assertIsInstance(journal["brique"]["revision"], int)

    @covers("Locks held simultaneously are acquired in deterministic continuity_id order;")
    async def test_no_lock_survives_the_operation(self):
        self.w.full("p")
        await self.create("k", parent_id="p")
        from substrate import locks
        self.assertEqual(locks._locks, {})                     # also enforced after every test by the harness

    @covers("Fails if pilot_ref is missing or invalid.")
    async def test_pilot_ref_is_validated_before_any_mutation(self):
        for bad in (None, {}, {"matter_id": ""}, {"context": "other", "matter_id": PILOT_ID}, {"context": "/root/agentic", "matter_id": "no_such_pilot"}):         # a context that is not a path; an unknown Matter
            before = self.e.snapshot()
            with self.assertRaises(InvalidRequest):
                await self.call(continuity_id="c1", pilot_ref=bad, initial_intention="goal")
            self.assertUnchanged(before)

    @covers("Fails if initial_intention is missing.")
    async def test_initial_intention_is_required(self):
        before = self.e.snapshot()
        with self.assertRaises(InvalidRequest):
            await self.call(continuity_id="c1", pilot_ref=PILOT)
        self.assertUnchanged(before)

    async def test_continuity_id_is_required_and_must_be_a_safe_identifier(self):
        for bad in (None, "", "a/b", "..", "a b"):
            with self.assertRaises(InvalidRequest):
                await self.call(continuity_id=bad, pilot_ref=PILOT, initial_intention="g")

    @covers("Fails if parent_id is provided but the parent continuity does not exist.")
    async def test_an_unknown_parent_fails_before_any_mutation(self):
        before = self.e.snapshot()
        with self.assertRaises(NotFound):
            await self.create("k", parent_id="ghost")
        self.assertUnchanged(before)

    @covers("Fails if one of the required Matter creations cannot be completed coherently.")
    async def test_a_failing_matter_creation_fails_explicitly(self):
        self.e.fail_create.add("mailbox_c1")
        with self.assertRaises(EngineError):
            await self.create()
        self.assertNotIn("runtime_c1", self.e.matters)         # nothing after the failed step was attempted
        self.assertEqual(self.structure_items(), [])
        self.e.fail_create.clear()
        self.assertTrue((await self.create())["created"])      # and a rerun completes it

    @covers("A failed parent or structure relation must not be silently ignored when that relation was explicitly requested.")
    async def test_a_failing_relation_is_never_swallowed(self):
        self.e.fail_structure_patch = True
        with self.assertRaises(EngineError):
            await self.create()
        self.e.fail_structure_patch = False
        self.w.full("p")
        self.e.always_conflict.add("continuity_p")
        with self.assertRaises(ConcurrencyError):
            await self.create("k", parent_id="p")

    @covers("Fails explicitly with a concurrency failure when the bounded retries")
    async def test_exhausted_revision_conflicts_fail_explicitly(self):
        self.w.full("p")
        self.e.always_conflict.add("continuity_p")
        with self.assertRaises(ConcurrencyError):
            await self.create("k", parent_id="p")


    @covers("A creation by succession carries predecessor_id, lineage_id and generation; a plain creation starts a new lineage: lineage_id is its own id and generation 1.")
    async def test_lineage_fields(self):
        await self.create()
        plain = self.w.state("continuity_c1")
        self.assertEqual((plain["lineage_id"], plain["generation"], plain["status"]), ("c1", 1, "active"))
        self.assertNotIn("predecessor_id", plain)
        await self.create("c1_g2", predecessor_id="c1", lineage_id="c1", generation=2, current_intention="moved on")
        child = self.w.state("continuity_c1_g2")
        self.assertEqual((child["predecessor_id"], child["lineage_id"], child["generation"], child["current_intention"]), ("c1", "c1", 2, "moved on"))

    @covers("Fails when generation is not a positive integer.")
    async def test_generation_must_be_a_positive_integer(self):
        for bad in (0, -1, "2", True, 1.5):
            before = self.e.snapshot()
            with self.assertRaises(InvalidRequest):
                await self.create("c9", generation=bad, predecessor_id="c1")
            self.assertEqual(self.e.snapshot(), before)


    @covers("pilot_ref designates exactly one of a pilot_definition Matter { context, matter_id } and a Capacity { context, capacity, limits? }; the Matter or the Capacity must exist, in the context that pilot_ref names (a pilot_definition may live in any context).",
            "Fails when pilot_ref designates both or neither of a Matter and a Capacity, or an unknown Matter or Capacity.")
    async def test_pilot_ref_is_a_matter_or_a_capacity_never_both(self):
        self.e.known_capacities.add("/root/shop/review_orders")
        self.e.pilot_capacities.add("/root/shop/review_orders")                    # a plan declares itself a pilot
        capacity = {"context": "/root/shop", "capacity": "review_orders", "limits": {"max_inferences_per_message": 12, "timeout_s": 90}}
        await self.create("cp", pilot_ref=capacity)
        self.assertEqual(self.w.state("continuity_cp")["pilot_ref"], capacity)
        await self.create("cm")                                                # the Matter form is unchanged
        self.assertEqual(self.w.state("continuity_cm")["pilot_ref"], PILOT)
        for bad in ({"context": "/root/agentic", "matter_id": PILOT_ID, "capacity": "x"},      # both
                    {"context": "/root/shop"},                                                    # neither
                    {"context": "/root/shop", "capacity": "ghost"},                              # unknown Capacity
                    {"context": "/root/shop", "capacity": "review_orders", "limits": {"nope": 1}},
                    {"context": "/root/shop", "capacity": "a/b"},
                    {"context": "shop", "capacity": "review_orders"},
                    {"context": "/root/agentic", "matter_id": "ghost_pilot"}):         # unknown Matter
            before = self.e.snapshot()
            with self.assertRaises(InvalidRequest):
                await self.create("cx", pilot_ref=bad)
            self.assertEqual(self.e.snapshot(), before)

    @covers("A pilot_ref designates a pilot, not any element that exists",
            "Fails with an invalid request when pilot_ref designates a Matter that is not a pilot_definition or a Capacity that does not declare itself a pilot")
    async def test_an_element_that_is_not_a_pilot_is_refused_before_anything_is_written(self):
        """A tool launched as a pilot used to be accepted (it only had to exist): the daughter called it with the message as parameters, failed three times
        and the message was set aside. A Matter of another kind was accepted the same way."""
        self.e.known_capacities.add("/root/shop/expedition.suivre")                # an ordinary capacity: it exists, it declares nothing
        self.e.matters["orders_data"] = {"data": {"x": 1}, "functional": {}, "brique": {"revision": 1}, "objective": {"name": "orders_data", "type": "dataset"}}
        self.e.matters["untyped"] = {"data": {}, "functional": {}, "brique": {"revision": 1}}
        for bad, needle in (({"context": "/root/shop", "capacity": "expedition.suivre"}, "does not declare itself a pilot"),
                            ({"context": "/root/agentic", "matter_id": "orders_data"}, "not a pilot_definition"),
                            ({"context": "/root/agentic", "matter_id": "untyped"}, "not a pilot_definition")):
            before = self.e.snapshot()
            with self.assertRaises(InvalidRequest) as caught:
                await self.create("cx", pilot_ref=bad)
            self.assertIn(needle, caught.exception.message)                        # the refusal says what is missing
            self.assertEqual(self.e.snapshot(), before)
        self.e.pilot_capacities.add("/root/shop/expedition.suivre")               # declared, it is accepted
        self.assertTrue((await self.create("cy", pilot_ref={"context": "/root/shop", "capacity": "expedition.suivre"}))["created"])

    async def test_set_pilot_applies_the_same_rule(self):
        self.e.known_capacities.add("/root/shop/tool")
        with self.assertRaises(InvalidRequest):
            await pilot_ref_module.ensure_exists(self.p, {"context": "/root/shop", "capacity": "tool"})
        await pilot_ref_module.ensure_exists(self.p, PILOT)                         # the pilot_definition of the test world passes


if __name__ == "__main__":
    unittest.main()
