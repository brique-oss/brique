import asyncio
import unittest

from .driver import AGENTIC, PILOT_REF
from .scenarios_base import SKIP, SKIP_REASON, Scenario

SLOW = "/root/agentic/test/test.slow"


def slow_child(kid):
    """The child waits for a tool behind a gate, then answers."""
    return [
        {"when": {"continuity": kid, "stimulus": "none"},
         "then": {"output": {"intentions": [{"id": "slow_{{call}}", "target_capacity": SLOW, "parameters": {"gate": "g_{{cid}}", "value": 1}, "await_response": True}],
                             "wait_group": {"condition": "all", "expected": [{"type": "intention", "ref": "slow_{{call}}"}]}}}},
        {"when": {"continuity": kid, "stimulus": "active_wait_result"},
         "then": {"output": {"messages": [{"to": "user:1", "kind": "user", "content": "the child is done"}]}}},
    ]


@unittest.skipIf(SKIP, SKIP_REASON)
class S28ActivationIsAnAwaitedCapacity(Scenario):
    PREFIX = "it_s28"

    async def test_an_explicit_activation_waits_for_the_child_to_be_back_at_rest(self):
        d, kid = self.d, self.PREFIX
        await d.reset_world(slow_child(kid))
        await d.create(kid, intention="Slow sub-task.")
        await d.send(kid, f"{kid}_m1", "do the slow task")                   # the wake starts the child in the background
        await d.until(lambda: self._status(kid, "waiting"), "the child to wait for its tool")
        call = asyncio.ensure_future(d.call(AGENTIC, "pilot.activate", "user", {"continuity_id": kid}, timeout=30))
        await asyncio.sleep(0.5)
        self.assertFalse(call.done())                                          # it waits: it does not answer "running"
        self.assertEqual([m["kind"] for m in await d.journal(kid)].count("pilot"), 0)
        await d.open_gate(f"g_{kid}")
        out = await asyncio.wait_for(call, 20)
        self.assertEqual(out["status"], "idle")                                # answered when the child was back at rest
        journal = await d.journal(kid)
        self.assertEqual([m["content"]["content"] for m in journal if m["kind"] == "pilot"], ["the child is done"])
        await d.assert_invariants(kid)

    async def _status(self, cid, status):
        return (await self.d.runtime(cid))["status"] == status

    def cids(self):
        return [self.PREFIX]


@unittest.skipIf(SKIP, SKIP_REASON)
class S29SubContinuityByOrdinaryCapacities(Scenario):
    """A pilot opens a sub-continuity with nothing but capacities it already knows, and waits for it like for any capacity."""
    PREFIX = "it_s29"

    async def test_create_send_activate_then_resume_with_the_child_finished(self):
        d, parent = self.d, self.PREFIX
        kid = f"{parent}_kid"
        step = lambda n, intention, then="": {"when": {"continuity": parent, "call_index": n}, "then": {"output": {
            "intentions": [dict(intention, await_response=True)],
            "wait_group": {"condition": "all", "expected": [{"type": "intention", "ref": intention["id"]}]}}}}
        await d.reset_world([
            step(1, {"id": "open", "target_capacity": "/root/agentic/continuity.create",
                     "parameters": {"continuity_id": kid, "pilot_ref": PILOT_REF, "initial_intention": "Slow sub-task.", "parent_id": parent}}),
            step(2, {"id": "hand_over", "target_capacity": "/root/agentic/mailbox.send",
                     "parameters": {"continuity_id": kid, "message": {"id": f"{kid}_m1", "from": f"continuity:{parent}", "kind": "user",
                                                                       "content": "do the slow task"}}}),
            step(3, {"id": "wait_for_it", "target_capacity": "/root/agentic/pilot.activate", "parameters": {"continuity_id": kid}}),
            {"when": {"continuity": parent, "call_index": 4},
             "then": {"output": {"messages": [{"to": "user:1", "kind": "user", "content": "the sub-task is finished"}]}}},
        ] + slow_child(kid))
        await d.create(parent, intention="Delegate a slow task.")
        await d.send(parent, f"{parent}_m1", "investigate")
        await d.until(lambda: self._status(kid, "waiting"), "the child to work")
        # the parent is waiting for its call to pilot.activate, like for any slow capacity
        await d.until(lambda: self._status(parent, "waiting"), "the parent to wait for its activation")
        responses = lambda: [m for m in self._journal_cache if m["kind"] == "capacity_response"]
        self._journal_cache = await d.journal(parent)
        self.assertEqual(len(responses()), 2)                                  # create and send answered, activate not yet
        await d.open_gate(f"g_{kid}")
        await d.quiet(parent, kid)
        await d.assert_invariants(parent, kid)

        journal = await d.journal(parent)
        activation = [m for m in journal if m["kind"] == "capacity_response" and m["correlation"].get("intention_id") == "wait_for_it"][0]
        self.assertEqual(activation["content"]["response"]["payload"]["status"], "idle")
        self.assertEqual([m["content"]["content"] for m in journal if m["kind"] == "pilot"], ["the sub-task is finished"])
        child = await d.continuity(kid)
        self.assertEqual(child["parent_id"], parent)
        self.assertEqual([m["content"]["content"] for m in child["messages"] if m["kind"] == "pilot"], ["the child is done"])
        # the parent's history keeps the delegation and its end, never the child's own steps
        self.assertNotIn("the child is done", str([m["content"] for m in journal]))

    async def _status(self, cid, status):
        try:
            return (await self.d.runtime(cid))["status"] == status
        except Exception:
            return False

    def cids(self):
        return [self.PREFIX, f"{self.PREFIX}_kid"]


if __name__ == "__main__":
    unittest.main()
