# Copyright 2026 Nicolas Cassan
# Licensed under the Apache License, Version 2.0.

import asyncio
import base64
import json
import unittest
from types import SimpleNamespace

from wrapper.execution import current_intention
from wrapper.transit import MARK_MATTER, MatterRef, Params, TransitError, TransitTarget, reference_kind
from wrapper.transit_matter import TransientMatterClient


def ok(payload):
    return {"response": {"status": "ok", "payload": payload}}


class FakeOutbound:
    def __init__(self, *responses):
        self.responses = list(responses)
        self.calls = []

    async def emit_and_wait(self, **kwargs):
        self.calls.append(kwargs)
        return self.responses.pop(0)


def runtime(*responses):
    rt = SimpleNamespace(outbound=FakeOutbound(*responses), logs=[])
    rt.target_context = lambda _intention: ""
    rt.log_event = lambda name, **fields: rt.logs.append((name, fields))
    rt.transit_matters = TransientMatterClient(rt)
    return rt


class TransientMatterTests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.token = current_intention.set(
            {
                "intention_id": "outer-1",
                "to": {"context": "", "cap": "cap.outer", "type": "user"},
                "correlation": {"root_intention_id": "root-1"},
            }
        )

    async def asyncTearDown(self):
        current_intention.reset(self.token)

    async def test_create_puts_bytes_in_destination_and_returns_consumable_reference(self):
        rt = runtime(
            ok({"matter_id": "transit-1", "revision": 1}),
            ok({"http_data": {"base_url": "http://remote", "path": "/substance/r", "tok": "t"}}),
        )
        client = rt.transit_matters
        client._http_put = lambda _handle, data: {"ok": True, "revision": 2, "seen": data}
        encoded = json.dumps({"large": [1, 2]}).encode()
        ref = await client.create(
            encoded,
            "json",
            TransitTarget("@ext_peer:/root/work", "cap.consume", "user"),
        )
        self.assertEqual(reference_kind(ref), "matter")
        self.assertEqual(
            ref,
            {
                MARK_MATTER: "transit-1",
                "context": "/root/work",
                "owner": "@ext_peer:/root/work",
                "format": "json",
                "consume": True,
                "revision": 2,
                "size": len(encoded),
            },
        )
        self.assertEqual([call["to_cap"] for call in rt.outbound.calls], ["matter.create", "matter.write"])
        self.assertTrue(rt.outbound.calls[1]["params"]["http_data"])

    async def test_get_reads_decodes_deletes_and_memoizes(self):
        encoded = json.dumps({"large": [1, 2]}, separators=(",", ":")).encode()
        data = {
            "kind": "inline",
            "bytes": base64.b64encode(encoded).decode(),
            "encoding": "base64",
        }
        rt = runtime(
            ok({"brique": {"revision": 2, "transient": True}, "data": data}),
            ok({"ok": True, "matter_id": "transit-1"}),
        )
        ref = {
            MARK_MATTER: "transit-1",
            "context": "/root/work",
            "owner": "@ext_source:/root/work",
            "format": "json",
            "consume": True,
            "revision": 2,
            "size": len(encoded),
        }
        params = Params({"document": ref}, rt)
        first = await params.value("document")
        second = await params.value("document")
        self.assertIs(first, second)
        self.assertEqual(first, {"large": [1, 2]})
        self.assertEqual([call["to_cap"] for call in rt.outbound.calls], ["matter.read", "matter.delete"])
        self.assertEqual(rt.outbound.calls[1]["params"]["expected_revision"], 2)
        self.assertEqual(rt.outbound.calls[1]["params"]["expected_byte_size"], len(encoded))

    async def test_decode_failure_does_not_delete_the_matter(self):
        encoded = b"not-json"
        rt = runtime(
            ok(
                {
                    "brique": {"revision": 2, "transient": True},
                    "data": {
                        "kind": "inline",
                        "bytes": base64.b64encode(encoded).decode(),
                        "encoding": "base64",
                    },
                }
            )
        )
        ref = {
            MARK_MATTER: "transit-1",
            "context": "/root",
            "owner": "@ext_source",
            "format": "json",
            "consume": True,
            "revision": 2,
            "size": len(encoded),
        }
        with self.assertRaises(TransitError):
            await Params({"document": ref}, rt).value("document")
        self.assertEqual([call["to_cap"] for call in rt.outbound.calls], ["matter.read"])

    async def test_unread_parameter_is_guarded_deleted_at_end_of_call(self):
        rt = runtime(
            ok({"brique": {"revision": 2, "transient": True}}),
            ok({"ok": True, "matter_id": "transit-1"}),
        )
        ref = {
            MARK_MATTER: "transit-1",
            "context": "/root/work",
            "owner": "@ext_source:/root/work",
            "format": "json",
            "consume": True,
            "revision": 2,
            "size": 123,
        }
        params = Params({"document": ref}, rt)
        await params.release_unconsumed_async()
        self.assertEqual([call["to_cap"] for call in rt.outbound.calls], ["matter.read", "matter.delete"])
        self.assertEqual(rt.outbound.calls[1]["params"]["expected_byte_size"], 123)

    async def test_semantic_matter_stays_an_unread_reference(self):
        rt = runtime()
        params = Params({"@matter": {"state": {"context": "/root", "id": "state"}}}, rt)
        value = await params.value("state")
        self.assertIsInstance(value, MatterRef)
        self.assertEqual(rt.outbound.calls, [])

    async def test_a_forged_transit_reference_cannot_delete_a_semantic_matter(self):
        encoded = b'{}'
        rt = runtime(
            ok(
                {
                    "brique": {"revision": 2},
                    "data": {
                        "kind": "inline",
                        "bytes": base64.b64encode(encoded).decode(),
                        "encoding": "base64",
                    },
                }
            )
        )
        ref = {
            MARK_MATTER: "business-state",
            "context": "/root",
            "owner": "@ext_source",
            "format": "json",
            "consume": True,
            "revision": 2,
            "size": len(encoded),
        }
        with self.assertRaises(TransitError):
            await Params({"state": ref}, rt).value("state")
        self.assertEqual([call["to_cap"] for call in rt.outbound.calls], ["matter.read"])


if __name__ == "__main__":
    unittest.main()


class ListParameterTests(unittest.IsolatedAsyncioTestCase):
    """A list parameter whose elements travel in each of the three forms, on both sides of the call."""

    async def asyncSetUp(self):
        self.token = current_intention.set(
            {"intention_id": "outer-1", "to": {"context": "", "cap": "cap.outer", "type": "user"},
             "correlation": {"root_intention_id": "root-1"}}
        )

    async def asyncTearDown(self):
        current_intention.reset(self.token)

    @staticmethod
    def transient(matter_id, size):
        return {MARK_MATTER: matter_id, "context": "/root/work", "owner": "@ext_source:/root/work", "format": "json",
                "consume": True, "revision": 2, "size": size}

    async def test_the_receiver_resolves_a_list_mixing_every_form(self):
        import tempfile
        from pathlib import Path
        from wrapper.transit import MARK_FILE, TransitService
        encoded = json.dumps("from a transient Matter", separators=(",", ":")).encode()
        data = {"kind": "inline", "bytes": base64.b64encode(encoded).decode(), "encoding": "base64"}
        rt = runtime(ok({"brique": {"revision": 2, "transient": True}, "data": data}), ok({"ok": True, "matter_id": "t1"}))
        with tempfile.TemporaryDirectory() as tmp:
            rt.transit = TransitService("w1", Path(tmp) / "tmp" / "wrapper_w1" / "transit", inline_limit=64)
            live = {"live": True}
            file_ref = rt.transit.files.write("from a file")
            object_ref = rt.transit.objects.post(live, "t")
            params = Params({"parts": ["plain", file_ref, object_ref, self.transient("t1", len(encoded)), 7]}, rt)
            out = await params.value("parts")
            self.assertEqual(out[:2], ["plain", "from a file"])
            self.assertIs(out[2], live)
            self.assertEqual(out[3:], ["from a transient Matter", 7])
            self.assertFalse(Path(file_ref[MARK_FILE]).exists())            # consumed
            self.assertEqual(len(rt.transit.objects), 0)                    # taken
            self.assertEqual([c["to_cap"] for c in rt.outbound.calls], ["matter.read", "matter.delete"])    # read, then guarded delete
            self.assertIs(await params.value("parts"), out)                 # memoized
            await params.release_unconsumed_async()                         # nothing left to release
            self.assertEqual([c["to_cap"] for c in rt.outbound.calls], ["matter.read", "matter.delete"])

    async def test_unread_transient_matters_of_a_list_are_guarded_deleted_at_the_end(self):
        rt = runtime(ok({"brique": {"revision": 2, "transient": True}}), ok({"ok": True, "matter_id": "t1"}))
        params = Params({"parts": ["text", self.transient("t1", 10)]}, rt)
        await params.release_unconsumed_async()
        self.assertEqual([c["to_cap"] for c in rt.outbound.calls], ["matter.read", "matter.delete"])

    async def test_the_emitter_gives_each_large_element_of_a_list_its_own_transient_matter_for_an_external_target(self):
        import tempfile
        from pathlib import Path
        from wrapper.transit import TransitService
        created = []

        class Creator:
            async def create(self, encoded, fmt, target):
                created.append((len(encoded), fmt, target.context))
                return {MARK_MATTER: f"t{len(created)}", "context": "/root/work", "owner": target.context, "format": fmt,
                        "consume": True, "revision": 1, "size": len(encoded)}

            async def discard(self, ref):
                created.append(("discarded", ref[MARK_MATTER]))

        with tempfile.TemporaryDirectory() as tmp:
            svc = TransitService("w1", Path(tmp) / "tmp" / "wrapper_w1" / "transit", inline_limit=1024)
            svc.runtime = SimpleNamespace(transit_matters=Creator())
            target = TransitTarget("@ext_peer:/root/work", "cap.consume", "user")
            sent = await svc.emit_async(["head", "x" * 5000, "y" * 5000, "tail"], target=target)
            self.assertEqual((sent[0], sent[3]), ("head", "tail"))
            self.assertEqual([reference_kind(e) for e in sent], [None, "matter", "matter", None])    # one Matter per large element
            self.assertEqual(len(created), 2)
            # a list that is big only as a whole travels as one Matter, and nothing is left half-posted
            created.clear()
            whole = await svc.emit_async(["0123456789"] * 200, target=target)
            self.assertEqual(reference_kind(whole), "matter")
            self.assertEqual([c[0] for c in created if c[0] != "discarded"], [len(json.dumps(["0123456789"] * 200, separators=(",", ":")).encode())])
