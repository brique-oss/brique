"""A real model calls an MCP tool natively, end to end (MCP realization plan, WP5 and WP9).

Matter pilot real_mcp_pilot (created here once, model Haiku through the recorded provider, mcp section {servers: [testcrm]})
-> Agentic offers the gateway's projection as native tools -> the model calls testcrm__customer_read -> Agentic signs the envelope and
calls /root/mcp/client/call -> the gateway (client_mcp_gateway) calls the local test MCP server -> the result is the native tool result
of the next inference -> the pilot answers the person.

Needs: BRIQUE_REAL_LLM=1; the engine with the agentic, test and mcp/client contexts; the test MCP server running
(go run ./testserver in root/mcp/client/code/client_mcp_gateway); the registry of security/mcp_registry.json installed for
client_mcp_gateway; the secret mcp_authority_key granted to agentic_core. Judged on facts: the tool turn, the gateway's terminal state,
the name read from the server in the answer. The wording is never asserted beyond that name.
"""
import json
import unittest

from ..integration.driver import TEST
from .harness import REAL, SKIP_REASON, RealDriver, pilot_texts

PILOT_ID = "real_mcp_pilot"
PILOT = {
    "id": PILOT_ID,
    "prompt": "You assist with the customer records of a small company. Use the customer tools to read records; never guess a record. "
              "Answer the person in one short sentence.",
    "model": {"provider": "recorded", "model": "claude-haiku-4-5-20251001"},
    "parameters": {"max_output_tokens": 1024, "temperature": 0, "upstream_provider": "anthropic"},
    "default_projection": {"exclude_kinds": ["projection"], "budget": 400000},
    "known_capacities": [],
    "rules": [],
    "limits": {"max_inferences_per_message": 6, "context_tokens": 200000},
    "mcp": {"servers": ["testcrm"], "mandatory": ["testcrm"]},
}
DESCRIPTOR = {"brique": {"kind": "matter", "substance_mode": "brique", "substance_type": "text"},
              "functional": {"format": "json", "schema_ref": "/root/agentic/schema/pilot_definition_data"},
              "objective": {"name": PILOT_ID, "type": "pilot_definition",
                            "description": "Pilot of the real MCP test: reads customer records through the native tools of the test MCP server."},
              "subjective": {}}


@unittest.skipUnless(REAL, SKIP_REASON)
class McpToolPilot(unittest.IsolatedAsyncioTestCase):

    async def test_the_model_reads_a_record_through_the_gateway_and_answers_with_it(self):
        async with RealDriver() as d:
            await d.reset_world()
            if not await d.exists(TEST, PILOT_ID):
                await d.call(TEST, "matter.create", "matter", {"matter_id": PILOT_ID, "matter": DESCRIPTOR, "payload": json.dumps(PILOT)})
            else:
                await d.write(TEST, PILOT_ID, PILOT)
            cid = "real_mcp_1"
            try:
                await d.create(cid, intention="Answer questions about customer records.", pilot_ref={"context": TEST, "matter_id": PILOT_ID})
                await d.send(cid, "m1", "What is the name of customer 42?")
                await d.quiet(cid, timeout=120)
                journal = await d.journal(cid)
                turns = [m["content"] for m in journal if m["kind"] == "completion" and isinstance(m["content"], dict) and m["content"].get("step") == "tool_turn"]
                self.assertTrue(turns, [m["kind"] for m in journal])
                self.assertIn("testcrm__customer_read", [c["name"] for t in turns for c in t["calls"]])
                answers = [m["content"]["response"] for m in journal if m["kind"] == "capacity_response"]
                states = [(a.get("payload") or {}).get("state") for a in answers]
                self.assertIn("completed", states, answers)
                texts = " ".join(pilot_texts(journal))
                self.assertIn("Ada", texts)
                errors = [m["content"] for m in journal if m["kind"] == "error"]
                self.assertEqual(errors, [])
                rt = await d.runtime(cid)
                self.assertEqual((rt["status"], rt["active_wait_groups"]), ("idle", []))
            finally:
                await d.cleanup(cid)


if __name__ == "__main__":
    unittest.main()
