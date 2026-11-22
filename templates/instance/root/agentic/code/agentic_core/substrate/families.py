"""The family of a capacity, when the code knows it for certain: what the pilot_definition declares, else what the name of an engine capacity says."""
from __future__ import annotations

from typing import Dict, Optional

# The capacities every context has: their names say their family.
ENGINE_FAMILIES = (("reflexive", ("read.", "meaning.", "vocabulary.", "edit.", "trace.")), ("matter", ("matter.", "structure.")),
                   ("execution", ("wrapper.",)), ("control", ("context.start", "context.restart", "context.stop")))


def family_of(ref: str, known: Dict[str, str]) -> Optional[str]:
    if ref in known:
        return known[ref]
    base = str(ref).rsplit("/", 1)[-1]
    for family, prefixes in ENGINE_FAMILIES:
        if base.startswith(prefixes):
            return family
    return None
