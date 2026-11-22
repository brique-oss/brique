# Copyright 2026 Nicolas Cassan
# Licensed under the Apache License, Version 2.0.

import json
import os
import threading
import unittest
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path
from unittest import mock

from wrapper import matter_client as mc
from wrapper.execution import current_intention

VECTORS = json.loads((Path(__file__).resolve().parents[2].parent / "vectors" / "matter_client.json").read_text(encoding="utf-8"))
INTENTION = {"intention_id": "i1", "to": {"context": "/ctx", "cap": "cap", "type": "user"}, "correlation": {}}


class Vectors(unittest.TestCase):
    def test_substance(self):
        for case in VECTORS["substance"]:
            if "error" in case:
                with self.assertRaises(mc.MatterError, msg=case["name"]) as caught:
                    mc.classify_substance(case["data"])
                self.assertEqual(caught.exception.details["cause"], case["error"], case["name"])
            else:
                got = mc.classify_substance(case["data"])
                self.assertEqual(got.kind, case["result"]["kind"], case["name"])
                self.assertEqual(got.text if got.kind == "inline" else got.url, case["result"].get("text") or case["result"].get("url"), case["name"])

    def test_revision(self):
        for case in VECTORS["revision"]:
            self.assertEqual(mc.revision_of(case["brique"]), case["revision"], str(case))

    def test_lines(self):
        for case in VECTORS["lines"]:
            if "error" in case:
                with self.assertRaises(mc.MatterError, msg=case["name"]) as caught:
                    mc.parse_lines(case["text"])
                self.assertEqual(caught.exception.details["cause"], case["error"], case["name"])
            else:
                self.assertEqual(mc.parse_lines(case["text"]), case["lines"], case["name"])

    def test_dump_lines(self):
        for case in VECTORS["dump_lines"]:
            self.assertEqual(mc.dump_lines(case["lines"]), case["text"], case["name"])

    def test_append_threshold(self):
        self.assertEqual(mc.INLINE_APPEND_MAX, VECTORS["append"]["inline_max_bytes"])

    def test_guarded_write_pause(self):
        spec = VECTORS["guarded_write_pause"]
        for case in spec["cases"]:
            self.assertEqual(mc.guarded_write_pause_ms(case["attempt"], 1.0, spec["base_ms"], spec["cap_ms"]), case["max_ms"], str(case))
            self.assertEqual(mc.guarded_write_pause_ms(case["attempt"], 0.0, spec["base_ms"], spec["cap_ms"]), 0.0)


class FakeOutbound:
    def __init__(self, answers):
        self.answers, self.calls = answers, []

    async def emit_and_wait(self, **kwargs):
        self.calls.append(kwargs)
        answer = self.answers.pop(0) if isinstance(self.answers, list) else self.answers(kwargs)
        return {"response": answer}


class FakeRuntime:
    def __init__(self, answers):
        self.outbound = FakeOutbound(answers)

    def target_context(self, intention):
        return intention["to"]["context"].strip("/")


def ok(payload):
    return {"status": "ok", "payload": payload}


def error(reason, code="refused"):
    return {"status": "error", "error": {"code": code, "message": reason, "details": {"reason": reason}}}


class Leases:
    """A lease server that answers each request from a script of (status, body)."""

    def __init__(self, script):
        self.script, self.puts = list(script), []
        outer = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def answer(self):
                status, body = outer.script.pop(0)
                self.send_response(status)
                self.end_headers()
                self.wfile.write(body.encode("utf-8"))

            def do_GET(self):
                self.answer()

            def do_PUT(self):
                outer.puts.append(self.rfile.read(int(self.headers["Content-Length"])))
                self.answer()

        self.server = HTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def handle(self):
        return {"kind": "http", "http": {"base_url": f"http://127.0.0.1:{self.server.server_port}", "path": "/substance/x", "tok": "t"}}

    def close(self):
        self.server.shutdown()


class ClientTest(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        current_intention.set(INTENTION)      # each test runs in a context of its own

    def client(self, answers):
        runtime = FakeRuntime(answers)
        return mc.MatterClient(runtime), runtime.outbound

    async def test_read_inline_goes_to_the_context_of_the_capacity(self):
        client, out = self.client([ok({"data": {"kind": "inline", "bytes": '{"a": 1}'}, "brique": {"revision": 7}, "functional": {"f": 1}})])
        view = await client.read("m1")
        self.assertEqual((view.content, view.revision, view.descriptor), ({"a": 1}, 7, {"f": 1}))
        call = out.calls[0]
        self.assertEqual((call["to_context"], call["to_cap"], call["to_type"], call["from_cap"]), ("ctx", "matter.read", "matter", "cap"))
        self.assertEqual(call["correlation"]["parent_intention_id"], "i1")

    async def test_read_through_a_lease_and_again_when_it_is_stale(self):
        leases = Leases([(409, ""), (200, '{"big": true}')])
        self.addCleanup(leases.close)
        client, out = self.client([ok({"data": leases.handle(), "brique": {"revision": 1}}), ok({"data": leases.handle(), "brique": {"revision": 2}})])
        view = await client.read("m1")
        self.assertEqual((view.content, view.revision), ({"big": True}, 2))
        self.assertEqual(len(out.calls), 2)

    async def test_a_substance_that_keeps_changing_is_a_consistency_failure(self):
        leases = Leases([(409, "")] * 3)
        self.addCleanup(leases.close)
        client, _ = self.client(lambda _kw: ok({"data": leases.handle(), "brique": {}}))
        with mock.patch.dict(os.environ, {"BRIQUE_LEASE_RETRIES": "3"}):
            with self.assertRaises(mc.MatterError) as caught:
                await client.read("m1")
        self.assertEqual(caught.exception.reason, "consistency_failure")

    async def test_write_is_guarded_by_a_decimal_revision(self):
        client, out = self.client([ok({"revision": 9})])
        self.assertEqual(await client.write("m1", {"a": 1}, expected_revision=1790000000000000001), 9)
        params = out.calls[0]["params"]
        self.assertEqual(params["expected_revision"], "1790000000000000001")
        self.assertEqual(json.loads(params["data"]), {"a": 1})
        client, out = self.client([ok({"revision": 10})])
        await client.write("m1", {"a": 1})
        self.assertNotIn("expected_revision", out.calls[0]["params"])

    async def test_a_refused_write_is_typed(self):
        client, _ = self.client([error("revision_mismatch")])
        with self.assertRaises(mc.RevisionMismatch):
            await client.write("m1", {}, expected_revision=1)

    async def test_append_inline_and_through_a_lease(self):
        client, out = self.client([ok({"total_lines": 3})])
        self.assertEqual(await client.append_lines("j", [{"a": 1}], 2), 3)
        self.assertEqual(out.calls[0]["params"], {"matter_id": "j", "expected_line_count": 2, "data": '{"a":1}\n'})
        leases = Leases([(200, "")])
        self.addCleanup(leases.close)
        client, out = self.client([ok({"http_data": leases.handle()})])
        big = [{"k": "x" * 1000}] * 300
        self.assertEqual(await client.append_lines("j", big, 5), 305)
        self.assertTrue(out.calls[0]["params"]["http_data"])
        self.assertEqual(leases.puts[0].decode("utf-8"), mc.dump_lines(big))

    async def test_an_append_lease_that_conflicts_is_a_line_count_mismatch(self):
        leases = Leases([(409, "")])
        self.addCleanup(leases.close)
        client, _ = self.client([ok({"http_data": leases.handle()})])
        with self.assertRaises(mc.LineCountMismatch):
            await client.append_lines("j", [{"k": "x" * 1000}] * 300, 5)

    async def test_read_lines(self):
        client, out = self.client([ok({"data": {"kind": "inline", "bytes": '{"a":1}\n{"b":2}\n', "total_lines": 9}, "brique": {"revision": 4}})])
        view = await client.read_lines("j", 7, 2)
        self.assertEqual((view.lines, view.total_lines, view.revision, view.offset), ([{"a": 1}, {"b": 2}], 9, 4, 7))
        self.assertEqual(out.calls[0]["params"]["data_range"], {"unit": "lines", "offset": 7, "length": 2})

    async def test_update_starts_again_after_a_lost_race(self):
        script = [ok({"data": {"bytes": '{"n": 1}'}, "brique": {"revision": 1}}), error("revision_mismatch"),
                  ok({"data": {"bytes": '{"n": 2}'}, "brique": {"revision": 2}}), ok({"revision": 3})]
        client, out = self.client(script)
        with mock.patch.dict(os.environ, {"BRIQUE_CAS_PAUSE_BASE_MS": "0"}):
            result = await client.update("m1", lambda c: ({"n": c["n"] + 1}, c["n"]))
        self.assertEqual(result, 2)
        self.assertEqual(json.loads(out.calls[-1]["params"]["data"]), {"n": 3})
        self.assertEqual(out.calls[-1]["params"]["expected_revision"], "2")

    async def test_update_with_nothing_to_write_writes_nothing(self):
        client, out = self.client([ok({"data": {"bytes": '{"n": 1}'}, "brique": {"revision": 1}})])
        self.assertEqual(await client.update("m1", lambda c: (None, "already")), "already")
        self.assertEqual(len(out.calls), 1)

    async def test_update_gives_up_after_the_attempts(self):
        def answers(kwargs):
            if kwargs["to_cap"] == "matter.read":
                return ok({"data": {"bytes": "{}"}, "brique": {"revision": 1}})
            return error("revision_mismatch")
        client, out = self.client(answers)
        with mock.patch.dict(os.environ, {"BRIQUE_CAS_PAUSE_BASE_MS": "0", "BRIQUE_CAS_ATTEMPTS": "3"}):
            with self.assertRaises(mc.ConcurrencyExhausted):
                await client.update("m1", lambda c: ({"x": 1}, None))
        self.assertEqual(len(out.calls), 6)


class KnownCapacity(unittest.IsolatedAsyncioTestCase):
    async def test_a_known_capacity_needs_no_running_intention(self):
        runtime = FakeRuntime([ok({"revision": 3})])
        self.assertEqual(await mc.MatterClient(runtime, "/ctx", from_cap="cap").write("m1", {}), 3)
        call = runtime.outbound.calls[0]
        self.assertEqual((call["to_context"], call["from_context"], call["from_cap"], call["correlation"]), ("/ctx", "", "cap", None))


class NoRunningCapacity(unittest.IsolatedAsyncioTestCase):
    async def test_needs_a_running_capacity(self):
        with self.assertRaises(RuntimeError):
            await mc.MatterClient(FakeRuntime([])).read("m1")


if __name__ == "__main__":
    unittest.main()
