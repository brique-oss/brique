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
import json
import os
import sys
from pathlib import Path
from typing import Any

from .calls import CapacityCaller
from .constants import (
    CAP_WRAPPER_READY,
    CAP_WRAPPER_STOP,
    CAP_MATTER_SUBSCRIBE,
    CAP_MATTER_UNSUBSCRIBE,
    KIND_INTENTION,
    KIND_RESPONSE,
    TYPE_EXECUTION,
    TYPE_MATTER,
)
from .correlator import AwaitCorrelator
from .execution import CapacityExecutor, MatterManager, current_activity, current_intention
from .outbound import OutboundIntentionAPI
from .registry import RuntimeRegistry
from .router import MessageRouter
from .scheduler import SchedulerFullError, WorkScheduler
from .transit import TransitService
from .transit_matter import TransientMatterClient
from .transport import TransportClient
from .util import new_id, utc_now_rfc3339


class WrapperRuntime:
    def __init__(self) -> None:
        self._raw_ctx_dir = os.environ.get("BRIQUE_CTX_DIR", "").strip()
        self.ctx_dir = Path(self._raw_ctx_dir or ".").resolve()
        self.ctx_id = os.environ.get("BRIQUE_CTX_ID", "")
        self.wrapper_name = os.environ.get("BRIQUE_WRAPPER_NAME", "")
        self.wrapper_src_dir = Path(
            os.environ.get("BRIQUE_WRAPPER_SRC_DIR", self.ctx_dir / "code" / self.wrapper_name)
        ).resolve()
        self.wrapper_build_dir = Path(
            os.environ.get("BRIQUE_WRAPPER_BUILD_DIR", self.ctx_dir / "build" / self.wrapper_name)
        ).resolve()
        self.wrapper_work_dir = Path(
            os.environ.get("BRIQUE_WORKDIR", self.ctx_dir / "tmp" / f"wrapper_{self.wrapper_name}")
        ).resolve()
        self.transport: TransportClient | None = None
        self.transit = TransitService(
            self.wrapper_name,
            self.wrapper_work_dir / "transit",
            allowed_file_root=self.context_tree_dir(),
            max_bytes=self._positive_env("BRIQUE_TRANSIT_MAX_BYTES", 64 * 1024 * 1024),
            ttl_seconds=self._positive_env("BRIQUE_TRANSIT_TTL_SECONDS", 60 * 60),
            runtime=self,
        )
        self.transit_matters = TransientMatterClient(self)
        self.correlator = AwaitCorrelator()
        self.registry = RuntimeRegistry(self)
        self.capacity_executor = CapacityExecutor(self)
        self.matter_manager = MatterManager(self)
        self.router = MessageRouter(self)
        self.outbound = OutboundIntentionAPI(self)
        self.calls = CapacityCaller(self)
        self.stop_requested = asyncio.Event()
        self.subscriptions: dict[str, dict[str, Any]] = {}
        self.inflight: set[asyncio.Task[Any]] = set()
        self.scheduler = WorkScheduler(
            self._positive_env("BRIQUE_MAX_CONCURRENCY", 16),
            self._positive_env("BRIQUE_QUEUE_LIMIT", 256),
        )

    async def load(self) -> None:
        if not self._raw_ctx_dir:
            raise RuntimeError("missing BRIQUE_CTX_DIR")
        if not Path(self._raw_ctx_dir).is_absolute():
            raise RuntimeError("BRIQUE_CTX_DIR must be an absolute path")
        if not self.ctx_dir.is_dir() or not (self.ctx_dir / "context.json").is_file():
            raise RuntimeError(f"missing BRIQUE_CTX_DIR: {self.ctx_dir}")
        if not self.ctx_id.strip() or not self.ctx_id.startswith("/"):
            raise RuntimeError("BRIQUE_CTX_ID must be a non-empty absolute context path")
        if not self.wrapper_name.strip():
            raise RuntimeError("missing BRIQUE_WRAPPER_NAME")
        self.wrapper_work_dir.mkdir(parents=True, exist_ok=True)
        # Nothing in flight survives a restart: delete the transit files of the previous life.
        self.transit.sweep()
        await self.registry.load_bindings()
        self.transport = TransportClient(self.boundary_url())
        await self.transport.connect()

    @staticmethod
    def _positive_env(name: str, default: int) -> int:
        raw = os.environ.get(name, "").strip()
        if not raw:
            return default
        try:
            value = int(raw)
        except ValueError as exc:
            raise RuntimeError(f"{name} must be a positive integer") from exc
        if value < 1:
            raise RuntimeError(f"{name} must be a positive integer")
        return value

    def boundary_url(self) -> str:
        from .util import load_json

        context_doc = load_json(self.ctx_dir / "context.json")
        engine_cfg = context_doc.get("brique", {}).get("engine_config", {})
        comm_cfg = engine_cfg.get("communication", {})
        for iface in comm_cfg.get("interfaces", []):
            if not isinstance(iface, dict):
                continue
            if iface.get("type") != "wrapper" or iface.get("name") != self.wrapper_name:
                continue
            if iface.get("driver") != "ws":
                continue
            cfg = iface.get("config", {}) if isinstance(iface.get("config"), dict) else {}
            path = str(cfg.get("path", "/ws")).strip() or "/ws"
            base_url = os.environ.get("BRIQUE_WS_URL", "").strip().rstrip("/")
            if base_url:
                return f"{base_url}{path}"
            addr = str(cfg.get("addr", "")).strip() or os.environ.get("BRIQUE_WS_ADDR", "").strip()
            if not addr:
                raise RuntimeError("shared websocket listener addr not provided")
            host = "127.0.0.1"
            if addr.startswith(":"):
                port = addr[1:]
            else:
                host_part, _, port_part = addr.rpartition(":")
                host = host_part or host
                port = port_part or "8080"
                if host in {"0.0.0.0", ""}:
                    host = "127.0.0.1"
            return f"ws://{host}:{port}{path}"
        raise RuntimeError(f"wrapper websocket interface not found for {self.wrapper_name}")

    def context_tree_dir(self) -> Path:
        """Filesystem root corresponding to the first component of ``ctx_id``."""
        root = self.ctx_dir
        components = [part for part in self.ctx_id.strip("/").split("/") if part]
        for _ in components[1:]:
            root = root.parent
        return root.resolve()

    async def run(self) -> int:
        await self.load()
        assert self.transport is not None
        self.scheduler.start()
        await self.transport.send(self.wrapper_ready_message())
        try:
            await self.transport.receive_loop(self._schedule_raw_message)
        except Exception:
            if not self.stop_requested.is_set():
                raise
        finally:
            await self._drain_inflight()
            await self._shutdown()
        return 0

    async def request_stop(self) -> None:
        if self.stop_requested.is_set():
            return
        self.stop_requested.set()
        if self.transport is not None:
            await self.transport.close()

    async def notify_running(self) -> None:
        """Send an intermediate "running" response for the capacity currently
        executing, so the caller knows the long-running work has started and
        keeps waiting instead of timing out. Safe to call zero, one, or many
        times per capacity call — each call just re-sends "running"; it does
        not replace the final response the capacity function still returns
        normally at the end of its execution. No-op outside of a capacity
        call (current_intention unset), so it is always safe to call.
        """
        # A direct call waiting for this capacity renews its inactivity budget.
        activity = current_activity.get()
        if activity is not None:
            activity.set()
        intention = current_intention.get()
        if intention is None or self.transport is None:
            return
        await self.transport.send(self.router.running_response(intention))

    async def _schedule_raw_message(self, raw: str) -> None:
        message = await asyncio.to_thread(self.router.decode_raw, raw)
        if message is None:
            self.log_event("incoming_message_rejected", reason="invalid_json_or_envelope")
            return
        if message.get("kind") == KIND_RESPONSE:
            await self.router.route_message(message)
            return
        intention = message.get("intention", {})
        to = intention.get("to", {}) if isinstance(intention, dict) else {}
        if (
            isinstance(to, dict)
            and str(to.get("type", "")).strip() == TYPE_EXECUTION
            and str(to.get("cap", "")).strip() == CAP_WRAPPER_STOP
        ):
            await self.router.route_message(message)
            return
        context = self.target_context(intention) if isinstance(intention, dict) else ""
        target_type = str(to.get("type", "")).strip()
        if target_type == TYPE_MATTER:
            params = intention.get("params", {}) if isinstance(intention.get("params"), dict) else {}
            target_cap = str(to.get("cap", "")).strip()
            if target_cap in {CAP_MATTER_SUBSCRIBE, CAP_MATTER_UNSUBSCRIBE}:
                target_name = "subscription:" + str(params.get("sub_id", "")).strip()
            else:
                target_name = str(params.get("matter_id", "")).strip()
        else:
            target_name = str(to.get("cap", "")).strip()
        key = ":".join((target_type, context, target_name))
        try:
            self.scheduler.submit(key, lambda: self._run_scheduled(message))
        except SchedulerFullError:
            self.log_event("scheduler_overloaded", queued=self.scheduler.queued, key=key)
            await self.router.reject_overloaded(intention)

    async def _drain_inflight(self) -> None:
        await self.scheduler.drain()
        if self.inflight:
            await asyncio.gather(*tuple(self.inflight), return_exceptions=True)

    async def _shutdown(self) -> None:
        await self.correlator.cancel_all("wrapper shutdown")
        for hook in self.registry.shutdown_hooks:
            try:
                result = hook()
                if asyncio.iscoroutine(result):
                    await result
            except Exception as exc:
                self.log_exception("shutdown_hook_failed", exc)
                continue
        await self.scheduler.close()
        if self.transport is not None:
            await self.transport.close()

    async def _run_scheduled(self, message: dict[str, Any]) -> None:
        try:
            await self.router.route_message(message)
        except Exception as exc:
            self.log_exception("scheduled_work_failed", exc)

    def log_event(self, event: str, **fields: Any) -> None:
        record = {"event": event, "wrapper": self.wrapper_name, **fields}
        sys.stderr.write(json.dumps(record, ensure_ascii=True, default=str) + "\n")
        sys.stderr.flush()

    def log_exception(self, event: str, exc: BaseException, **fields: Any) -> None:
        self.log_event(event, error_type=type(exc).__name__, error=str(exc), **fields)

    def wrapper_ready_message(self) -> dict[str, Any]:
        return {
            "kind": KIND_INTENTION,
            "ts": utc_now_rfc3339(),
            "intention": {
                "intention_id": new_id(),
                "await_response": False,
                "to": {
                    "context": self.ctx_id,
                    "cap": CAP_WRAPPER_READY,
                    "type": TYPE_EXECUTION,
                },
                "from": {
                    "context": "",
                    "cap": "",
                    "type": TYPE_EXECUTION,
                },
                "identity": self.wrapper_identity(),
                "params": {
                    "wrapper": self.wrapper_name,
                },
                "correlation": {
                    "root_intention_id": "",
                    "parent_intention_id": "",
                },
            },
        }

    def wrapper_identity(self) -> dict[str, Any]:
        return {
            "id": self.wrapper_name,
            "kind": "wrapper",
        }

    def target_context(self, intention: dict[str, Any]) -> str:
        raw_context = str(intention.get("to", {}).get("context", "")).strip()
        prefix = f"@wrapper_{self.wrapper_name}:/"
        if raw_context.startswith(prefix):
            return raw_context[len(prefix) :]
        return raw_context.strip("/")

    def relative_context(self, absolute_context: str) -> str | None:
        """Express an absolute context path relative to the wrapper root.

        Returns None when the context is outside this wrapper root.
        """
        boundary = self.ctx_id.strip().rstrip("/")
        context = str(absolute_context).strip().rstrip("/")
        if not boundary or not context:
            return None
        if context == boundary:
            return ""
        if context.startswith(boundary + "/"):
            return context[len(boundary) + 1 :]
        return None

    def is_local_caller(self, intention: dict[str, Any]) -> bool:
        """True when the caller of ``intention`` is a capacity bound in this wrapper.

        A DSL capacity can live under the boundary context without being bound
        here, so the registry lookup is what makes the answer reliable.
        """
        origin = intention.get("from", {})
        if not isinstance(origin, dict):
            return False
        relative = self.relative_context(str(origin.get("context", "")))
        cap = str(origin.get("cap", "")).strip()
        if relative is None or not cap:
            return False
        return self.registry.resolve_capacity(relative, cap) is not None

    def response_from_context(self, intention: dict[str, Any]) -> str:
        raw_context = str(intention.get("to", {}).get("context", "")).strip()
        prefix = f"@wrapper_{self.wrapper_name}:/"
        if raw_context.startswith(prefix):
            return raw_context[len(prefix) :]
        return ""

    def extract_write_value(self, intention: dict[str, Any]) -> Any:
        import base64

        params = intention.get("params", {}) if isinstance(intention.get("params"), dict) else {}
        if "value" in params:
            return params["value"]
        if "data" in params:
            return params["data"]
        payload = params.get("payload")
        if isinstance(payload, dict):
            if "value" in payload:
                return payload["value"]
            if payload.get("kind") == "inline" and isinstance(payload.get("bytes"), str):
                return base64.b64decode(payload["bytes"])
        return None

    def normalize_relative_context(self, relative_context: str) -> str:
        return str(relative_context).strip().strip("/")

    def absolute_context(self, relative_context: str) -> str:
        rel = self.normalize_relative_context(relative_context)
        if not rel:
            return self.ctx_id
        if not self.ctx_id:
            return rel
        return f"{self.ctx_id.rstrip('/')}/{rel}"

    async def notify_matter_written(
        self,
        relative_context: str,
        matter_name: str,
        *,
        value: Any = None,
        op: str = "write",
        extra_params: dict[str, Any] | None = None,
    ) -> int:
        context_id = self.normalize_relative_context(relative_context)
        deliveries = []
        for sub in list(self.subscriptions.values()):
            if not isinstance(sub, dict):
                continue
            if str(sub.get("matter_id", "")).strip() != matter_name:
                continue
            if self.normalize_relative_context(str(sub.get("context", ""))) != context_id:
                continue
            target = sub.get("target", {})
            if not isinstance(target, dict):
                continue
            to_context = str(target.get("context", "")).strip()
            to_cap = str(target.get("cap", "")).strip()
            to_type = str(target.get("type", "")).strip()
            if not to_context or not to_cap or not to_type:
                continue
            params = {
                "event": "matter_written",
                "op": op,
                "matter_id": matter_name,
                "substance_mode": "wrapper",
                "value": value,
            }
            if isinstance(extra_params, dict):
                params.update(extra_params)
            deliveries.append(
                self.outbound.emit_intention(
                    to_context=to_context,
                    to_cap=to_cap,
                    to_type=to_type,
                    from_context=context_id,
                    from_cap="matter_event",
                    from_type="matter",
                    params=params,
                    await_response=False,
                )
            )
        if not deliveries:
            return 0
        results = await asyncio.gather(*deliveries, return_exceptions=True)
        notified = 0
        for result in results:
            if isinstance(result, BaseException):
                self.log_exception("matter_notification_delivery_failed", result, matter=matter_name)
            else:
                notified += 1
        return notified



async def _amain() -> int:
    runtime = WrapperRuntime()
    return await runtime.run()


def main() -> int:
    try:
        return asyncio.run(_amain())
    except KeyboardInterrupt:
        return 0
    except Exception as exc:  # pragma: no cover - startup/teardown race path
        sys.stderr.write(f"python wrapper fatal error: {exc}\n")
        sys.stderr.flush()
        return 1
