# Copyright 2026 Nicolas Cassan
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

from __future__ import annotations

import asyncio
import base64
import hashlib
import os
import struct
from urllib.parse import urlparse


class StdlibWebSocketClient:
    def __init__(self, url: str) -> None:
        self.url = url
        self._reader: asyncio.StreamReader | None = None
        self._writer: asyncio.StreamWriter | None = None
        self._closed = False

    async def connect(self) -> None:
        parsed = urlparse(self.url)
        if parsed.scheme != "ws":
            raise RuntimeError(f"unsupported websocket scheme: {parsed.scheme}")
        host = parsed.hostname or "127.0.0.1"
        port = parsed.port or 80
        path = parsed.path or "/"
        if parsed.query:
            path = f"{path}?{parsed.query}"

        reader, writer = await asyncio.open_connection(host, port)

        key = base64.b64encode(os.urandom(16)).decode("ascii")
        request = (
            f"GET {path} HTTP/1.1\r\n"
            f"Host: {host}:{port}\r\n"
            "Upgrade: websocket\r\n"
            "Connection: Upgrade\r\n"
            f"Sec-WebSocket-Key: {key}\r\n"
            "Sec-WebSocket-Version: 13\r\n"
            "\r\n"
        )
        writer.write(request.encode("ascii"))
        await writer.drain()

        status_line = await reader.readline()
        if not status_line.startswith(b"HTTP/1.1 101"):
            raise RuntimeError(f"websocket handshake failed: {status_line.decode('ascii', errors='ignore').strip()}")

        headers: dict[str, str] = {}
        while True:
            line = await reader.readline()
            if line in {b"", b"\r\n"}:
                break
            text = line.decode("ascii", errors="ignore").strip()
            if ":" not in text:
                continue
            key_name, value = text.split(":", 1)
            headers[key_name.strip().lower()] = value.strip()

        expected_accept = base64.b64encode(
            hashlib.sha1((key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").encode("ascii")).digest()
        ).decode("ascii")
        if headers.get("sec-websocket-accept", "") != expected_accept:
            raise RuntimeError("websocket handshake invalid accept header")

        self._reader = reader
        self._writer = writer
        self._closed = False

    async def send_text(self, payload: str) -> None:
        await self._send_frame(0x1, payload.encode("utf-8"))

    async def recv_text(self) -> str:
        while True:
            opcode, payload = await self._read_frame()
            if opcode == 0x1:
                return payload.decode("utf-8")
            if opcode == 0x8:
                await self.close()
                raise EOFError("websocket closed")
            if opcode == 0x9:
                await self._send_frame(0xA, payload)
                continue
            if opcode == 0xA:
                continue

    async def close(self) -> None:
        if self._closed:
            return
        self._closed = True
        writer = self._writer
        self._reader = None
        self._writer = None
        if writer is None:
            return
        try:
            await self._send_frame(0x8, b"")
        except Exception:
            pass
        writer.close()
        try:
            await writer.wait_closed()
        except Exception:
            pass

    async def _send_frame(self, opcode: int, payload: bytes) -> None:
        writer = self._writer
        if writer is None:
            raise RuntimeError("websocket is not connected")
        mask = os.urandom(4)
        header = bytearray()
        header.append(0x80 | (opcode & 0x0F))
        size = len(payload)
        if size < 126:
            header.append(0x80 | size)
        elif size <= 0xFFFF:
            header.append(0x80 | 126)
            header.extend(struct.pack("!H", size))
        else:
            header.append(0x80 | 127)
            header.extend(struct.pack("!Q", size))
        header.extend(mask)
        masked = bytes(b ^ mask[i % 4] for i, b in enumerate(payload))
        writer.write(bytes(header) + masked)
        await writer.drain()

    async def _read_frame(self) -> tuple[int, bytes]:
        reader = self._reader
        if reader is None:
            raise RuntimeError("websocket is not connected")
        head = await reader.readexactly(2)
        opcode = head[0] & 0x0F
        masked = (head[1] & 0x80) != 0
        size = head[1] & 0x7F
        if size == 126:
            size = struct.unpack("!H", await reader.readexactly(2))[0]
        elif size == 127:
            size = struct.unpack("!Q", await reader.readexactly(8))[0]
        mask = await reader.readexactly(4) if masked else b""
        payload = await reader.readexactly(size)
        if masked:
            payload = bytes(b ^ mask[i % 4] for i, b in enumerate(payload))
        return opcode, payload
