from __future__ import annotations

from support import Port, record_tool_call

CAP = "world.get_inventory"


# <brique:capacity name="world.get_inventory">
async def world_get_inventory(runtime, params: dict) -> dict:
    await record_tool_call(Port(runtime, CAP), CAP, params, "done")
    raise RuntimeError("inventory service unavailable (HTTP 503)")
# </brique:capacity>
