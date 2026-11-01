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

from __future__ import annotations

from functools import wraps
from typing import Any

from .constants import MODE_WRAPPER
from .models import MatterBinding
from .util import invoke_callable


def build_matter_binding(
    runtime: "WrapperRuntime",
    *,
    context_id: str,
    matter_name: str,
    entry: Any,
) -> MatterBinding:
    normalized_context = str(context_id).strip().strip("/")
    normalized_name = str(matter_name).strip()
    read_symbol = None
    write_symbol = None
    notify_on_write = False
    event_value = None

    if isinstance(entry, dict):
        read_symbol = entry.get("read")
        write_symbol = entry.get("write")
        notify_on_write = bool(entry.get("notify_on_write", False))
        event_value = entry.get("event_value")
    elif callable(entry):
        read_symbol = entry

    if notify_on_write and write_symbol is not None:
        write_symbol = _wrap_notifying_writer(
            runtime,
            normalized_context,
            normalized_name,
            write_symbol,
            event_value=event_value,
        )

    return MatterBinding(
        context_id=normalized_context,
        name=normalized_name,
        read_symbol=read_symbol,
        write_symbol=write_symbol,
        notify_on_write=notify_on_write,
        event_value=event_value if callable(event_value) else None,
    )


def _wrap_notifying_writer(
    runtime: "WrapperRuntime",
    relative_context: str,
    matter_name: str,
    symbol: Any,
    *,
    event_value: Any = None,
):
    @wraps(symbol)
    async def wrapped(*args: Any, **kwargs: Any) -> Any:
        result = await invoke_callable(symbol, *args, **kwargs)
        notification_failed = False
        try:
            if callable(event_value):
                notify_value = await invoke_callable(
                    event_value, args=args, kwargs=kwargs, result=result
                )
            elif isinstance(result, dict) and "value" in result:
                notify_value = result.get("value")
            else:
                notify_value = args[0] if args else result
            notified = await runtime.notify_matter_written(
                relative_context,
                matter_name,
                value=notify_value,
            )
        except Exception as exc:
            notified = 0
            notification_failed = True
            logger = getattr(runtime, "log_exception", None)
            if callable(logger):
                logger(
                    "matter_notification_failed",
                    exc,
                    context=relative_context,
                    matter=matter_name,
                )

        if result is None:
            payload = {
                "ok": True,
                "matter_id": matter_name,
                "substance_mode": MODE_WRAPPER,
            }
        elif isinstance(result, dict):
            payload = dict(result)
            payload.setdefault("matter_id", matter_name)
            payload.setdefault("substance_mode", MODE_WRAPPER)
        else:
            payload = {
                "ok": True,
                "matter_id": matter_name,
                "substance_mode": MODE_WRAPPER,
                "value": result,
            }
        payload["notified"] = notified
        if notification_failed:
            payload["notification_failed"] = True
        return payload

    setattr(wrapped, "_brique_notifies_on_write", True)
    return wrapped
