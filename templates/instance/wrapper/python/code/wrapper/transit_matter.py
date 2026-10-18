# Copyright 2026 Nicolas Cassan
# Licensed under the Apache License, Version 2.0.

"""Ephemeral Matter data-plane used for cross-instance parameter transit."""

from __future__ import annotations

import asyncio
import base64
import json
import time
import urllib.parse
import urllib.request
from typing import Any

from .constants import STATUS_ERROR, TYPE_MATTER
from .execution import current_intention
from .transit import MARK_MATTER, TransitError, TransitTarget, _decode


class TransientMatterClient:
    """Create, consume and remove Matters that carry one call parameter.

    These references are deliberately different from semantic MatterRef values:
    only an exact ``@matter`` reference with ``consume: true`` reaches this class.
    """

    def __init__(self, runtime: Any) -> None:
        self.runtime = runtime
        self._expiry_tasks: set[asyncio.Task[Any]] = set()

    async def create(self, encoded: bytes, fmt: str, target: TransitTarget) -> dict[str, Any]:
        origin = self._current_origin()
        local_context = self._destination_context(target.context)
        ttl_seconds = int(getattr(getattr(self.runtime, "transit", None), "files", SimpleTTL()).ttl_seconds)
        expires_at = int(time.time()) + max(1, ttl_seconds)
        descriptor = {
            "brique": {
                "kind": "matter",
                "substance_mode": "brique",
                "substance_type": "binary",
                "substance_framing": "bytes",
                "transient": True,
                "transient_expires_at": expires_at,
            },
            # Deliberately empty semantic sections: temporary transport must not
            # pollute the vocabulary or look like domain state.
            "objective": {},
            "functional": {},
            "subjective": {},
        }
        created = await self._call(
            target.context,
            "matter.create",
            {"matter": descriptor},
        )
        matter_id = str(created.get("matter_id", "")).strip()
        revision = self._positive_int(created.get("revision"))
        if not matter_id or revision is None:
            raise TransitError("unreadable", "matter.create returned an incomplete transit identity")

        try:
            opened = await self._call(
                target.context,
                "matter.write",
                {
                    "matter_id": matter_id,
                    "expected_revision": revision,
                    "http_data": True,
                },
            )
            handle = opened.get("http_data")
            if not isinstance(handle, dict):
                raise TransitError("unreadable", "matter.write did not return an HTTP upload lease")
            committed = await asyncio.to_thread(self._http_put, handle, encoded)
            committed_revision = self._positive_int(committed.get("revision"))
            if committed_revision is None:
                raise TransitError("unreadable", "Matter upload did not return its committed revision")
        except BaseException:
            await self._best_effort_delete(target.context, matter_id, revision)
            raise

        ref = {
            MARK_MATTER: matter_id,
            "context": local_context,
            "owner": target.context,
            "format": fmt,
            "consume": True,
            "revision": committed_revision,
            "size": len(encoded),
        }
        if ttl_seconds > 0:
            # A Task copies the current contextvars context. Do not retain the
            # complete source intention (and possibly its large params) for the
            # whole TTL: expiry needs only this compact routing identity.
            token = current_intention.set(None)
            try:
                task = asyncio.create_task(self._expire(ref, ttl_seconds, origin))
            finally:
                current_intention.reset(token)
            self._expiry_tasks.add(task)
            task.add_done_callback(self._expiry_tasks.discard)
        return ref

    async def consume(self, ref: dict[str, Any]) -> Any:
        context = str(ref.get("context", "")).strip()
        matter_id = str(ref.get(MARK_MATTER, "")).strip()
        fmt = str(ref.get("format", "")).strip()
        expected_revision = self._positive_int(ref.get("revision"))
        expected_size = self._non_negative_int(ref.get("size"))
        if not context or not matter_id or not fmt or expected_revision is None or expected_size is None:
            raise TransitError("invalid_reference", "transient Matter reference is incomplete")

        payload = await self._call(
            context,
            "matter.read",
            {"matter_id": matter_id, "read_mode": "brique|data"},
        )
        brique = payload.get("brique")
        if not isinstance(brique, dict) or brique.get("transient") is not True:
            raise TransitError("invalid_reference", "Matter is not marked as transient by its descriptor")
        if self._positive_int(brique.get("revision")) != expected_revision:
            raise TransitError("invalid_reference", "transient Matter revision changed before consumption")
        data = payload.get("data")
        if not isinstance(data, dict):
            raise TransitError("unreadable", "matter.read returned no transit substance")
        encoded = await self._read_data(data, expected_size)
        if len(encoded) != expected_size:
            raise TransitError(
                "unreadable",
                f"transient Matter size is {len(encoded)} bytes, expected {expected_size}",
            )
        value = _decode(encoded, fmt)

        # Decoding succeeded: consume the Matter with revision and size guards.
        await self._call(
            context,
            "matter.delete",
            {
                "matter_id": matter_id,
                "expected_revision": expected_revision,
                "expected_byte_size": expected_size,
            },
        )
        return value

    async def discard(self, ref: dict[str, Any], *, origin: dict[str, str] | None = None) -> None:
        """Producer-side cleanup after a definitive delivery failure."""
        owner = str(ref.get("owner", "")).strip()
        matter_id = str(ref.get(MARK_MATTER, "")).strip()
        revision = self._positive_int(ref.get("revision"))
        size = self._non_negative_int(ref.get("size"))
        if not owner or not matter_id or revision is None or size is None:
            return
        try:
            await self._call(
                owner,
                "matter.delete",
                {
                    "matter_id": matter_id,
                    "expected_revision": revision,
                    "expected_byte_size": size,
                },
                origin=origin,
            )
        except BaseException as exc:
            if isinstance(exc, TransitError) and exc.reason in {"not_found", "unknown_matter"}:
                return
            self.runtime.log_event(
                "transit_matter_cleanup_failed",
                matter_id=matter_id,
                error_type=type(exc).__name__,
            )

    async def discard_received(self, ref: dict[str, Any]) -> None:
        """Receiver-side cleanup when a capacity ends without reading a parameter."""
        context = str(ref.get("context", "")).strip()
        matter_id = str(ref.get(MARK_MATTER, "")).strip()
        revision = self._positive_int(ref.get("revision"))
        size = self._non_negative_int(ref.get("size"))
        if not context or not matter_id or revision is None or size is None:
            return
        try:
            payload = await self._call(
                context,
                "matter.read",
                {"matter_id": matter_id, "read_mode": "brique"},
            )
            brique = payload.get("brique")
            if (
                not isinstance(brique, dict)
                or brique.get("transient") is not True
                or self._positive_int(brique.get("revision")) != revision
            ):
                return
            await self._call(
                context,
                "matter.delete",
                {
                    "matter_id": matter_id,
                    "expected_revision": revision,
                    "expected_byte_size": size,
                },
            )
        except BaseException as exc:
            self.runtime.log_event(
                "received_transit_matter_cleanup_failed",
                matter_id=matter_id,
                error_type=type(exc).__name__,
            )

    async def _expire(
        self,
        ref: dict[str, Any],
        ttl_seconds: int,
        origin: dict[str, str],
    ) -> None:
        await asyncio.sleep(ttl_seconds)
        await self.discard(ref, origin=origin)

    async def _best_effort_delete(self, context: str, matter_id: str, revision: int) -> None:
        try:
            await self._call(
                context,
                "matter.delete",
                {"matter_id": matter_id, "expected_revision": revision},
            )
        except BaseException:
            pass

    async def _call(
        self,
        context: str,
        cap: str,
        params: dict[str, Any],
        *,
        origin: dict[str, str] | None = None,
    ) -> dict[str, Any]:
        routing = origin or self._current_origin()
        correlation = {
            "root_intention_id": routing["root_intention_id"],
            "parent_intention_id": routing["parent_intention_id"],
        }
        message = await self.runtime.outbound.emit_and_wait(
            to_context=context,
            to_cap=cap,
            to_type=TYPE_MATTER,
            from_context=routing["from_context"],
            from_cap=routing["from_cap"],
            params=params,
            correlation=correlation,
        )
        body = message.get("response", {}) if isinstance(message.get("response"), dict) else {}
        if str(body.get("status", "")) == STATUS_ERROR:
            error = body.get("error") if isinstance(body.get("error"), dict) else {}
            raise TransitError(
                str(error.get("details", {}).get("reason", "unreadable"))
                if isinstance(error.get("details"), dict)
                else "unreadable",
                str(error.get("message", f"{cap} failed")),
            )
        payload = body.get("payload")
        if not isinstance(payload, dict):
            raise TransitError("unreadable", f"{cap} returned no payload")
        return payload

    def _current_origin(self) -> dict[str, str]:
        intention = current_intention.get()
        if intention is None:
            raise TransitError("unsupported", "transient Matter operations require a running capacity")
        target = intention.get("to", {}) if isinstance(intention.get("to"), dict) else {}
        current_id = str(intention.get("intention_id", "")).strip()
        inherited = intention.get("correlation", {})
        inherited = inherited if isinstance(inherited, dict) else {}
        return {
            "from_context": self.runtime.target_context(intention),
            "from_cap": str(target.get("cap", "")).strip(),
            "root_intention_id": str(inherited.get("root_intention_id", "")).strip() or current_id,
            "parent_intention_id": current_id,
        }

    async def _read_data(self, data: dict[str, Any], expected_size: int) -> bytes:
        kind = str(data.get("kind", "")).strip()
        if kind == "inline":
            raw = data.get("bytes", "")
            if not isinstance(raw, str):
                raise TransitError("unreadable", "inline Matter bytes are not a string")
            if str(data.get("encoding", "")).strip() == "base64":
                try:
                    return base64.b64decode(raw, validate=True)
                except ValueError as exc:
                    raise TransitError("unreadable", "inline Matter base64 is invalid") from exc
            return raw.encode("utf-8")
        if kind == "http":
            handle = data.get("http")
            if not isinstance(handle, dict):
                raise TransitError("unreadable", "matter.read returned no HTTP download lease")
            return await asyncio.to_thread(self._http_get, handle, expected_size)
        raise TransitError("unreadable", f"unsupported Matter data kind {kind!r}")

    @staticmethod
    def _lease_url(handle: dict[str, Any]) -> str:
        base = str(handle.get("base_url", "")).rstrip("/")
        path = str(handle.get("path", ""))
        token = str(handle.get("tok", ""))
        if not base or not path or not token:
            raise TransitError("invalid_reference", "incomplete Matter HTTP lease")
        return f"{base}{path}?{urllib.parse.urlencode({'tok': token})}"

    @classmethod
    def _http_put(cls, handle: dict[str, Any], data: bytes) -> dict[str, Any]:
        request = urllib.request.Request(cls._lease_url(handle), data=data, method="PUT")
        try:
            with urllib.request.urlopen(request) as response:
                payload = json.loads(response.read().decode("utf-8"))
        except Exception as exc:
            raise TransitError("unreadable", f"Matter upload failed: {exc}") from exc
        if not isinstance(payload, dict):
            raise TransitError("unreadable", "Matter upload returned an invalid response")
        return payload

    @classmethod
    def _http_get(cls, handle: dict[str, Any], expected_size: int) -> bytes:
        request = urllib.request.Request(cls._lease_url(handle), method="GET")
        try:
            with urllib.request.urlopen(request) as response:
                data = response.read(expected_size + 1)
        except Exception as exc:
            raise TransitError("unreadable", f"Matter download failed: {exc}") from exc
        if len(data) > expected_size:
            raise TransitError("unreadable", "Matter download exceeds the declared transit size")
        return data

    def _destination_context(self, context: str) -> str:
        if not context.strip().startswith("@ext_"):
            raw = context.strip()
            if raw.startswith("/"):
                return "/" + raw.strip("/")
            base = str(getattr(self.runtime, "ctx_id", "/root")).strip().rstrip("/") or "/root"
            suffix = raw.strip("/")
            return f"{base}/{suffix}" if suffix else base
        _, separator, path = context.partition(":")
        if not separator or not path.strip():
            return "/root"
        normalized = "/" + path.strip().strip("/")
        return normalized if normalized != "/" else "/root"

    @staticmethod
    def _positive_int(raw: Any) -> int | None:
        try:
            value = int(raw)
        except (TypeError, ValueError):
            return None
        return value if value > 0 else None

    @staticmethod
    def _non_negative_int(raw: Any) -> int | None:
        try:
            value = int(raw)
        except (TypeError, ValueError):
            return None
        return value if value >= 0 else None


class SimpleTTL:
    """Fallback used by focused tests that instantiate the client alone."""

    ttl_seconds = 0
