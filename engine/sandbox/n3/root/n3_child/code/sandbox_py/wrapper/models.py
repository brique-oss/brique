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


@dataclass
class CapacityBinding:
    context_id: str
    name: str
    symbol: Callable[..., Any]


@dataclass
class MatterBinding:
    context_id: str
    name: str
    read_symbol: Callable[..., Any] | None = None
    write_symbol: Callable[..., Any] | None = None
    lock: asyncio.Lock = field(default_factory=asyncio.Lock)
