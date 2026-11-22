"""Many creators and deleters of continuities write the same Structure (`continuities`) at once: they take turns, none of them fails for it.

Each one reads the Structure, then patches it with the revision it read; one wins per round and the others read again. With 12 attempts and no pause, forty
simultaneous creations made the last ones give up ("revision conflicts on structure continuities exhausted 12 attempts"), on the real engine as here.
"""
import asyncio
import unittest

from . import harness  # noqa: F401  (puts agentic_core on the path)
from fake_engine import make_engine
from substrate import cas
from substrate.errors import ConcurrencyError, RevisionMismatch


def adding(item):
    return lambda f: [] if item in f.get("items", []) else [{"op": "add", "path": ["functional", "items"], "value": item}]


class StructureWriters(unittest.IsolatedAsyncioTestCase):
    async def test_forty_simultaneous_writers_all_succeed_and_none_is_lost(self):
        engine = make_engine()
        results = await asyncio.gather(*[cas.patch_structure(engine.port("t"), "continuities", adding(f"m{i}")) for i in range(40)], return_exceptions=True)
        self.assertEqual([r for r in results if isinstance(r, BaseException)], [])
        self.assertEqual(sorted(engine.structures["continuities"]["functional"]["items"]), sorted(f"m{i}" for i in range(40)))
        self.assertEqual(engine.conflicts, 0)                    # inside one wrapper they take turns: no revision is fought over

    async def test_an_outside_writer_is_still_met_by_a_new_read(self):
        engine = make_engine()

        class Racing(type(engine.port("t"))):
            raced = False

            async def structure_patch(self, structure_id, semantic_patch, expected_revision):
                if not Racing.raced:                             # someone else (not this wrapper) writes between our read and our patch
                    Racing.raced = True
                    engine.structures[structure_id]["brique"]["revision"] = engine.next_rev()
                return await super().structure_patch(structure_id, semantic_patch, expected_revision)
        port = Racing(engine, "t")
        self.assertTrue(await cas.patch_structure(port, "continuities", adding("late")))
        self.assertIn("late", engine.structures["continuities"]["functional"]["items"])
        self.assertEqual(engine.conflicts, 1)

    async def test_a_conflict_that_never_clears_still_ends_with_an_error(self):
        engine = make_engine()

        class Always(type(engine.port("t"))):
            async def structure_patch(self, structure_id, semantic_patch, expected_revision):
                raise RevisionMismatch("revision mismatch")
        with self.assertRaises(ConcurrencyError):
            await cas.patch_structure(Always(engine, "t"), "continuities", adding("x"), attempts=4)
