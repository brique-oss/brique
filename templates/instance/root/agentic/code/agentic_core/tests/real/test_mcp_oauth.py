"""An MCP server behind OAuth, end to end: the operator's login, a model's call, the automatic refresh, the revocation.

The server is the test MCP server in OAuth mode (root/mcp/client/code/client_mcp_gateway/testserver, run as
`go run ./testserver -addr 127.0.0.1:18298 -oauth -token-life 40s`), with its own authorization server (PKCE, RFC 9207 iss,
rotating refresh tokens, POST /admin/revoke, GET /admin/stats). The registry names it oauthcrm (auth oauth, issuer
http://127.0.0.1:18298, client brique, scope ceiling read).

Before the run, the operator logs in once:
    client_mcp_gateway login oauthcrm --store <machine>/wrappers/client_mcp_gateway
The test ends by revoking every token: log in again before running it a second time.

Run with BRIQUE_REAL_LLM=1 (Haiku through the recorded provider). About 1.5 minutes: it waits for an access token to expire.
Judged on facts: the gateway's state of each call and the counters of the authorization server, never the wording.
"""
import asyncio
import json
import time
import unittest
import urllib.request

from ..integration.driver import TEST
from .harness import REAL, SKIP_REASON, RealDriver, pilot_texts

AUTHORIZATION_SERVER = "http://127.0.0.1:18298"
TOKEN_LIFE_S = 40
PILOT_ID = "real_mcp_oauth_pilot"
PILOT = {
    "id": PILOT_ID,
    "prompt": "You assist with the customer records of a small company. Use the customer tools to read records; never guess a record. "
              "If a tool fails, say so in one sentence. Answer the person in one short sentence.",
    "model": {"provider": "recorded", "model": "claude-haiku-4-5-20251001"},
    "parameters": {"max_output_tokens": 1024, "temperature": 0, "upstream_provider": "anthropic"},
    "default_projection": {"exclude_kinds": ["projection"], "budget": 400000},
    "known_capacities": [],
    "rules": [],
    "limits": {"max_inferences_per_message": 6, "context_tokens": 200000},
    "mcp": {"servers": ["oauthcrm"], "max_calls_per_turn": 2},
}
DESCRIPTOR = {"brique": {"kind": "matter", "substance_mode": "brique", "substance_type": "text"},
              "functional": {"format": "json", "schema_ref": "/root/agentic/schema/pilot_definition_data"},
              "objective": {"name": PILOT_ID, "type": "pilot_definition",
                            "description": "Pilot of the real OAuth MCP test: reads customer records through an OAuth-protected MCP server."},
              "subjective": {}}


def authorization_server(path, method="GET"):
    request = urllib.request.Request(AUTHORIZATION_SERVER + path, method=method, data=b"" if method == "POST" else None)
    with urllib.request.urlopen(request, timeout=10) as answer:
        return json.loads(answer.read())


def mcp_states(journal, since=0):
    """(state, reason) of each gateway answer after line `since`."""
    out = []
    for m in journal:
        if m["kind"] == "capacity_response" and m["seq"] > since:
            payload = (m["content"].get("response") or {}).get("payload") or {}
            out.append((payload.get("state"), payload.get("reason") or ""))
    return out


@unittest.skipUnless(REAL, SKIP_REASON)
class OAuthMcpServer(unittest.IsolatedAsyncioTestCase):

    async def test_login_call_refresh_and_revocation(self):
        authorization_server("/admin/stats")                                   # the server must run (see the docstring)
        async with RealDriver() as d:
            await d.reset_world()
            if not await d.exists(TEST, PILOT_ID):
                await d.call(TEST, "matter.create", "matter", {"matter_id": PILOT_ID, "matter": DESCRIPTOR, "payload": json.dumps(PILOT)})
            else:
                await d.write(TEST, PILOT_ID, PILOT)
            cid = "real_mcp_oauth_1"
            try:
                await d.create(cid, intention="Answer questions about customer records.", pilot_ref={"context": TEST, "matter_id": PILOT_ID})

                # 1. the grant of the login is used: the call completes
                await d.send(cid, "m1", "What is the name of customer 42?")
                await d.quiet(cid, timeout=120)
                journal = await d.journal(cid)
                projection = [m["content"] for m in journal if m["kind"] == "event" and (m["content"] or {}).get("event") == "mcp_projection"]
                self.assertTrue(projection and "oauthcrm__customer_read" in projection[0]["tools"], projection)
                self.assertIn(("completed", ""), mcp_states(journal), mcp_states(journal))
                self.assertIn("Ada", " ".join(pilot_texts(journal)))
                seen = max(m["seq"] for m in journal)

                # 2. the access token expires: the gateway refreshes it by itself, and the call completes
                before = authorization_server("/admin/stats")["refreshes"]
                await asyncio.sleep(TOKEN_LIFE_S + 5)
                await d.send(cid, "m2", "And the name of customer 7?")
                await d.quiet(cid, timeout=120)
                journal = await d.journal(cid)
                self.assertIn(("completed", ""), mcp_states(journal, seen), mcp_states(journal, seen))
                self.assertIn("Turing", " ".join(pilot_texts(journal)))
                self.assertGreaterEqual(authorization_server("/admin/stats")["refreshes"], before + 1)
                seen = max(m["seq"] for m in journal)

                # 3. the authorization server revokes every token. The next run asks for a projection: the gateway cannot refresh the
                #    grant, so it leaves the server out (authentication_required) rather than offer tools it cannot call; the model is told
                #    the server is unavailable and answers the person; nothing is sent to the server
                authorization_server("/admin/revoke", "POST")
                await d.send(cid, "m3", "Read customer 42 again, please.")
                await d.quiet(cid, timeout=120)
                journal = await d.journal(cid)
                later = [m for m in journal if m["seq"] > seen]
                projections = [m["content"] for m in later if m["kind"] == "event" and (m["content"] or {}).get("event") == "mcp_projection"]
                self.assertTrue(projections, [m["kind"] for m in later])
                self.assertEqual(projections[-1]["tools"], [])
                self.assertIn({"server": "oauthcrm", "reason": "authentication_required"}, projections[-1]["unavailable"])
                self.assertTrue(all(state != "completed" for state, _ in mcp_states(journal, seen)), mcp_states(journal, seen))
                self.assertTrue([m for m in later if m["kind"] == "pilot"], "the pilot did not answer the person")
                rt = await d.runtime(cid)
                self.assertEqual((rt["status"], rt["active_wait_groups"]), ("idle", []))
            finally:
                await d.cleanup(cid)


if __name__ == "__main__":
    unittest.main()
