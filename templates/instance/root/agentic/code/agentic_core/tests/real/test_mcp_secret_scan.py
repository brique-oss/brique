"""No downstream secret leaves its two places: a full run with a static token and with OAuth, then every byte the run wrote is searched.

A downstream secret is a credential of an MCP server: the static token of statcrm (a machine secret the engine gives to the gateway
as MCP_SECRET_MCP_STATCRM_TOKEN) and every access and refresh token the authorization server of oauthcrm issued (the grant and its
refreshes). It may exist in exactly two places: the secrets folder of the machine and the grant file of the gateway's private store.
The run goes through every layer that could copy one: the engine and its traces, the gateway (journal, projections, log), Agentic
(continuity journal, ledger, wrapper log), the provider adapter and the recorded model calls, the launcher's logs.

Servers (root/mcp/client/code/client_mcp_gateway/testserver):
    testserver -addr 127.0.0.1:18297 -bearer-file <machine>/secrets/mcp_statcrm_token
    testserver -addr 127.0.0.1:18298 -oauth -token-life 40s
and a fresh grant: client_mcp_gateway login oauthcrm --store <machine>/wrappers/client_mcp_gateway

Run with BRIQUE_REAL_LLM=1 (Haiku through the recorded provider). About a minute: it waits for an access token to expire, so that the
refreshed tokens are searched as well. Judged on bytes, never on wording.
"""
import asyncio
import base64
import json
import os
import tempfile
import time
import unittest
import urllib.request
from pathlib import Path

from ..integration.driver import TEST
from .harness import REAL, SKIP_REASON, RealDriver, pilot_texts

INSTANCE = Path(__file__).resolve().parents[6]                      # .../brique-nico
MACHINE = Path.home() / ".brique" / INSTANCE.name
SECRETS = MACHINE / "secrets"
STORE = MACHINE / "wrappers" / "client_mcp_gateway"
GRANTS = STORE / "oauth"
LAUNCHER = Path.home() / "Library" / "Application Support" / "brique-launcher"
STATIC_SECRET = SECRETS / "mcp_statcrm_token"
AUTHORIZATION_SERVER = "http://127.0.0.1:18298"
TOKEN_LIFE_S = 40
SKIPPED_DIRS = {".git", "envs", "node_modules", "__pycache__"}
PILOT_ID = "real_mcp_secret_scan_pilot"
PILOT = {
    "id": PILOT_ID,
    "prompt": "You assist with the customer records of a small company, kept in two CRMs. Use the customer tools; never guess a record. "
              "When asked about a customer, read it in both CRMs. Answer the person in one short sentence.",
    "model": {"provider": "recorded", "model": "claude-haiku-4-5-20251001"},
    "parameters": {"max_output_tokens": 1024, "temperature": 0, "upstream_provider": "anthropic"},
    "default_projection": {"exclude_kinds": ["projection"], "budget": 400000},
    "known_capacities": [],
    "rules": [],
    "limits": {"max_inferences_per_message": 6, "context_tokens": 200000},
    "mcp": {"servers": ["statcrm", "oauthcrm"], "max_calls_per_turn": 4},
}
DESCRIPTOR = {"brique": {"kind": "matter", "substance_mode": "brique", "substance_type": "text"},
              "functional": {"format": "json", "schema_ref": "/root/agentic/schema/pilot_definition_data"},
              "objective": {"name": PILOT_ID, "type": "pilot_definition",
                            "description": "Pilot of the real secret scan: reads a customer in a CRM with a static token and in one behind OAuth."},
              "subjective": {}}


def authorization_server(path):
    with urllib.request.urlopen(AUTHORIZATION_SERVER + path, timeout=10) as answer:
        return json.loads(answer.read())


def needles(secret):
    """The forms a secret could take once copied: as it is, in base64 (a Basic header, a JSON bytes field), and the bearer header."""
    raw = secret.encode()
    return {raw, base64.b64encode(raw).rstrip(b"="), base64.urlsafe_b64encode(raw).rstrip(b"="), b"Bearer " + raw}


def files_written_since(roots, since, allowed):
    """Every regular file under the roots modified at or after `since`, except the allowed places (a folder or a file)."""
    for root in roots:
        if not root.exists():
            continue
        for directory, subdirs, names in os.walk(root):
            here = Path(directory)
            subdirs[:] = [d for d in subdirs if d not in SKIPPED_DIRS and here / d not in allowed]
            for name in names:
                path = here / name
                if path in allowed or path.parent in allowed or path.is_symlink():
                    continue
                try:
                    if path.is_file() and path.stat().st_mtime >= since:
                        yield path
                except OSError:
                    continue


def leaks(paths, secrets):
    """(file, secret label) for each secret found in a file; the value itself is never printed."""
    found = []
    for path in paths:
        try:
            data = path.read_bytes()
        except OSError:
            continue
        for label, forms in secrets.items():
            if any(form in data for form in forms):
                found.append((str(path), label))
    return found


def mcp_states(journal):
    out = []
    for m in journal:
        if m["kind"] == "capacity_response":
            payload = (m["content"].get("response") or {}).get("payload") or {}
            out.append(payload.get("state"))
    return out


@unittest.skipUnless(REAL, SKIP_REASON)
class DownstreamSecretsStayInTheirPlaces(unittest.IsolatedAsyncioTestCase):

    async def test_a_full_run_writes_no_downstream_secret_anywhere(self):
        self.assertEqual(STATIC_SECRET.stat().st_mode & 0o777, 0o600)
        self.assertEqual((GRANTS / "oauthcrm.json").stat().st_mode & 0o777, 0o600)
        static = STATIC_SECRET.read_text().rstrip("\r\n")
        self.assertGreaterEqual(len(static), 16)
        since = min(STATIC_SECRET.stat().st_mtime, time.time()) - 1         # every file written since the static secret exists
        refreshes = authorization_server("/admin/stats")["refreshes"]

        async with RealDriver() as d:
            await d.reset_world()
            if not await d.exists(TEST, PILOT_ID):
                await d.call(TEST, "matter.create", "matter", {"matter_id": PILOT_ID, "matter": DESCRIPTOR, "payload": json.dumps(PILOT)})
            else:
                await d.write(TEST, PILOT_ID, PILOT)
            cid = "real_mcp_secret_scan_1"
            try:
                await d.create(cid, intention="Answer questions about customer records.", pilot_ref={"context": TEST, "matter_id": PILOT_ID})
                # both credentials are used: the static token, then the grant of the login
                await d.send(cid, "m1", "What is the name of customer 42?")
                await d.quiet(cid, timeout=120)
                journal = await d.journal(cid)
                projection = [m["content"] for m in journal if m["kind"] == "event" and (m["content"] or {}).get("event") == "mcp_projection"]
                self.assertTrue(projection, "no projection")
                self.assertTrue({"statcrm__customer_read", "oauthcrm__customer_read"} <= set(projection[0]["tools"]), projection[0])
                self.assertGreaterEqual(mcp_states(journal).count("completed"), 2, mcp_states(journal))
                self.assertIn("Ada", " ".join(pilot_texts(journal)))
                # the access token expires: the refresh issues new tokens, which must not leak either
                await asyncio.sleep(TOKEN_LIFE_S + 5)
                await d.send(cid, "m2", "And customer 7?")
                await d.quiet(cid, timeout=120)
                journal = await d.journal(cid)
                self.assertIn("Turing", " ".join(pilot_texts(journal)))
                self.assertGreaterEqual(authorization_server("/admin/stats")["refreshes"], refreshes + 1)
                surfaces = json.dumps([journal, await d.runtime(cid)]).encode()   # what the engine answers about the continuity
                await asyncio.sleep(2)                                            # let the logs reach their files
                secrets = self.secrets(static)
                # the files of the continuity (its journal, its ledger) are searched before the cleanup deletes them
                before = self.scan(since)
                self.assertTrue(any(cid in p.name for p in before), "the journal of the continuity was not among the scanned files")
                found = leaks(before, secrets)
                found += [("<journal and runtime of the continuity>", label) for label, forms in secrets.items() if any(f in surfaces for f in forms)]
            finally:
                await d.cleanup(cid)

        await asyncio.sleep(2)
        after = self.scan(since)
        found += leaks(after, secrets)
        self.assertTrue(any(p.name == "attempts.jsonl" for p in after), "the gateway journal was not among the scanned files")
        self.assertTrue(any("real_calls" in p.name for p in after), "the recorded model calls were not among the scanned files")
        self.assertEqual(sorted(set(found)), [], f"downstream secrets found outside their places ({len(before)} and {len(after)} files scanned)")

    def secrets(self, static):
        """The static token and every token the authorization server issued; the scanner is first shown to find them where they belong."""
        oauth_tokens = authorization_server("/admin/issued")["tokens"]
        self.assertGreaterEqual(len(oauth_tokens), 4)                        # the login and at least one refresh, each an access and a refresh
        secrets = {"static token of statcrm": needles(static)}
        for i, token in enumerate(oauth_tokens):
            secrets[f"oauth token {i} of oauthcrm"] = needles(token)
        self.assertIn("static token of statcrm", {label for _, label in leaks([STATIC_SECRET], secrets)})
        self.assertTrue(leaks([GRANTS / "oauthcrm.json"], secrets), "the scanner did not find the current grant in its own file")
        return secrets

    @staticmethod
    def scan(since):
        """The files of the instance, the machine directory, the launcher and the engine's temporary files written since `since`."""
        paths = list(files_written_since([INSTANCE, MACHINE, LAUNCHER], since, {SECRETS, GRANTS}))
        temp = Path(tempfile.gettempdir())
        paths += [p for p in temp.iterdir() if p.is_file() and not p.is_symlink() and p.stat().st_mtime >= since
                  and (p.name.startswith("brique") or p.name.startswith("agentic_core"))]
        return paths


if __name__ == "__main__":
    unittest.main()
