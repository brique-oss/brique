import unittest

from .driver import AGENTIC, PILOT_REF
from .scenarios_base import SKIP, SKIP_REASON, Scenario, say

CREATE = "/root/agentic/continuity.create"


def create_child(ref, child, parent, goal):
    return {"id": ref, "target_capacity": CREATE, "await_response": True,
            "parameters": {"continuity_id": child, "pilot_ref": PILOT_REF, "initial_intention": goal, "parent_id": parent}}


def wait_for_children(*children):
    return {"condition": "all", "expected": [{"type": "continuity", "ref": c, "correlation": {"continuity_id": c}} for c in children]}


@unittest.skipIf(SKIP, SKIP_REASON)
class S09Delegation(Scenario):
    PREFIX = "it_s09_p"

    def cids(self):
        return ["it_s09_p", "it_s09_c"]

    async def test_parent_creates_a_child_delegates_and_gets_the_answer_back(self):
        d, p, c = self.d, "it_s09_p", "it_s09_c"
        await d.reset_world([
            {"when": {"continuity": p, "stimulus": "active_wait_result", "call_index": 3}, "then": {"output": say("the child is done")}},
            {"when": {"continuity": p, "stimulus": "active_wait_result", "call_index": 2},
             "then": {"output": {"messages": [{"to": c, "kind": "continuity", "content": "do the work"}], "wait_group": wait_for_children(c)}}},
            {"when": {"continuity": p, "stimulus": "none", "message_contains": "delegate"},
             "then": {"output": {"wait_group": {"condition": "all", "expected": [{"type": "intention", "ref": "mk_{{call}}"}]},
                                 "intentions": [create_child("mk_{{call}}", c, p, "Do the delegated work.")]}}},
            {"when": {"continuity": c, "message_contains": "do the work"},
             "then": {"output": {"messages": [{"to": p, "kind": "continuity", "content": "work done by {{cid}}"}]}}},
        ])
        await d.create(p, intention="Coordinate.")
        await d.send(p, "it_s09_m1", "please delegate this")
        await d.until(lambda: self._child_exists(c), "the child continuity")
        await d.quiet(p, c, timeout=40)
        await d.assert_invariants(p, c)

        parent, child = await d.continuity(p), await d.continuity(c)
        self.assertEqual(parent["children"], [c])
        self.assertEqual(child["parent_id"], p)
        self.assertEqual(child["initial_intention"], "Do the delegated work.")
        refs = await d.structure_refs()
        self.assertIn(f"continuity_{p}", refs)
        self.assertIn(f"continuity_{c}", refs)

        # the child treated its message with its own run, and answered the parent
        self.assertEqual([m["content"] for m in child["messages"] if m["kind"] == "continuity"], ["do the work"])
        reply = [m for m in parent["messages"] if m["kind"] == "continuity" and m["from"] == f"continuity:{c}"]
        self.assertEqual(len(reply), 1)
        self.assertEqual(reply[0]["correlation"]["continuity_id"], c)
        calls = await d.llm_calls()
        self.assertEqual(len([x for x in calls if x["continuity"] == p]), 3)         # decision, after creation, after the return
        self.assertEqual(len([x for x in calls if x["continuity"] == c]), 1)
        # the return came in as an awaited result, never as a message of the normal flow
        stimulus = [x for x in calls if x["continuity"] == p][2]["active_stimulus"]
        self.assertEqual((stimulus["kind"], stimulus["outcome"], stimulus["received"]), ("active_wait_result", "satisfied", 1))
        outs = [m["content"]["content"] for m in parent["messages"] if m["kind"] == "pilot"]
        self.assertEqual(outs, ["do the work", "the child is done"])             # the delegation sent out, then the final answer

    async def _child_exists(self, c):
        return await self.d.exists(AGENTIC, f"continuity_{c}")


@unittest.skipIf(SKIP, SKIP_REASON)
class S10ParallelDelegation(Scenario):
    PREFIX = "it_s10_p"

    def cids(self):
        return ["it_s10_p", "it_s10_a", "it_s10_b"]

    async def test_two_children_one_resumption_with_both_returns(self):
        d, p, a, b = self.d, "it_s10_p", "it_s10_a", "it_s10_b"
        await d.reset_world([
            {"when": {"continuity": p, "stimulus": "active_wait_result", "call_index": 3}, "then": {"output": say("both children answered")}},
            {"when": {"continuity": p, "stimulus": "active_wait_result", "call_index": 2},
             "then": {"output": {"messages": [{"to": a, "kind": "continuity", "content": "task for a"}, {"to": b, "kind": "continuity", "content": "task for b"}],
                                 "wait_group": wait_for_children(a, b)}}},
            {"when": {"continuity": p, "stimulus": "none", "message_contains": "fan out"},
             "then": {"output": {"wait_group": {"condition": "all", "expected": [{"type": "intention", "ref": "mkA_{{call}}"}, {"type": "intention", "ref": "mkB_{{call}}"}]},
                                 "intentions": [create_child("mkA_{{call}}", a, p, "Work A."), create_child("mkB_{{call}}", b, p, "Work B.")]}}},
            {"when": {"message_contains": "task for"}, "then": {"output": {"messages": [{"to": p, "kind": "continuity", "content": "done by {{cid}}"}]}}},
        ])
        await d.create(p)
        await d.send(p, "it_s10_m1", "fan out")
        await d.until(lambda: self._both(a, b), "both children")
        await d.quiet(p, a, b, timeout=40)
        await d.assert_invariants(p, a, b)
        parent = await d.continuity(p)
        self.assertEqual(sorted(parent["children"]), [a, b])
        calls = await d.llm_calls()
        self.assertEqual(len([x for x in calls if x["continuity"] == p]), 3)         # a single resumption for both returns
        stimulus = [x for x in calls if x["continuity"] == p][2]["active_stimulus"]
        self.assertEqual(stimulus["received"], 2)
        returns = [m["from"] for m in parent["messages"] if m["kind"] == "continuity" and m["from"] != f"continuity:{p}"]
        self.assertEqual(sorted(returns), [f"continuity:{a}", f"continuity:{b}"])
        for child in (a, b):
            self.assertEqual(len([x for x in calls if x["continuity"] == child]), 1)  # each child had its own run

    async def _both(self, a, b):
        return await self.d.exists(AGENTIC, f"continuity_{a}") and await self.d.exists(AGENTIC, f"continuity_{b}")


if __name__ == "__main__":
    unittest.main()
