# Copyright 2026 Nicolas Cassan
# Licensed under the Apache License, Version 2.0.

"""The semantic Matter client of a wrapper: read, write, append and guarded update of the Matters of a context.

A capacity that keeps its state in Matters does the same few things: read a Matter whose substance is served inline or through a one-shot
HTTP lease, write it guarded by ``expected_revision``, append lines to a journal guarded by ``expected_line_count``, and replay a read-compute-write
that lost a race. This does them once. The rules are shared by the three runtimes and fixed by ``vectors/matter_client.json``.

It needs a running capacity (the calls it makes are children of the intention being served), and the destination context is the one of that
capacity unless one is given.
"""

from __future__ import annotations

import asyncio
import copy
import json
import random
import urllib.error
import urllib.parse
import urllib.request
from collections.abc import Awaitable, Callable
from typing import Any, NamedTuple

from . import settings
from . import errors
from .errors import CapacityCallError
from .constants import STATUS_ERROR, TYPE_MATTER
from .execution import current_intention

INLINE_APPEND_MAX = 240 * 1024       # matter.append accepts 256 KiB inline; a bigger block goes through its HTTP lease


# The errors of a Matter operation are the errors of a call (wrapper/errors.py); these names are kept for what already uses them.
MatterError = CapacityCallError
RevisionMismatch = errors.RevisionMismatch
LineCountMismatch = errors.LineCountMismatch
MatterBusy = errors.Busy
MatterNotFound = errors.NotFound
typed_error = errors.typed_error


class ConcurrencyExhausted(CapacityCallError):
    """A guarded update lost the race on every attempt."""


def _consistency(message: str, **details: Any) -> MatterError:
    return MatterError("conflict", message, {"reason": "consistency_failure", **details})


class MatterView(NamedTuple):
    """content is the parsed substance (a JSON object); descriptor is the functional section of the Matter element."""
    matter_id: str
    content: dict[str, Any]
    descriptor: dict[str, Any]
    brique: dict[str, Any]
    revision: int


class LinesView(NamedTuple):
    """A range of lines of a line-framed (JSON Lines) Matter, each line parsed, plus the committed dimensions."""
    lines: list[dict[str, Any]]
    total_lines: int
    revision: int
    offset: int


class Substance(NamedTuple):
    kind: str            # "inline" or "lease"
    text: str            # the text, for an inline substance
    url: str             # where to fetch it, for a lease


def revision_of(brique: Any) -> int:
    try:
        return int(brique.get("revision", 0))
    except (AttributeError, TypeError, ValueError):
        return 0


def lease_url(handle: dict[str, Any]) -> str:
    """The URL of a lease: the engine nests it under data.http (its descriptor documents the fields flat): both are accepted."""
    nested = handle.get("http")
    handle = nested if isinstance(nested, dict) else handle
    base, path, token = handle.get("base_url"), handle.get("path"), handle.get("tok")
    if not (isinstance(base, str) and isinstance(path, str) and isinstance(token, str) and base and path and token):
        raise _consistency("the Matter lease is malformed", cause="malformed_lease")
    return f"{base.rstrip('/')}{path}?{urllib.parse.urlencode({'tok': token})}"


def classify_substance(data: Any) -> Substance:
    if not isinstance(data, dict):
        raise _consistency("the Matter has no readable substance", cause="no_substance")
    if data.get("kind") == "http":
        return Substance("lease", "", lease_url(data))
    text = data.get("bytes")
    if not isinstance(text, str):
        raise _consistency("the Matter substance is not text", cause="not_text")
    return Substance("inline", text, "")


def parse_lines(text: Any) -> list[dict[str, Any]]:
    if text is None or text == "":
        return []
    if not isinstance(text, str):
        raise _consistency("the journal substance is not text", cause="not_text")
    out: list[dict[str, Any]] = []
    for raw in text.split("\n"):
        if not raw.strip():
            continue
        try:
            value = json.loads(raw)
        except ValueError as exc:
            raise _consistency("a journal line is not valid JSON", cause="invalid_line") from exc
        if not isinstance(value, dict):
            raise _consistency("a journal line is not a JSON object", cause="invalid_line")
        out.append(value)
    return out


def dump_lines(lines: list[dict[str, Any]]) -> str:
    """The block appended to a journal: one compact JSON document per line, each ending with LF."""
    return "".join(json.dumps(line, ensure_ascii=False, separators=(",", ":")) + "\n" for line in lines)


def guarded_write_pause_ms(attempt: int, rnd: float, base_ms: int | None = None, cap_ms: int | None = None) -> float:
    """The pause after a lost race, attempt number ``attempt`` (0 for the first): random up to a bound that doubles up to a cap."""
    base = settings.value("BRIQUE_CAS_PAUSE_BASE_MS") if base_ms is None else base_ms
    cap = settings.value("BRIQUE_CAS_PAUSE_CAP_MS") if cap_ms is None else cap_ms
    return rnd * min(cap, base * (2 ** min(attempt, 30)))


class _StaleLease(Exception):
    """The substance changed between matter.read and its HTTP fetch: read the Matter again."""


def _http_get(url: str, timeout_s: float) -> str:
    try:
        with urllib.request.urlopen(url, timeout=timeout_s) as response:       # one-shot read lease
            return response.read().decode("utf-8")
    except urllib.error.HTTPError as exc:
        if exc.code == 409:
            raise _StaleLease() from exc
        raise _consistency(f"the substance lease was refused (HTTP {exc.code})", status=exc.code) from exc
    except (urllib.error.URLError, OSError) as exc:
        raise _consistency(f"the substance lease could not be fetched: {exc}") from exc


def _http_put(url: str, body: bytes, timeout_s: float) -> None:
    request = urllib.request.Request(url, data=body, method="PUT")
    try:
        with urllib.request.urlopen(request, timeout=timeout_s):
            return
    except urllib.error.HTTPError as exc:
        if exc.code == 409:
            raise LineCountMismatch("refused", "the journal moved on while the append lease was in use",
                                    {"reason": "line_count_mismatch"}) from exc
        raise _consistency(f"the append lease was refused (HTTP {exc.code})", status=exc.code) from exc
    except (urllib.error.URLError, OSError) as exc:
        raise _consistency(f"the append lease could not be used: {exc}") from exc


Compute = Callable[[dict[str, Any]], "tuple[dict[str, Any] | None, Any]"]


Step = Callable[[], Awaitable[Any]]


async def retry_on_conflict(step: Step, *, conflict: tuple[type[BaseException], ...] = (RevisionMismatch,),
                            attempts: int | None = None, what: str = "") -> Any:
    """Run ``step`` (a read, a recomputation, a guarded write) and run it again after a short random pause when it raises one of ``conflict``:
    another writer got there first. The pause is random up to a bound that doubles up to a cap, so that writers that lost the same round do not
    come back together. ConcurrencyExhausted after BRIQUE_CAS_ATTEMPTS lost races (``attempts`` to say otherwise)."""
    attempts = settings.value("BRIQUE_CAS_ATTEMPTS") if attempts is None else max(1, attempts)
    for attempt in range(attempts):
        try:
            return await step()
        except conflict:
            await asyncio.sleep(guarded_write_pause_ms(attempt, random.random()) / 1000.0)
    raise ConcurrencyExhausted("conflict", f"revision conflicts on {what} exhausted {attempts} attempts",
                               {"reason": "concurrency_failure", "what": what, "attempts": attempts})


class MatterClient:
    """The Matters of one context, from inside a running capacity.

    ``context`` is where the Matters live; by default the context of the capacity being served. ``from_cap`` names the capacity the calls
    are made for when it is known here (the calls then carry no context of their own, and the engine stamps the one of the wrapper);
    without it the capacity is the one being served, found from the running intention.
    """

    def __init__(self, runtime: Any, context: str | None = None, *, from_cap: str | None = None, timeout_s: float | None = None) -> None:
        self.runtime = runtime
        self.context = context
        self.from_cap = from_cap
        self.timeout_s = timeout_s       # inactivity budget of a call, renewed by each `running` of the callee; None for the runtime's own

    # -- the call --------------------------------------------------------------
    def _origin(self) -> tuple[str, str, dict[str, str] | None]:
        if self.from_cap is not None:
            return "", self.from_cap, None
        intention = current_intention.get()
        if intention is None:
            raise RuntimeError("Matter operations need a running capacity")
        target = intention.get("to", {}) if isinstance(intention.get("to"), dict) else {}
        current_id = str(intention.get("intention_id", "")).strip()
        inherited = intention.get("correlation") if isinstance(intention.get("correlation"), dict) else {}
        return (
            self.runtime.target_context(intention),
            str(target.get("cap", "")).strip(),
            {"root_intention_id": str(inherited.get("root_intention_id", "")).strip() or current_id, "parent_intention_id": current_id},
        )

    async def _call(self, cap: str, params: dict[str, Any], context: str | None = None) -> dict[str, Any]:
        from_context, from_cap, correlation = self._origin()
        message = await self.runtime.outbound.emit_and_wait(
            to_context=context or self.context or from_context,
            to_cap=cap,
            to_type=TYPE_MATTER,
            from_context=from_context,
            from_cap=from_cap,
            params=params,
            correlation=correlation,
            timeout_s=self.timeout_s,
        )
        body = message.get("response") if isinstance(message.get("response"), dict) else {}
        if str(body.get("status", "")) == STATUS_ERROR:
            raise typed_error(body.get("error"))
        payload = body.get("payload")
        return payload if isinstance(payload, dict) else {}

    async def _text(self, data: Any) -> str | None:
        """The text of a substance; None when its lease went stale (the caller reads the Matter again)."""
        substance = classify_substance(data)
        if substance.kind == "inline":
            return substance.text
        timeout_s = settings.value("BRIQUE_LEASE_TIMEOUT_MS") / 1000.0
        try:
            return await asyncio.to_thread(_http_get, substance.url, timeout_s)
        except _StaleLease:
            return None

    @staticmethod
    def _lease_retries() -> int:
        return settings.value("BRIQUE_LEASE_RETRIES")

    @staticmethod
    def _content(matter_id: str, text: Any) -> dict[str, Any]:
        if not isinstance(text, str) or not text.strip():
            raise _consistency("the Matter substance is empty", matter_id=matter_id)
        try:
            value = json.loads(text)
        except ValueError as exc:
            raise _consistency("the Matter substance is not valid JSON", matter_id=matter_id) from exc
        if not isinstance(value, dict):
            raise _consistency("the Matter substance is not a JSON object", matter_id=matter_id)
        return value

    # -- reading ----------------------------------------------------------------
    async def _view(self, matter_id: str, payload: dict[str, Any]) -> MatterView | None:
        text = await self._text(payload.get("data"))
        if text is None:
            return None
        brique = payload.get("brique") or {}
        return MatterView(matter_id, self._content(matter_id, text), payload.get("functional") or {}, brique, revision_of(brique))

    async def read(self, matter_id: str, *, context: str | None = None) -> MatterView:
        """The parsed substance (a JSON object) with its revision: inline up to 256 KiB, through a one-shot HTTP lease beyond."""
        for _ in range(self._lease_retries()):
            payload = await self._call("matter.read", {"matter_id": matter_id, "read_mode": "functional|data|brique"}, context)
            view = await self._view(matter_id, payload)
            if view is not None:
                return view
        raise _consistency("the substance kept changing while it was being fetched", matter_id=matter_id)

    async def read_text(self, matter_id: str, *, context: str | None = None) -> str:
        """The substance exactly as the text it is, nothing parsed."""
        for _ in range(self._lease_retries()):
            payload = await self._call("matter.read", {"matter_id": matter_id, "read_mode": "data"}, context)
            text = await self._text(payload.get("data"))
            if text is not None:
                return text
        raise _consistency("the substance kept changing while it was being fetched", matter_id=matter_id)

    async def read_many(self, matter_ids: list[str], *, context: str | None = None) -> list[MatterView]:
        """Several Matters in ONE engine request, in the order given. A missing Matter is MatterNotFound, like ``read``."""
        payload = await self._call("matter.read_batch", {"matter_ids": list(matter_ids), "read_mode": "functional|data|brique"}, context)
        items = payload.get("result")
        if not isinstance(items, list) or len(items) != len(matter_ids):
            raise _consistency("the batch read answered another number of Matters than asked", matter_ids=list(matter_ids))
        views: list[MatterView] = []
        for matter_id, item in zip(matter_ids, items):
            if not isinstance(item, dict) or not item.get("ok"):
                raise typed_error(item.get("error") if isinstance(item, dict) else None)
            view = await self._view(matter_id, item)
            views.append(view if view is not None else await self.read(matter_id, context=context))      # a stale lease: read that Matter again
        return views

    async def exists(self, matter_id: str, *, context: str | None = None) -> bool:
        payload = await self._call("matter.exists", {"matter_id": matter_id}, context)
        return bool(payload.get("exist"))

    async def objective(self, matter_id: str, *, context: str | None = None) -> dict[str, Any]:
        """The objective part of the meaning of a Matter (its name, its type), without reading its substance."""
        payload = await self._call("matter.read", {"matter_id": matter_id, "read_mode": "meaning"}, context)
        meaning = payload.get("meaning")
        objective = meaning.get("objective") if isinstance(meaning, dict) else None
        return objective if isinstance(objective, dict) else {}

    # -- writing ----------------------------------------------------------------
    async def create(self, matter_id: str, descriptor: dict[str, Any], content: dict[str, Any] | None = None) -> int:
        """Create a Matter. With no content it is created with no substance (a journal: line-framed, empty)."""
        params: dict[str, Any] = {"matter_id": matter_id, "matter": descriptor}
        if content is not None:
            params["payload"] = json.dumps(content, ensure_ascii=False, indent=2)
        payload = await self._call("matter.create", params)
        return int(payload.get("revision", 0))

    async def write(self, matter_id: str, content: dict[str, Any], *, expected_revision: int | None = None) -> int:
        """One atomic write of the whole content, guarded by ``expected_revision`` (RevisionMismatch when the Matter moved on)."""
        params: dict[str, Any] = {"matter_id": matter_id, "data": json.dumps(content, ensure_ascii=False, indent=2)}
        if expected_revision is not None:
            # revisions are nanosecond timestamps (> 2**53): the decimal string is exact whatever the JSON number handling of the caller
            params["expected_revision"] = str(int(expected_revision))
        payload = await self._call("matter.write", params)
        return int(payload.get("revision", 0))

    async def delete(self, matter_id: str) -> None:
        await self._call("matter.delete", {"matter_id": matter_id})

    # -- line-framed Matters (a journal) ----------------------------------------------
    async def stat(self, matter_id: str) -> tuple[int, int]:
        """(revision, total_lines) of a line-framed Matter, without reading its substance."""
        payload = await self._call("matter.stat", {"matter_id": matter_id})
        return int(payload.get("revision", 0)), int(payload.get("total_lines", 0))

    async def read_lines(self, matter_id: str, offset: int, length: int) -> LinesView:
        """A range of lines (``offset`` is 0-based): inline up to 256 KiB, through a one-shot HTTP lease beyond."""
        params = {"matter_id": matter_id, "read_mode": "data|brique",
                  "data_range": {"unit": "lines", "offset": int(offset), "length": int(length)}}
        for _ in range(self._lease_retries()):
            payload = await self._call("matter.read", params)
            text = await self._text(payload.get("data"))
            if text is None:
                continue
            brique = payload.get("brique") or {}
            data = payload["data"]
            total = data.get("total_lines", brique.get("substance_line_count", 0))
            return LinesView(parse_lines(text), int(total or 0), revision_of(brique), int(offset))
        raise _consistency("the substance kept changing while it was being fetched", matter_id=matter_id)

    async def append_lines(self, matter_id: str, lines: list[dict[str, Any]], expected_line_count: int) -> int:
        """Append a block of lines in one atomic commit, guarded by ``expected_line_count``; returns the new total of lines
        (LineCountMismatch when the journal moved on). A block above the inline limit goes through the engine's HTTP lease."""
        text = dump_lines(lines)
        body = text.encode("utf-8")
        params: dict[str, Any] = {"matter_id": matter_id, "expected_line_count": int(expected_line_count)}
        if len(body) <= INLINE_APPEND_MAX:
            params["data"] = text
            payload = await self._call("matter.append", params)
            return int(payload.get("total_lines", int(expected_line_count) + len(lines)))
        params["http_data"] = True
        payload = await self._call("matter.append", params)
        timeout_s = settings.value("BRIQUE_LEASE_TIMEOUT_MS") / 1000.0
        await asyncio.to_thread(_http_put, lease_url(payload.get("http_data") or {}), body, timeout_s)
        return int(expected_line_count) + len(lines)

    # -- guarded update ------------------------------------------------------------
    async def update(self, matter_id: str, compute: Compute, *, attempts: int | None = None) -> Any:
        """Read, recompute, write guarded by ``expected_revision``, and start again after a short random pause when another writer got there first.

        ``compute(content)`` gets a copy of the content and returns ``(new_content, result)``; ``new_content`` None means there is nothing to
        write (the update is already done). Returns ``result``. ConcurrencyExhausted after BRIQUE_CAS_ATTEMPTS lost races. Run it inside
        ``scheduler.critical(...)`` when the writers of this process should take turns instead of racing.
        """

        async def step() -> Any:
            view = await self.read(matter_id)
            new_content, result = compute(copy.deepcopy(view.content))
            if new_content is not None:
                await self.write(matter_id, new_content, expected_revision=view.revision)
            return result

        return await retry_on_conflict(step, attempts=attempts, what=matter_id)
