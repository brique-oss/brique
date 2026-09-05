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

import inspect
from functools import wraps
from typing import Any, Callable

from .constants import MODE_WRAPPER
from .models import MatterBinding


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
    symbol: Callable[..., Any],
    *,
    event_value: Callable[..., Any] | None = None,
) -> Callable[..., Any]:
    @wraps(symbol)
    async def wrapped(*args: Any, **kwargs: Any) -> Any:
        result = symbol(*args, **kwargs)
        if inspect.isawaitable(result):
            result = await result
        if callable(event_value):
            notify_value = event_value(args=args, kwargs=kwargs, result=result)
        else:
            notify_value = _extract_notification_value(args, result)
        notified = await runtime.notify_matter_written(
            relative_context,
            matter_name,
            value=notify_value,
        )
        if result is None:
            return {
                "ok": True,
                "matter_id": matter_name,
                "substance_mode": MODE_WRAPPER,
                "notified": notified,
            }
        if isinstance(result, dict):
            payload = dict(result)
            payload.setdefault("matter_id", matter_name)
            payload.setdefault("substance_mode", MODE_WRAPPER)
            payload["notified"] = notified
            return payload
        return {
            "ok": True,
            "matter_id": matter_name,
            "substance_mode": MODE_WRAPPER,
            "value": result,
            "notified": notified,
        }

    return wrapped


def _extract_notification_value(args: tuple[Any, ...], result: Any) -> Any:
    if isinstance(result, dict) and "value" in result:
        return result.get("value")
    if args:
        return args[0]
    return result
