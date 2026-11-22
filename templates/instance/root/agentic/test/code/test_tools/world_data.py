"""A tiny deterministic shop world used by the real-LLM tests. Test infrastructure only."""
from __future__ import annotations

ORDERS = {
    "A100": {"order_id": "A100", "customer_id": "C1", "status": "shipped", "carrier": "ColisExpress",
             "eta": "2026-10-14", "items": [{"sku": "KB-77", "name": "Mechanical keyboard", "qty": 1}], "total_eur": 129.0},
    "A200": {"order_id": "A200", "customer_id": "C2", "status": "delivered", "carrier": "ColisExpress",
             "delivered_on": "2026-10-02", "items": [{"sku": "MS-12", "name": "Wireless mouse", "qty": 2}], "total_eur": 58.0},
    "A300": {"order_id": "A300", "customer_id": "C1", "status": "delayed", "carrier": "PostNord",
             "eta": "2026-10-21", "delay_reason": "customs", "items": [{"sku": "MN-27", "name": "27-inch monitor", "qty": 1}], "total_eur": 349.0},
}

CUSTOMERS = {
    "C1": {"customer_id": "C1", "name": "Camille Durand", "tier": "gold", "email": "camille.durand@example.test", "open_orders": ["A100", "A300"]},
    "C2": {"customer_id": "C2", "name": "Yan Weber", "tier": "standard", "email": "yan.weber@example.test", "open_orders": []},
}
