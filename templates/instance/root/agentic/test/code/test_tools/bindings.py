from __future__ import annotations

import functools
import importlib.util
from pathlib import Path
from typing import Any

THIS_DIR = Path(__file__).resolve().parent


def _load(rel_path: str, symbol: str):
    p = THIS_DIR / rel_path
    spec = importlib.util.spec_from_file_location(p.stem, p)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return getattr(module, symbol)


test_echo = _load("test_echo.py", "test_echo")
test_slow = _load("test_slow.py", "test_slow")
test_open_gate = _load("test_open_gate.py", "test_open_gate")
test_fail = _load("test_fail.py", "test_fail")
infer_fake = _load("infer_fake.py", "infer_fake")
infer_recorded = _load("infer_recorded.py", "infer_recorded")
world_get_order = _load("world_get_order.py", "world_get_order")
world_get_customer = _load("world_get_customer.py", "world_get_customer")
world_get_inventory = _load("world_get_inventory.py", "world_get_inventory")
weather_fetch = _load("weather_fetch.py", "weather_fetch")


def build_bindings(runtime) -> dict[str, Any]:
    bind = functools.partial
    return {
        "capacities": {
            ("", "test.echo"): bind(test_echo, runtime),
            ("", "test.slow"): bind(test_slow, runtime),
            ("", "test.open_gate"): bind(test_open_gate, runtime),
            ("", "test.fail"): bind(test_fail, runtime),
            ("", "infer.fake"): bind(infer_fake, runtime),
            ("", "infer.recorded"): bind(infer_recorded, runtime),
            ("", "world.get_order"): bind(world_get_order, runtime),
            ("", "world.get_customer"): bind(world_get_customer, runtime),
            ("", "world.get_inventory"): bind(world_get_inventory, runtime),
            ("", "weather.fetch"): bind(weather_fetch, runtime),
        },
        "matters": {},
        "shutdown": [],
    }
