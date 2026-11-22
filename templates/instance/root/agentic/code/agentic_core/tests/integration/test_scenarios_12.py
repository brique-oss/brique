import asyncio
import unittest

from .driver import AGENTIC, TEST
from .scenarios_base import SKIP, SKIP_REASON, Scenario

NESTED = {"context": TEST, "capacity": "delegate_to_plan"}
INVESTIGATE = {"context": TEST, "capacity": "investigate_orders"}


@unittest.skipIf(SKIP, SKIP_REASON)
class S31PlanDelegatesToPlan(Scenario):
    """A Capacity pilot delegates to a sub-continuity that is itself piloted by a Capacity (S26's plan)."""
    PREFIX = "it_s31"

    async def test_a_sub_continuity_piloted_by_a_capacity(self):
        d, cid = self.d, self.PREFIX
        kid = f"{cid}_kid"
        await d.reset_world([
            {"when": {"step": "analyse", "prompt_contains": "A100"}, "then": {"output": {"verdict": "ok", "why": "shipped, on time"}}},
            {"when": {"step": "analyse", "prompt_contains": "A300"}, "then": {"output": {"verdict": "risk", "why": "held at customs"}}},
            {"when": {"step": "summary"}, "then": {"output": {"summary": "A100 on time, A300 at risk (customs)."}}},
            {"when": {"step": "wrap"}, "then": {"output": {"answer": "One order is fine; the other is held at customs."}}},
        ])
        await d.create(cid, intention="Delegate the review.", pilot_ref=NESTED)
        await d.send(cid, f"{cid}_m1", {"customer_id": "C1", "child_id": kid, "child_message_id": f"{kid}_task"})
        await d.quiet(cid, kid)
        await d.assert_invariants(cid, kid)

        parent = await d.journal(cid)
        self.assertEqual(self.kinds(parent), ["user", "prompt", "completion", "pilot"])              # one prompt of its own
        self.assertEqual([m["content"]["step"] for m in parent if m["kind"] == "prompt"], ["wrap"])
        # the parent's prompt was fed by the child plan's own final message
        wrap = [c for c in await d.llm_calls() if c.get("step") == "wrap"][0]
        self.assertIn("A100 on time, A300 at risk (customs).", wrap["system_prompt"])

        child = await d.continuity(kid)
        self.assertEqual(child["pilot_ref"], {"context": TEST, "capacity": "review_orders"})           # a Capacity pilots the child
        self.assertEqual(child["parent_id"], cid)
        steps = [m["content"]["step"] for m in child["messages"] if m["kind"] == "prompt"]
        self.assertEqual(sorted(steps), ["analyse", "analyse", "summary"])                              # the child's own prompts live there
        self.assertEqual([m["content"]["content"] for m in parent if m["kind"] == "pilot"], ["One order is fine; the other is held at customs."])

    def cids(self):
        return [self.PREFIX, f"{self.PREFIX}_kid"]


@unittest.skipIf(SKIP, SKIP_REASON)
class S32CrashWhileWaitingForASubContinuity(Scenario):
    """The wrapper dies while a plan waits for its sub-continuity. A new activation takes over both orphan runs: the plan is
    replayed, its recorded prompts are not asked again, and the answer is produced once."""
    PREFIX = "it_s32"

    async def test_replay_after_a_crash_in_the_middle_of_the_wait(self):
        d, cid = self.d, self.PREFIX
        kid = f"{cid}_kid"
        await d.reset_world([
            {"when": {"step": "triage"}, "then": {"output": {"order_id": "A300"}}},
            {"when": {"step": "conclude"}, "then": {"output": {"answer": "A300 is held at customs: expect a delay."}}},
            {"when": {"continuity": kid, "stimulus": "none"},
             "then": {"output": {"intentions": [{"id": "slow_{{call}}", "target_capacity": "/root/agentic/test/test.slow",
                                                 "parameters": {"gate": f"g_{kid}", "value": 1}, "await_response": True}],
                                 "wait_group": {"condition": "all", "expected": [{"type": "intention", "ref": "slow_{{call}}"}]}}}},
            {"when": {"continuity": kid, "stimulus": "active_wait_result"},
             "then": {"output": {"messages": [{"to": "user:1", "kind": "user", "content": "order A300 is blocked at customs since Monday"}]}}},
            {"when": {"continuity": kid, "stimulus": "recovery"},
             "then": {"output": {"messages": [{"to": "user:1", "kind": "user", "content": "order A300 is blocked at customs since Monday"}]}}},
        ])
        await d.create(cid, intention="Investigate.", pilot_ref=INVESTIGATE)
        await d.send(cid, f"{cid}_m1", {"customer_id": "C1", "child_id": kid, "child_message_id": f"{kid}_task"})
        await d.until(lambda: self._status(kid, "waiting"), "the sub-continuity to wait for its tool")
        before = [c.get("step") for c in await d.llm_calls() if c.get("step")]
        self.assertEqual(before, ["triage"])                                    # the plan asked its first prompt, the child waits

        # the wrapper dies in the middle of the wait: both the plan's run and the child's run become orphans
        await d.call(AGENTIC, "wrapper.restart", "execution", {"name": "agentic_core"}, timeout=30)
        await asyncio.sleep(4)
        await d.open_gate(f"g_{kid}")
        out = await d.call(AGENTIC, "pilot.activate", "user", {"continuity_id": cid}, timeout=60)     # takes the orphan over, replays
        self.assertEqual(out["status"], "idle")
        await d.quiet(cid, kid)
        await d.assert_invariants(cid, kid)

        parent = await d.journal(cid)
        # recorded steps were not asked again: one triage, one conclusion, each completed once, one answer
        steps = [m["content"]["step"] for m in parent if m["kind"] == "prompt"]
        self.assertEqual(steps, ["triage", "conclude"])
        self.assertEqual([m["kind"] for m in parent].count("completion"), 2)
        self.assertEqual([m["content"]["content"] for m in parent if m["kind"] == "pilot"], ["A300 is held at customs: expect a delay."])
        asked = [c.get("step") for c in await d.llm_calls() if c.get("step")]
        self.assertEqual(asked.count("triage"), 1)                              # the model was not asked twice for it
        self.assertEqual(asked.count("conclude"), 1)
        child = await d.continuity(kid)
        self.assertIn("pilot", [m["kind"] for m in child["messages"]])          # the sub-continuity finished and answered
        self.assertEqual(len([m for m in child["messages"] if m["id"] == f"{kid}_task"]), 1)    # its task was never duplicated

    async def _status(self, cid, status):
        try:
            return (await self.d.runtime(cid))["status"] == status
        except Exception:
            return False

    def cids(self):
        return [self.PREFIX, f"{self.PREFIX}_kid"]


if __name__ == "__main__":
    unittest.main()
