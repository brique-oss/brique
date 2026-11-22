from __future__ import annotations

import functools
import importlib.util
import json
import os
import time
import traceback
from pathlib import Path
from typing import Any

THIS_DIR = Path(__file__).resolve().parent


def _load(rel_path: str, symbol: str):
    p = THIS_DIR / rel_path
    spec = importlib.util.spec_from_file_location(p.stem, p)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return getattr(module, symbol)


diagnostic = _load("diagnostic.py", "install")
diagnostic()          # kill -USR1 <pid of the wrapper> writes what every task awaits (see diagnostic.py)

continuity_create = _load("continuity_create.py", "continuity_create")
continuity_delete = _load("continuity_delete.py", "continuity_delete")
continuity_project = _load("continuity_project.py", "continuity_project")
continuity_stranded = _load("continuity_stranded.py", "continuity_stranded")
continuity_succeed = _load("continuity_succeed.py", "continuity_succeed")
input_answer = _load("input_answer.py", "input_answer")
input_request = _load("input_request.py", "input_request")
mailbox_send = _load("mailbox_send.py", "mailbox_send")
mailbox_read = _load("mailbox_read.py", "mailbox_read")
mailbox_ack = _load("mailbox_ack.py", "mailbox_ack")
mailbox_release = _load("mailbox_release.py", "mailbox_release")
pilot_activate = _load("pilot_activate.py", "pilot_activate")
pilot_infer = _load("pilot_infer.py", "pilot_infer")
pilot_launch = _load("pilot_launch.py", "pilot_launch")
reading_mark = _load("reading_mark.py", "reading_mark")


def record_unexpected(name: str, exc: BaseException) -> None:
    """The detail of an unexpected failure goes to a file of the wrapper's working directory: the process error stream may be discarded."""
    try:
        path = Path(os.environ.get("BRIQUE_WORKDIR") or ".") / "unexpected_errors.jsonl"
        line = {"at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), "capacity": name, "type": type(exc).__name__,
                "error": str(exc), "traceback": traceback.format_exc()}
        with path.open("a", encoding="utf-8") as handle:
            handle.write(json.dumps(line, ensure_ascii=False) + "\n")
    except Exception:
        pass


def typed(capacity, runtime):
    """Bind a capacity so that its expected errors reach the caller as typed errors.

    An AgenticError carries the code, the reason and the details the caller maps back to the most
    specific error (substrate.errors.from_engine_error). Any other exception stays unexpected: the
    wrapper library answers it with a generic failure and an error_id, and keeps the detail in its log.
    """
    from substrate.errors import AgenticError
    from wrapper.calls import CapacityCallError

    async def run(params):
        try:
            return await capacity(runtime, params)
        except AgenticError as err:
            raise CapacityCallError(err.code, err.message, {**err.details, "reason": err.reason}) from err
        except CapacityCallError:
            raise
        except Exception as exc:
            record_unexpected(getattr(capacity, "__name__", "capacity"), exc)
            raise

    return run


def build_bindings(runtime) -> dict[str, Any]:
    bind = lambda capacity, rt: typed(capacity, rt)   # noqa: E731
    return {
        "capacities": {
            ("", "continuity.create"): bind(continuity_create, runtime),
            ("", "continuity.delete"): bind(continuity_delete, runtime),
            ("", "continuity.project"): bind(continuity_project, runtime),
            ("", "continuity.succeed"): bind(continuity_succeed, runtime),
            ("", "continuity.stranded"): bind(continuity_stranded, runtime),
            ("", "input.answer"): bind(input_answer, runtime),
            ("", "input.request"): bind(input_request, runtime),
            ("", "mailbox.send"): bind(mailbox_send, runtime),
            ("", "mailbox.read"): bind(mailbox_read, runtime),
            ("", "mailbox.ack"): bind(mailbox_ack, runtime),
            ("", "mailbox.release"): bind(mailbox_release, runtime),
            ("", "pilot.activate"): bind(pilot_activate, runtime),
            ("", "pilot.infer"): bind(pilot_infer, runtime),
            ("", "pilot.launch"): bind(pilot_launch, runtime),
            ("", "reading.mark"): bind(reading_mark, runtime),
        },
        "matters": {},
        "shutdown": [],
    }
