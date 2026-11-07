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

import asyncio
from dataclasses import dataclass, field
from typing import Any, Callable


# Set on a capacity by wrapper.serial; the registry reads it when it builds the binding.
SERIAL_ATTRIBUTE = "_brique_serial"


def serial(capacity: Callable[..., Any]) -> Callable[..., Any]:
    """Mark a capacity to run one call at a time, in arrival order.

    A capacity is parallel by default. Use this for one that cannot run two calls at once,
    and that never waits on another call of itself (it would wait for its own turn).
    """
    setattr(capacity, SERIAL_ATTRIBUTE, True)
    return capacity


@dataclass
class CapacityBinding:
    context_id: str
    name: str
    symbol: Callable[..., Any]
    # A capacity runs its calls in parallel, as many at once as the wrapper's concurrency allows.
    # serial=True makes it run one call at a time, in arrival order (see wrapper.serial).
    serial: bool = False


@dataclass
class MatterBinding:
    context_id: str
    name: str
    read_symbol: Callable[..., Any] | None = None
    write_symbol: Callable[..., Any] | None = None
    notify_on_write: bool = False
    event_value: Callable[..., Any] | None = None
    lock: asyncio.Lock = field(default_factory=asyncio.Lock)
