"""The errors of a capacity cross the engine boundary with their own reason, and come back as the class they were (bindings.typed, errors.from_engine_error)."""
import asyncio
import importlib.util
import sys
import unittest

from . import harness  # noqa: F401  (puts agentic_core on the path)
from substrate.errors import AlreadyAnswered, MessageParked, RequestCancelled, RequestClosed, WaitTimeout, from_engine_error

LIB = harness.os.path.abspath(harness.os.path.join(harness.AGENT_CORE, "..", "..", "..", "..", "wrapper", "python", "lib"))      # the wrapper library of this instance
if LIB not in sys.path:
    sys.path.insert(0, LIB)


def load_bindings():
    path = harness.os.path.join(harness.os.path.dirname(harness.__file__), "..", "..", "bindings.py")
    spec = importlib.util.spec_from_file_location("bindings_under_test", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class TypedErrors(unittest.TestCase):
    def test_a_typed_reason_survives_the_engine_boundary_and_comes_back_as_its_class(self):
        from wrapper.calls import CapacityCallError
        bindings = load_bindings()
        for error_class, reason in ((MessageParked, "message_parked"), (WaitTimeout, "timeout"), (RequestCancelled, "cancelled"),
                                    (RequestClosed, "closed"), (AlreadyAnswered, "already_answered")):
            async def capacity(runtime, params, error_class=error_class):
                raise error_class("because", details={"continuity_id": "c1"})
            with self.assertRaises(CapacityCallError) as caught:
                asyncio.run(bindings.typed(capacity, object())({}))
            details = caught.exception.details if hasattr(caught.exception, "details") else {}
            self.assertEqual(details.get("reason"), reason)                      # not overwritten by the generic "refused"
            back = from_engine_error({"code": caught.exception.code, "message": "because", "details": details})
            self.assertIsInstance(back, error_class)                              # and the caller gets the class back
            self.assertEqual(back.reason, reason)


if __name__ == "__main__":
    unittest.main()
