"""The performance settings: the environment of the wrapper changes a limit with no change of code, and a value that cannot hold is ignored."""
import os
import unittest
from unittest import mock

from . import harness  # noqa: F401  (puts agentic_core on the path)
from substrate import cas, continuity_store, port as port_module
from substrate.settings import PREFIX, setting
import pilot_activate


class Setting(unittest.TestCase):
    def test_the_default_holds_when_nothing_is_set(self):
        with mock.patch.dict(os.environ, {}, clear=False):
            os.environ.pop(PREFIX + "X", None)
            self.assertEqual(setting("X", 8, 1), 8)

    def test_the_environment_changes_it_with_the_type_of_the_default(self):
        with mock.patch.dict(os.environ, {PREFIX + "I": "32", PREFIX + "F": "0.5"}):
            self.assertEqual(setting("I", 8, 1), 32)
            self.assertIsInstance(setting("I", 8, 1), int)
            self.assertEqual(setting("F", 1.0, 0.0), 0.5)
            self.assertIsInstance(setting("F", 1.0, 0.0), float)

    def test_a_value_that_cannot_hold_is_ignored(self):
        for bad in ("", "abc", "1.5x", "nan", "-3", "0"):
            with mock.patch.dict(os.environ, {PREFIX + "I": bad}):
                self.assertEqual(setting("I", 8, 1), 8, bad)
        with mock.patch.dict(os.environ, {PREFIX + "I": "2.5"}):
            self.assertEqual(setting("I", 8, 1), 8)          # an integer setting does not take a fraction

    def test_the_settings_keep_the_values_they_had_as_constants(self):
        # nothing is set in the tests: the defaults are the former constants, so no behaviour changed
        self.assertEqual((port_module.WAKE_ATTEMPTS,), (40,))
        self.assertEqual((port_module.DEFAULT_TIMEOUT_S,), (60.0,))
        self.assertEqual((continuity_store.MAX_OBJECTS, continuity_store.CHUNK_LINES), (512, 200))
        self.assertEqual((pilot_activate.AWAIT_TIMEOUT_S, pilot_activate.WAIT_TIMEOUT_S, pilot_activate.MAX_CHAIN_STEPS), (600.0, 3600.0, 50))
        self.assertEqual((pilot_activate.DELIVERY_ATTEMPTS, pilot_activate.DELIVERY_BACKOFF_S), (3, 0.2))     # POLL_S and RESERVATION_TTL_S are lowered by the harness
