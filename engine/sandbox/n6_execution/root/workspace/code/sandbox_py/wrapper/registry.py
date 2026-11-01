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

import importlib.util
import inspect
from pathlib import Path
from typing import Any

from .matter import build_matter_binding
from .models import CapacityBinding, MatterBinding


class RuntimeRegistry:
    def __init__(self, runtime: "WrapperRuntime") -> None:
        self.runtime = runtime
        self.capacity_bindings: dict[tuple[str, str], CapacityBinding] = {}
        self.matter_bindings: dict[tuple[str, str], MatterBinding] = {}
        self.shutdown_hooks: list[Any] = []

    async def load_bindings(self) -> None:
        fragment = await self._load_fragment(self.runtime.wrapper_src_dir / "bindings.py", "root")
        self.capacity_bindings = self._build_capacity_bindings(fragment)
        self.matter_bindings = self._build_matter_bindings(fragment)
        self.shutdown_hooks = self._build_shutdown_hooks(fragment)

    def resolve_capacity(self, relative_context: str, name: str) -> CapacityBinding | None:
        return self.capacity_bindings.get((relative_context, name))

    def resolve_matter(self, relative_context: str, name: str) -> MatterBinding | None:
        return self.matter_bindings.get((relative_context, name))

    async def _load_fragment(self, path: Path, label: str) -> dict[str, Any]:
        spec = importlib.util.spec_from_file_location(f"wrapper_binding_{label.replace('/', '_')}", path)
        if spec is None or spec.loader is None:
            raise RuntimeError(f"failed to load bindings module from {path}")
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        build_fn = getattr(module, "build_bindings", None)
        if not callable(build_fn):
            raise RuntimeError(f"{path} must export build_bindings(runtime)")
        fragment = build_fn(self.runtime)
        if inspect.isawaitable(fragment):
            fragment = await fragment
        if not isinstance(fragment, dict):
            raise RuntimeError(f"{path} build_bindings(runtime) must return a dict")
        return fragment

    def _build_capacity_bindings(self, fragment: dict[str, Any]) -> dict[tuple[str, str], CapacityBinding]:
        out: dict[tuple[str, str], CapacityBinding] = {}
        bindings = fragment.get("capacities", {})
        if not isinstance(bindings, dict):
            raise RuntimeError("bindings 'capacities' must be a dict")
        for key, symbol in bindings.items():
            if not isinstance(key, tuple) or len(key) != 2:
                raise RuntimeError(f"invalid capacity binding key {key!r}; expected (relative_context, name)")
            context_id = str(key[0]).strip().strip("/")
            cap_name = str(key[1]).strip()
            if not cap_name:
                raise RuntimeError(f"capacity binding {key!r} has an empty name")
            if not callable(symbol):
                raise RuntimeError(f"capacity binding {(context_id, cap_name)!r} must be callable")
            if (context_id, cap_name) in out:
                raise RuntimeError(f"duplicate capacity binding {(context_id, cap_name)!r}")
            out[(context_id, cap_name)] = CapacityBinding(
                context_id=context_id,
                name=cap_name,
                symbol=symbol,
            )
        return out

    def _build_matter_bindings(self, fragment: dict[str, Any]) -> dict[tuple[str, str], MatterBinding]:
        out: dict[tuple[str, str], MatterBinding] = {}
        bindings = fragment.get("matters", {})
        if not isinstance(bindings, dict):
            raise RuntimeError("bindings 'matters' must be a dict")
        for key, entry in bindings.items():
            if not isinstance(key, tuple) or len(key) != 2:
                raise RuntimeError(f"invalid matter binding key {key!r}; expected (relative_context, name)")
            context_id = str(key[0]).strip().strip("/")
            matter_name = str(key[1]).strip()
            if not matter_name:
                raise RuntimeError(f"matter binding {key!r} has an empty name")
            if not callable(entry) and not isinstance(entry, dict):
                raise RuntimeError(f"matter binding {(context_id, matter_name)!r} must be callable or a dict")
            if isinstance(entry, dict):
                for accessor in ("read", "write", "event_value"):
                    value = entry.get(accessor)
                    if value is not None and not callable(value):
                        raise RuntimeError(
                            f"matter binding {(context_id, matter_name)!r} accessor {accessor!r} must be callable"
                        )
                if entry.get("read") is None and entry.get("write") is None:
                    raise RuntimeError(
                        f"matter binding {(context_id, matter_name)!r} needs a read or write accessor"
                    )
            if (context_id, matter_name) in out:
                raise RuntimeError(f"duplicate matter binding {(context_id, matter_name)!r}")
            out[(context_id, matter_name)] = build_matter_binding(
                self.runtime,
                context_id=context_id,
                matter_name=matter_name,
                entry=entry,
            )
        return out

    def _build_shutdown_hooks(self, fragment: dict[str, Any]) -> list[Any]:
        out: list[Any] = []
        hooks = fragment.get("shutdown", [])
        if not isinstance(hooks, list):
            raise RuntimeError("bindings 'shutdown' must be a list")
        for index, hook in enumerate(hooks):
            if not callable(hook):
                raise RuntimeError(f"shutdown hook at index {index} must be callable")
            out.append(hook)
        return out
