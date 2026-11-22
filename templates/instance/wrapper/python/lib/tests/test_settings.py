# Copyright 2026 Nicolas Cassan
# Licensed under the Apache License, Version 2.0.

import json
import os
import unittest
from pathlib import Path
from unittest import mock

from wrapper import settings

VECTOR = Path(__file__).resolve().parents[2].parent / "vectors" / "settings.json"
DECLARED = json.loads(VECTOR.read_text(encoding="utf-8"))["settings"]


class SettingsRegistry(unittest.TestCase):
    def test_the_declarations_are_the_shared_ones(self):
        self.assertEqual([row["name"] for row in DECLARED], [name for name in settings.SETTINGS if name in {r["name"] for r in DECLARED}])
        for row in DECLARED:
            declared = settings.SETTINGS[row["name"]]
            self.assertEqual((declared.default, declared.minimum, declared.policy), (row["default"], row["minimum"], row["policy"]), row["name"])
            self.assertTrue(row["what"] and row["unit"], row["name"])

    def test_nothing_set_means_the_defaults(self):
        with mock.patch.dict(os.environ, {}, clear=True):
            for row in DECLARED:
                self.assertEqual(settings.value(row["name"]), row["default"], row["name"])
            self.assertEqual(settings.not_default(), {})

    def test_a_tolerant_setting_ignores_what_cannot_hold(self):
        for bad in ("", "abc", "1.5", "-3", "0x10"):
            with mock.patch.dict(os.environ, {"BRIQUE_BUSY_ATTEMPTS": bad}):
                self.assertEqual(settings.value("BRIQUE_BUSY_ATTEMPTS"), 8, bad)
        with mock.patch.dict(os.environ, {"BRIQUE_BUSY_ATTEMPTS": "0"}):      # below its minimum of 1
            self.assertEqual(settings.value("BRIQUE_BUSY_ATTEMPTS"), 8)
        with mock.patch.dict(os.environ, {"BRIQUE_BUSY_BASE_MS": "0"}):       # 0 is allowed for this one
            self.assertEqual(settings.value("BRIQUE_BUSY_BASE_MS"), 0)

    def test_a_strict_setting_stops_the_start(self):
        for bad in ("abc", "0", "-1"):
            with mock.patch.dict(os.environ, {"BRIQUE_MAX_CONCURRENCY": bad}):
                with self.assertRaises(RuntimeError, msg=bad):
                    settings.value("BRIQUE_MAX_CONCURRENCY")

    def test_describe_says_where_each_value_comes_from(self):
        with mock.patch.dict(os.environ, {"BRIQUE_CAS_ATTEMPTS": "3", "BRIQUE_LEASE_RETRIES": "nope", "BRIQUE_MAX_CONCURRENCY": "zero"}):
            rows = {row["name"]: row for row in settings.describe()}
            self.assertEqual((rows["BRIQUE_CAS_ATTEMPTS"]["value"], rows["BRIQUE_CAS_ATTEMPTS"]["source"]), (3, "env"))
            self.assertEqual((rows["BRIQUE_LEASE_RETRIES"]["value"], rows["BRIQUE_LEASE_RETRIES"]["source"]), (5, "ignored"))
            self.assertEqual(rows["BRIQUE_MAX_CONCURRENCY"]["source"], "invalid")
            self.assertEqual(rows["BRIQUE_BUSY_CAP_MS"]["source"], "default")
            self.assertEqual(settings.not_default(), {"BRIQUE_CAS_ATTEMPTS": 3})

    def test_the_table_is_generated_from_the_shared_file(self):
        text = settings.table(str(VECTOR))
        self.assertEqual(len(text.splitlines()), len(DECLARED) + 2)
        self.assertIn("| `BRIQUE_BUSY_ATTEMPTS` | 8 | 1 | attempts | ignored |", text)
        self.assertIn("| `BRIQUE_QUEUE_LIMIT` | 256 | 1 | works | stops the start |", text)


class HostedSettings(unittest.TestCase):
    def setUp(self):
        self.addCleanup(settings.SETTINGS.pop, "BRIQUE_TEST_FRACTION", None)
        self.addCleanup(settings.SETTINGS.pop, "BRIQUE_TEST_COUNT", None)

    def test_a_declared_setting_has_the_type_of_its_default(self):
        settings.declare("BRIQUE_TEST_FRACTION", 0.5, 0.0, what="a fraction")
        settings.declare("BRIQUE_TEST_COUNT", 4, 1, what="a count")
        with mock.patch.dict(os.environ, {"BRIQUE_TEST_FRACTION": "0.25", "BRIQUE_TEST_COUNT": "7"}):
            self.assertEqual((settings.value("BRIQUE_TEST_FRACTION"), settings.value("BRIQUE_TEST_COUNT")), (0.25, 7))
            self.assertIsInstance(settings.value("BRIQUE_TEST_FRACTION"), float)
            self.assertEqual(settings.not_default()["BRIQUE_TEST_FRACTION"], 0.25)
        for bad in ("abc", "nan", "-1", "1.5"):
            with mock.patch.dict(os.environ, {"BRIQUE_TEST_COUNT": bad}):
                self.assertEqual(settings.value("BRIQUE_TEST_COUNT"), 4, bad)      # an integer setting does not take a fraction
        with mock.patch.dict(os.environ, {"BRIQUE_TEST_FRACTION": "nan"}):
            self.assertEqual(settings.value("BRIQUE_TEST_FRACTION"), 0.5)

    def test_the_registry_makes_its_own_table(self):
        settings.declare("BRIQUE_TEST_COUNT", 4, 1, what="a count")
        table = settings.registry_table("BRIQUE_TEST_")
        self.assertIn("| `BRIQUE_TEST_COUNT` | 4 | 1 | ignored | a count |", table)
        self.assertNotIn("BRIQUE_BUSY", table)

    def test_declaring_again_keeps_the_first_declaration(self):
        first = settings.declare("BRIQUE_TEST_COUNT", 4, 1)
        self.assertIs(settings.declare("BRIQUE_TEST_COUNT", 9, 0), first)


if __name__ == "__main__":
    unittest.main()
