# Copyright 2026 Nicolas Cassan
# Licensed under the Apache License, Version 2.0.

"""The errors of a call, typed by what a caller reacts to.

An engine response with status error carries ``error``: a code, a message and ``details`` whose ``reason`` says what happened. ``typed_error``
turns it into the most specific class, so that a caller writes ``except Busy`` or ``except RevisionMismatch`` instead of comparing strings. The reason
decides first, the code when the reason says nothing known. The classification is shared by the three runtimes and fixed by ``vectors/errors.json``.

Every class is a ``CapacityCallError``: code that catches that keeps working. Hosted code that has errors of its own (a reason its capacities
answer with) registers them with ``register``.
"""

from __future__ import annotations

from collections.abc import Callable
from typing import Any


class CapacityCallError(Exception):
    """A call was answered with an error response."""

    kind = "other"
    outcome_unknown = False      # True when the call may have been executed: asking again blindly could do it twice

    def __init__(self, code: str, message: str, details: dict[str, Any] | None = None) -> None:
        super().__init__(message)
        self.code = code
        self.details = details or {}

    @property
    def reason(self) -> str:
        reason = self.details.get("reason", "")
        return reason if isinstance(reason, str) else ""


class Busy(CapacityCallError):
    """The callee refused the call BEFORE running it because it is busy (the engine's Matter loop, the pending-work queue of a wrapper): nothing was
    done, asking again is safe. The wrapper library already asked again; this is what is left when it stayed busy."""

    kind = "busy"


class RevisionMismatch(CapacityCallError):
    """The Matter changed since it was read: ``expected_revision`` no longer holds. Read it again."""

    kind = "revision_mismatch"


class LineCountMismatch(RevisionMismatch):
    """The journal has more lines than ``expected_line_count``."""

    kind = "line_count_mismatch"


class ConnectionLost(CapacityCallError):
    """The connection of the callee dropped under an awaited call: the call may have been executed, never ask again blindly."""

    kind = "connection_lost"
    outcome_unknown = True


class NotFound(CapacityCallError):
    kind = "not_found"


BUSY_REASONS = ("busy", "loop busy")

Factory = Callable[[str, str, dict[str, Any]], CapacityCallError]

_BY_REASON: dict[str, Factory] = {
    **{reason: Busy for reason in BUSY_REASONS},
    "line_count_mismatch": LineCountMismatch,
    "revision_mismatch": RevisionMismatch,
    "revision not coherent": RevisionMismatch,
    "connection_lost": ConnectionLost,
}
_BY_CODE: dict[str, Factory] = {"not_found": NotFound}


def register(reason: str, factory: Factory) -> None:
    """Make ``reason`` come back as the error ``factory(code, message, details)`` builds. A reason of the library is not replaced."""
    _BY_REASON.setdefault(reason, factory)


def typed_error(error: Any) -> CapacityCallError:
    """The most specific error for the error object of an engine response."""
    error = error if isinstance(error, dict) else {}
    code = str(error.get("code", "") or "internal")
    message = str(error.get("message", "") or "engine error")
    details = error.get("details") if isinstance(error.get("details"), dict) else {}
    reason = details.get("reason", "")
    factory = _BY_REASON.get(reason) if isinstance(reason, str) else None
    factory = factory or _BY_CODE.get(code) or CapacityCallError
    return factory(code, message, details)
