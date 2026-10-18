# Copyright 2026 Nicolas Cassan
# Licensed under the Apache License, Version 2.0.

import asyncio
import tempfile
import threading
import time
import unittest
from pathlib import Path
from types import SimpleNamespace

from wrapper.execution import CapacityExecutor
from wrapper.models import CapacityBinding, MatterBinding
from wrapper.runtime import WrapperRuntime
from wrapper.transit import (
    FORMAT_BYTES,
    FORMAT_TEXT,
    MARK_FILE,
    MARK_OBJECT,
    MatterRef,
    ObjectTable,
    Params,
    TransitError,
    TransitService,
    TransitTarget,
    reference_kind,
)


def service(tmp: str, limit: int = 64) -> TransitService:
    return TransitService("w1", Path(tmp) / "tmp" / "wrapper_w1" / "transit", inline_limit=limit)


LOCAL = TransitTarget("/root/demo", "cap.local", "user", same_wrapper=True)
INSTANCE = TransitTarget("/root/other", "cap.remote", "user")


class FakeRegistry:
    def __init__(self) -> None:
        self.capacities: dict = {}
        self.matters: dict = {}

    def resolve_capacity(self, context: str, name: str):
        return self.capacities.get((context, name))

    def resolve_matter(self, context: str, name: str):
        return self.matters.get((context, name))


def fake_runtime(tmp: str, limit: int = 64):
    rt = SimpleNamespace(
        transit=service(tmp, limit),
        registry=FakeRegistry(),
        ctx_id="/root/demo",
        relative_context=lambda ctx: WrapperRuntime.relative_context(SimpleNamespace(ctx_id="/root/demo"), ctx),
    )
    return rt


class ReferenceKindTests(unittest.TestCase):
    def test_only_exact_shapes_are_references(self) -> None:
        self.assertEqual(reference_kind({MARK_FILE: "/x/transit_a.json", "format": "json"}), "file")
        self.assertEqual(reference_kind({MARK_OBJECT: "obj_1", "wrapper": "w1", "type": "t"}), "object")
        self.assertIsNone(reference_kind({MARK_FILE: "/x", "other": 1}))
        self.assertIsNone(reference_kind({MARK_FILE: 3}))
        self.assertIsNone(reference_kind({"name": "x"}))
        self.assertIsNone(reference_kind("@file"))
        self.assertIsNone(reference_kind(None))


class ObjectTableTests(unittest.TestCase):
    def test_post_then_take_once(self) -> None:
        table = ObjectTable("w1")
        obj = {"big": [1, 2, 3]}
        ref = table.post(obj, "view")
        self.assertFalse(ref[MARK_OBJECT].startswith("$"))
        self.assertIs(table.take(ref, "view"), obj)
        with self.assertRaises(TransitError) as ctx:
            table.take(ref)
        self.assertEqual(ctx.exception.reason, "not_found")

    def test_wrong_wrapper_fails_and_keeps_the_entry(self) -> None:
        table = ObjectTable("w1")
        ref = table.post("x" * 10, "t")
        foreign = dict(ref, wrapper="w2")
        with self.assertRaises(TransitError) as ctx:
            table.take(foreign)
        self.assertEqual(ctx.exception.reason, "wrong_wrapper")
        self.assertEqual(len(table), 1)

    def test_type_mismatch_does_not_destroy_the_entry(self) -> None:
        table = ObjectTable("w1")
        ref = table.post([1], "view")
        with self.assertRaises(TransitError) as ctx:
            table.take(ref, "prompt")
        self.assertEqual(ctx.exception.reason, "type_mismatch")
        self.assertEqual(table.take(ref, "view"), [1])

    def test_an_expected_type_rejects_an_untyped_object(self) -> None:
        table = ObjectTable("w1")
        ref = table.post([1])
        with self.assertRaises(TransitError) as ctx:
            table.take(ref, "view")
        self.assertEqual(ctx.exception.reason, "type_mismatch")
        self.assertEqual(table.take(ref), [1])

    def test_discard_is_idempotent(self) -> None:
        table = ObjectTable("w1")
        ref = table.post([1])
        self.assertTrue(table.discard(ref))
        self.assertFalse(table.discard(ref))

    def test_concurrent_posts_and_takes(self) -> None:
        table = ObjectTable("w1")
        refs: list = []
        lock = threading.Lock()

        def work() -> None:
            for i in range(200):
                ref = table.post(i)
                with lock:
                    refs.append(ref)

        threads = [threading.Thread(target=work) for _ in range(8)]
        for t in threads:
            t.start()
        for t in threads:
            t.join()
        self.assertEqual(len(table), 1600)
        self.assertEqual(len({r[MARK_OBJECT] for r in refs}), 1600)
        for ref in refs:
            table.take(ref)
        self.assertEqual(len(table), 0)


class FileTransitTests(unittest.TestCase):
    def test_write_read_consumes_the_file(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            svc = service(tmp)
            ref = svc.files.write({"a": [1, 2]})
            path = Path(ref[MARK_FILE])
            self.assertTrue(path.is_absolute())
            self.assertTrue(path.exists())
            self.assertEqual(ref["format"], "json")
            self.assertEqual(svc.files.read(ref), {"a": [1, 2]})
            self.assertFalse(path.exists())
            with self.assertRaises(TransitError) as ctx:
                svc.files.read(ref)
            self.assertEqual(ctx.exception.reason, "not_found")

    def test_text_and_bytes_formats(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            svc = service(tmp)
            self.assertEqual(svc.files.read(svc.files.write("héllo", FORMAT_TEXT)), "héllo")
            self.assertEqual(svc.files.read(svc.files.write(b"\x00\x01", FORMAT_BYTES)), b"\x00\x01")

    def test_refuses_to_read_or_delete_a_file_that_is_not_a_transit_file(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            svc = service(tmp)
            precious = Path(tmp) / "precious.json"
            precious.write_text("{}")
            with self.assertRaises(TransitError) as ctx:
                svc.files.read({MARK_FILE: str(precious), "format": "json"})
            self.assertEqual(ctx.exception.reason, "invalid_reference")
            self.assertFalse(svc.files.discard({MARK_FILE: str(precious), "format": "json"}))
            self.assertTrue(precious.exists())
            with self.assertRaises(TransitError):
                svc.files.read({MARK_FILE: "relative/transit_a.json"})

    def test_refuses_a_transit_looking_file_outside_the_instance_tree(self) -> None:
        with tempfile.TemporaryDirectory() as tmp, tempfile.TemporaryDirectory() as foreign_tmp:
            svc = service(tmp)
            foreign = Path(foreign_tmp) / "tmp" / "wrapper_w2" / "transit" / "transit_secret.json"
            foreign.parent.mkdir(parents=True)
            foreign.write_text('{"secret":true}')
            with self.assertRaises(TransitError) as ctx:
                svc.files.read({MARK_FILE: str(foreign.resolve()), "format": "json"})
            self.assertEqual(ctx.exception.reason, "invalid_reference")
            self.assertTrue(foreign.exists())

    def test_refuses_a_symbolic_link_transit_file(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            svc = service(tmp)
            target = Path(tmp) / "secret.json"
            target.write_text('{"secret":true}')
            link = svc.files.directory / "transit_link.json"
            link.parent.mkdir(parents=True)
            link.symlink_to(target)
            with self.assertRaises(TransitError) as ctx:
                svc.files.read({MARK_FILE: str(link.absolute()), "format": "json"})
            self.assertEqual(ctx.exception.reason, "invalid_reference")
            self.assertTrue(target.exists())

    def test_refuses_a_symbolic_link_in_the_transit_path(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            svc = service(tmp)
            real = Path(tmp) / "real"
            real.mkdir()
            target = real / "transit_secret.json"
            target.write_text('{"secret":true}')
            wrapper_dir = Path(tmp) / "tmp" / "wrapper_other"
            wrapper_dir.mkdir(parents=True)
            (wrapper_dir / "transit").symlink_to(real, target_is_directory=True)
            linked = wrapper_dir / "transit" / target.name
            with self.assertRaises(TransitError) as ctx:
                svc.files.read({MARK_FILE: str(linked.absolute()), "format": "json"})
            self.assertEqual(ctx.exception.reason, "invalid_reference")
            self.assertTrue(target.exists())

    def test_a_file_that_cannot_be_decoded_is_not_lost_until_released(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            svc = service(tmp)
            ref = svc.files.write(None, "json", encoded=b"not json")
            with self.assertRaises(TransitError) as ctx:
                svc.files.read(ref)
            self.assertEqual(ctx.exception.reason, "unreadable")
            self.assertTrue(Path(ref[MARK_FILE]).exists())
            svc.release(ref)
            self.assertFalse(Path(ref[MARK_FILE]).exists())

    def test_sweep_removes_leftovers_only(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            svc = service(tmp)
            svc.files.write({"x": 1})
            svc.files.write({"y": 2})
            other = svc.files.directory / "keep.txt"
            other.write_text("keep")
            self.assertEqual(svc.sweep(), 2)
            self.assertTrue(other.exists())


class EmitTests(unittest.TestCase):
    def test_scalars_and_references_pass_through(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            svc = service(tmp)
            for value in (None, True, 3, 1.5, "text"):
                self.assertEqual(svc.emit(value, target=LOCAL), value)
                self.assertEqual(svc.emit(value, target=INSTANCE), value)
            ref = svc.objects.post([1])
            self.assertIs(svc.emit(ref, target=INSTANCE), ref)

    def test_local_receiver_gets_an_object_reference(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            svc = service(tmp)
            payload = {"k": list(range(5))}
            ref = svc.emit(payload, target=LOCAL, type_tag="view")
            self.assertEqual(reference_kind(ref), "object")
            self.assertIs(svc.resolve(ref, "view"), payload)

    def test_remote_receiver_small_value_is_inline_large_value_is_a_file(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            svc = service(tmp, limit=64)
            small = {"a": 1}
            self.assertIs(svc.emit(small, target=INSTANCE), small)
            large = {"data": "x" * 500}
            ref = svc.emit(large, target=INSTANCE)
            self.assertEqual(reference_kind(ref), "file")
            self.assertEqual(svc.resolve(ref), large)
            self.assertFalse(Path(ref[MARK_FILE]).exists())

    def test_inline_limit_uses_the_actual_ascii_escaped_wire_size(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            svc = service(tmp, limit=64)
            ref = svc.emit({"text": "é" * 20}, target=INSTANCE)
            self.assertEqual(reference_kind(ref), "file")
            self.assertEqual(svc.resolve(ref), {"text": "é" * 20})

    def test_large_text_is_transit_even_though_it_is_a_scalar(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            svc = service(tmp, limit=64)
            value = "x" * 500
            ref = svc.emit(value, target=INSTANCE)
            self.assertEqual(reference_kind(ref), "file")
            self.assertEqual(ref["format"], "text")
            self.assertEqual(svc.resolve(ref), value)

    def test_bytes_always_travel_as_a_file_to_a_remote_receiver(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            svc = service(tmp)
            ref = svc.emit(b"abc", target=INSTANCE)
            self.assertEqual(ref["format"], FORMAT_BYTES)
            self.assertEqual(svc.resolve(ref), b"abc")

    def test_non_serializable_value_to_a_remote_receiver_is_a_typed_error(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            svc = service(tmp)
            with self.assertRaises(TransitError) as ctx:
                svc.emit({"x": object()}, target=INSTANCE)
            self.assertEqual(ctx.exception.reason, "invalid_reference")

    def test_sync_emit_refuses_external_bulk_instead_of_leaking_a_file_path(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            svc = service(tmp, limit=1)
            external = TransitTarget("@ext_peer:/root", "cap.remote", "user")
            with self.assertRaises(TransitError):
                svc.emit(b"abc", target=external)


class ParamsTests(unittest.IsolatedAsyncioTestCase):
    async def test_value_resolves_every_form_and_memoizes(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            rt = fake_runtime(tmp)
            obj = {"live": True}
            file_ref = rt.transit.files.write({"from": "file"})
            params = Params(
                {"literal": 7, "obj": rt.transit.objects.post(obj, "t"), "file": file_ref}, rt
            )
            self.assertEqual(await params.value("literal"), 7)
            self.assertIs(await params.value("obj", "t"), obj)
            self.assertIs(await params.value("obj", "t"), obj)  # memoized: no second take
            self.assertEqual(await params.value("file"), {"from": "file"})
            self.assertEqual(await params.value("file"), {"from": "file"})
            with self.assertRaises(TransitError) as ctx:
                await params.value("absent")
            self.assertEqual(ctx.exception.reason, "not_found")

    async def test_params_stays_a_plain_dict_for_existing_code(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            rt = fake_runtime(tmp)
            params = Params({"a": 1}, rt)
            self.assertIsInstance(params, dict)
            self.assertEqual(params.get("a"), 1)
            self.assertEqual(dict(params), {"a": 1})

    async def test_unconsumed_references_are_released_at_the_end_of_the_call(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            rt = fake_runtime(tmp)
            unused_file = rt.transit.files.write({"unused": 1})
            unused_obj = rt.transit.objects.post([1])
            used_obj = rt.transit.objects.post([2])
            params = Params({"f": unused_file, "o": unused_obj, "u": used_obj}, rt)
            await params.value("u")
            params.release_unconsumed()
            self.assertFalse(Path(unused_file[MARK_FILE]).exists())
            self.assertEqual(len(rt.transit.objects), 0)

    async def test_a_matter_param_comes_back_as_an_unread_matter_ref(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            rt = fake_runtime(tmp)
            read = []
            # even a matter bound in this wrapper is not read by get: the capacity does it explicitly
            rt.registry.matters[("", "state")] = MatterBinding("", "state", read_symbol=lambda: read.append(1))
            entry = {"ref": "/root/demo/state", "context": "/root/demo", "id": "state", "mode": "read"}
            params = Params({"@matter": {"st": entry}}, rt)
            ref = await params.value("st")
            self.assertIsInstance(ref, MatterRef)
            self.assertEqual((ref.context, ref.id, ref.mode), ("/root/demo", "state", "read"))
            self.assertEqual(read, [])
            self.assertIs(await params.value("st"), ref)  # memoized

    async def test_a_small_plain_object_param_is_returned_as_it_is(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            rt = fake_runtime(tmp)
            small = {"a": [1, 2], "b": "x"}
            params = Params({"cfg": small}, rt)
            self.assertIs(await params.value("cfg"), small)

    async def test_file_resolution_does_not_block_the_event_loop(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            rt = fake_runtime(tmp)
            ref = rt.transit.files.write({"large": True})
            original = rt.transit.resolve

            def slow_resolve(raw, expect_type=None):
                time.sleep(0.05)
                return original(raw, expect_type)

            rt.transit.resolve = slow_resolve
            params = Params({"file": ref}, rt)
            resolving = asyncio.create_task(params.value("file"))
            await asyncio.sleep(0.01)
            self.assertFalse(resolving.done())
            self.assertEqual(await resolving, {"large": True})

    async def test_cancelled_async_emission_releases_the_file_created_by_its_worker(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            svc = service(tmp, limit=1)
            original = svc.files.write

            def slow_write(value, fmt="json", *, encoded=None):
                time.sleep(0.05)
                return original(value, fmt, encoded=encoded)

            svc.files.write = slow_write
            emitting = asyncio.create_task(svc.emit_async({"large": "value"}, target=INSTANCE))
            await asyncio.sleep(0.01)
            emitting.cancel()
            with self.assertRaises(asyncio.CancelledError):
                await emitting
            self.assertEqual(list(svc.files.directory.glob("transit_*")), [])


class ExecutorIntegrationTests(unittest.IsolatedAsyncioTestCase):
    async def test_capacity_receives_params_and_unconsumed_transit_is_released(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            rt = fake_runtime(tmp)
            seen = {}

            async def capacity(params):
                seen["type"] = type(params)
                view = await params.value("view")
                return {"echo": view["n"]}

            rt.registry.capacities[("", "cap.echo")] = CapacityBinding("", "cap.echo", capacity)
            view_ref = rt.transit.objects.post({"n": 42}, "view")
            stray = rt.transit.files.write({"stray": True})
            intention = {"params": {"view": view_ref, "stray": stray}}
            result = await CapacityExecutor(rt).execute_capacity("", "cap.echo", intention)
            self.assertEqual(result, {"echo": 42})
            self.assertIs(seen["type"], Params)
            self.assertEqual(len(rt.transit.objects), 0)
            self.assertFalse(Path(stray[MARK_FILE]).exists())

    async def test_unconsumed_references_are_released_even_when_the_capacity_fails(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            rt = fake_runtime(tmp)

            async def capacity(params):
                raise RuntimeError("boom")

            rt.registry.capacities[("", "cap.fail")] = CapacityBinding("", "cap.fail", capacity)
            stray = rt.transit.files.write({"stray": True})
            with self.assertRaises(RuntimeError):
                await CapacityExecutor(rt).execute_capacity("", "cap.fail", {"params": {"x": stray}})
            self.assertFalse(Path(stray[MARK_FILE]).exists())


class RuntimeLocalityTests(unittest.TestCase):
    def make(self) -> SimpleNamespace:
        registry = FakeRegistry()
        registry.capacities[("", "a.cap")] = object()
        registry.capacities[("child", "b.cap")] = object()
        return SimpleNamespace(ctx_id="/root/demo", registry=registry, relative_context=None)

    def test_relative_context(self) -> None:
        rt = SimpleNamespace(ctx_id="/root/demo")
        self.assertEqual(WrapperRuntime.relative_context(rt, "/root/demo"), "")
        self.assertEqual(WrapperRuntime.relative_context(rt, "/root/demo/child"), "child")
        self.assertIsNone(WrapperRuntime.relative_context(rt, "/root/other"))
        self.assertIsNone(WrapperRuntime.relative_context(rt, "/root/demonstration"))
        self.assertIsNone(WrapperRuntime.relative_context(rt, ""))

    def test_context_tree_dir_matches_the_logical_context_root(self) -> None:
        rt = SimpleNamespace(ctx_dir=Path("/instance/root/demo/child"), ctx_id="/root/demo/child")
        self.assertEqual(WrapperRuntime.context_tree_dir(rt), Path("/instance/root"))

    def test_local_caller_requires_a_bound_capacity(self) -> None:
        rt = self.make()
        rt.relative_context = lambda c: WrapperRuntime.relative_context(rt, c)

        def local(intention):
            return WrapperRuntime.is_local_caller(rt, intention)

        self.assertTrue(local({"from": {"context": "/root/demo", "cap": "a.cap"}}))
        self.assertTrue(local({"from": {"context": "/root/demo/child", "cap": "b.cap"}}))
        # a DSL capacity under the boundary context is not bound here
        self.assertFalse(local({"from": {"context": "/root/demo/child", "cap": "plan.dsl"}}))
        self.assertFalse(local({"from": {"context": "/root/other", "cap": "a.cap"}}))
        self.assertFalse(local({"from": {"context": "/root/demo", "cap": ""}}))
        self.assertFalse(local({}))


if __name__ == "__main__":
    unittest.main()
