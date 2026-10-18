# Copyright 2026 Nicolas Cassan
# Licensed under the Apache License, Version 2.0.

import asyncio
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace

from wrapper.calls import CapacityCallError, CapacityCaller
from wrapper.execution import CapacityExecutor, current_intention
from wrapper.models import CapacityBinding
from wrapper.router import MessageRouter
from wrapper.runtime import WrapperRuntime
from wrapper.transit import MARK_FILE, MARK_MATTER, MARK_OBJECT, MatterRef, Params, TransitError, TransitService, reference_kind


class FakeRegistry:
    def __init__(self) -> None:
        self.capacities: dict = {}

    def bind(self, context: str, name: str, symbol) -> None:
        self.capacities[(context, name)] = CapacityBinding(context, name, symbol)

    def resolve_capacity(self, context: str, name: str):
        return self.capacities.get((context, name))


class FakeOutbound:
    """Scripted engine: each item is a response body or an exception to raise."""

    def __init__(self, *script) -> None:
        self.script = list(script)
        self.waited: list = []
        self.fired: list = []

    async def emit_and_wait(self, **kwargs):
        self.waited.append(kwargs)
        item = self.script.pop(0)
        if isinstance(item, BaseException):
            raise item
        return {"response": item}

    async def emit_intention(self, **kwargs):
        self.fired.append(kwargs)
        return "id"


def ok(payload: dict) -> dict:
    return {"status": "ok", "payload": payload}


def build(tmp: str, *script, limit: int = 64) -> SimpleNamespace:
    rt = SimpleNamespace(
        ctx_id="/root/demo",
        transport=None,
        transit=TransitService("w1", Path(tmp) / "tmp" / "wrapper_w1" / "transit", inline_limit=limit),
        registry=FakeRegistry(),
        outbound=FakeOutbound(*script),
    )
    rt.relative_context = lambda c: WrapperRuntime.relative_context(rt, c)
    rt.target_context = lambda i: str(i.get("to", {}).get("context", "")).strip("/")
    rt.is_local_caller = lambda i: WrapperRuntime.is_local_caller(rt, i)
    rt.notify_running = lambda: WrapperRuntime.notify_running(rt)
    rt.capacity_executor = CapacityExecutor(rt)
    rt.calls = CapacityCaller(rt)
    rt.transit.runtime = rt
    return rt


class CallTestCase(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self) -> None:
        # contextvars set here are seen by the test: asyncSetUp runs in the same task
        self.token = current_intention.set(
            {
                "intention_id": "outer-1",
                "to": {"cap": "cap.outer", "context": ""},
                "correlation": {"root_intention_id": "root-1", "parent_intention_id": ""},
            }
        )

    async def asyncTearDown(self) -> None:
        current_intention.reset(self.token)


class DirectCallTests(CallTestCase):
    async def test_objects_are_passed_untouched_in_both_directions(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            rt = build(tmp)
            seen = {}

            async def cap(params):
                seen["view"] = await params.value("view")
                return {"same": seen["view"], "n": len(seen["view"]["items"])}

            rt.registry.bind("", "cap.sum", cap)
            view = {"items": [1, 2, 3]}
            result = await rt.calls.call(to_context="", to_cap="cap.sum", params={"view": view}, direct=True)
            self.assertIs(seen["view"], view)
            self.assertIs(await result.value("same"), view)
            self.assertEqual(await result.value("n"), 3)
            self.assertEqual(len(rt.transit.objects), 0)
            self.assertEqual(rt.outbound.waited, [])

    async def test_a_direct_call_needs_a_capacity_bound_here(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            rt = build(tmp)
            with self.assertRaises(TransitError) as ctx:
                await rt.calls.call(to_context="", to_cap="cap.absent", direct=True)
            self.assertEqual(ctx.exception.reason, "unsupported")

    async def test_exceptions_of_the_capacity_propagate_natively(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            rt = build(tmp)

            async def cap(params):
                raise ValueError("boom")

            rt.registry.bind("", "cap.fail", cap)
            with self.assertRaises(ValueError):
                await rt.calls.call(to_context="", to_cap="cap.fail", direct=True)

    async def test_an_unknown_sub_context_target_is_resolved_relative_to_the_root(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            rt = build(tmp)

            async def cap(params):
                return {"where": "child"}

            rt.registry.bind("child", "cap.deep", cap)
            result = await rt.calls.call(to_context="/root/demo/child", to_cap="cap.deep", direct=True)
            self.assertEqual(await result.value("where"), "child")

    async def test_the_budget_is_an_inactivity_budget(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            rt = build(tmp)
            cancelled = []

            async def slow(params):
                try:
                    await asyncio.sleep(0.4)
                except asyncio.CancelledError:
                    cancelled.append(True)
                    raise

            async def alive(params):
                for _ in range(6):
                    await asyncio.sleep(0.03)
                    await rt.notify_running()
                return {"done": True}

            rt.registry.bind("", "cap.slow", slow)
            rt.registry.bind("", "cap.alive", alive)
            with self.assertRaises(asyncio.TimeoutError):
                await rt.calls.call(to_context="", to_cap="cap.slow", direct=True, timeout_s=0.05)
            await asyncio.sleep(0)
            self.assertEqual(cancelled, [True])
            # six checkpoints of 30 ms outlast a 50 ms budget only because each one renews it
            result = await rt.calls.call(to_context="", to_cap="cap.alive", direct=True, timeout_s=0.05)
            self.assertTrue(await result.value("done"))

    async def test_timeout_waits_for_callee_cleanup_before_releasing_params(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            rt = build(tmp)
            cleanup = []

            async def slow(params):
                try:
                    await asyncio.sleep(1)
                finally:
                    cleanup.append(await params.value("view"))

            rt.registry.bind("", "cap.slow", slow)
            with self.assertRaises(asyncio.TimeoutError):
                await rt.calls.call(
                    to_context="", to_cap="cap.slow", params={"view": {"k": 1}}, direct=True, timeout_s=0.01
                )
            self.assertEqual(cleanup, [{"k": 1}])

    async def test_unconsumed_references_are_released_after_a_direct_call(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            rt = build(tmp)

            async def ignores(params):
                return {}

            rt.registry.bind("", "cap.ignores", ignores)
            ref = rt.transit.objects.post([1])
            await rt.calls.call(to_context="", to_cap="cap.ignores", params={"o": ref}, direct=True)
            self.assertEqual(len(rt.transit.objects), 0)


class EngineCallTests(CallTestCase):
    async def test_address_alone_selects_a_transient_matter_for_external_bulk(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            rt = build(tmp, ok({}), limit=64)

            class Matters:
                def __init__(self):
                    self.created = []

                async def create(self, encoded, fmt, target):
                    self.created.append((encoded, fmt, target))
                    return {
                        MARK_MATTER: "transit-1",
                        "context": "/root/work",
                        "owner": target.context,
                        "format": fmt,
                        "consume": True,
                        "revision": 2,
                        "size": len(encoded),
                    }

                async def discard(self, _ref):
                    return None

            rt.transit_matters = Matters()
            await rt.calls.call(
                to={"context": "@ext_peer:/root/work", "cap": "cap.remote", "type": "user"},
                params={"document": {"data": "x" * 500}},
            )
            sent = rt.outbound.waited[0]
            self.assertEqual(reference_kind(sent["params"]["document"]), "matter")
            self.assertEqual(sent["to_context"], "@ext_peer:/root/work")
            self.assertEqual(rt.transit_matters.created[0][2].cap, "cap.remote")

    async def test_a_local_target_receives_objects_as_references(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            rt = build(tmp, ok({"x": 1}))

            async def cap(params):
                return {}

            rt.registry.bind("", "cap.local", cap)
            view = {"a": [1]}
            result = await rt.calls.call(
                to_context="", to_cap="cap.local", params={"view": view, "n": 3}, type_tags={"view": "view"}
            )
            sent = rt.outbound.waited[0]
            self.assertEqual(reference_kind(sent["params"]["view"]), "object")
            self.assertEqual(sent["params"]["view"]["type"], "view")
            self.assertEqual(sent["params"]["n"], 3)
            self.assertEqual((sent["from_context"], sent["from_cap"]), ("", "cap.outer"))
            self.assertEqual(
                sent["correlation"],
                {"root_intention_id": "root-1", "parent_intention_id": "outer-1"},
            )
            self.assertEqual(await result.value("x"), 1)
            self.assertIs(rt.transit.objects.take(sent["params"]["view"]), view)

    async def test_a_remote_target_receives_a_value_or_a_file_by_size(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            rt = build(tmp, ok({}), limit=64)
            small, large = {"a": 1}, {"data": "x" * 500}
            await rt.calls.call(
                to_context="/root/other", to_cap="cap.remote", params={"small": small, "large": large}
            )
            sent = rt.outbound.waited[0]["params"]
            self.assertIs(sent["small"], small)
            self.assertEqual(reference_kind(sent["large"]), "file")
            self.assertTrue(Path(sent["large"][MARK_FILE]).exists())
            self.assertEqual(rt.transit.resolve(sent["large"]), large)

    async def test_a_matter_ref_uses_the_intention_matters_field(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            rt = build(tmp, ok({}))
            ref = MatterRef({"context": "/root/demo", "id": "state", "mode": "read"})
            await rt.calls.call(to_context="/root/other", to_cap="cap.remote", params={"source": ref})
            sent = rt.outbound.waited[0]
            self.assertEqual(sent["params"], {})
            self.assertEqual(
                sent["matters"],
                [{"context": "/root/demo", "id": "state", "repr": "source", "mode": "read"}],
            )

    async def test_the_producer_releases_when_the_callee_is_not_found(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            rt = build(tmp, {"status": "error", "error": {"code": "not_found", "message": "no cap", "details": {"reason": "unknown_cap"}}})
            large = {"data": "x" * 500}
            with self.assertRaises(CapacityCallError) as ctx:
                await rt.calls.call(to_context="/root/other", to_cap="cap.missing", params={"large": large})
            self.assertEqual(ctx.exception.code, "not_found")
            self.assertEqual(ctx.exception.details, {"reason": "unknown_cap"})
            sent_file = Path(rt.outbound.waited[0]["params"]["large"][MARK_FILE])
            self.assertFalse(sent_file.exists())

    async def test_an_object_posted_for_a_missing_local_looking_target_is_released(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            rt = build(tmp, {"status": "error", "error": {"code": "not_found", "message": "gone"}})

            async def cap(params):
                return {}

            rt.registry.bind("", "cap.local", cap)
            with self.assertRaises(CapacityCallError):
                await rt.calls.call(to_context="", to_cap="cap.local", params={"view": {"k": 1}})
            self.assertEqual(len(rt.transit.objects), 0)

    async def test_a_timeout_does_not_release_what_the_callee_may_still_take(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            rt = build(tmp, asyncio.TimeoutError())

            async def cap(params):
                return {}

            rt.registry.bind("", "cap.local", cap)
            with self.assertRaises(asyncio.TimeoutError):
                await rt.calls.call(to_context="", to_cap="cap.local", params={"view": {"k": 1}}, timeout_s=0.01)
            self.assertEqual(len(rt.transit.objects), 1)

    async def test_another_failure_releases(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            rt = build(tmp, RuntimeError("socket closed"))

            async def cap(params):
                return {}

            rt.registry.bind("", "cap.local", cap)
            with self.assertRaises(RuntimeError):
                await rt.calls.call(to_context="", to_cap="cap.local", params={"view": {"k": 1}})
            self.assertEqual(len(rt.transit.objects), 0)

    async def test_without_waiting_nothing_is_awaited_and_nothing_is_released(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            rt = build(tmp)

            async def cap(params):
                return {}

            rt.registry.bind("", "cap.local", cap)
            out = await rt.calls.call(
                to_context="", to_cap="cap.local", params={"view": {"k": 1}}, await_response=False
            )
            self.assertIsNone(out)
            self.assertEqual(len(rt.outbound.fired), 1)
            self.assertFalse(rt.outbound.fired[0]["await_response"])
            self.assertEqual(len(rt.transit.objects), 1)

    async def test_awaiting_a_response_outside_a_capacity_is_refused_without_leaking(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            rt = build(tmp)
            current_intention.reset(self.token)
            try:
                with self.assertRaises(TransitError):
                    await rt.calls.call(to_context="/root/other", to_cap="c", params={"large": {"d": "x" * 500}})
            finally:
                self.token = current_intention.set({"to": {"cap": "cap.outer", "context": ""}})
            self.assertEqual(list(rt.transit.files.directory.glob("*")) if rt.transit.files.directory.exists() else [], [])

    async def test_a_result_reference_nobody_consumed_is_released_when_the_capacity_ends(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            rt = build(tmp)
            produced = rt.transit.files.write({"big": True})
            rt.outbound.script.append(ok({"blob": produced}))

            async def outer(params):
                await rt.calls.call(to_context="/root/other", to_cap="cap.remote")  # result ignored
                return {}

            rt.registry.bind("", "cap.outer", outer)
            await rt.capacity_executor.execute_capacity("", "cap.outer", {"to": {"cap": "cap.outer", "context": ""}})
            self.assertFalse(Path(produced[MARK_FILE]).exists())


class ReturnEmissionTests(CallTestCase):
    async def run_capacity(self, rt, caller: dict, result: dict) -> dict:
        async def cap(params):
            return result

        rt.registry.bind("", "cap.callee", cap)
        intention = {"to": {"cap": "cap.callee", "context": ""}, "from": caller, "params": {}}
        return await rt.capacity_executor.execute_capacity("", "cap.callee", intention)

    async def test_returns_are_untouched_unless_the_wrapper_opts_in(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            rt = build(tmp)
            payload = {"data": {"k": [1, 2]}}
            out = await self.run_capacity(rt, {"context": "/root/demo", "cap": "cap.outer"}, payload)
            self.assertIs(out, payload)
            self.assertFalse(rt.transit.emit_returns)

    async def test_a_local_caller_receives_objects(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            rt = build(tmp)
            rt.transit.emit_returns = True

            async def outer(params):
                return {}

            rt.registry.bind("", "cap.outer", outer)
            data = {"k": [1, 2]}
            out = await self.run_capacity(
                rt, {"context": "/root/demo", "cap": "cap.outer"}, {"data": data, "count": 2, "ok": True}
            )
            self.assertEqual(reference_kind(out["data"]), "object")
            self.assertEqual((out["count"], out["ok"]), (2, True))
            self.assertIs(rt.transit.resolve(out["data"]), data)

    async def test_a_dsl_or_remote_caller_receives_values_or_files(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            rt = build(tmp, limit=64)
            rt.transit.emit_returns = True
            big = {"data": "x" * 500}
            out = await self.run_capacity(
                rt, {"context": "/root/demo/child", "cap": "plan.dsl"}, {"small": {"a": 1}, "big": big}
            )
            self.assertEqual(out["small"], {"a": 1})
            self.assertEqual(reference_kind(out["big"]), "file")
            self.assertEqual(rt.transit.resolve(out["big"]), big)


class RouterErrorTests(unittest.IsolatedAsyncioTestCase):
    async def test_typed_errors_travel_up_unchanged(self) -> None:
        sent: list = []

        class Transport:
            async def send(self, message):
                sent.append(message)

        class Executor:
            def __init__(self, exc):
                self.exc = exc

            async def execute_capacity(self, *args):
                raise self.exc

        def router_for(exc):
            rt = SimpleNamespace(
                transport=Transport(),
                capacity_executor=Executor(exc),
                target_context=lambda i: "",
                response_from_context=lambda i: "",
                wrapper_identity=lambda: {"id": "w1", "kind": "wrapper"},
            )
            return MessageRouter(rt)

        intention = {"intention_id": "i1", "to": {"cap": "c", "context": "", "type": "user"}, "from": {}}
        await router_for(CapacityCallError("refused", "no", {"reason": "busy"})).route_capacity(intention, "c")
        await router_for(TransitError("not_found", "object gone")).route_capacity(intention, "c")
        await router_for(TransitError("type_mismatch", "bad type")).route_capacity(intention, "c")
        errors = [m["response"]["error"] for m in sent]
        self.assertEqual([(e["code"], e["details"]) for e in errors], [
            ("refused", {"reason": "busy"}),
            ("not_found", {"reason": "not_found"}),
            ("invalid", {"reason": "type_mismatch"}),
        ])


if __name__ == "__main__":
    unittest.main()
