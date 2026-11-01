# Copyright 2026 Nicolas Cassan
# Licensed under the Apache License, Version 2.0.

import unittest
import json
from types import SimpleNamespace

from wrapper.constants import CONTROL_MESSAGE_LIMIT_BYTES
from wrapper.transport import TransportClient
from wrapper.execution import current_delegation, current_intention
from wrapper.outbound import OutboundIntentionAPI
from wrapper.router import MessageRouter


class FakeWebSocket:
    def __init__(self) -> None:
        self.sent = []

    async def send_text(self, payload: str) -> None:
        self.sent.append(payload)

    async def send(self, payload: str) -> None:
        self.sent.append(payload)


class TransportLimitTests(unittest.IsolatedAsyncioTestCase):
    async def test_rejects_an_oversized_control_message_before_send(self) -> None:
        client = TransportClient("ws://127.0.0.1/ws")
        client._ws = FakeWebSocket()
        with self.assertRaisesRegex(ValueError, "store large payloads in Matter substance"):
            await client.send({"payload": "x" * CONTROL_MESSAGE_LIMIT_BYTES})
        self.assertEqual(client._ws.sent, [])

    async def test_sends_a_small_control_message(self) -> None:
        client = TransportClient("ws://127.0.0.1/ws")
        client._ws = FakeWebSocket()
        await client.send({"ok": True})
        self.assertEqual(client._ws.sent, ['{"ok":true}'])

    async def test_delegated_message_uses_transport_frame(self) -> None:
        client = TransportClient("ws://127.0.0.1/ws")
        client._ws = FakeWebSocket()
        handle = "A" * 43
        await client.send({"kind": "response", "response": {}}, handle)
        wire = json.loads(client._ws.sent[0])
        self.assertEqual(wire["brique_wrapper_frame"], "brique.wrapper/1")
        self.assertEqual(wire["delegation"], handle)
        self.assertEqual(wire["message"]["kind"], "response")

    async def test_delegated_child_forces_identity_and_correlation(self) -> None:
        sent = []

        async def send_message(message):
            sent.append(message)

        runtime = SimpleNamespace(
            wrapper_identity=lambda: {"id": "wrapper", "kind": "wrapper"},
            send_message=send_message,
        )
        delegation_token = current_delegation.set("B" * 43)
        intention_token = current_intention.set(
            {
                "intention_id": "parent-id",
                "correlation": {"root_intention_id": "root-id", "parent_intention_id": "older"},
            }
        )
        try:
            await OutboundIntentionAPI(runtime).emit_intention(
                to_context="/root/target",
                to_cap="target.cap",
                to_type="user",
                correlation={"root_intention_id": "forged", "parent_intention_id": "forged"},
            )
        finally:
            current_intention.reset(intention_token)
            current_delegation.reset(delegation_token)
        body = sent[0]["intention"]
        self.assertEqual(body["identity"], {})
        self.assertEqual(
            body["correlation"],
            {"root_intention_id": "root-id", "parent_intention_id": "parent-id"},
        )

    def test_wrapper_frame_decoder_is_strict_and_raw_remains_compatible(self) -> None:
        raw = '{"kind":"response","response":{}}'
        self.assertEqual(MessageRouter.decode_frame(raw), ({"kind": "response", "response": {}}, ""))
        handle = "C" * 43
        framed = json.dumps(
            {
                "brique_wrapper_frame": "brique.wrapper/1",
                "message": {"kind": "response", "response": {}},
                "delegation": handle,
            }
        )
        self.assertEqual(MessageRouter.decode_frame(framed)[1], handle)
        extra = json.loads(framed)
        extra["unexpected"] = True
        self.assertIsNone(MessageRouter.decode_frame(json.dumps(extra)))
        duplicate = framed[:-1] + ',"delegation":"' + handle + '"}'
        self.assertIsNone(MessageRouter.decode_frame(duplicate))


if __name__ == "__main__":
    unittest.main()
