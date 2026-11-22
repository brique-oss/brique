# Copyright 2026 Nicolas Cassan
# Licensed under the Apache License, Version 2.0.

import json
import unittest
from pathlib import Path

from wrapper import errors
from wrapper.calls import CapacityCallError

CASES = json.loads((Path(__file__).resolve().parents[2].parent / "vectors" / "errors.json").read_text(encoding="utf-8"))


class TypedErrors(unittest.TestCase):
    def test_the_classification_is_the_shared_one(self):
        for case in CASES["cases"]:
            error = errors.typed_error(case["error"])
            self.assertEqual((error.kind, error.outcome_unknown), (case["kind"], case["outcome_unknown"]), str(case))

    def test_every_typed_error_is_a_capacity_call_error(self):
        for case in CASES["cases"]:
            self.assertIsInstance(errors.typed_error(case["error"]), CapacityCallError)
        self.assertTrue(issubclass(errors.LineCountMismatch, errors.RevisionMismatch))

    def test_the_fields_of_the_response_are_kept(self):
        error = errors.typed_error({"code": "refused", "message": "wrapper pending-work queue is full", "details": {"reason": "busy", "n": 3}})
        self.assertIsInstance(error, errors.Busy)
        self.assertEqual((error.code, str(error), error.reason, error.details["n"]), ("refused", "wrapper pending-work queue is full", "busy", 3))

    def test_an_error_with_nothing_in_it_has_the_defaults(self):
        error = errors.typed_error(None)
        self.assertEqual((error.code, str(error)), (CASES["defaults"]["code"], CASES["defaults"]["message"]))

    def test_hosted_code_registers_its_own_reasons(self):
        class Parked(CapacityCallError):
            kind = "parked"

        self.addCleanup(errors._BY_REASON.pop, "message_parked", None)
        errors.register("message_parked", Parked)
        errors.register("message_parked", CapacityCallError)       # the first registration stays
        self.assertIsInstance(errors.typed_error({"code": "refused", "message": "m", "details": {"reason": "message_parked"}}), Parked)
        errors.register("busy", Parked)                             # a reason of the library is not replaced
        self.assertIsInstance(errors.typed_error({"details": {"reason": "busy"}}), errors.Busy)


if __name__ == "__main__":
    unittest.main()
