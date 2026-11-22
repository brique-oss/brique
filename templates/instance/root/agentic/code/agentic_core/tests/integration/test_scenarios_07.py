import unittest

from .scenarios_base import SKIP, SKIP_REASON, Scenario, say


@unittest.skipIf(SKIP, SKIP_REASON)
class S21LargeSubstance(Scenario):
    """A continuity whose journal outgrows the 256 KB inline limit of matter.read stays fully usable:
    the substance is then served through an HTTP lease, which agentic_core reads transparently.

    Out of scope here: the generic wrapper transport refuses any inbound frame above 1 MiB (the websockets
    default) and then stops serving. Intentions and inference requests must stay below it."""
    PREFIX = "it_s21"

    async def test_journal_above_the_inline_limit(self):
        d, cid = self.d, self.PREFIX
        await d.reset_world([{"when": {"message_contains": "big-", "continuity": cid}, "then": {"output": say("noted {{call}}")}}])
        await d.create(cid, intention="Keep a very long history.")
        chunk = "big-" + "x" * 30000
        for i in range(1, 11):
            await d.send(cid, f"{cid}_m{i}", f"{chunk}{i}")
            await d.quiet(cid)
        journal = await d.journal(cid, frame=True)         # the driver reads through the lease as well
        size = sum(len(str(m["content"])) for m in journal)
        self.assertGreater(size, 256 * 1024)               # really above the inline limit
        self.assertEqual([m["seq"] for m in journal], list(range(1, len(journal) + 1)))
        self.assertEqual(self.kinds(journal).count("user"), 10)
        self.assertEqual(self.kinds(journal).count("pilot"), 10)   # every message was treated, before and after the limit
        await d.assert_invariants(cid)

