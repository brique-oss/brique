"""A public, open MCP server (DeepWiki, https://mcp.deepwiki.com/mcp) reached through the outbound gateway, both ways.

1. Deterministic: the Capacity pilot deepwiki_topics (/root/agentic/test) calls read_wiki_structure from its plan, with no inference.
2. Model-selected: a Matter pilot whose mcp section names deepwiki gets its tools natively, and Haiku calls one of them.

Needs: BRIQUE_REAL_LLM=1 (the second test spends tokens); network access to mcp.deepwiki.com; the registry of security/mcp_registry.json
installed for client_mcp_gateway (server deepwiki, plan /root/agentic/test/capacity/deepwiki_topics); the secret mcp_authority_key granted
to agentic_core. Judged on facts: the gateway's terminal state of each call and where it came from, never the wording.
"""
import json
import time
import unittest

from ..integration.driver import TEST
from .harness import REAL, SKIP_REASON, RealDriver, pilot_texts

REPO = "modelcontextprotocol/go-sdk"
PILOT_ID = "real_mcp_public_pilot"
PILOT = {
    "id": PILOT_ID,
    "prompt": "You answer questions about public GitHub repositories. Use the DeepWiki tools to read their documentation; never guess. "
              "Answer the person in at most three short sentences.",
    "model": {"provider": "recorded", "model": "claude-haiku-4-5-20251001"},
    "parameters": {"max_output_tokens": 1500, "temperature": 0, "upstream_provider": "anthropic"},
    "default_projection": {"exclude_kinds": ["projection"], "budget": 400000},
    "known_capacities": [],
    "rules": [],
    "limits": {"max_inferences_per_message": 6, "context_tokens": 200000},
    "mcp": {"servers": ["deepwiki"], "mandatory": ["deepwiki"], "max_calls_per_turn": 2},
}
DESCRIPTOR = {"brique": {"kind": "matter", "substance_mode": "brique", "substance_type": "text"},
              "functional": {"format": "json", "schema_ref": "/root/agentic/schema/pilot_definition_data"},
              "objective": {"name": PILOT_ID, "type": "pilot_definition",
                            "description": "Pilot of the real public MCP test: answers about GitHub repositories with the native tools of DeepWiki."},
              "subjective": {}}


def gateway_answers(journal):
    return [(m.get("correlation") or {}).get("intention_id") and (m["content"].get("response") or {}) for m in journal if m["kind"] == "capacity_response"]


@unittest.skipUnless(REAL, SKIP_REASON)
class PublicMcpServer(unittest.IsolatedAsyncioTestCase):

    async def test_a_capacity_pilot_calls_deepwiki_from_its_plan_with_no_inference(self):
        async with RealDriver() as d:
            await d.reset_world()
            cid = "real_mcp_plan_1"
            call_id = f"plan_deepwiki_{int(time.time())}"
            try:
                await d.create(cid, intention="Read the documentation topics of a repository.",
                               pilot_ref={"context": TEST, "capacity": "deepwiki_topics"})
                await d.send(cid, "m1", {"repo": REPO, "call_id": call_id})
                await d.quiet(cid, timeout=180)
                journal = await d.journal(cid)
                answers = [m["content"] for m in journal if m["kind"] == "pilot"]
                self.assertTrue(answers, [m["kind"] for m in journal])
                payload = answers[-1].get("content") if isinstance(answers[-1], dict) else None
                if isinstance(payload, str):
                    payload = json.loads(payload)
                self.assertEqual((payload["call_id"], payload["state"], payload["transmission"]), (call_id, "completed", "complete"), payload)
                topics = "\n".join(b.get("text", "") for b in (payload["result"] or {}).get("content", []))
                self.assertGreater(len(topics), 50, topics)
                self.assertEqual(await d.real_calls(), [])                       # no model was asked anything
                self.assertEqual([m for m in journal if m["kind"] == "error"], [])
            finally:
                await d.cleanup(cid)

    async def test_a_model_given_deepwiki_calls_it_natively(self):
        async with RealDriver() as d:
            await d.reset_world()
            if not await d.exists(TEST, PILOT_ID):
                await d.call(TEST, "matter.create", "matter", {"matter_id": PILOT_ID, "matter": DESCRIPTOR, "payload": json.dumps(PILOT)})
            else:
                await d.write(TEST, PILOT_ID, PILOT)
            cid = "real_mcp_public_1"
            try:
                await d.create(cid, intention="Answer questions about GitHub repositories.", pilot_ref={"context": TEST, "matter_id": PILOT_ID})
                await d.send(cid, "m1", f"What are the main documentation topics of the GitHub repository {REPO}?")
                await d.quiet(cid, timeout=240)
                journal = await d.journal(cid)
                turns = [m["content"] for m in journal if m["kind"] == "completion" and isinstance(m["content"], dict) and m["content"].get("step") == "tool_turn"]
                called = [c["name"] for t in turns for c in t["calls"]]
                self.assertTrue(called and all(name.startswith("deepwiki__") for name in called), called)
                states = [(m["content"].get("response") or {}).get("payload", {}).get("state") for m in journal if m["kind"] == "capacity_response"]
                self.assertIn("completed", states, states)
                self.assertTrue(" ".join(pilot_texts(journal)).strip())
                self.assertEqual([m["content"] for m in journal if m["kind"] == "error"], [])
                rt = await d.runtime(cid)
                self.assertEqual((rt["status"], rt["active_wait_groups"]), ("idle", []))
            finally:
                await d.cleanup(cid)


if __name__ == "__main__":
    unittest.main()
