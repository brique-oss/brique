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

"""Transit references: ``@object``, ``@file`` and transient ``@matter``.

Transit is what one capacity hands to another so that the second can work. Its
receiver consumes it. A Matter used here is explicitly transient; semantic
Matter references remain in the intention ``matters`` field and are not read.

* ``@file``   a file in the sender's transit directory, deleted by the receiver.
* ``@object`` an object posted in a wrapper-local table, removed by the receiver.
* ``@matter`` a Matter created in another instance, deleted after successful read.
"""

from __future__ import annotations

import asyncio
import json
import os
import threading
import time
import uuid
from dataclasses import dataclass
from pathlib import Path
from typing import Any

MARK_FILE = "@file"
MARK_OBJECT = "@object"
MARK_MATTER = "@matter"

INLINE_LIMIT_BYTES = 64 * 1024
DEFAULT_MAX_TRANSIT_BYTES = 64 * 1024 * 1024
DEFAULT_TRANSIT_TTL_SECONDS = 60 * 60

TRANSIT_DIR_NAME = "transit"
TRANSIT_FILE_PREFIX = "transit_"

FORMAT_JSON = "json"
FORMAT_TEXT = "text"
FORMAT_BYTES = "bytes"
_EXTENSIONS = {FORMAT_JSON: "json", FORMAT_TEXT: "txt", FORMAT_BYTES: "bin"}

_FILE_KEYS = {MARK_FILE, "format"}
_OBJECT_KEYS = {MARK_OBJECT, "wrapper", "type"}
_MATTER_KEYS = {
    MARK_MATTER,
    "context",
    "owner",
    "format",
    "consume",
    "revision",
    "size",
}


@dataclass(frozen=True)
class TransitTarget:
    """Logical destination used to choose a transit representation."""

    context: str
    cap: str
    type: str
    same_wrapper: bool = False

    @property
    def external(self) -> bool:
        return self.context.strip().startswith("@ext_")


class TransitError(Exception):
    """Typed failure of a transit reference.

    ``reason`` is one of: not_found, type_mismatch, wrong_wrapper, unreadable,
    invalid_reference, unsupported.
    """

    def __init__(self, reason: str, message: str) -> None:
        super().__init__(message)
        self.reason = reason

    @property
    def code(self) -> str:
        return "not_found" if self.reason == "not_found" else "invalid"


def reference_kind(value: Any) -> str | None:
    """Return the kind of an exact transit reference, otherwise ``None``.

    A reference is an object that carries exactly its mark and its descriptor
    fields. Any other object is data, even if it contains a mark.
    """
    if not isinstance(value, dict):
        return None
    keys = set(value)
    if MARK_FILE in keys and isinstance(value[MARK_FILE], str) and keys <= _FILE_KEYS:
        return "file"
    if MARK_OBJECT in keys and isinstance(value[MARK_OBJECT], str) and keys <= _OBJECT_KEYS:
        return "object"
    if (
        MARK_MATTER in keys
        and isinstance(value[MARK_MATTER], str)
        and isinstance(value.get("context"), str)
        and value.get("consume") is True
        and keys <= _MATTER_KEYS
    ):
        return "matter"
    return None


def _is_scalar(value: Any) -> bool:
    return value is None or isinstance(value, (bool, int, float, str))


class ObjectTable:
    """Wrapper-local table of posted objects. Thread-safe.

    ``post`` stores an object and returns its reference. ``take`` removes it, once.
    Names are unique and never start with ``$`` (the DSL resolves such strings).
    """

    def __init__(self, wrapper_name: str, ttl_seconds: int = DEFAULT_TRANSIT_TTL_SECONDS) -> None:
        self.wrapper_name = wrapper_name
        self.ttl_seconds = ttl_seconds
        self._items: dict[str, tuple[float, str, Any]] = {}
        self._lock = threading.Lock()

    def post(self, obj: Any, type_tag: str = "") -> dict[str, Any]:
        name = "obj_" + uuid.uuid4().hex
        with self._lock:
            self._sweep_locked()
            self._items[name] = (time.monotonic(), type_tag, obj)
        return {MARK_OBJECT: name, "wrapper": self.wrapper_name, "type": type_tag}

    def take(self, ref: dict[str, Any], expect_type: str | None = None) -> Any:
        if ref.get("wrapper") != self.wrapper_name:
            raise TransitError(
                "wrong_wrapper",
                f"object reference belongs to wrapper {ref.get('wrapper')!r}, not {self.wrapper_name!r}",
            )
        name = ref[MARK_OBJECT]
        with self._lock:
            entry = self._items.get(name)
            if entry is None:
                raise TransitError("not_found", f"object {name!r} is not posted (already taken or never posted)")
            _, type_tag, obj = entry
            if expect_type is not None and expect_type != type_tag:
                raise TransitError(
                    "type_mismatch", f"object {name!r} has type {type_tag!r}, expected {expect_type!r}"
                )
            del self._items[name]
        return obj

    def discard(self, ref: dict[str, Any]) -> bool:
        """Remove a posted object without reading it. Returns whether it was present."""
        if ref.get("wrapper") != self.wrapper_name:
            return False
        with self._lock:
            return self._items.pop(ref[MARK_OBJECT], None) is not None

    def __len__(self) -> int:
        with self._lock:
            return len(self._items)

    def sweep_expired(self) -> int:
        with self._lock:
            return self._sweep_locked()

    def _sweep_locked(self) -> int:
        cutoff = time.monotonic() - self.ttl_seconds
        expired = [name for name, (created, _, _) in self._items.items() if created < cutoff]
        for name in expired:
            self._items.pop(name, None)
        return len(expired)


def _encode(value: Any, fmt: str) -> bytes:
    if fmt == FORMAT_JSON:
        try:
            # Match TransportClient's wire encoding so the 64 KiB inline decision
            # is based on the bytes that will actually enter the control message.
            return json.dumps(value, ensure_ascii=True, separators=(",", ":"), allow_nan=False).encode("utf-8")
        except (TypeError, ValueError) as exc:
            raise TransitError("invalid_reference", f"value is not JSON serializable: {exc}") from exc
    if fmt == FORMAT_TEXT:
        return str(value).encode("utf-8")
    if fmt == FORMAT_BYTES:
        return bytes(value)
    raise TransitError("invalid_reference", f"unknown transit format {fmt!r}")


def _decode(data: bytes, fmt: str) -> Any:
    try:
        if fmt == FORMAT_JSON:
            return json.loads(data.decode("utf-8"))
        if fmt == FORMAT_TEXT:
            return data.decode("utf-8")
    except (UnicodeDecodeError, ValueError) as exc:
        raise TransitError("unreadable", f"cannot decode transit file as {fmt}: {exc}") from exc
    if fmt == FORMAT_BYTES:
        return data
    raise TransitError("invalid_reference", f"unknown transit format {fmt!r}")


class FileTransit:
    """Transit files of one wrapper, in ``<boundary context dir>/tmp/wrapper_<name>/transit/``."""

    def __init__(
        self,
        directory: Path,
        allowed_root: Path | None = None,
        max_bytes: int = DEFAULT_MAX_TRANSIT_BYTES,
        ttl_seconds: int = DEFAULT_TRANSIT_TTL_SECONDS,
    ) -> None:
        self.directory = Path(directory)
        self.max_bytes = max_bytes
        self.ttl_seconds = ttl_seconds
        if allowed_root is not None:
            self.allowed_root = Path(allowed_root).resolve()
        elif (
            self.directory.name == TRANSIT_DIR_NAME
            and self.directory.parent.name.startswith("wrapper_")
            and self.directory.parent.parent.name == "tmp"
        ):
            self.allowed_root = self.directory.parent.parent.parent.resolve()
        else:
            # A non-canonical standalone directory authorizes itself only; never
            # infer a broad filesystem root from an unexpected layout.
            self.allowed_root = self.directory.resolve()

    def write(self, value: Any, fmt: str = FORMAT_JSON, *, encoded: bytes | None = None) -> dict[str, Any]:
        data = encoded if encoded is not None else _encode(value, fmt)
        if len(data) > self.max_bytes:
            raise TransitError(
                "unsupported",
                f"transit payload is {len(data)} bytes, limit is {self.max_bytes}; use a Matter",
            )
        if fmt not in _EXTENSIONS:
            raise TransitError("invalid_reference", f"unknown transit format {fmt!r}")
        self.directory.mkdir(parents=True, exist_ok=True)
        name = f"{TRANSIT_FILE_PREFIX}{uuid.uuid4().hex}.{_EXTENSIONS[fmt]}"
        final = self.directory / name
        pending = self.directory / (name + ".tmp")
        fd = os.open(pending, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        try:
            with os.fdopen(fd, "wb") as stream:
                stream.write(data)
        except BaseException:
            try:
                pending.unlink()
            except OSError:
                pass
            raise
        os.replace(pending, final)
        return {MARK_FILE: str(final.resolve()), "format": fmt}

    def _checked_path(self, ref: dict[str, Any]) -> Path:
        path = Path(ref[MARK_FILE])
        if not path.is_absolute():
            raise TransitError("invalid_reference", "an @file reference must be an absolute path")
        if (
            path.parent.name != TRANSIT_DIR_NAME
            or not path.parent.parent.name.startswith("wrapper_")
            or path.parent.parent.parent.name != "tmp"
            or not path.name.startswith(TRANSIT_FILE_PREFIX)
        ):
            # The receiver deletes what it reads: refuse anything that is not a transit file.
            raise TransitError("invalid_reference", "an @file reference must point to a transit file")
        current = Path(path.anchor)
        for part in path.parts[1:]:
            current /= part
            if current.is_symlink():
                raise TransitError(
                    "invalid_reference", "an @file reference must not contain a symbolic link"
                )
        resolved = path.resolve(strict=False)
        allowed = False
        for root in (self.allowed_root, self.directory.resolve()):
            try:
                resolved.relative_to(root)
                allowed = True
                break
            except ValueError:
                continue
        if not allowed:
            raise TransitError(
                "invalid_reference", "an @file reference must stay inside the instance context tree"
            )
        return resolved

    def read(self, ref: dict[str, Any]) -> Any:
        """Read and consume a transit file (it is deleted once decoded)."""
        path = self._checked_path(ref)
        fmt = str(ref.get("format") or FORMAT_JSON)
        try:
            size = path.stat().st_size
            if size > self.max_bytes:
                raise TransitError(
                    "unsupported",
                    f"transit file is {size} bytes, limit is {self.max_bytes}; use a Matter",
                )
            data = path.read_bytes()
            if len(data) > self.max_bytes:
                raise TransitError(
                    "unsupported",
                    f"transit file is {len(data)} bytes, limit is {self.max_bytes}; use a Matter",
                )
        except FileNotFoundError as exc:
            raise TransitError("not_found", f"transit file {path.name!r} does not exist (already consumed?)") from exc
        except OSError as exc:
            raise TransitError("unreadable", f"cannot read transit file {path.name!r}: {exc}") from exc
        value = _decode(data, fmt)
        try:
            path.unlink()
        except FileNotFoundError:
            pass
        return value

    def discard(self, ref: dict[str, Any]) -> bool:
        """Delete a transit file without reading it. Returns whether a file was removed."""
        try:
            path = self._checked_path(ref)
            path.unlink()
            return True
        except (TransitError, OSError):
            return False

    def sweep(self) -> int:
        """Delete every transit file left by a previous life of this wrapper."""
        removed = 0
        if not self.directory.is_dir():
            return 0
        for entry in self.directory.iterdir():
            if entry.is_file() and entry.name.startswith(TRANSIT_FILE_PREFIX):
                try:
                    entry.unlink()
                    removed += 1
                except OSError:
                    continue
        return removed

    def sweep_expired(self) -> int:
        removed = 0
        if not self.directory.is_dir():
            return 0
        cutoff = time.time() - self.ttl_seconds
        for entry in self.directory.iterdir():
            try:
                if (
                    entry.is_file()
                    and entry.name.startswith(TRANSIT_FILE_PREFIX)
                    and entry.stat().st_mtime < cutoff
                ):
                    entry.unlink()
                    removed += 1
            except OSError:
                continue
        return removed


class TransitService:
    """The two runtime operations of Wrapper.md §17.3: ``resolve`` (get) and ``emit``."""

    def __init__(
        self,
        wrapper_name: str,
        directory: Path,
        inline_limit: int = INLINE_LIMIT_BYTES,
        allowed_file_root: Path | None = None,
        max_bytes: int = DEFAULT_MAX_TRANSIT_BYTES,
        ttl_seconds: int = DEFAULT_TRANSIT_TTL_SECONDS,
        runtime: Any | None = None,
    ) -> None:
        self.objects = ObjectTable(wrapper_name, ttl_seconds)
        self.files = FileTransit(directory, allowed_file_root, max_bytes, ttl_seconds)
        self.inline_limit = inline_limit
        self.runtime = runtime
        self._next_expiry_sweep = 0.0
        # Return values are emitted for the caller only when the wrapper opts in: a caller written
        # before this section expects plain data, never a reference.
        self.emit_returns = False

    def sweep(self) -> int:
        return self.files.sweep()

    def resolve(self, raw: Any, expect_type: str | None = None) -> Any:
        """Return the value a parameter stands for, whatever its form."""
        self._sweep_expired_if_due()
        kind = reference_kind(raw)
        if kind == "file":
            return self.files.read(raw)
        if kind == "object":
            return self.objects.take(raw, expect_type)
        if kind == "matter":
            raise TransitError("unsupported", "a transient Matter must be resolved asynchronously")
        return raw

    def release(self, raw: Any) -> None:
        """Release a reference that was never consumed."""
        kind = reference_kind(raw)
        if kind == "file":
            self.files.discard(raw)
        elif kind == "object":
            self.objects.discard(raw)

    def emit(self, value: Any, *, target: TransitTarget, type_tag: str = "") -> Any:
        """Choose the form of an outgoing parameter or return value (chart B).

        The destination address, not hosted code, selects the representation.
        External Matter creation is asynchronous and is handled by ``emit_async``.
        """
        self._sweep_expired_if_due()
        if reference_kind(value) is not None or (_is_scalar(value) and not isinstance(value, str)):
            return value
        if isinstance(value, str):
            encoded = _encode(value, FORMAT_TEXT)
            if len(encoded) <= self.inline_limit:
                return value
            if target.same_wrapper:
                return self.objects.post(value, type_tag)
            if target.external:
                raise TransitError("unsupported", "external bulk transit requires asynchronous Matter emission")
            return self.files.write(None, FORMAT_TEXT, encoded=encoded)
        if target.same_wrapper:
            return self.objects.post(value, type_tag)
        if isinstance(value, (bytes, bytearray)):
            if target.external:
                raise TransitError("unsupported", "external bulk transit requires asynchronous Matter emission")
            return self.files.write(None, FORMAT_BYTES, encoded=bytes(value))
        encoded = _encode(value, FORMAT_JSON)
        if len(encoded) <= self.inline_limit:
            return value
        if target.external:
            raise TransitError("unsupported", "external bulk transit requires asynchronous Matter emission")
        return self.files.write(None, FORMAT_JSON, encoded=encoded)

    def _sweep_expired_if_due(self) -> None:
        now = time.monotonic()
        if now < self._next_expiry_sweep:
            return
        self._next_expiry_sweep = now + min(60, max(1, self.files.ttl_seconds // 4))
        self.objects.sweep_expired()
        self.files.sweep_expired()

    async def emit_async(self, value: Any, *, target: TransitTarget, type_tag: str = "") -> Any:
        """Run potentially heavy emission off-loop without leaking on cancellation."""
        if reference_kind(value) is not None or (_is_scalar(value) and not isinstance(value, str)):
            return self.emit(value, target=target, type_tag=type_tag)
        if target.same_wrapper and not isinstance(value, str):
            return self.emit(value, target=target, type_tag=type_tag)

        fmt = (
            FORMAT_BYTES
            if isinstance(value, (bytes, bytearray))
            else FORMAT_TEXT
            if isinstance(value, str)
            else FORMAT_JSON
        )
        encode_task = asyncio.create_task(asyncio.to_thread(_encode, value, fmt))
        try:
            encoded = await asyncio.shield(encode_task)
        except asyncio.CancelledError as cancelled:
            try:
                await encode_task
            except BaseException:
                pass
            raise cancelled

        if len(encoded) <= self.inline_limit and fmt != FORMAT_BYTES:
            return value
        if target.same_wrapper:
            return self.objects.post(value, type_tag)
        if target.external or len(encoded) > self.files.max_bytes:
            if self.runtime is None or not hasattr(self.runtime, "transit_matters"):
                raise TransitError("unsupported", "transient Matter transport is not configured; use a Matter")
            return await self.runtime.transit_matters.create(encoded, fmt, target)

        task = asyncio.create_task(asyncio.to_thread(self.files.write, None, fmt, encoded=encoded))
        try:
            return await asyncio.shield(task)
        except asyncio.CancelledError as cancelled:
            try:
                emitted = await task
            except BaseException:
                pass
            else:
                self.release(emitted)
            raise cancelled


class MatterRef(dict):
    """A semantic Matter parameter, as ``get`` returns it.

    ``get`` does not read semantic Matters. It only says that the parameter is a
    Matter and where it is (context, id, mode). The capacity handles it explicitly: it
    reads, writes or fails on it with the means a matter always required
    (Wrapper.md §10.1, §10.2), because a matter can come with a lot more than its
    content (revision, lease, locator, range reads, appends).
    """

    @property
    def context(self) -> str:
        return str(self.get("context", ""))

    @property
    def id(self) -> str:
        return str(self.get("id", ""))

    @property
    def mode(self) -> str:
        return str(self.get("mode", ""))


class Params(dict):
    """The incoming parameters of a capacity call, plus lazy resolution.

    It is a plain ``dict``: code written against the dictionary keeps working and
    sees references unresolved. ``await params.value(name)`` resolves a parameter
    that is a scalar, a small plain value, an ``@file`` or an ``@object``, and
    memoizes the result for the call (the ``get`` of Wrapper.md §17.3; ``dict.get``
    is already taken). A matter parameter comes back as a ``MatterRef``, unread.
    """

    def __init__(self, data: dict[str, Any], runtime: Any) -> None:
        super().__init__(data)
        self._runtime = runtime
        self._resolved: dict[str, Any] = {}
        self._consumed: set[str] = set()

    async def value(self, name: str, expect_type: str | None = None) -> Any:
        if name in self._resolved:
            return self._resolved[name]
        matters = dict.get(self, "@matter")
        if name in self and name != "@matter":
            raw = dict.__getitem__(self, name)
            kind = reference_kind(raw)
            if kind == "matter":
                result = await self._runtime.transit_matters.consume(raw)
            elif kind == "file":
                # Transit files may be large: disk I/O and decoding must not block
                # the wrapper's communication event loop.
                result = await asyncio.to_thread(self._runtime.transit.resolve, raw, expect_type)
            else:
                result = self._runtime.transit.resolve(raw, expect_type)
        elif isinstance(matters, dict) and name in matters:
            result = MatterRef(matters[name])
        else:
            raise TransitError("not_found", f"missing parameter {name!r}")
        self._resolved[name] = result
        self._consumed.add(name)
        return result

    def release_unconsumed(self) -> None:
        """Release the transit references of this call that no one consumed."""
        for name, raw in dict.items(self):
            if name in self._consumed or name == "@matter":
                continue
            self._runtime.transit.release(raw)

    async def release_unconsumed_async(self) -> None:
        """Release local references and guarded-delete unread transient Matters."""
        transient_matters: list[dict[str, Any]] = []
        for name, raw in dict.items(self):
            if name in self._consumed or name == "@matter":
                continue
            if reference_kind(raw) == "matter":
                transient_matters.append(raw)
            else:
                self._runtime.transit.release(raw)
        for ref in transient_matters:
            await self._runtime.transit_matters.discard_received(ref)
