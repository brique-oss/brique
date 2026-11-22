"""BriquePort: how a capacity speaks to the engine. A recording stub stands in for the engine and the wrapper runtime.

What is checked: the protocol of the journal (a line-framed Matter: append guarded by expected_line_count, read by line range,
the HTTP lease for a large block), the form in which a parameter leaves (a value, or a transit reference for what is large, released
when nobody consumed it) and arrives (resolved once), and the DIRECT_CALLS switch.
"""
import asyncio
import json
import os
import sys
import threading
import unittest
from http.server import BaseHTTPRequestHandler, HTTPServer

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)
sys.path.insert(0, ROOT)


def _wrapper_lib():
    here = os.path.abspath(HERE)
    while here != os.path.dirname(here):
        candidate = os.path.join(here, "wrapper", "python", "lib")
        if os.path.isfile(os.path.join(candidate, "wrapper", "__init__.py")):
            return candidate
        here = os.path.dirname(here)
    raise RuntimeError("wrapper/python/lib not found above the tests")


sys.path.insert(0, _wrapper_lib())

from substrate import port as port_module  # noqa: E402
from substrate.errors import ConsistencyError, LineCountMismatch, NotFound  # noqa: E402
from substrate.port import INLINE_APPEND_MAX, BriquePort, dump_lines, resolve_params  # noqa: E402


class StubOutbound:
    def __init__(self, handler):
        self.handler, self.sent = handler, []

    async def emit_and_wait(self, **kw):
        self.sent.append(kw)
        status, payload = self.handler(kw)
        body = {"intention_id": "i", "status": status}
        body["payload" if status == "ok" else "error"] = payload
        return {"kind": "response", "response": body}


class StubRuntime:
    def __init__(self, handler, transit=None, calls=None):
        self.outbound = StubOutbound(handler)
        if transit is not None:
            self.transit = transit
        if calls is not None:
            self.calls = calls


def ok(payload):
    return "ok", payload


def failed(code, reason, message="refused"):
    return "error", {"code": code, "message": message, "details": {"reason": reason}}


def line(seq, **kw):
    return dict({"id": f"m{seq}", "seq": seq, "from": "user:1", "to": "c1", "kind": "user", "content": "x"}, **kw)


class JournalProtocol(unittest.IsolatedAsyncioTestCase):
    async def test_an_append_is_one_block_of_lines_guarded_by_the_line_count(self):
        rt = StubRuntime(lambda kw: ok({"ok": True, "total_lines": 5}))
        total = await BriquePort(rt, "test").matter_append_lines("journal_c1", [line(4), line(5)], 3)
        self.assertEqual(total, 5)
        sent = rt.outbound.sent[0]
        self.assertEqual((sent["to_cap"], sent["to_type"]), ("matter.append", "matter"))
        self.assertEqual(sent["params"]["expected_line_count"], 3)
        self.assertEqual(sent["params"]["data"], dump_lines([line(4), line(5)]))
        self.assertTrue(sent["params"]["data"].endswith("\n"))                       # every line, hence the block, ends with LF
        self.assertEqual(sent["params"]["data"].count("\n"), 2)
        self.assertNotIn("http_data", sent["params"])

    async def test_a_refused_line_count_guard_is_a_line_count_mismatch(self):
        rt = StubRuntime(lambda kw: failed("refused", "line_count_mismatch"))
        with self.assertRaises(LineCountMismatch):
            await BriquePort(rt, "test").matter_append_lines("journal_c1", [line(4)], 3)

    async def test_a_range_of_lines_is_read_and_parsed(self):
        text = dump_lines([line(2), line(3)])
        rt = StubRuntime(lambda kw: ok({"brique": {"revision": 77, "substance_line_count": 3}, "data": {"kind": "inline", "bytes": text}}))
        view = await BriquePort(rt, "test").matter_read_lines("journal_c1", 1, 2)
        self.assertEqual((view.total_lines, view.revision, view.offset), (3, 77, 1))
        self.assertEqual([m["seq"] for m in view.lines], [2, 3])
        self.assertEqual(rt.outbound.sent[0]["params"]["data_range"], {"unit": "lines", "offset": 1, "length": 2})
        self.assertEqual(rt.outbound.sent[0]["params"]["read_mode"], "data|brique")

    async def test_a_line_that_is_not_json_is_a_consistency_failure(self):
        rt = StubRuntime(lambda kw: ok({"brique": {}, "data": {"kind": "inline", "bytes": "{not json}\n"}}))
        with self.assertRaises(ConsistencyError):
            await BriquePort(rt, "test").matter_read_lines("journal_c1", 0, 10)

    async def test_stat_gives_the_revision_and_the_line_count_without_reading(self):
        rt = StubRuntime(lambda kw: ok({"ok": True, "revision": 9, "total_lines": 42}))
        self.assertEqual(await BriquePort(rt, "test").matter_stat("journal_c1"), (9, 42))
        self.assertEqual(rt.outbound.sent[0]["to_cap"], "matter.stat")


class BatchRead(unittest.IsolatedAsyncioTestCase):
    def item(self, matter_id, content, revision):
        return {"ok": True, "matter_id": matter_id, "functional": {"format": "json"}, "brique": {"revision": revision},
                "data": {"kind": "inline", "bytes": json.dumps(content)}}

    async def test_several_matters_are_read_in_one_request_in_the_order_given(self):
        rt = StubRuntime(lambda kw: ok({"result": [self.item("runtime_c1", {"status": "idle"}, 5), self.item("mailbox_c1", {"entries": []}, 7)]}))
        views = await BriquePort(rt, "test").matter_read_many(["runtime_c1", "mailbox_c1"])
        self.assertEqual([(v.matter_id, v.content, v.revision) for v in views], [("runtime_c1", {"status": "idle"}, 5), ("mailbox_c1", {"entries": []}, 7)])
        self.assertEqual(len(rt.outbound.sent), 1)                                       # one engine request, not two
        self.assertEqual((rt.outbound.sent[0]["to_cap"], rt.outbound.sent[0]["params"]["matter_ids"]), ("matter.read_batch", ["runtime_c1", "mailbox_c1"]))

    async def test_a_missing_matter_is_not_found_like_a_single_read(self):
        rt = StubRuntime(lambda kw: ok({"result": [self.item("runtime_c1", {}, 1), {"ok": False, "matter_id": "mailbox_c1", "error": {"code": "not_found", "message": "gone"}}]}))
        with self.assertRaises(NotFound):
            await BriquePort(rt, "test").matter_read_many(["runtime_c1", "mailbox_c1"])

    async def test_an_answer_of_another_length_is_a_consistency_failure(self):
        rt = StubRuntime(lambda kw: ok({"result": [self.item("runtime_c1", {}, 1)]}))
        with self.assertRaises(ConsistencyError):
            await BriquePort(rt, "test").matter_read_many(["runtime_c1", "mailbox_c1"])


class _Lease(BaseHTTPRequestHandler):
    status, body = 200, b""

    def do_PUT(self):
        length = int(self.headers.get("Content-Length", "0"))
        type(self).body = self.rfile.read(length)
        self.send_response(type(self).status)
        self.end_headers()

    def log_message(self, *args):
        pass


class LargeAppend(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        _Lease.status, _Lease.body = 200, b""
        self.server = HTTPServer(("127.0.0.1", 0), _Lease)
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()

    def handler(self, kw):
        if "http_data" in kw["params"]:
            host, port = self.server.server_address
            return ok({"ok": True, "http_data": {"kind": "http", "method": "PUT", "base_url": f"http://{host}:{port}", "path": "/substance/r1", "tok": "t"}})
        return ok({"ok": True, "total_lines": 9})

    async def test_a_block_above_the_inline_limit_goes_through_the_engine_lease(self):
        rt = StubRuntime(self.handler)
        big = line(4, content="x" * (INLINE_APPEND_MAX + 1000))
        total = await BriquePort(rt, "test").matter_append_lines("journal_c1", [big], 3)
        self.assertEqual(total, 4)
        sent = rt.outbound.sent[0]["params"]
        self.assertTrue(sent["http_data"])
        self.assertNotIn("data", sent)                                              # the block itself never travels in the intention
        self.assertEqual(sent["expected_line_count"], 3)
        self.assertEqual(_Lease.body.decode("utf-8"), dump_lines([big]))            # it was PUT, whole, through the lease

    async def test_a_lease_refused_because_the_journal_moved_is_a_line_count_mismatch(self):
        _Lease.status = 409
        with self.assertRaises(LineCountMismatch):
            await BriquePort(StubRuntime(self.handler), "test").matter_append_lines("journal_c1", [line(4, content="x" * (INLINE_APPEND_MAX + 1))], 3)


class FakeTransit:
    """Stands in for the transit service of the runtime: a large string leaves as a reference, the rest as a value."""
    def __init__(self):
        self.released, self.posted = [], []

    async def emit_async(self, value, *, target, type_tag=""):
        if isinstance(value, (dict, str)) and len(json.dumps(value)) > 64 * 1024:
            ref = {"@file": f"/tmp/transit/{len(self.posted)}", "format": "json"}
            self.posted.append((ref, target))
            return ref
        return value

    def release(self, ref):
        self.released.append(ref)


class Parameters(unittest.IsolatedAsyncioTestCase):
    async def test_a_large_parameter_leaves_as_a_reference_and_a_small_one_as_a_value(self):
        transit = FakeTransit()
        rt = StubRuntime(lambda kw: ok({"response": {}}), transit=transit)
        await BriquePort(rt, "test").call_capacity("/root/llm/infer", {"request": {"text": "x" * 100_000}, "provider": "fake"})
        sent = rt.outbound.sent[0]["params"]
        self.assertEqual(sent["provider"], "fake")                                  # small: the value itself
        self.assertEqual(list(sent["request"]), ["@file", "format"])               # large: a transit reference, chosen by the runtime
        ref, target = transit.posted[0]
        self.assertEqual((target.context, target.cap, target.same_wrapper), ("/root/llm", "infer", False))
        self.assertEqual(transit.released, [])                                      # consumed by the callee: nothing to release

    async def test_a_reference_nobody_consumed_is_released_when_the_call_fails(self):
        transit = FakeTransit()
        rt = StubRuntime(lambda kw: failed("not_found", "unknown_cap", "no such capacity"), transit=transit)
        with self.assertRaises(NotFound):
            await BriquePort(rt, "test").call_capacity("/root/llm/infer", {"request": {"text": "x" * 100_000}})
        self.assertEqual(len(transit.released), 1)

    async def test_engine_capacities_always_take_plain_values(self):
        transit = FakeTransit()
        rt = StubRuntime(lambda kw: ok({"ok": True}), transit=transit)
        await BriquePort(rt, "test").matter_write("m1", content={"text": "x" * 100_000})
        self.assertEqual(transit.posted, [])

    async def test_received_transit_parameters_are_resolved_once(self):
        class Incoming(dict):
            async def value(self, name):
                return {"resolved": name}
        params = Incoming({"request": {"@file": "/tmp/x", "format": "json"}, "provider": "fake", "plain": {"a": 1}})
        out = await resolve_params(params)
        self.assertEqual(out["request"], {"resolved": "request"})
        self.assertEqual((out["provider"], out["plain"]), ("fake", {"a": 1}))
        self.assertEqual(await resolve_params({"request": {"@file": "/x"}}), {"request": {"@file": "/x"}})     # a plain dict is left as is


class Direct(unittest.IsolatedAsyncioTestCase):
    class Calls:
        def __init__(self):
            self.seen = []

        def is_local(self, context, cap, to_type="user"):
            return context == "/root/agentic"

        async def call(self, *, to, params, direct, timeout_s=None):
            self.seen.append((to["cap"], direct))
            return {"done": True}

    async def test_direct_calls_are_a_single_switch_off_by_default(self):
        calls = self.Calls()
        rt = StubRuntime(lambda kw: ok({"routed": True}), calls=calls)
        port = BriquePort(rt, "test")
        self.assertEqual(await port.call_capacity("/root/agentic/mailbox.read", {}), {"routed": True})        # routed (and traced) by the engine
        self.assertEqual(calls.seen, [])
        port_module.DIRECT_CALLS = True
        try:
            self.assertEqual(await port.call_capacity("/root/agentic/mailbox.read", {}), {"done": True})      # a plain function call
            self.assertEqual(calls.seen, [("mailbox.read", True)])
            self.assertEqual(len(rt.outbound.sent), 1)                                                         # nothing more went through the engine
            await port.call_capacity("/root/llm/infer", {})                                                    # another wrapper: always routed
            self.assertEqual(len(rt.outbound.sent), 2)
        finally:
            port_module.DIRECT_CALLS = False


if __name__ == "__main__":
    unittest.main()
