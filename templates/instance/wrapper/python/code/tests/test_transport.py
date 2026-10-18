# Copyright 2026 Nicolas Cassan
# Licensed under the Apache License, Version 2.0.

import unittest

from wrapper.constants import CONTROL_MESSAGE_LIMIT_BYTES
from wrapper.transport import TransportClient


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


if __name__ == "__main__":
    unittest.main()
