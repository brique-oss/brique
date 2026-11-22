import asyncio
import unittest

from .driver import AGENTIC, PILOT_REF, TEST
from .scenarios_base import SKIP, SKIP_REASON, Scenario, say
from .test_scenarios_10 import slow_child

CREATE = "/root/agentic/continuity.create"
SEND = "/root/agentic/mailbox.send"
ACTIVATE = "/root/agentic/pilot.activate"
SLOW = "/root/agentic/test/test.slow"

REVIEW = {"context": TEST, "capacity": "review_orders"}
INVESTIGATE = {"context": TEST, "capacity": "investigate_orders"}
NESTED = {"context": TEST, "capacity": "delegate_to_plan"}

# rules shared by every plan of the Capacity pilots (they carry no continuity: the same plan runs in many continuities)
PLAN_STEPS = [
    {"when": {"step": "analyse", "prompt_contains": "A100"}, "then": {"output": {"verdict": "ok", "why": "shipped, on time"}}},
    {"when": {"step": "analyse", "prompt_contains": "A300"}, "then": {"output": {"verdict": "risk", "why": "held at customs"}}},
    {"when": {"step": "summary"}, "then": {"output": {"summary": "A100 on time, A300 at risk (customs)."}}},
    {"when": {"step": "triage"}, "then": {"output": {"order_id": "A300"}}},
    {"when": {"step": "conclude"}, "then": {"output": {"answer": "A300 is held at customs."}}},
    {"when": {"step": "wrap"}, "then": {"output": {"answer": "One order is fine; the other is held at customs."}}},
]


def awaited(*intentions):
    """One pilot output: these intentions, answered by Response, resumed once all of them are in."""
    return {"intentions": [dict(i, await_response=True) for i in intentions],
            "wait_group": {"condition": "all", "expected": [{"type": "intention", "ref": i["id"]} for i in intentions]}}


def at_call(cid, n, output):
    return {"when": {"continuity": cid, "call_index": n}, "then": {"output": output}}


def create(ref, cid, parent, pilot_ref):
    return {"id": ref, "target_capacity": CREATE, "parameters": {"continuity_id": cid, "pilot_ref": pilot_ref, "initial_intention": "Work.", "parent_id": parent}}


def send(ref, cid, parent, content):
    return {"id": ref, "target_capacity": SEND,
            "parameters": {"continuity_id": cid, "message": {"id": f"{cid}_task", "from": f"continuity:{parent}", "kind": "user", "content": content}}}


def activate(ref, cid):
    return {"id": ref, "target_capacity": ACTIVATE, "parameters": {"continuity_id": cid}}


async def waiting(d, cid):
    try:
        return (await d.runtime(cid))["status"] == "waiting"
    except Exception:
        return False


def pilot_texts(journal):
    return [m["content"]["content"] for m in journal if m["kind"] == "pilot"]


@unittest.skipIf(SKIP, SKIP_REASON)
class S33Width(Scenario):
    """Eight unrelated pilots woken at the same instant, half driven by a Matter (and held in a wait), half by a Capacity."""
    PREFIX = "it_s33"

    def cids(self):
        return self.matters() + self.plans()

    def matters(self):
        return [f"{self.PREFIX}_m{i}" for i in range(4)]

    def plans(self):
        return [f"{self.PREFIX}_p{i}" for i in range(4)]

    async def test_many_pilots_at_once_keep_their_own_causality(self):
        d = self.d
        matters, plans = self.matters(), self.plans()
        rules = PLAN_STEPS[:]
        for m in matters:
            rules += slow_child(m)
        await d.reset_world(rules)
        for m in matters:
            await d.create(m, intention=f"Matter pilot {m}.")
        for p in plans:
            await d.create(p, intention=f"Plan pilot {p}.", pilot_ref=REVIEW)

        await asyncio.gather(*[d.send(m, f"{m}_m1", f"work for {m}") for m in matters],
                             *[d.send(p, f"{p}_m1", {"customer_id": "C1"}) for p in plans])
        # the Matter pilots are all inside their wait together; the plans run to the end meanwhile
        for m in matters:
            await d.until(lambda m=m: waiting(d, m), f"{m} to wait for its tool")
        await d.quiet(*plans, timeout=40)
        self.assertEqual(len(await d.tool_calls("test.slow", "start")), 4)
        for m in matters:
            self.assertEqual((await d.runtime(m))["status"], "waiting")
        await asyncio.gather(*[d.open_gate(f"g_{m}") for m in matters])
        await d.quiet(*matters, timeout=40)
        await d.assert_invariants(*matters, *plans)

        for m in matters:
            journal = await d.journal(m)
            self.assertEqual(pilot_texts(journal), ["the child is done"])
            self.assertEqual([x["id"] for x in journal if x["kind"] == "user"], [f"{m}_m1"])
            others = [o for o in matters if o != m]
            self.assertFalse([o for o in others if o in str([x["content"] for x in journal])], f"{m} saw another continuity")
        for p in plans:
            journal = await d.journal(p)
            self.assertEqual(pilot_texts(journal), ["A100 on time, A300 at risk (customs)."])
            self.assertEqual([x["content"]["step"] for x in journal if x["kind"] == "prompt"].count("summary"), 1)
        calls = await d.llm_calls()
        for m in matters:
            self.assertEqual(len([c for c in calls if c["continuity"] == m]), 2)       # one run each: the tool call, the resumption


@unittest.skipIf(SKIP, SKIP_REASON)
class S34Depth(Scenario):
    """Five levels, each waiting for the one below, alternating Matter and Capacity pilots:
    Matter -> investigate_orders (plan) -> Matter -> delegate_to_plan (plan) -> review_orders (plan)."""
    PREFIX = "it_s34"

    def levels(self):
        p = self.PREFIX
        return [p, f"{p}_l2", f"{p}_l3", f"{p}_l4", f"{p}_l5"]

    def cids(self):
        return self.levels()

    async def test_a_chain_of_pilots_each_waiting_for_the_next(self):
        d = self.d
        l1, l2, l3, l4, l5 = self.levels()
        await d.reset_world(PLAN_STEPS + [
            at_call(l1, 1, awaited(create("mk2", l2, l1, INVESTIGATE))),
            at_call(l1, 2, awaited(send("sd2", l2, l1, {"customer_id": "C1", "child_id": l3, "child_message_id": f"{l3}_task"}))),
            at_call(l1, 3, awaited(activate("ac2", l2))),
            at_call(l1, 4, say("the chain is done")),
            at_call(l3, 1, awaited(create("mk4", l4, l3, NESTED))),
            at_call(l3, 2, awaited(send("sd4", l4, l3, {"customer_id": "C1", "child_id": l5, "child_message_id": f"{l5}_task"}))),
            at_call(l3, 3, awaited(activate("ac4", l4))),
            at_call(l3, 4, say("order A300 is held at customs")),
        ])
        await d.create(l1, intention="Top of the chain.")
        await d.send(l1, f"{l1}_m1", "start the chain")
        await d.quiet(*self.levels(), timeout=60)
        await d.assert_invariants(*self.levels())

        refs = {c: (await d.continuity(c)) for c in self.levels()}
        self.assertEqual([refs[c]["parent_id"] for c in (l2, l3, l4, l5)], [l1, l2, l3, l4])             # the chain is a chain
        kinds = ["matter_id" in refs[c]["pilot_ref"] for c in self.levels()]
        self.assertEqual(kinds, [True, False, True, False, False])                                    # Matter, plan, Matter, plan, plan
        self.assertEqual(pilot_texts(refs[l1]["messages"]), ["the chain is done"])

        prompts = lambda c: [m["content"]["step"] for m in refs[c]["messages"] if m["kind"] == "prompt"]
        self.assertEqual(prompts(l1), [])                                                              # Matter levels carry no plan prompts
        self.assertEqual(prompts(l3), [])
        self.assertEqual(prompts(l2), ["triage", "conclude"])
        self.assertEqual(prompts(l4), ["wrap"])
        self.assertEqual(sorted(prompts(l5)), ["analyse", "analyse", "summary"])
        # each level kept its own story: what is below never leaks into the journal above
        self.assertNotIn("customs", str([m["content"] for m in refs[l1]["messages"] if m["kind"] != "user"]))
        self.assertEqual(len([m for m in refs[l3]["messages"] if m["id"] == f"{l3}_task"]), 1)
        self.assertEqual(len([m for m in refs[l5]["messages"] if m["id"] == f"{l5}_task"]), 1)


@unittest.skipIf(SKIP, SKIP_REASON)
class S35Tree(Scenario):
    """Width and depth together: a Matter root opens three children of three kinds at once, hands them work at once and waits
    for all of them to be back at rest. One child is a Matter, one a plan, one a plan that has a Matter grandchild."""
    PREFIX = "it_s35"

    def kids(self):
        p = self.PREFIX
        return f"{p}_k1", f"{p}_k2", f"{p}_k3", f"{p}_g"

    def cids(self):
        return [self.PREFIX, *self.kids()]

    async def test_a_root_fans_out_to_a_matter_a_plan_and_a_plan_with_a_grandchild(self):
        d, root = self.d, self.PREFIX
        k1, k2, k3, g = self.kids()
        await d.reset_world(PLAN_STEPS + [
            at_call(root, 1, awaited(create("mk1", k1, root, PILOT_REF), create("mk2", k2, root, REVIEW), create("mk3", k3, root, INVESTIGATE))),
            at_call(root, 2, awaited(send("sd1", k1, root, "plain task"), send("sd2", k2, root, {"customer_id": "C1"}),
                                     send("sd3", k3, root, {"customer_id": "C1", "child_id": g, "child_message_id": f"{g}_task"}))),
            at_call(root, 3, awaited(activate("ac1", k1), activate("ac2", k2), activate("ac3", k3))),
            at_call(root, 4, say("the whole tree is at rest")),
            {"when": {"continuity": k1}, "then": {"output": say("k1 done")}},
            {"when": {"continuity": g}, "then": {"output": say("order A300 is held at customs")}},
        ])
        await d.create(root, intention="Root of the tree.")
        await d.send(root, f"{root}_m1", "fan out")
        await d.quiet(*self.cids(), timeout=60)
        await d.assert_invariants(*self.cids())

        top = await d.continuity(root)
        self.assertEqual(sorted(top["children"]), sorted([k1, k2, k3]))
        self.assertEqual(pilot_texts(top["messages"]), ["the whole tree is at rest"])
        self.assertEqual([m["content"]["step"] for m in top["messages"] if m["kind"] == "prompt"], [])
        responses = [m for m in top["messages"] if m["kind"] == "capacity_response"]
        self.assertEqual(len(responses), 9)                                                           # 3 creates, 3 sends, 3 activations
        activations = [m for m in responses if m["correlation"].get("intention_id", "").startswith("ac")]
        self.assertEqual([m["content"]["response"]["payload"]["status"] for m in activations], ["idle"] * 3)
        self.assertEqual((await d.continuity(g))["parent_id"], k3)
        self.assertEqual(pilot_texts((await d.continuity(k1))["messages"]), ["k1 done"])
        self.assertEqual(sorted(x["content"]["step"] for x in (await d.continuity(k2))["messages"] if x["kind"] == "prompt"),
                         ["analyse", "analyse", "summary"])
        self.assertEqual(sorted(x["content"]["step"] for x in (await d.continuity(k3))["messages"] if x["kind"] == "prompt"),
                         ["conclude", "triage"])
        calls = await d.llm_calls()
        self.assertEqual(len([c for c in calls if c["continuity"] == root]), 4)


@unittest.skipIf(SKIP, SKIP_REASON)
class S36TreeCrash(Scenario):
    """The wrapper dies while a tree is in the middle of its waits (a Matter child and a Matter grandchild hold a tool).
    The root's takeover brings every orphan back; nothing is asked or sent twice and the tree ends at rest."""
    PREFIX = "it_s36"

    def kids(self):
        p = self.PREFIX
        return f"{p}_k1", f"{p}_k3", f"{p}_g"

    def cids(self):
        return [self.PREFIX, *self.kids()]

    async def test_every_orphan_of_a_tree_is_taken_over_once(self):
        d, root = self.d, self.PREFIX
        k1, k3, g = self.kids()
        await d.reset_world(PLAN_STEPS + slow_child(k1) + slow_child(g) + [
            at_call(root, 1, awaited(create("mk1", k1, root, PILOT_REF), create("mk3", k3, root, INVESTIGATE))),
            at_call(root, 2, awaited(send("sd1", k1, root, "plain task"),
                                     send("sd3", k3, root, {"customer_id": "C1", "child_id": g, "child_message_id": f"{g}_task"}))),
            at_call(root, 3, awaited(activate("ac1", k1), activate("ac3", k3))),
            at_call(root, 4, say("the whole tree is at rest")),
        ])
        await d.create(root, intention="Root of the tree.")
        await d.send(root, f"{root}_m1", "fan out")
        await d.until(lambda: waiting(d, k1), "the Matter child to hold its tool")
        await d.until(lambda: waiting(d, g), "the grandchild to hold its tool")
        await d.until(lambda: waiting(d, root), "the root to wait for its activations")
        self.assertEqual(len(await d.tool_calls("test.slow", "start")), 2)

        await d.call(AGENTIC, "wrapper.restart", "execution", {"name": "agentic_core"}, timeout=30)
        await asyncio.sleep(4)
        await d.open_gate(f"g_{k1}")
        await d.open_gate(f"g_{g}")
        out = await d.call(AGENTIC, "pilot.activate", "user", {"continuity_id": root}, timeout=90)
        self.assertEqual(out["status"], "idle")
        lost = [m for m in await d.journal(root) if m["kind"] == "capacity_response" and m["correlation"].get("intention_id") in ("ac1", "ac3")]
        self.assertEqual([m["content"]["response"]["status"] for m in lost], ["error", "error"])       # the lost activations are not replayed
        # the root's takeover does not wake its children: each orphan is taken over by its own activation, and the plan
        # of k3 re-activates its own grandchild when it is replayed
        for cid in (k1, k3):
            self.assertEqual((await d.call(AGENTIC, "pilot.activate", "user", {"continuity_id": cid}, timeout=90))["status"], "idle")
        try:
            await d.quiet(*self.cids(), timeout=30)
        except AssertionError:
            state = {c: (await d.runtime(c), await d.mailbox(c), self.kinds(await d.journal(c))) for c in self.cids()}
            self.fail("tree not at rest after takeover: " + repr(state))
        await d.assert_invariants(*self.cids())

        top = await d.continuity(root)
        self.assertEqual(pilot_texts(top["messages"]), ["the whole tree is at rest"])                 # answered once
        for cid in (k1, k3, g):
            journal = (await d.continuity(cid))["messages"]
            self.assertEqual(len([m for m in journal if m["id"] == f"{cid}_task"]), 1)               # no task sent twice
        self.assertEqual(len(await d.tool_calls("test.slow", "start")), 2)                          # no tool call replayed
        self.assertEqual(pilot_texts((await d.continuity(k1))["messages"]), ["the child is done"])
        asked = [c.get("step") for c in await d.llm_calls() if c.get("step")]
        self.assertEqual(asked.count("triage"), 1)
        self.assertEqual(asked.count("conclude"), 1)


if __name__ == "__main__":
    unittest.main()
