"""Substrate of the agentic_core wrapper.

Infrastructure shared by the capacity files: no Brique binding lives here.
It holds the access port to the engine, the optimistic-concurrency loop, the
per-continuity critical-section locks and the schema validation that the
engine itself does not perform.
"""

import importlib.util
import os
import sys
from pathlib import Path


def _ensure_wrapper_library() -> None:
    """The substrate uses the runtime library of the instance (locks, Matter client, diagnostic): put it on the path when the process did not.

    main.py does it for the wrapper; this does it for anything else that imports the substrate (the tests). BRIQUE_WRAPPER_LIB overrides the lookup.
    """
    if importlib.util.find_spec("wrapper") is not None:
        return
    override = os.environ.get("BRIQUE_WRAPPER_LIB", "").strip()
    if override:
        sys.path.insert(0, str(Path(override).resolve()))
        return
    here = Path(__file__).resolve().parent
    for ancestor in here.parents:
        candidate = ancestor / "wrapper" / "python" / "lib"
        if (candidate / "wrapper" / "__init__.py").is_file():
            sys.path.insert(0, str(candidate))
            return


_ensure_wrapper_library()
