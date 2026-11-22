from __future__ import annotations

from support import Port, record_tool_call

CAP = "test.fail"


# <brique:capacity name="test.fail">
async def test_fail(runtime, params: dict) -> dict:
    await record_tool_call(Port(runtime, CAP), CAP, params, "done")
    raise RuntimeError(str(params.get("message") or "test.fail always fails"))
# </brique:capacity>
