from __future__ import annotations

import asyncio

from support import Port, record_tool_call

CAP = "test.slow"
POLL_S = 0.05
MAX_WAIT_S = 300.0
HEARTBEAT_S = 10.0


# <brique:capacity name="test.slow">
async def test_slow(runtime, params: dict) -> dict:
    gate = str(params.get("gate", "")).strip()
    if not gate:
        raise ValueError("gate is required")
    await runtime.notify_running()                  # a gate may stay closed long: say so at once
    port = Port(runtime, CAP)
    rank = await record_tool_call(port, CAP, {"gate": gate, "value": params.get("value")}, "start")
    waited = 0.0
    while waited < MAX_WAIT_S:
        state, _ = await port.read("gates")          # durable: a wrapper restart does not lose an opened gate
        if gate in state.get("open", []):
            await record_tool_call(port, CAP, {"gate": gate}, "done")
            return {"gate": gate, "value": params.get("value"), "call_index": rank}
        await asyncio.sleep(POLL_S)
        waited += POLL_S
        if int(waited / HEARTBEAT_S) != int((waited - POLL_S) / HEARTBEAT_S):
            await runtime.notify_running()          # heartbeat: the caller's wait budget is renewed
    raise TimeoutError(f"gate {gate} was never opened")
# </brique:capacity>
