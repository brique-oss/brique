from __future__ import annotations

from support import Port, record_tool_call
from world_data import CUSTOMERS

CAP = "world.get_customer"


# <brique:capacity name="world.get_customer">
async def world_get_customer(runtime, params: dict) -> dict:
    await record_tool_call(Port(runtime, CAP), CAP, params, "done")
    customer_id = str(params.get("customer_id", "")).strip().upper()
    if not customer_id:
        raise ValueError("customer_id is required")
    if customer_id not in CUSTOMERS:
        raise LookupError(f"customer {customer_id} not found")
    return {"customer": CUSTOMERS[customer_id]}
# </brique:capacity>
