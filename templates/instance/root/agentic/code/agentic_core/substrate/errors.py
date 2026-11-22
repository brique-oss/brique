from __future__ import annotations

from typing import Any, Optional


class AgenticError(Exception):
    """Base error of agentic_core. The message carries the code for the engine response."""

    code = "internal"
    reason = "agentic_failure"

    def __init__(self, message: str, *, details: Optional[dict] = None) -> None:
        super().__init__(f"{self.code}/{self.reason}: {message}")
        self.message = message
        self.details = details or {}


class InvalidRequest(AgenticError):
    code = "invalid"
    reason = "invalid_request"


class NotFound(AgenticError):
    code = "not_found"
    reason = "not_found"


class Refused(AgenticError):
    code = "refused"
    reason = "refused"


class MessageParked(Refused):
    """The pilot could not treat the message after its attempts and set it aside (an answer of pilot.launch that waited for the task)."""

    reason = "message_parked"


class WaitTimeout(Refused):
    """What was awaited did not come within its bound."""

    reason = "timeout"


class RequestCancelled(Refused):
    """The request has no addressee any more: its continuity was deleted or replaced while it waited."""

    reason = "cancelled"


class RequestClosed(Refused):
    """The request is closed (timed out or cancelled): it cannot be answered any more."""

    reason = "closed"


class AlreadyAnswered(Refused):
    """The request already has an answer: only the first valid answer counts."""

    reason = "already_answered"


class ConsistencyError(AgenticError):
    """An existing Matter or relation contradicts the expected state."""

    code = "conflict"
    reason = "consistency_failure"


class IdentityConflict(ConsistencyError):
    """A message id was reused with a different content: the first message is not the one the caller now describes."""

    reason = "identity_conflict"


class ConcurrencyError(AgenticError):
    """Bounded retries on revision conflict were exhausted."""

    code = "conflict"
    reason = "concurrency_failure"


class RevisionMismatch(AgenticError):
    code = "refused"
    reason = "revision_mismatch"


class LineCountMismatch(RevisionMismatch):
    """A journal append was guarded by expected_line_count and the journal has more lines than expected."""

    reason = "line_count_mismatch"


class BusyError(AgenticError):
    """The callee refused the call BEFORE running it because it is busy: the engine's Matter loop ("loop busy") or the pending-work queue of a wrapper
    ("busy"). Nothing was done, so asking again is safe for any call; it is not a refusal of substance (Refused)."""
    code = "unavailable"
    reason = "busy"


from wrapper.errors import BUSY_REASONS, typed_error as _classified      # the reasons and their classification are the ones of the wrapper library


class EngineError(AgenticError):
    """An engine response with status error that has no more specific mapping."""

    def __init__(self, code: str, reason: str, message: str, details: Optional[dict] = None) -> None:
        self.code = code or "internal"
        self.reason = reason or "engine_error"
        super().__init__(message, details=details)


BY_REASON = {cls.reason: cls for cls in (InvalidRequest, NotFound, Refused, ConsistencyError, IdentityConflict, ConcurrencyError,
                                         MessageParked, WaitTimeout, RequestCancelled, RequestClosed, AlreadyAnswered)}


def from_engine_error(error: Any) -> AgenticError:
    """Map the error object of an engine response to the most specific AgenticError."""
    error = error if isinstance(error, dict) else {}
    code = str(error.get("code", "") or "internal")
    message = str(error.get("message", "") or "engine error")
    details = error.get("details") if isinstance(error.get("details"), dict) else {}
    reason = str(details.get("reason", "") or "")
    kind = _classified(error).kind
    if kind == "busy":
        return BusyError(message, details=details)
    if kind == "line_count_mismatch":
        return LineCountMismatch(message, details=details)
    if kind == "revision_mismatch":
        return RevisionMismatch(message, details=details)
    # an error that a capacity of agentic_core raised went through the engine with its reason: it comes back as the class it was
    typed = BY_REASON.get(reason)
    if typed is not None:
        return typed(message, details=details)
    if code == "not_found":
        return NotFound(message, details=details)
    return EngineError(code, reason, message, details)
