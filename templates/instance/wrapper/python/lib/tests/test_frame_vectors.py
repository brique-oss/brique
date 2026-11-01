# Copyright 2026 Nicolas Cassan
# Licensed under the Apache License, Version 2.0.

import json
import pathlib
import unittest

from wrapper.router import MessageRouter

VECTORS = pathlib.Path(__file__).resolve().parents[2] / ".." / "vectors" / "wrapper_frames.json"


class FrameVectorTests(unittest.TestCase):
    """The shared corpus of wrapper frames (templates/instance/wrapper/vectors): this
    runtime must agree with the engine and with the Go and C++ runtimes."""

    def test_every_vector(self) -> None:
        corpus = json.loads(VECTORS.resolve().read_text(encoding="utf-8"))
        self.assertTrue(corpus["cases"])
        for case in corpus["cases"]:
            with self.subTest(case["name"]):
                decoded = MessageRouter.decode_frame(case["raw"])
                self.assertEqual(decoded is not None, case["accept"])
                if decoded is not None:
                    message, delegation = decoded
                    self.assertEqual(message.get("kind"), case["message_kind"])
                    self.assertEqual(delegation, case["delegation"])


if __name__ == "__main__":
    unittest.main()
