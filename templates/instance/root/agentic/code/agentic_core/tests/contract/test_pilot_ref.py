import unittest

from substrate import pilot_ref
from substrate.errors import InvalidRequest


class PilotRefPolicyAndLimits(unittest.TestCase):
    def test_a_pilot_definition_may_live_in_any_context(self):
        self.assertEqual(pilot_ref.normalize({"matter_id": "pilot_x"}), {"context": "/root/agentic", "matter_id": "pilot_x"})        # the default
        self.assertEqual(pilot_ref.normalize({"context": "/root/agentic/test/", "matter_id": " it_pilot "}), {"context": "/root/agentic/test", "matter_id": "it_pilot"})
        for bad in ("agentic", "/root/*", "/root/../x", 3):
            with self.assertRaises(InvalidRequest):
                pilot_ref.normalize({"context": bad, "matter_id": "pilot_x"})

    def test_a_capacity_pilot_carries_its_policy_and_its_window(self):
        ref = pilot_ref.normalize({"context": "/root/shop", "capacity": "plan", "allowed_capacities": [" /root/vocabulary.* "], "forbidden_capacities": ["/root/edit.*"],
                                   "limits": {"context_tokens": 1000, "compaction_required_ratio": 0.9, "wait_timeout_s": 60}})
        self.assertEqual((ref["allowed_capacities"], ref["forbidden_capacities"]), (["/root/vocabulary.*"], ["/root/edit.*"]))
        self.assertEqual(ref["limits"]["context_tokens"], 1000)

    def test_a_pilot_definition_carries_its_own_policy_and_limits(self):
        for key, value in (("allowed_capacities", ["/root/x"]), ("forbidden_capacities", ["/root/x"]), ("limits", {"timeout_s": 5})):
            with self.assertRaises(InvalidRequest):
                pilot_ref.normalize({"matter_id": "pilot_x", key: value})

    def test_contexts_are_canonical_absolute_paths(self):
        ref = pilot_ref.normalize({"context": "/root/shop", "capacity": "plan", "allowed_contexts": ["/root/shop/", " /root/music "], "forbidden_contexts": ["/root/agentic"]})
        self.assertEqual((ref["allowed_contexts"], ref["forbidden_contexts"]), (["/root/shop", "/root/music"], ["/root/agentic"]))
        for bad in ("root/shop", "/root/*", "/root/./shop", "/root//shop", "/root/shop/../x", 3):
            with self.assertRaises(InvalidRequest):
                pilot_ref.normalize({"context": "/root/shop", "capacity": "plan", "forbidden_contexts": [bad]})
        with self.assertRaises(InvalidRequest):                                                      # a pilot_definition carries its own
            pilot_ref.normalize({"matter_id": "pilot_x", "forbidden_contexts": ["/root/shop"]})

    def test_what_is_not_a_pattern_or_a_window_is_refused(self):
        base = {"context": "/root/shop", "capacity": "plan"}
        for bad in ({"allowed_capacities": "/root/x"}, {"forbidden_capacities": [""]}, {"forbidden_capacities": [3]},
                    {"limits": {"context_tokens": 1.5}}, {"limits": {"compaction_advised_ratio": 2}}, {"limits": {"bogus": 1}}):
            with self.assertRaises(InvalidRequest):
                pilot_ref.normalize(dict(base, **bad))


if __name__ == "__main__":
    unittest.main()
