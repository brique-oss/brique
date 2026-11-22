from __future__ import annotations

from support import Port, record_tool_call

CAP = "test.echo"


# <brique:capacity name="test.echo">
async def test_echo(runtime, params: dict) -> dict:
    port = Port(runtime, CAP)
    payload = await params.value("payload")             # a large payload arrives as a transit reference
    rank = await record_tool_call(port, CAP, payload, "done")
    return {"echo": payload, "call_index": rank}
# </brique:capacity>
