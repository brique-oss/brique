"""Every invariant and failure line of every capacity contract must be exercised by a @covers test,
or waived here with a reason. A @covers prefix that matches no contract line is a stale test."""
import importlib
import inspect
import unittest

from .harness import descriptor

CAPS = {
    "continuity.create": "test_continuity_create.ContinuityCreateContract",
    "continuity.delete": "test_continuity_delete.ContinuityDeleteContract",
    "continuity.project": "test_continuity_project.ContinuityProjectContract",
    "continuity.succeed": "test_continuity_succeed.ContinuitySucceedContract",
    "continuity.stranded": "test_continuity_stranded.ContinuityStrandedContract",
    "mailbox.send": "test_mailbox_send.MailboxSendContract",
    "mailbox.read": "test_mailbox_read.MailboxReadContract",
    "mailbox.ack": "test_mailbox_ack.MailboxAckContract",
    "mailbox.release": "test_mailbox_release.MailboxReleaseContract",
    "pilot.activate": "test_pilot_activate.PilotActivateContract",
    "pilot.infer": "test_pilot_infer.PilotInferContract",
    "input.request": "test_input_request.InputRequestContract",
    "input.answer": "test_input_request.InputAnswerContract",
}

# Lines that are shared wording of the concurrency model, covered once by the substrate tests.
SHARED = ("Mutating operations on one continuity", "Mutations of the Matters of one continuity are serialized by short critical-section locks",
          "Every write to a mutable Matter carries expected_revision", "Revision is opaque concurrency metadata",
          "The concurrency model of agentic_core is defined once")

# line prefix -> reason it is not a unit-testable behaviour of this capacity alone
WAIVED = {
    "continuity.delete": {
        "Active work is not interrupted in the middle of inference": "a safe-point rule owned by mailbox.send / pilot.activate (tested there: deferred controls)",
        "Does not pretend rollback when some external effect already existed.": "negative statement; no rollback exists in the code path (tests/contract: history untouched)",
        "Deletion requested during running or waiting is deferred until the applicable safe point.": "owned by mailbox.send (deferred control) and pilot.activate (safe point)",
    },
    "continuity.create": {},
    "continuity.project": {},
    "continuity.succeed": {},
    "continuity.stranded": {},
    "mailbox.send": {},
    "mailbox.read": {},
    "mailbox.ack": {},
    "mailbox.release": {},
    "pilot.activate": {},
    "pilot.infer": {},
    "input.request": {},
    "input.answer": {},
}


def lines(cap):
    tc = descriptor(cap)["transformation_contract"]
    return [t for key in ("invariants", "failure") for t in tc.get(key, []) if not t.startswith(SHARED)]


def collected(cap):
    module, cls = CAPS[cap].split(".")
    klass = getattr(importlib.import_module(f"tests.contract.{module}"), cls)
    return [p for _, fn in inspect.getmembers(klass, inspect.isfunction) for p in getattr(fn, "_covers", ())]


class ContractCoverage(unittest.TestCase):
    def test_every_contract_line_is_covered_or_waived(self):
        problems = []
        for cap in CAPS:
            prefixes = collected(cap)
            waived = WAIVED[cap]
            for line in lines(cap):
                covered = any(line.startswith(p) for p in prefixes)
                is_waived = any(line.startswith(w) for w in waived)
                if covered and is_waived:
                    problems.append(f"{cap}: waived but also covered: {line[:70]}")
                if not covered and not is_waived:
                    problems.append(f"{cap}: UNCOVERED: {line[:90]}")
            for p in prefixes:
                matches = [l for l in lines(cap) if l.startswith(p)]
                if not matches:
                    problems.append(f"{cap}: stale @covers prefix: {p[:70]}")
            for w in waived:
                if not any(l.startswith(w) for l in lines(cap)):
                    problems.append(f"{cap}: stale waiver: {w[:70]}")
        self.assertEqual(problems, [], "\n" + "\n".join(problems))


if __name__ == "__main__":
    unittest.main()
