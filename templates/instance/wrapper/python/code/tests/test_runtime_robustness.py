# Copyright 2026 Nicolas Cassan
# Licensed under the Apache License, Version 2.0.

import asyncio
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace

from wrapper._ws_stdlib import StdlibWebSocketClient
from wrapper.execution import CapacityExecutor, MatterManager
from wrapper.matter import build_matter_binding
from wrapper.models import CapacityBinding, MatterBinding
from wrapper.constants import CAP_MATTER_SUBSCRIBE, CAP_MATTER_UNSUBSCRIBE
from wrapper.router import MessageRouter
from wrapper.runtime import WrapperRuntime
from wrapper.scheduler import SchedulerFullError, WorkScheduler
from wrapper.transit import TransitError, TransitService, TransitTarget


class SchedulerTests(unittest.IsolatedAsyncioTestCase):
    async def test_global_parallelism_and_same_binding_order(self) -> None:
        scheduler = WorkScheduler(max_concurrency=2, queue_limit=8)
        scheduler.start()
        release = asyncio.Event()
        both_started = asyncio.Event()
        started: list[str] = []

        async def work(name: str) -> None:
            started.append(name)
            if len(started) == 2:
                both_started.set()
            await release.wait()

        scheduler.submit("cap:a", lambda: work("a1"))
        scheduler.submit("cap:a", lambda: work("a2"))
        scheduler.submit("cap:b", lambda: work("b1"))
        await asyncio.wait_for(both_started.wait(), 1)
        self.assertEqual(started, ["a1", "b1"])
        release.set()
        await scheduler.drain()
        await scheduler.close()
        self.assertEqual(started, ["a1", "b1", "a2"])

    async def test_queue_is_bounded(self) -> None:
        scheduler = WorkScheduler(max_concurrency=1, queue_limit=1)
        gate = asyncio.Event()
        scheduler.submit("a", gate.wait)
        scheduler.submit("b", gate.wait)
        with self.assertRaises(SchedulerFullError):
            scheduler.submit("c", lambda: asyncio.sleep(0))
        gate.set()
        await scheduler.close()


class InvocationTests(unittest.IsolatedAsyncioTestCase):
    async def test_sync_callable_returning_awaitable_is_awaited(self) -> None:
        async def eventual():
            return {"done": True}

        def decorated_adapter(_params):
            return eventual()

        registry = SimpleNamespace(
            resolve_capacity=lambda _context, _name: CapacityBinding("", "cap", decorated_adapter)
        )
        runtime = SimpleNamespace(registry=registry, transit=SimpleNamespace(emit_returns=False))
        result = await CapacityExecutor(runtime).execute_capacity("", "cap", {"params": {}})
        self.assertEqual(result, {"done": True})

    async def test_same_capacity_is_serialized_even_for_direct_calls(self) -> None:
        active = 0
        maximum = 0

        async def capacity(_params):
            nonlocal active, maximum
            active += 1
            maximum = max(maximum, active)
            await asyncio.sleep(0.01)
            active -= 1
            return {}

        registry = SimpleNamespace(resolve_capacity=lambda _context, _name: CapacityBinding("", "cap", capacity))
        runtime = SimpleNamespace(registry=registry, transit=SimpleNamespace(emit_returns=False))
        executor = CapacityExecutor(runtime)
        await asyncio.gather(
            executor.execute_direct("", "cap", {}),
            executor.execute_direct("", "cap", {}),
        )
        self.assertEqual(maximum, 1)

    async def test_notification_failure_does_not_rollback_a_committed_write(self) -> None:
        state = {"value": 0}
        binding = None

        def write(value):
            state["value"] = value
            return {"ok": True, "value": value}

        binding = MatterBinding("", "state", write_symbol=write, notify_on_write=True)

        async def notify(*_args, **_kwargs):
            self.assertFalse(binding.lock.locked())
            raise RuntimeError("notification transport failed")

        runtime = SimpleNamespace(
            registry=SimpleNamespace(resolve_matter=lambda _context, _name: binding),
            notify_matter_written=notify,
            log_exception=lambda *_args, **_kwargs: None,
        )
        result = await MatterManager(runtime).write_matter("", "state", 7)
        self.assertEqual(state["value"], 7)
        self.assertTrue(result["ok"])
        self.assertTrue(result["notification_failed"])

    async def test_direct_matter_writer_keeps_notification_semantics(self) -> None:
        notifications = []

        async def notify(context_id, matter_name, *, value):
            notifications.append((context_id, matter_name, value))
            return 1

        runtime = SimpleNamespace(
            notify_matter_written=notify,
            log_exception=lambda *_args, **_kwargs: None,
        )
        binding = build_matter_binding(
            runtime,
            context_id="child",
            matter_name="state",
            entry={
                "write": lambda value: {"ok": True, "value": value},
                "notify_on_write": True,
            },
        )

        result = await binding.write_symbol(7)

        self.assertEqual(result["notified"], 1)
        self.assertEqual(notifications, [("child", "state", 7)])


class SubscriptionTests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self) -> None:
        self.sent = []

        class Transport:
            async def send(inner_self, message):
                self.sent.append(message)

        self.runtime = SimpleNamespace(
            subscriptions={},
            transport=Transport(),
            target_context=lambda _intention: "child",
            response_from_context=lambda _intention: "child",
            wrapper_identity=lambda: {"id": "w", "kind": "wrapper"},
            log_exception=lambda *_args, **_kwargs: None,
        )
        self.router = MessageRouter(self.runtime)

    @staticmethod
    def intention(cap: str, owner: str = "caller") -> dict:
        return {
            "intention_id": "i",
            "await_response": True,
            "to": {"type": "matter", "cap": cap, "context": "child"},
            "from": {"type": "user", "cap": owner, "context": "/root/source"},
            "params": {"matter_id": "state", "sub_id": "sub-1"},
        }

    async def test_retry_is_idempotent_but_another_owner_cannot_replace_or_remove(self) -> None:
        subscribe = self.intention(CAP_MATTER_SUBSCRIBE)
        await self.router.route_matter(subscribe, CAP_MATTER_SUBSCRIBE)
        await self.router.route_matter(subscribe, CAP_MATTER_SUBSCRIBE)
        self.assertEqual(len(self.runtime.subscriptions), 1)
        self.assertEqual(self.sent[-1]["response"]["status"], "ok")

        conflicting = self.intention(CAP_MATTER_SUBSCRIBE, owner="other")
        await self.router.route_matter(conflicting, CAP_MATTER_SUBSCRIBE)
        self.assertEqual(self.sent[-1]["response"]["error"]["details"]["reason"], "duplicate_sub_id")

        unsubscribe = self.intention(CAP_MATTER_UNSUBSCRIBE, owner="other")
        await self.router.route_matter(unsubscribe, CAP_MATTER_UNSUBSCRIBE)
        self.assertEqual(
            self.sent[-1]["response"]["error"]["details"]["reason"],
            "subscription_owner_mismatch",
        )
        self.assertIn("sub-1", self.runtime.subscriptions)

    async def test_notification_origin_stays_relative_to_wrapper_root(self) -> None:
        emitted = []

        class Outbound:
            async def emit_intention(inner_self, **kwargs):
                emitted.append(kwargs)
                return "event-id"

        runtime = SimpleNamespace(
            subscriptions={
                "sub": {
                    "matter_id": "state",
                    "context": "child",
                    "target": {"context": "/root/target", "cap": "consume", "type": "user"},
                }
            },
            outbound=Outbound(),
            log_exception=lambda *_args, **_kwargs: None,
        )
        runtime.normalize_relative_context = lambda value: WrapperRuntime.normalize_relative_context(runtime, value)
        notified = await WrapperRuntime.notify_matter_written(runtime, "child", "state", value=1)
        self.assertEqual(notified, 1)
        self.assertEqual(emitted[0]["from_context"], "child")


class TransitLimitTests(unittest.TestCase):
    def test_large_transit_is_rejected_and_file_is_private(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            service = TransitService("w", Path(tmp) / "tmp" / "wrapper_w" / "transit", max_bytes=32)
            with self.assertRaisesRegex(TransitError, "use a Matter"):
                service.emit(b"x" * 33, target=TransitTarget("/root/other", "cap.remote", "user"))
            ref = service.emit(
                b"small", target=TransitTarget("/root/other", "cap.remote", "user")
            )
            mode = Path(ref["@file"]).stat().st_mode & 0o777
            self.assertEqual(mode, 0o600)


class StdlibWebSocketTests(unittest.IsolatedAsyncioTestCase):
    async def test_fragmented_text_is_reassembled(self) -> None:
        client = StdlibWebSocketClient("ws://127.0.0.1/ws")
        frames = [(False, 0x1, b"hel"), (True, 0x0, b"lo")]

        async def read_frame():
            return frames.pop(0)

        client._read_frame = read_frame
        self.assertEqual(await client.recv_text(), "hello")

    async def test_close_frame_is_sent_before_writer_is_cleared(self) -> None:
        class Writer:
            def close(self):
                pass

            async def wait_closed(self):
                pass

        client = StdlibWebSocketClient("ws://127.0.0.1/ws")
        writer = Writer()
        client._writer = writer
        observed = []

        async def send_frame(opcode, payload):
            observed.append((opcode, payload, client._writer is writer))

        client._send_frame = send_frame
        await client.close()
        self.assertEqual(observed, [(0x8, b"", True)])


if __name__ == "__main__":
    unittest.main()
