"""Driver of the end-to-end integration scenarios.

It speaks the engine's own protocol over the shared WebSocket listener, as a UI client would.
It contains no agentic logic: it sends real intentions and reads real Matters.
"""
from __future__ import annotations

import asyncio
import json
import os
import urllib.parse
import urllib.error
import urllib.request
import uuid
from datetime import datetime, timezone
from typing import Any, Callable, Dict, List, Optional

import websockets

URL = os.environ.get("BRIQUE_IT_WS", "ws://127.0.0.1:8080/agentic/test/ws")   # dedicated UI route of the test context
UI = "it_driver"
SLOW_CALL_S = float(os.environ.get("BRIQUE_IT_SLOW_CALL_S", "12"))    # a call longer than this triggers the diagnostic of the wrapper (see Driver._dump_wrapper_tasks)
AGENTIC = "/root/agentic"
TEST = "/root/agentic/test"
PILOT_ID = "it_pilot"
PILOT_REF = {"context": TEST, "matter_id": PILOT_ID}          # the pilots of the tests live in the test context


class ConnectionLostInDriver(Exception):
    """The connection of the driver to the engine closed while a request waited for its answer."""

    def __init__(self, code, reason) -> None:
        super().__init__(f"connection closed (code {code}) {reason}".strip())
        self.code, self.reason = code, reason


class DriverError(Exception):
    def __init__(self, error: Dict[str, Any]) -> None:
        self.error = error or {}
        details = self.error.get("details") or {}
        self.code = self.error.get("code", "")
        self.reason = details.get("reason", "")
        super().__init__(f"{self.code}/{self.reason}: {self.error.get('message', '')}")


class Driver:
    def __init__(self, url: str = URL) -> None:
        self.url = url
        self.ws = None
        self._pending: Dict[str, asyncio.Future] = {}
        self._reader: Optional[asyncio.Task] = None
        self.created: List[str] = []

    async def __aenter__(self) -> "Driver":
        self.ws = await websockets.connect(self.url, max_size=None)
        self._reader = asyncio.ensure_future(self._read())
        return self

    async def __aexit__(self, *exc) -> None:
        if self._reader:
            self._reader.cancel()
        if self.ws:
            await self.ws.close()

    async def _reconnect(self) -> None:
        if self._reader:
            self._reader.cancel()
        self.ws = await websockets.connect(self.url, max_size=None)
        self._reader = asyncio.ensure_future(self._read())

    async def _read(self) -> None:
        try:
            async for raw in self.ws:
                try:
                    body = json.loads(raw).get("response") or {}
                except ValueError:
                    continue
                future = self._pending.get(str(body.get("intention_id", "")))
                if future is None or body.get("status") == "running":
                    continue                 # an intermediate response: the final one is still coming
                self._pending.pop(str(body.get("intention_id", "")), None)
                if not future.done():
                    future.set_result(body)
        except websockets.exceptions.ConnectionClosed as exc:
            # The engine (or the network) closed this connection. Every request still waiting would otherwise wait for its whole timeout (30 s) for an
            # answer that cannot come: it is told at once, with the code the engine gave (4001: another client took the route).
            code = getattr(getattr(exc, "rcvd", None), "code", None)
            reason = getattr(getattr(exc, "rcvd", None), "reason", "") or ""
            for future in list(self._pending.values()):
                if not future.done():
                    future.set_exception(ConnectionLostInDriver(code, reason))

    # -- intentions ---------------------------------------------------------------------------
    async def call(self, context: str, cap: str, to_type: str, params: Optional[Dict[str, Any]] = None, *, timeout: float = 30.0) -> Dict[str, Any]:
        iid = str(uuid.uuid4())
        future = asyncio.get_running_loop().create_future()
        self._pending[iid] = future
        frame = json.dumps({"kind": "intention", "ts": datetime.now(timezone.utc).isoformat(), "intention": {
            "intention_id": iid, "await_response": True,
            "to": {"context": context, "cap": cap, "type": to_type},
            "from": {"context": f"@ui_{UI}:/driver", "cap": "", "type": "ui"},
            "identity": {"id": f"ui_{UI}", "kind": "ui"},
            "params": params or {}, "correlation": {"root_intention_id": "", "parent_intention_id": ""}}})
        try:
            await self.ws.send(frame)
        except websockets.exceptions.ConnectionClosed:           # a long run outlives its connection: connect again, once, and send again
            await self._reconnect()
            await self.ws.send(frame)
        # A call that takes more than SLOW_CALL_S is the symptom of a stalled wrapper (seen in the full suite, never alone): ask the wrapper what every
        # task of it waits for, once, while it is stalled (diagnostic.py, SIGUSR1), and say where the file is. Without it the stall is gone when we look.
        slow = asyncio.get_running_loop().call_later(SLOW_CALL_S, self._dump_wrapper_tasks, cap, iid)
        try:
            body = await asyncio.wait_for(future, timeout)
        except ConnectionLostInDriver as lost:
            # said on stderr (a suite that hid this would hide what loses requests), then asked again once on a new connection: the same intention
            # identifier, so a request the engine did receive is not done twice
            print(f"[driver] {lost}: asking {cap} again on a new connection", file=__import__("sys").stderr, flush=True)
            await self._reconnect()
            future = asyncio.get_running_loop().create_future()
            self._pending[iid] = future
            await self.ws.send(frame)
            body = await asyncio.wait_for(future, timeout)
        finally:
            slow.cancel()
        if body.get("status") == "error":
            raise DriverError(body.get("error"))
        return body.get("payload") or {}

    def _dump_wrapper_tasks(self, cap: str, iid: str) -> None:
        try:
            import signal, sys, tempfile
            from .crash import wrapper_pids
            files = []
            for pid in wrapper_pids():
                os.kill(pid, signal.SIGUSR1)
                files.append(os.path.join(tempfile.gettempdir(), f"agentic_core_tasks_{pid}.txt"))
            print(f"[driver] {cap} ({iid[:8]}) has waited {SLOW_CALL_S:.0f} s: the wrapper was asked for its tasks -> {files}", file=sys.stderr, flush=True)
        except Exception as exc:                    # a diagnostic must never fail a test
            print(f"[driver] could not ask the wrapper for its tasks: {exc}", file=__import__("sys").stderr, flush=True)

    # -- matters ------------------------------------------------------------------------------
    async def _text(self, context: str, matter_id: str) -> str:
        # A read lease is bound to the revision it was opened on: if the matter moved on before the GET (a write
        # just committed), the engine answers 409 and the client asks for a new lease, as the agentic port does.
        for attempt in range(8):
            data = (await self.call(context, "matter.read", "matter", {"matter_id": matter_id, "read_mode": "data"})).get("data") or {}
            if data.get("kind") != "http":                   # up to 256 KB the substance is inline
                return data.get("bytes") or ""
            lease = data.get("http") if isinstance(data.get("http"), dict) else data
            url = f"{lease['base_url']}{lease['path']}?{urllib.parse.urlencode({'tok': lease['tok']})}"
            try:
                return await asyncio.get_running_loop().run_in_executor(None, lambda: urllib.request.urlopen(url, timeout=30).read().decode("utf-8"))
            except urllib.error.HTTPError as exc:
                if exc.code != 409 or attempt == 7:
                    raise
                await asyncio.sleep(0.1)
        return ""

    async def content(self, context: str, matter_id: str) -> Dict[str, Any]:
        """The content of a Matter is its substance: one JSON document."""
        text = await self._text(context, matter_id)
        return json.loads(text) if isinstance(text, str) and text.strip() else {}

    async def lines(self, context: str, matter_id: str) -> List[Dict[str, Any]]:
        """The content of a line-framed Matter (the journal): one JSON document per line."""
        return [json.loads(raw) for raw in (await self._text(context, matter_id)).split("\n") if raw.strip()]

    async def exists(self, context: str, matter_id: str) -> bool:
        return bool((await self.call(context, "matter.exists", "matter", {"matter_id": matter_id})).get("exist"))

    async def write(self, context: str, matter_id: str, content: Dict[str, Any]) -> None:
        await self.call(context, "matter.write", "matter", {"matter_id": matter_id, "data": json.dumps(content, ensure_ascii=False, indent=1)})

    # -- test world ----------------------------------------------------------------------------------
    async def reset_world(self, rules: List[Dict[str, Any]]) -> None:
        await self.write(TEST, "llm_script", {"rules": rules})
        await self.write(TEST, "llm_calls", {"calls": []})
        await self.write(TEST, "tool_calls", {"calls": []})
        await self.write(TEST, "gates", {"open": []})

    async def create(self, cid: str, *, intention: Any = "Test continuity.", parent: Optional[str] = None,
                     pilot_ref: Optional[Dict[str, Any]] = None) -> None:
        params = {"continuity_id": cid, "pilot_ref": pilot_ref or PILOT_REF, "initial_intention": intention}
        if parent:
            params["parent_id"] = parent
        await self.call(AGENTIC, "continuity.create", "user", params)
        self.created.append(cid)

    async def send(self, cid: str, mid: str, content: Any = "hello", kind: str = "user", **extra) -> Dict[str, Any]:
        message = dict({"id": mid, "from": "user:1", "kind": kind, "content": content}, **extra)
        return await self.call(AGENTIC, "mailbox.send", "user", {"continuity_id": cid, "message": message})

    async def open_gate(self, gate: str) -> None:
        await self.call(TEST, "test.open_gate", "user", {"gate": gate})

    async def runtime(self, cid: str) -> Dict[str, Any]:
        return await self.content(AGENTIC, f"runtime_{cid}")

    async def mailbox(self, cid: str) -> List[Dict[str, Any]]:
        return (await self.content(AGENTIC, f"mailbox_{cid}")).get("entries", [])

    async def journal(self, cid: str, frame: bool = False) -> List[Dict[str, Any]]:
        """The lines of the journal. The cognitive frame, the first line of every continuity, is left out unless asked: scenarios
        read the thread of the exchange, and one scenario reads the frame itself."""
        lines = await self.lines(AGENTIC, f"journal_{cid}")
        return lines if frame else [m for m in lines if m["kind"] != "framework"]

    async def continuity(self, cid: str, frame: bool = False) -> Dict[str, Any]:
        """The header of the continuity, with the lines of its journal under "messages" (without the cognitive frame unless asked)."""
        return dict(await self.content(AGENTIC, f"continuity_{cid}"), messages=await self.journal(cid, frame=frame))

    async def measures(self, cid: str) -> List[Dict[str, Any]]:
        """The context measures of the journal (inference_measured events), in order."""
        return [m["content"] for m in await self.journal(cid) if m["kind"] == "event" and m["content"].get("event") == "inference_measured"]

    async def llm_calls(self) -> List[Dict[str, Any]]:
        return (await self.content(TEST, "llm_calls")).get("calls", [])

    async def tool_calls(self, tool: Optional[str] = None, state: Optional[str] = None) -> List[Dict[str, Any]]:
        calls = (await self.content(TEST, "tool_calls")).get("calls", [])
        return [c for c in calls if (tool is None or c["tool"] == tool) and (state is None or c["state"] == state)]

    # -- waiting -------------------------------------------------------------------------------------------
    async def until(self, predicate: Callable[[], Any], what: str, timeout: float = 20.0, every: float = 0.1) -> Any:
        deadline = asyncio.get_running_loop().time() + timeout
        while True:
            value = await predicate()
            if value:
                return value
            if asyncio.get_running_loop().time() > deadline:
                raise AssertionError(f"timeout waiting for: {what}")
            await asyncio.sleep(every)

    async def quiet(self, *cids: str, timeout: float = 20.0, allow_pending: int = 0) -> None:
        """Wait until every continuity is idle with an empty mailbox and a journal that stopped growing."""
        last: Dict[str, int] = {}
        stable = {"n": 0}

        async def settled():
            sizes = {}
            for cid in cids:
                rt = await self.runtime(cid)
                entries = await self.mailbox(cid)
                journal = await self.journal(cid)
                sizes[cid] = len(journal)
                if rt.get("status") != "idle" or len([e for e in entries if e["status"] == "pending"]) > allow_pending or any(e["status"] == "claimed" for e in entries):
                    stable["n"] = 0
                    last.update(sizes)
                    return False
            stable["n"] = stable["n"] + 1 if sizes == last else 0
            last.clear()
            last.update(sizes)
            return stable["n"] >= 3
        await self.until(settled, f"{cids} to be quiet", timeout)

    async def cleanup(self, *cids: str) -> None:
        for cid in list(dict.fromkeys(list(cids) + self.created)):
            try:
                await self.call(AGENTIC, "continuity.delete", "user", {"continuity_id": cid, "cascade": True})
            except DriverError:
                pass
        self.created.clear()

    async def structure_refs(self) -> List[str]:
        view = await self.call(AGENTIC, "structure.read", "matter", {"structure_id": "continuities"})
        return sorted(i["matter_id"] for i in (view.get("functional") or {}).get("items", []))

    async def control(self, cid: str, mid: str, action: str, **content) -> Dict[str, Any]:
        return await self.send(cid, mid, dict({"action": action}, **content), kind="control")

    async def gone(self, cid: str) -> bool:
        for matter in (f"continuity_{cid}", f"journal_{cid}", f"mailbox_{cid}", f"runtime_{cid}"):
            if await self.exists(AGENTIC, matter):
                return False
        return f"continuity_{cid}" not in await self.structure_refs()

    # -- invariants checked at the end of every scenario -----------------------------------------------------
    async def assert_invariants(self, *cids: str, allow_pending: int = 0) -> None:
        for cid in cids:
            c = await self.continuity(cid, frame=True)
            seqs = [m["seq"] for m in c["messages"]]
            assert sorted(seqs) == list(range(1, len(seqs) + 1)), f"{cid}: seq not contiguous {seqs}"
            ids = [m["id"] for m in c["messages"]]
            assert len(ids) == len(set(ids)), f"{cid}: duplicated message ids"
            entries = await self.mailbox(cid)
            durable = set(ids)
            assert all(e["message_id"] in durable for e in entries), f"{cid}: orphan mailbox reservation"
            pending = [e for e in entries if e["status"] == "pending"]
            assert len(pending) <= allow_pending, f"{cid}: unexpected pending entries {pending}"
            assert not [e for e in entries if e["status"] == "claimed"], f"{cid}: claimed entries remain"
            rt = await self.runtime(cid)
            assert rt["status"] == "idle" and not rt.get("run_id") and not rt.get("current_message_id"), f"{cid}: runtime not idle {rt}"
            assert rt["active_wait_groups"] == [], f"{cid}: wait groups remain {rt['active_wait_groups']}"
