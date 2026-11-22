import unittest

from substrate.journal import measure_line, usage_detail


class UsageDetail(unittest.TestCase):
    def test_window_is_stated_only_when_given_and_positive(self):
        usage = {"input_tokens": 10, "output_tokens": 1}
        self.assertEqual(usage_detail(usage, None, 200000)["window_tokens"], 200000)
        for bad in (None, 0, -5):
            self.assertNotIn("window_tokens", usage_detail(usage, None, bad))

    def test_uncached_is_never_negative_and_needs_a_cache_report(self):
        self.assertEqual(usage_detail({"input_tokens": 10, "cached_input_tokens": 30})["uncached_input_tokens"], 0)
        self.assertNotIn("uncached_input_tokens", usage_detail({"input_tokens": 10}))

    def test_an_estimated_measure_carries_no_breakdown(self):
        line = measure_line("c", "m1", 99, estimated=True, detail={"output_tokens": 3})
        self.assertEqual(line["content"], {"event": "inference_measured", "input_tokens": 99, "message_id": "m1", "estimated": True})


if __name__ == "__main__":
    unittest.main()
