"""The wrapper layer: bindings match the capacity descriptors, and the capacities speak the
engine protocol through the generic outbound API (a recording stub stands in for the engine)."""
import asyncio
import inspect
import json
import os
import sys
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)
sys.path.insert(0, ROOT)
sys.path.insert(0, HERE)


def _wrapper_lib():
    """The runtime library is shared by every wrapper of the instance: <instance>/wrapper/python/lib."""
    here = os.path.abspath(HERE)
    while here != os.path.dirname(here):
        candidate = os.path.join(here, "wrapper", "python", "lib")
        if os.path.isfile(os.path.join(candidate, "wrapper", "__init__.py")):
            return candidate
        here = os.path.dirname(here)
    raise RuntimeError("wrapper/python/lib not found above the tests")


sys.path.insert(0, _wrapper_lib())

from wrapper.registry import RuntimeRegistry  # noqa: E402

CAPACITY_DIR = os.path.join(os.path.dirname(os.path.dirname(ROOT)), "capacity")


class StubOutbound:
    def __init__(self, handler):
        self.handler, self.sent = handler, []

    async def emit_and_wait(self, **kw):
        self.sent.append(kw)
        status, payload = self.handler(kw)
        body = {"intention_id": "i", "status": status}
        body["payload" if status == "ok" else "error"] = payload
        return {"kind": "response", "response": body}

    async def emit_intention(self, **kw):
        self.sent.append(dict(kw, fire=True))
        return "i"


class StubRuntime:
    def __init__(self, handler):
        self.outbound = StubOutbound(handler)
        self.running_acks = 0
        self.wrapper_src_dir = type("P", (), {"__truediv__": lambda s, n: __import__("pathlib").Path(ROOT) / n})()


async def _notify_running(self):
    self.running_acks += 1


StubRuntime.notify_running = _notify_running

claimed_entry = {"message_id": "m1", "priority": "normal", "status": "claimed", "claimed_by": "R1"}


def substance(content):
    """matter.read serves a text substance inline: its bytes are the JSON document."""
    text = json.dumps(content)
    return {"kind": "inline", "bytes": text, "size": len(text)}


class Wrapper(unittest.IsolatedAsyncioTestCase):
    async def test_bindings_cover_exactly_the_declared_capacities(self):
        runtime = StubRuntime(lambda kw: ("ok", {}))
        registry = RuntimeRegistry(runtime)
        await registry.load_bindings()
        def wrapper_runs(name):          # a DSL capacity (pilot.recover) is executed by the engine: the wrapper binds nothing for it
            with open(os.path.join(CAPACITY_DIR, name + ".json")) as fh:
                return json.load(fh)["brique"]["kind"] != "dsl"
        declared = sorted(f[:-5] for f in os.listdir(CAPACITY_DIR) if f.endswith(".json") and wrapper_runs(f[:-5]))
        self.assertEqual(sorted(name for (_, name) in registry.capacity_bindings), declared)
        for descriptor in declared:
            with open(os.path.join(CAPACITY_DIR, descriptor + ".json")) as fh:
                brique = json.load(fh)["brique"]
            binding = registry.resolve_capacity(brique["rel_ctx"], brique["cap_name"])
            self.assertIsNotNone(binding, descriptor)
            self.assertTrue(inspect.iscoroutinefunction(binding.symbol), descriptor)   # the wrapper awaits it

    async def test_outbound_calls_follow_the_engine_protocol(self):
        state = {"revision": 7}

        def engine(kw):
            self.assertEqual(kw["from_cap"], "mailbox.ack")           # response routing needs the exact capacity name
            self.assertEqual(kw["from_context"], "")
            if kw["to_cap"] == "matter.read":
                self.assertEqual((kw["to_context"], kw["to_type"], kw["params"]["read_mode"]), ("/root/agentic", "matter", "functional|data|brique"))
                return "ok", {"matter_id": kw["params"]["matter_id"], "brique": {"revision": state["revision"]},
                              "functional": {"format": "json"}, "data": substance({"continuity_id": "c1", "entries": [claimed_entry]})}
            if kw["to_cap"] == "matter.write":
                self.assertEqual(kw["params"]["expected_revision"], "7")       # guarded write, exact decimal form
                self.assertNotIn("functional", kw["params"])                       # the content is the substance
                self.assertEqual(json.loads(kw["params"]["data"])["entries"], [])
                return "ok", {"ok": True, "revision": 8}
            self.fail(kw["to_cap"])
        runtime = StubRuntime(engine)
        registry = RuntimeRegistry(runtime)
        await registry.load_bindings()
        result = await registry.resolve_capacity("", "mailbox.ack").symbol({"continuity_id": "c1", "message_id": "m1", "run_id": "R1"})
        self.assertEqual(result, {"continuity_id": "c1", "message_id": "m1", "acknowledged": True})
        self.assertEqual([s["to_cap"] for s in runtime.outbound.sent], ["matter.read", "matter.write"])

    async def test_revision_conflict_error_is_retried_from_a_fresh_read(self):
        calls = {"writes": 0}

        def engine(kw):
            if kw["to_cap"] == "matter.read":
                return "ok", {"brique": {"revision": 3 + calls["writes"]}, "functional": {"format": "json"},
                              "data": substance({"continuity_id": "c1", "entries": [claimed_entry]})}
            calls["writes"] += 1
            if calls["writes"] == 1:
                return "error", {"code": "refused", "message": "revision mismatch", "details": {"reason": "revision_mismatch"}}
            self.assertEqual(kw["params"]["expected_revision"], "4")           # re-read, recomputed, retried
            return "ok", {"revision": 5}
        runtime = StubRuntime(engine)
        registry = RuntimeRegistry(runtime)
        await registry.load_bindings()
        await registry.resolve_capacity("", "mailbox.ack").symbol({"continuity_id": "c1", "message_id": "m1", "run_id": "R1"})
        self.assertEqual(calls["writes"], 2)

    async def test_a_substance_above_the_inline_limit_is_read_through_its_http_lease(self):
        import http.server
        import threading
        import urllib.parse
        served = {"hits": [], "stale_first": True}
        content = {"continuity_id": "c1", "entries": [claimed_entry]}

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self_h):
                query = urllib.parse.parse_qs(urllib.parse.urlparse(self_h.path).query)
                served["hits"].append((urllib.parse.urlparse(self_h.path).path, query.get("tok")))
                if served["stale_first"]:                     # the Matter changed between read and fetch
                    served["stale_first"] = False
                    self_h.send_response(409)
                    self_h.end_headers()
                    return
                body = json.dumps(content).encode()
                self_h.send_response(200)
                self_h.end_headers()
                self_h.wfile.write(body)

            def log_message(self_h, *a):
                pass

        server = http.server.HTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        base = f"http://127.0.0.1:{server.server_address[1]}"
        reads = {"n": 0}
        writes = []

        def engine(kw):
            if kw["to_cap"] == "matter.read":
                reads["n"] += 1
                return "ok", {"matter_id": "mailbox_c1", "brique": {"revision": 10 + reads["n"]}, "functional": {"format": "json"},
                              "data": {"kind": "http", "http": {"kind": "http", "base_url": base, "path": f"/substance/r{reads['n']}", "tok": "secret",
                                                                "size_hint": 900000, "method": "GET"}}}      # nested as the real engine serves it
            writes.append(kw["params"])
            return "ok", {"ok": True, "revision": 99}
        runtime = StubRuntime(engine)
        registry = RuntimeRegistry(runtime)
        await registry.load_bindings()
        result = await registry.resolve_capacity("", "mailbox.ack").symbol({"continuity_id": "c1", "message_id": "m1", "run_id": "R1"})
        self.assertEqual(result["acknowledged"], True)
        self.assertEqual(reads["n"], 2)                                             # the stale lease was replaced by a fresh read
        self.assertEqual([h[0] for h in served["hits"]], ["/substance/r1", "/substance/r2"])
        self.assertEqual({tuple(h[1]) for h in served["hits"]}, {("secret",)})       # the token travels as the tok query parameter
        self.assertEqual(writes[0]["expected_revision"], "12")                      # guarded by the revision of the fresh read
        self.assertEqual(json.loads(writes[0]["data"])["entries"], [])

    async def test_the_runtime_correlator_ignores_running_and_renews_the_wait_budget(self):
        """The engine runtime handles `running` natively: the caller sees only the final response, and each `running`
        renews the timeout, which is an inactivity budget rather than a total duration."""
        from wrapper.correlator import AwaitCorrelator
        correlator = AwaitCorrelator()
        pending = await correlator.register("i1")

        async def feed():
            for _ in range(3):                      # three acknowledgements, 0.15 s apart: longer than the 0.2 s budget in total
                await asyncio.sleep(0.15)
                await correlator.resolve({"kind": "response", "response": {"intention_id": "i1", "status": "running"}})
            await asyncio.sleep(0.05)
            await correlator.resolve({"kind": "response", "response": {"intention_id": "i1", "status": "ok", "payload": {"done": True}}})
        asyncio.ensure_future(feed())
        final = await pending.wait(timeout_s=0.2)
        self.assertEqual(final["response"]["payload"], {"done": True})
        silent = await correlator.register("i2")
        with self.assertRaises(asyncio.TimeoutError):          # silence still times out
            await silent.wait(timeout_s=0.05)

    async def test_long_capacities_acknowledge_themselves_and_a_wake_does_not(self):
        runtime = StubRuntime(lambda kw: ("error", {"code": "not_found", "message": "stop", "details": {}}))
        registry = RuntimeRegistry(runtime)
        await registry.load_bindings()
        for cap, params in (("pilot.activate", {"continuity_id": "c1"}), ("pilot.infer", {"continuity_id": "c1"}),
                            ("continuity.succeed", {"continuity_id": "c1", "summary": "s"}), ("continuity.delete", {"continuity_id": "c1"})):
            before = runtime.running_acks
            try:
                await registry.resolve_capacity("", cap).symbol(params)
            except Exception:
                pass                                         # the stub engine refuses: only the acknowledgement is observed
            self.assertGreater(runtime.running_acks, before, cap)          # `running` came first, the final answer after
        before = runtime.running_acks
        try:
            await registry.resolve_capacity("", "pilot.activate").symbol({"continuity_id": "c1", "wake": True})
        except Exception:
            pass
        self.assertEqual(runtime.running_acks, before)                    # a wake answers at once, with nothing to acknowledge
        for cap in ("mailbox.send", "mailbox.read", "continuity.project"):
            before = runtime.running_acks
            try:
                await registry.resolve_capacity("", cap).symbol({"continuity_id": "c1", "message": {}})
            except Exception:
                pass
            self.assertEqual(runtime.running_acks, before, cap)           # short capacities never acknowledge


if __name__ == "__main__":
    unittest.main()
