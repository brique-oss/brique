"""The performance settings of agentic_core: limits, timeouts, retries and sizes that the load of an instance calls for tuning.

Each one is a constant of its module with a default, and the environment of the wrapper can change it, with no change of code: the variable
BRIQUE_AGENTIC_<NAME of the constant> in `run.env` of the wrapper in the context.json of /root/agentic (like BRIQUE_MAX_CONCURRENCY and
BRIQUE_QUEUE_LIMIT of the wrapper library). The engine reads that configuration when it starts: a change applies at its next start.

A value that is not a number of the type of the default, or that is below the minimum of the setting, is ignored: the default holds. The list of all
the settings, of the layers that have them, and of what they cost or buy is in document/performance_settings.md.
"""
from __future__ import annotations

from typing import Union

from wrapper import settings as registry

PREFIX = "BRIQUE_AGENTIC_"


def setting(name: str, default: Union[int, float], minimum: Union[int, float, None] = None, what: str = "") -> Union[int, float]:
    """Declare BRIQUE_AGENTIC_<name> in the registry of the wrapper library (so that it shows in the log of the start, in the diagnostic and in the
    generated table) and read it. A value that cannot hold is ignored: the default holds."""
    registry.declare(PREFIX + name, default, 0 if minimum is None else minimum, what=what)
    return registry.value(PREFIX + name)


if __name__ == "__main__":
    # python3 -m substrate.settings : the table of the settings of agentic_core, generated from their declarations
    import importlib
    import os
    import sys
    sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
    for module in ("substrate.port", "substrate.inference_window", "substrate.continuity_store",
                   "input_request", "pilot_activate", "pilot_launch"):
        importlib.import_module(module)
    print(registry.registry_table(PREFIX))
