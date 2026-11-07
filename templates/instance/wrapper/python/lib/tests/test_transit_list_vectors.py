# Copyright 2026 Nicolas Cassan
# Licensed under the Apache License, Version 2.0.

"""The shared corpus of list parameters (wrapper/vectors/transit_lists.json): this runtime must agree with the Go and C++ ones."""

import json
import os
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace

from test_calls import CallTestCase, build
from wrapper.execution import CapacityExecutor
from wrapper.transit import MARK_MATTER, MatterRef, Params, TransitError, TransitService, TransitTarget, reference_kind

HERE = Path(__file__).resolve().parent
PATH = Path(os.environ.get("BRIQUE_TRANSIT_LIST_VECTORS", HERE.parent.parent.parent / "vectors" / "transit_lists.json"))
CORPUS = json.loads(PATH.read_text(encoding="utf-8"))
LIMIT = CORPUS["inline_limit_bytes"]
TARGETS = {
    "local": TransitTarget("/root/demo", "cap.local", "user", same_wrapper=True),
    "instance": TransitTarget("/root/other", "cap.remote", "user"),
    "external": TransitTarget("@ext_peer:/root/work", "cap.consume", "user"),
}


def make(spec):
    """A value from its spec (see the corpus)."""
    if isinstance(spec, dict):
        if "text" in spec:
            return "x" * spec["text"]
        if "object" in spec:
            return {"k": "x" * spec["object"]}
        if "list" in spec:
            return [make(s) for s in spec["list"]]
        if "repeat" in spec:
            return [make(spec["repeat"]) for _ in range(spec["times"])]
    if isinstance(spec, list):
        return [make(s) for s in spec]
    return spec


class FakeMatters:
    """Stands for the destination of a transient Matter: it only counts what it was given."""

    def __init__(self):
        self.created, self.discarded = [], []

    async def create(self, encoded, fmt, target):
        self.created.append(len(encoded))
        return {MARK_MATTER: f"t{len(self.created)}", "context": "/root/work", "owner": target.context, "format": fmt,
                "consume": True, "revision": 1, "size": len(encoded)}

    async def discard(self, ref):
        self.discarded.append(ref[MARK_MATTER])


def form(result):
    kind = reference_kind(result)
    if kind is not None:
        return {"form": kind}
    if isinstance(result, list) and any(reference_kind(e) is not None for e in result):
        return {"form": "elements", "elements": [reference_kind(e) or "inline" for e in result]}
    return {"form": "inline"}


class LocalityVectors(CallTestCase):
    async def test_every_case(self):
        with tempfile.TemporaryDirectory() as tmp:
            rt = build(tmp)
            rt.ctx_id = CORPUS["locality"]["root"]
            for context, cap in CORPUS["locality"]["bound"]:
                rt.registry.bind(context, cap, lambda params: {})
            for case in CORPUS["locality"]["cases"]:
                with self.subTest(case["context"] + " " + case["cap"]):
                    self.assertEqual(rt.calls.is_local(case["context"], case["cap"]), case["local"])


class EmitVectors(unittest.IsolatedAsyncioTestCase):
    async def test_every_case(self):
        for case in CORPUS["emit"]:
            with self.subTest(case["name"]), tempfile.TemporaryDirectory() as tmp:
                svc = TransitService("w1", Path(tmp) / "tmp" / "wrapper_w1" / "transit", inline_limit=LIMIT)
                svc.runtime = SimpleNamespace(transit_matters=FakeMatters())
                result = await svc.emit_async(make(case["list"]), target=TARGETS[case["target"]])
                self.assertEqual(form(result), case["expect"])


class ReceiveVectors(unittest.IsolatedAsyncioTestCase):
    async def test_every_case(self):
        for case in CORPUS["receive"]:
            with self.subTest(case["name"]), tempfile.TemporaryDirectory() as tmp:
                svc = TransitService("w1", Path(tmp) / "tmp" / "wrapper_w1" / "transit", inline_limit=LIMIT)
                rt = SimpleNamespace(transit=svc, ctx_id="/root/demo", transit_matters=FakeMatters())
                raw = []
                for element in case["elements"]:
                    if "plain" in element:
                        raw.append(element["plain"])
                    elif "file" in element:
                        raw.append(svc.files.write(element["file"]))
                    else:
                        raw.append(svc.objects.post(element["object"], ""))
                params = Params({"parts": raw}, rt)
                self.assertEqual(await params.value("parts"), case["expect"])
                self.assertEqual(await params.value("parts"), case["expect"])        # memoized
                await params.release_unconsumed_async()
                left = list((Path(tmp) / "tmp" / "wrapper_w1" / "transit").glob("*"))
                self.assertEqual((len(left), len(svc.objects)), (0, 0))
                if "consumed" in case:
                    wanted = sum(1 for e in case["elements"] if "file" in e), sum(1 for e in case["elements"] if "object" in e)
                    self.assertEqual((case["consumed"]["files"], case["consumed"]["objects"]), wanted)


def ref_text(ref):
    return f"{ref.context}/{ref.id}"


class IncomingMatterVectors(unittest.IsolatedAsyncioTestCase):
    async def test_every_case(self):
        for case in CORPUS["incoming_matters"]:
            with self.subTest(case["name"]):
                rt = SimpleNamespace(ctx_id="/root/demo")
                grouped = CapacityExecutor(rt)._normalize_matters(case["matters"])
                params = Params({"@matter": grouped}, rt)
                for name, wanted in case["expect"].items():
                    self.assertEqual([ref_text(r) for r in params.matters(name)], wanted)
                for name, wanted in case.get("single", {}).items():
                    if wanted == "type_mismatch":
                        with self.assertRaises(TransitError) as ctx:
                            params.matter(name)
                        self.assertEqual(ctx.exception.reason, "type_mismatch")
                    else:
                        self.assertEqual(ref_text(params.matter(name)), wanted)
                for name in case.get("absent", []):
                    with self.assertRaises(TransitError) as ctx:
                        params.matters(name)
                    self.assertEqual(ctx.exception.reason, "not_found")


class OutgoingMatterVectors(CallTestCase):
    @staticmethod
    def convert(value):
        if isinstance(value, dict) and "matter" in value:
            context, _, matter_id = value["matter"].rpartition("/")
            return MatterRef({"context": context, "id": matter_id})
        if isinstance(value, list):
            return [OutgoingMatterVectors.convert(v) for v in value]
        return value

    async def test_every_case(self):
        for case in CORPUS["outgoing_matters"]:
            with self.subTest(case["name"]), tempfile.TemporaryDirectory() as tmp:
                rt = build(tmp, {"status": "ok", "payload": {}})
                params = {k: self.convert(v) for k, v in case["params"].items()}
                if "error" in case["expect"]:
                    with self.assertRaises(TransitError) as ctx:
                        await rt.calls.call(to_context="/root/other", to_cap="cap.remote", params=params)
                    self.assertEqual(ctx.exception.reason, case["expect"]["error"])
                    self.assertEqual(rt.outbound.waited, [])
                    continue
                await rt.calls.call(to_context="/root/other", to_cap="cap.remote", params=params)
                sent = rt.outbound.waited[0]
                grouped = {}
                for m in sent["matters"]:
                    grouped.setdefault(m["repr"], []).append(f'{m["context"]}/{m["id"]}')
                self.assertEqual(grouped, case["expect"]["matters"])
                self.assertEqual(sorted(sent["params"]), sorted(case["expect"]["params"]))


if __name__ == "__main__":
    unittest.main()
