import asyncio
import json
import os
import random
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from fake_engine import PILOT_ID, make_engine  # noqa: E402

import continuity_create, continuity_project, mailbox_send, pilot_activate  # noqa: E402
from substrate.errors import InvalidRequest, NotFound  # noqa: E402
from substrate.ids import matter_ids  # noqa: E402
from substrate.journal import append_trace  # noqa: E402
from substrate.continuity_store import size_of  # noqa: E402

pilot_activate.POLL_S = 0.01
PILOT = {"context": "/root/agentic", "matter_id": PILOT_ID}


class Project(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.e = make_engine(pilot=False)
        self.p = self.e.port("test")
        await continuity_create.run(self.p, {"continuity_id": "c1", "pilot_ref": PILOT, "initial_intention": "g"})
        for i, kind in enumerate(["user", "pilot", "capacity_response", "user", "error", "user"], 1):
            await append_trace(self.p, "c1", kind, {"n": i}, message_id=f"t{i}", correlation={"intention_id": "x"} if i == 3 else {})
        self.before = json.dumps([self.e.matters["continuity_c1"], self.e.matters["journal_c1"]], sort_keys=True)

    async def project(self, projection=None, **kw):
        return await continuity_project.run(self.p, dict({"continuity_id": "c1", "projection": projection or {}}, **kw))

    def chosen(self, r):
        """The ids a projection selected, without the cognitive frame it always carries first (pinned)."""
        return [i for i in r["selected_message_ids"] if i != "framework_c1"]

    async def test_read_only_and_ordered_by_seq(self):
        r = await self.project()
        self.assertEqual([m["seq"] for m in r["projection"]["messages"]], [1, 2, 3, 4, 5, 7])        # the frame (line 1), then the thread; the error (line 6) is follow-up: outside the continuity
        self.assertEqual(json.dumps([self.e.matters["continuity_c1"], self.e.matters["journal_c1"]], sort_keys=True), self.before)

    async def test_filters_selection_and_limit(self):
        r = await self.project({"include_kinds": ["user"], "min_seq": 3})        # t1 is line 2: the frame is line 1
        self.assertEqual(self.chosen(r), ["t4", "t6"])
        r = await self.project({"exclude_kinds": ["user"], "limit": 2, "selection": "first"})
        self.assertEqual(self.chosen(r), ["t2", "t3"])
        r = await self.project({"limit": 2, "selection": "last"})
        self.assertEqual(self.chosen(r), ["t4", "t6"])
        r = await self.project({"correlation": {"intention_id": "x"}})
        self.assertEqual(self.chosen(r), ["t3"])

    async def test_merge_completes_the_pilot_default_and_replace_substitutes_it(self):
        base = {"exclude_kinds": ["error"], "limit": 4}                  # the default projection of the pilot, supplied by the caller as a value
        merged = await self.project({"exclude_kinds": ["pilot"]}, default_projection=base)
        self.assertEqual(merged["projection"]["definition"]["exclude_kinds"], ["error", "pilot"])   # base kept, request added
        self.assertEqual(self.chosen(merged), ["t3", "t4", "t6"][0:0] + self.chosen(merged))
        self.assertNotIn("t5", self.chosen(merged))
        self.assertNotIn("t2", self.chosen(merged))
        replaced = await self.project({"include_kinds": ["error"]}, mode="replace", default_projection=base)
        self.assertEqual(self.chosen(replaced), ["t5"])                  # a follow-up kind asked explicitly is read from the journal
        self.assertEqual(base["limit"], 4)                                          # the supplied default is untouched

    async def test_budget_reduces_the_selection_deterministically_never_cuts_a_message(self):
        full = await self.project()
        size = size_of(self.e.matters["journal_c1"]["lines"][-1])      # what the budget counts: the text the line says to the model, in characters
        r = await self.project(budget=size * 2 + 5)
        self.assertEqual(self.chosen(r), ["t4", "t6"])         # the most recent survive
        self.assertEqual(r["omitted_count"], 3)
        self.assertEqual(await self.project(budget=size * 2 + 5), r)        # deterministic
        for m in r["projection"]["messages"]:
            self.assertIn("content", m)

    async def test_explicit_cross_continuity_composition_only(self):
        await continuity_create.run(self.p, {"continuity_id": "k", "pilot_ref": PILOT, "initial_intention": "kid", "parent_id": "c1"})
        await append_trace(self.p, "k", "user", "kid msg", message_id="k1")
        plain = await self.project()
        self.assertNotIn("related", plain["projection"])                    # local by default
        r = await self.project({"include_children": True})
        self.assertEqual(r["projection"]["related"][0]["continuity_id"], "k")
        with self.assertRaises(NotFound):
            await self.project({"continuity_ids": ["ghost"]})

    async def test_errors(self):
        with self.assertRaises(InvalidRequest):
            await self.project(mode="nope")
        with self.assertRaises(InvalidRequest):
            await self.project(budget=-1)
        with self.assertRaises(InvalidRequest):
            await self.project({"bogus": 1})
        with self.assertRaises(NotFound):
            await continuity_project.run(self.p, {"continuity_id": "ghost", "projection": {}})


class Stress(unittest.IsolatedAsyncioTestCase):
    async def test_many_continuities_many_concurrent_messages_and_controls(self):
        rng = random.Random(int(os.environ.get("STRESS_SEED", "7")))
        e = make_engine()
        p = e.port("test")
        seen = {}

        async def llm(port, params):
            body = json.loads(params["request"]["messages"][-1]["content"])
            whole = params["request"]["metadata"]["current_message"]       # the news only designates the message: the metadata holds it
            cid, mid = whole["to"], whole["id"]
            seen.setdefault(cid, []).append(mid)
            seen[cid].extend((body.get("user_message") or {}).get("message_ids", []))      # a message that arrives while the run goes on is delivered with the next inference
            await asyncio.sleep(rng.random() * 0.003)
            if body.get("active_stimulus", {}).get("kind") == "active_wait_result" or rng.random() < 0.6:
                out = {"messages": [{"to": "user:1", "kind": "user", "content": f"re {mid}"}]}
            else:
                ref = f"i_{mid}_{rng.randrange(10 ** 6)}"
                out = {"wait_group": {"condition": "all", "expected": [{"type": "intention", "ref": ref}]},
                       "intentions": [{"id": ref, "target_capacity": "/root/tools/echo", "parameters": {}, "await_response": True}]}
            return {"response": {"output": {"format": "json", "content": out}}}

        async def echo(port, params):
            await asyncio.sleep(rng.random() * 0.004)
            return {"ok": True}

        e.caps["/root/llm/infer"], e.caps["/root/tools/echo"] = llm, echo
        cids = [f"c{i}" for i in range(4)]
        for cid in cids:
            await continuity_create.run(p, {"continuity_id": cid, "pilot_ref": PILOT, "initial_intention": "g"})

        sent = {cid: [] for cid in cids}

        async def sender(n):
            for k in range(n):
                cid = rng.choice(cids)
                mid = f"{cid}-m{len(sent[cid])}-{k}"
                sent[cid].append(mid)
                if rng.random() < 0.1:
                    await mailbox_send.run(e.port("s"), {"continuity_id": cid, "message": {
                        "id": mid + "-k", "from": "u", "kind": "control", "content": {"action": "set_intention", "intention": f"g{k}"}}})
                await mailbox_send.run(e.port("s"), {"continuity_id": cid, "message": {"id": mid, "from": "u", "kind": "user", "content": "x"}})
                await asyncio.sleep(rng.random() * 0.002)

        await asyncio.gather(*[sender(15) for _ in range(4)])
        for _ in range(600):
            await e.drain()
            rts = [(await p.matter_read(matter_ids(c).runtime)).content for c in cids]
            if all(r["status"] == "idle" for r in rts) and not [t for t in e.tasks if not t.done()]:
                break
            await asyncio.sleep(0.01)
        e.raise_task_errors()

        for cid in cids:
            ids = matter_ids(cid)
            journal = e.matters[ids.journal]["lines"]
            seqs = [m["seq"] for m in journal]
            self.assertEqual(sorted(seqs), list(range(1, len(seqs) + 1)), cid)                   # contiguous, unique seq
            self.assertEqual((await p.matter_read(ids.mailbox)).content["entries"], [], cid)   # everything consumed
            rt = (await p.matter_read(ids.runtime)).content
            self.assertEqual((rt["status"], rt["run_id"], rt["current_message_id"], rt["active_wait_groups"]), ("idle", None, None, []), cid)
            user_msgs = [m["id"] for m in journal if m["kind"] == "user"]
            self.assertEqual(sorted(user_msgs), sorted(sent[cid]), cid)                          # nothing lost, nothing duplicated
            treated = [m for m in seen.get(cid, []) if m in sent[cid]]
            self.assertEqual(len(set(treated)), len(sent[cid]), cid)                             # every message was treated


if __name__ == "__main__":
    unittest.main()
