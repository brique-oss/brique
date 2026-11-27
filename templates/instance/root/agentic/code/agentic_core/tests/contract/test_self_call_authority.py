"""A run that agentic_core started itself acts with its own principal (substrate/authority.py); anyone else's call keeps its delegation."""
import asyncio
import os
import re
import unittest

from substrate import authority
from wrapper.execution import current_delegation, current_intention

HERE = os.path.dirname(os.path.abspath(__file__))


def served(origin, delegation="handle"):
    """What own_authority_when_self_called leaves as the delegation, in a fresh task context like the wrapper's."""
    async def body():
        current_intention.set({"intention_id": "i", "from": origin})
        current_delegation.set(delegation)
        dropped = authority.own_authority_when_self_called()

        async def child():
            return current_delegation.get()
        inherited = await asyncio.ensure_future(child())          # the tasks a run creates inherit the choice
        return dropped, current_delegation.get(), inherited
    return asyncio.run(body())


class SelfCallAuthority(unittest.TestCase):
    def test_a_call_agentic_core_made_to_itself_drops_the_delegation(self):
        self.assertEqual(served({"context": "/root/agentic", "cap": "mailbox.send", "type": "execution"}), (True, "", ""))

    def test_every_other_caller_keeps_its_delegation(self):
        for origin in ({"context": "@local_ui:/c1/ui", "cap": "panel"},
                       {"context": "/root/mcp", "cap": "mcp.reader.response"},
                       {"context": "/root/agentic/test", "cap": "mailbox.send"},
                       {"context": "/root/agentic", "cap": "a.plan.defined.here"},
                       {"context": "/root/pilots", "cap": "plan"},
                       {}, None):
            with self.subTest(origin=origin):
                self.assertEqual(served(origin), (False, "handle", "handle"))

    def test_nothing_happens_without_a_delegation(self):
        self.assertEqual(served({"context": "/root/agentic", "cap": "pilot.activate"}, delegation=""), (False, "", ""))

    def test_the_list_of_own_capacities_is_the_list_of_bindings(self):
        with open(os.path.join(HERE, "..", "..", "bindings.py"), encoding="utf-8") as handle:
            bound = set(re.findall(r'\("", "([a-z_.]+)"\)', handle.read()))
        self.assertEqual(bound, set(authority.OWN_CAPACITIES))


if __name__ == "__main__":
    unittest.main()
