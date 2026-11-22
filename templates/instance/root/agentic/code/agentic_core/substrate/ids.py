from __future__ import annotations

import re
import uuid
from datetime import datetime, timezone
from typing import NamedTuple

from .errors import InvalidRequest

_ID_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_.\-]*$")

CONTEXT = "/root/agentic"
STRUCTURE_ID = "continuities"
# The Matter whose text opens every continuity: the cognitive frame of a pilot (kind framework).
FRAMEWORK_CONTEXT, FRAMEWORK_MATTER = "/root/agentic/prompt", "pilot_framework"


class MatterIds(NamedTuple):
    continuity: str     # the header
    mailbox: str
    runtime: str
    journal: str        # the durable stream of the continuity, JSON Lines


def check_continuity_id(continuity_id) -> str:
    """continuity_id is the logical key; it must be usable inside a Matter name."""
    value = str(continuity_id or "").strip()
    if not value or not _ID_RE.match(value) or ".." in value:
        raise InvalidRequest("continuity_id is missing or not a valid identifier", details={"continuity_id": continuity_id})
    return value


def matter_ids(continuity_id: str) -> MatterIds:
    cid = check_continuity_id(continuity_id)
    return MatterIds(f"continuity_{cid}", f"mailbox_{cid}", f"runtime_{cid}", f"journal_{cid}")


def now() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z")


def new_run_id() -> str:
    return f"run_{uuid.uuid4().hex[:16]}"


def new_id(prefix: str) -> str:
    return f"{prefix}_{uuid.uuid4().hex[:16]}"
