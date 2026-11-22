from __future__ import annotations

from support import Port

CAP = "test.open_gate"


# <brique:capacity name="test.open_gate">
async def test_open_gate(runtime, params: dict) -> dict:
    gate = str(params.get("gate", "")).strip()
    if not gate:
        raise ValueError("gate is required")

    def fn(f):
        opened = f.setdefault("open", [])
        if gate in opened:
            return None, True
        opened.append(gate)
        return f, True
    await Port(runtime, CAP).update("gates", fn)
    return {"gate": gate, "opened": True}
# </brique:capacity>
