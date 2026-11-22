import os
import unittest

from .driver import AGENTIC, Driver

SKIP = os.environ.get("BRIQUE_IT") != "1"
SKIP_REASON = "integration scenarios need the running engine with the agentic and test contexts started (set BRIQUE_IT=1)"


def say(text="ok"):
    return {"messages": [{"to": "user:1", "kind": "user", "content": text}]}


class Scenario(unittest.IsolatedAsyncioTestCase):
    PREFIX = "it_sxx"

    async def asyncSetUp(self):
        self.d = await Driver().__aenter__()
        await self.d.cleanup(*self.cids())          # leftovers of an interrupted previous run

    async def asyncTearDown(self):
        try:
            await self.d.cleanup(*self.cids())
        finally:
            await self.d.__aexit__()

    def cids(self):
        return [self.PREFIX]

    def contents(self, journal, kind):
        return [m["content"] for m in journal if m["kind"] == kind]

    def kinds(self, journal):
        """The kinds of the lines, without the context measures: bookkeeping written once per inference."""
        return [m["kind"] for m in journal if not (m["kind"] == "event" and (m.get("content") or {}).get("event") == "inference_measured")]
