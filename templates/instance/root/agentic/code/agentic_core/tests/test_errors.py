import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from substrate import errors as E  # noqa: E402


def over_the_wire(error: E.AgenticError):
    """What typed() in bindings.py sends and from_engine_error receives: code, message and the reason in the details."""
    return E.from_engine_error({"code": error.code, "message": error.message, "details": {**error.details, "reason": error.reason}})


class ErrorsKeepTheirTypeThroughTheEngine(unittest.TestCase):
    def test_every_error_of_agentic_comes_back_as_the_class_it_was(self):
        for original in (E.InvalidRequest("bad"), E.NotFound("gone"), E.Refused("no"), E.ConsistencyError("contradiction"),
                         E.IdentityConflict("same id, other content"), E.ConcurrencyError("conflicts")):
            back = over_the_wire(original)
            self.assertIs(type(back), type(original), original)
            self.assertEqual(back.message, original.message)

    def test_an_identity_conflict_is_also_a_consistency_error(self):
        self.assertIsInstance(over_the_wire(E.IdentityConflict("x")), E.ConsistencyError)      # what tolerates a contradiction tolerates it

    def test_the_guards_of_the_matters_keep_their_own_classes(self):
        self.assertIsInstance(E.from_engine_error({"code": "refused", "message": "m", "details": {"reason": "line_count_mismatch"}}), E.LineCountMismatch)
        self.assertIsInstance(E.from_engine_error({"code": "refused", "message": "m", "details": {"reason": "revision_mismatch"}}), E.RevisionMismatch)

    def test_an_error_of_the_engine_itself_stays_an_engine_error(self):
        back = E.from_engine_error({"code": "refused", "message": "matter unknown", "details": {"reason": "matter_not_found"}})
        self.assertIs(type(back), E.EngineError)
        self.assertIs(type(E.from_engine_error({"code": "refused", "message": "no reason at all"})), E.EngineError)
        self.assertIs(type(E.from_engine_error({"code": "not_found", "message": "element missing"})), E.NotFound)       # by code, as before


if __name__ == "__main__":
    unittest.main()
