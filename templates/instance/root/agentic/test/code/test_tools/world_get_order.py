from __future__ import annotations

from support import Port, record_tool_call
from world_data import ORDERS

CAP = "world.get_order"


# <brique:capacity name="world.get_order">
async def world_get_order(runtime, params: dict) -> dict:
    await record_tool_call(Port(runtime, CAP), CAP, params, "done")
    order_id = str(params.get("order_id", "")).strip().upper()
    if not order_id:
        raise ValueError("order_id is required")
    if order_id not in ORDERS:
        raise LookupError(f"order {order_id} not found")
    return {"order": ORDERS[order_id]}
# </brique:capacity>
