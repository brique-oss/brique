import json
import unittest

from .harness import PILOT_ID, CapacityContract, covers, message
from substrate.errors import InvalidRequest, NotFound
from substrate.continuity_store import size_of
from substrate.conversation import message_of
from substrate.kinds import THREAD_KINDS
import continuity_project


class ContinuityProjectContract(CapacityContract):
    CAP, MODULE = "continuity.project", continuity_project

    async def asyncSetUp(self):
        await super().asyncSetUp()
        # stored out of order on purpose: canonical order is message.seq
        msgs = [message(3, "m3", kind="capacity_response", content={"response": {"status": "ok"}}),
                message(1, "m1", content="Hello, here is a very long original message."),
                message(2, "m2", kind="pilot", content={"event": "message_out"}),
                message(4, "m4", kind="capacity_intention", content={"id": "i1"}),
                message(5, "m5", kind="error", content={"event": "validation_error"}),          # follow-up kinds: in the journal,
                message(6, "m6", kind="event", content={"event": "run_recovered"})]             # outside the continuity by default
        self.w.full("c1", messages=msgs, children=["k"])
        self.w.full("k", parent="c1", messages=[message(1, "k1", content="from the child")])
        self.w.full("par", children=["c1"])
        self.e.matters["continuity_c1"]["data"]["parent_id"] = "par"
        self.w.messages_of_parent = [message(1, "par1", content="from the parent")]
        self.w.lay_journal("par", self.w.messages_of_parent)
        self.snapshot = self.e.snapshot()

    async def project(self, projection=None, **kw):
        return await self.call(**dict({"continuity_id": "c1", "projection": projection or {}}, **kw))

    # --- the cognitive frame is pinned: every projection carries it, whatever its filters, limit or budget ---------------------
    def framed(self):
        self.w.full("f", messages=[message(1, "framework_f", kind="framework", content={"text": "FRAME", "source": "s"}),
                                   message(2, "u2", content="a" * 50), message(3, "u3", content="b" * 50), message(4, "u4", content="c" * 50)])

    async def framed_ids(self, projection=None, **kw):
        out = await self.call(**dict({"continuity_id": "f", "projection": projection or {}}, **kw))
        return [m["id"] for m in out["projection"]["messages"]], out

    @covers("The pinned lines (kind framework, the cognitive frame) are always carried first by a projection")
    async def test_the_frame_is_always_the_first_line_of_a_projection(self):
        self.framed()
        ids, out = await self.framed_ids()
        self.assertEqual(ids, ["framework_f", "u2", "u3", "u4"])
        self.assertEqual(out["selected_message_ids"], ids)

    async def test_the_frame_survives_every_filter(self):
        self.framed()
        for projection in ({"include_kinds": ["user"]}, {"exclude_kinds": ["framework"]}, {"limit": 1}, {"limit": 1, "selection": "first"},
                           {"limit": 0}, {"min_seq": 3}, {"message_ids": ["u3"]}, {"from": ["nobody"]}):
            ids, _ = await self.framed_ids(projection)
            self.assertEqual(ids[0], "framework_f", projection)
            self.assertEqual(ids.count("framework_f"), 1, projection)                        # once, never duplicated
        self.assertEqual((await self.framed_ids({"include_kinds": ["user"], "limit": 1}))[0], ["framework_f", "u4"])   # the rest is filtered as asked
        self.assertEqual((await self.framed_ids({"limit": 2, "selection": "first"}))[0], ["framework_f", "u2", "u3"])    # the limit never counts the frame
        self.assertEqual((await self.framed_ids({"limit": 2}))[0], ["framework_f", "u3", "u4"])

    async def test_the_frame_is_never_dropped_by_a_budget(self):
        self.framed()
        ids, out = await self.framed_ids(budget=1)                                               # nothing but the frame can fit
        self.assertEqual(ids, ["framework_f"])
        self.assertEqual(out["omitted_count"], 3)                                                # the others, never the frame

    async def test_payload_matches_the_declared_outputs(self):
        self.assertPayloadMatchesContract(await self.project())

    @covers("continuity.project never mutates the continuity header, journal, mailbox or runtime Matters, and never reads pilot_definition.",
            "The journal remains the source of truth.", "A dynamic projection never modifies the default projection supplied by the caller.",
            "Does not alter any Matter when projection fails.")
    async def test_nothing_is_ever_written(self):
        self.e.matters[PILOT_ID]["data"]["default_projection"] = {"limit": 1}
        self.snapshot = self.e.snapshot()
        self.e.reads.clear()
        await self.project({"include_kinds": ["user"]})
        await self.project({"limit": 3}, mode="replace", budget=10_000)
        await self.project({"include_children": True}, default_projection={"limit": 1})
        self.assertNotIn(("read", PILOT_ID), self.e.reads)               # the pilot_definition is never read: the default is a value
        for bad in ({"projection": {"bogus": 1}}, {"mode": "nope"}, {"budget": 0}, {"projection": {"continuity_ids": ["ghost"]}}):
            with self.assertRaises((InvalidRequest, NotFound)):
                await self.project(**bad)
        self.assertUnchanged(self.snapshot)
        self.assertEqual([c for c in self.e.log], [])

    @covers("Projection is deterministic for the same source state, definition, mode, and budget.")
    async def test_determinism(self):
        runs = [await self.project({"exclude_kinds": ["error"], "limit": 2}, budget=5000) for _ in range(3)]
        self.assertEqual(runs[0], runs[1])
        self.assertEqual(runs[1], runs[2])

    @covers("Projection performs no LLM inference.")
    async def test_no_inference_or_capacity_call(self):
        await self.project()
        self.assertEqual(self.e.calls, [])

    @covers("Projection performs no cognitive summarization.", "Projection never reformulates message meaning.",
            "Projection never decides importance through semantic reasoning.")
    async def test_selected_messages_are_the_stored_messages_verbatim(self):
        stored = {m["id"]: m for m in self.w.journal("c1")}
        r = await self.project()
        for m in r["projection"]["messages"]:
            self.assertEqual(m, stored[m["id"]])                  # byte-for-byte the durable message
        thread = {i for i, m in stored.items() if m["kind"] in THREAD_KINDS}
        self.assertEqual(sorted(r["selected_message_ids"]), sorted(thread))   # nothing dropped from the continuity when nothing is asked

    @covers("By default the projection takes the whole continuity, the thread of the LLM:")
    async def test_follow_up_kinds_are_outside_the_continuity_unless_asked(self):
        r = await self.project()
        self.assertEqual(r["selected_message_ids"], ["m1", "m2", "m3", "m4"])         # the thread: user, pilot, response, intention
        for kinds, wanted in ((["error"], ["m5"]), (["event"], ["m6"]), (["error", "event", "user"], ["m1", "m5", "m6"])):
            asked = await self.project({"include_kinds": kinds})
            self.assertEqual(asked["selected_message_ids"], wanted)                   # follow-up kinds are read from the journal itself
        self.assertEqual((await self.project({"include_kinds": ["control"]}))["selected_message_ids"], [])

    @covers("Canonical relative message order follows message.seq.")
    async def test_order_is_seq_not_storage_order(self):
        r = await self.project()
        self.assertEqual([m["seq"] for m in r["projection"]["messages"]], [1, 2, 3, 4])
        r = await self.project({"exclude_kinds": ["pilot"], "limit": 2, "selection": "last"})
        self.assertEqual(r["selected_message_ids"], ["m3", "m4"])

    @covers("Parent and child continuities are not included unless explicitly requested.")
    async def test_related_continuities_only_when_explicitly_requested(self):
        self.assertNotIn("related", (await self.project())["projection"])
        r = await self.project({"include_children": True, "include_parent": True})
        self.assertEqual(sorted(x["continuity_id"] for x in r["projection"]["related"]), ["k", "par"])
        r = await self.project({"continuity_ids": ["k"]})
        self.assertEqual([x["continuity_id"] for x in r["projection"]["related"]], ["k"])

    async def test_merge_keeps_the_pilot_base_and_replace_ignores_it(self):
        base = {"exclude_kinds": ["error"]}                      # the default projection of the pilot, supplied by the caller as a value
        merged = await self.project({"exclude_kinds": ["pilot"]}, default_projection=base)
        self.assertEqual(merged["projection"]["definition"]["exclude_kinds"], ["error", "pilot"])
        replaced = await self.project({"exclude_kinds": ["pilot"]}, mode="replace", default_projection=base)
        self.assertEqual(replaced["projection"]["definition"]["exclude_kinds"], ["pilot"])
        self.assertIn("m4", replaced["selected_message_ids"])

    async def test_budget_reduces_the_selection_never_a_message(self):
        full = await self.project()
        sizes = [size_of(m) for m in full["projection"]["messages"]]
        r = await self.project(budget=sum(sizes[-2:]))
        self.assertEqual(r["selected_message_ids"], ["m3", "m4"])
        self.assertEqual(r["omitted_count"], 2)
        for m in r["projection"]["messages"]:
            self.assertIn(m, full["projection"]["messages"])         # whole messages only: no truncation

    async def test_the_budget_counts_the_text_the_conversation_says_not_the_line(self):
        full = await self.project()
        said = [len(message_of(m)["content"]) for m in full["projection"]["messages"]]
        lines = [len(json.dumps(m, sort_keys=True)) for m in full["projection"]["messages"]]
        self.assertTrue(all(w > s for w, s in zip(lines, said)))      # a line carries identifiers, dates and correlation the model never sees
        r = await self.project(budget=sum(said))                      # exactly what the four messages say: nothing is dropped
        self.assertEqual((r["selected_message_ids"], r["omitted_count"]), (["m1", "m2", "m3", "m4"], 0))
        r = await self.project(budget=sum(said) - 1)
        self.assertEqual(r["omitted_count"], 1)

    def summarised(self):
        self.w.full("s", messages=[message(1, "framework_s", kind="framework", content={"text": "FRAME", "source": "x"}),
                                   message(2, "summary_s", kind="summary", content="R" * 60),
                                   message(3, "u3", content="a" * 50), message(4, "u4", content="b" * 50), message(5, "u5", content="c" * 50)])

    async def test_a_budget_never_drops_the_summary_of_a_succession(self):
        self.summarised()
        ids = lambda out: [m["id"] for m in out["projection"]["messages"]]
        out = await self.call(continuity_id="s", projection={}, budget=1)                    # nothing fits: the frame and the summary stay
        self.assertEqual(ids(out), ["framework_s", "summary_s"])
        out = await self.call(continuity_id="s", projection={}, budget=len("Summary of the previous continuity:\n" + "R" * 60) + 50)
        self.assertEqual(ids(out), ["framework_s", "summary_s", "u5"])                       # the oldest of the others go first
        self.assertEqual(out["omitted_count"], 2)

    @covers("Fails if the bound continuity does not correspond to continuity_id.")
    async def test_an_unknown_continuity_fails(self):
        with self.assertRaises(NotFound):
            await self.call(continuity_id="ghost", projection={})

    @covers("Fails if projection or mode is structurally invalid.")
    async def test_invalid_projection_or_mode_fails(self):
        for bad in ({"projection": {"include_kinds": "user", "bogus": True}}, {"projection": {"selection": "middle"}}, {"mode": "append"}):
            with self.assertRaises(InvalidRequest):
                await self.project(**bad)

    @covers("Fails if an explicitly referenced related continuity cannot be resolved.")
    async def test_an_unresolvable_related_continuity_fails(self):
        with self.assertRaises(NotFound):
            await self.project({"continuity_ids": ["ghost"]})

    @covers("Fails if budget is invalid.")
    async def test_an_invalid_budget_fails(self):
        for bad in (0, -5, "10", True):
            with self.assertRaises(InvalidRequest):
                await self.project(budget=bad)


if __name__ == "__main__":
    unittest.main()
