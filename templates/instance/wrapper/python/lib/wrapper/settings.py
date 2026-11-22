# Copyright 2026 Nicolas Cassan
# Licensed under the Apache License, Version 2.0.

"""The numeric settings of the wrapper runtime, read from the environment (BRIQUE_<NAME>).

Every setting is declared here, once, with its default, its minimum and what an invalid value does: the policy ``strict`` stops the wrapper at
start, the policy ``tolerant`` ignores the value and uses the default. The declarations are the same in the three runtimes: ``vectors/settings.json``
holds them with what each one is for, every runtime's test compares its own to it, and the table of the settings is generated from that file:

    python3 -m wrapper.settings table ../../vectors/settings.json

``describe()`` says what a running wrapper really uses, so that a setting that did not take effect (a variable the engine did not pass on) shows.
"""

from __future__ import annotations

import json
import os
import sys
from typing import Any, NamedTuple


class Setting(NamedTuple):
    name: str
    default: int | float
    minimum: int | float
    policy: str       # "strict" or "tolerant"
    what: str = ""    # what it sets, for the generated table (the shared file holds it for the built-in ones)


SETTINGS: dict[str, Setting] = {
    setting.name: setting
    for setting in (
        Setting("BRIQUE_MAX_CONCURRENCY", 16, 1, "strict"),
        Setting("BRIQUE_QUEUE_LIMIT", 256, 1, "strict"),
        Setting("BRIQUE_TRANSIT_MAX_BYTES", 64 * 1024 * 1024, 1, "strict"),
        Setting("BRIQUE_TRANSIT_TTL_SECONDS", 60 * 60, 1, "strict"),
        Setting("BRIQUE_BUSY_ATTEMPTS", 8, 1, "tolerant"),
        Setting("BRIQUE_BUSY_BASE_MS", 20, 0, "tolerant"),
        Setting("BRIQUE_BUSY_CAP_MS", 1000, 0, "tolerant"),
        Setting("BRIQUE_LEASE_TIMEOUT_MS", 30000, 1, "tolerant"),
        Setting("BRIQUE_LEASE_RETRIES", 5, 1, "tolerant"),
        Setting("BRIQUE_CAS_ATTEMPTS", 12, 1, "tolerant"),
        Setting("BRIQUE_CAS_PAUSE_BASE_MS", 2, 0, "tolerant"),
        Setting("BRIQUE_CAS_PAUSE_CAP_MS", 50, 0, "tolerant"),
        Setting("BRIQUE_HEARTBEAT_MS", 10000, 50, "tolerant"),
    )
}


def declare(name: str, default: int | float, minimum: int | float = 0, *, strict: bool = False, what: str = "") -> Setting:
    """Declare a setting of hosted code, so that it shows in ``describe()``, in the log of the start and in the diagnostic like the built-in ones.

    The value read has the type of ``default`` (a float default reads a float). Declaring a name again keeps the first declaration."""
    existing = SETTINGS.get(name)
    if existing is None:
        existing = SETTINGS[name] = Setting(name, default, minimum, "strict" if strict else "tolerant", what)
    return existing


def _read(setting: Setting) -> tuple[Any, str]:
    """(value, source) where source is "default", "env" or "ignored" (a value was given and could not be used)."""
    raw = os.environ.get(setting.name, "").strip()
    if not raw:
        return setting.default, "default"
    try:
        number = type(setting.default)(raw)
    except (TypeError, ValueError):
        number = None
    if number is not None and number == number and number >= setting.minimum:       # not NaN, not below the minimum
        return number, "env"
    if setting.policy == "strict":
        raise RuntimeError(f"{setting.name} must be {'a positive integer' if setting.minimum == 1 else f'an integer of {setting.minimum} or more'}")
    return setting.default, "ignored"


def value(name: str) -> Any:
    """The value of a declared setting; RuntimeError for an invalid value of a strict one."""
    return _read(SETTINGS[name])[0]


def describe() -> list[dict[str, Any]]:
    """What the running process uses for each setting, and where it comes from."""
    rows = []
    for setting in SETTINGS.values():
        try:
            used, source = _read(setting)
        except RuntimeError:
            used, source = setting.default, "invalid"
        rows.append({"name": setting.name, "default": setting.default, "minimum": setting.minimum, "policy": setting.policy, "value": used, "source": source})
    return rows


def not_default() -> dict[str, Any]:
    """The settings that are not at their default, for the log of the start and the diagnostic."""
    return {row["name"]: row["value"] for row in describe() if row["source"] != "default" and row["value"] != row["default"]}


def registry_table(prefix: str = "") -> str:
    """The table of the settings declared in this process whose name starts with ``prefix``, in Markdown, from their declarations."""
    lines = ["| Variable | Default | Min. | Invalid value | What it sets |", "|---|---|---|---|---|"]
    for setting in SETTINGS.values():
        if setting.name.startswith(prefix):
            policy = "stops the start" if setting.policy == "strict" else "ignored"
            lines.append(f"| `{setting.name}` | {setting.default} | {setting.minimum} | {policy} | {setting.what} |")
    return "\n".join(lines)


def table(vector_path: str) -> str:
    """The table of the settings, in Markdown, from the shared declarations."""
    with open(vector_path, encoding="utf-8") as handle:
        rows = json.load(handle)["settings"]
    lines = ["| Variable | Default | Min. | Unit | Invalid value | What it sets |", "|---|---|---|---|---|---|"]
    for row in rows:
        policy = "stops the start" if row["policy"] == "strict" else "ignored"
        lines.append(f"| `{row['name']}` | {row['default']} | {row['minimum']} | {row['unit']} | {policy} | {row['what']} |")
    return "\n".join(lines)


if __name__ == "__main__":
    if len(sys.argv) == 3 and sys.argv[1] == "table":
        print(table(sys.argv[2]))
    else:
        print(json.dumps(describe(), indent=2))
