"""The launcher of pilots (capacity_pilot_launch.md): a pilot launches other pilots as sub-continuities."""
import unittest

from .driver import AGENTIC
from .driver import PILOT_REF
from .scenarios_base import SKIP, SKIP_REASON, say
from .test_scenarios_19 import PLAN_PILOT, AtelierScenario, awaited, intention


@unittest.skipIf(SKIP, SKIP_REASON)
class S81APilotLaunchesPilots(AtelierScenario):
    """A Matter pilot launches a Matter pilot and a Capacity pilot: two sub-continuities, referenced by their parent, each started by its first
    message; the launcher answers at once and the parent goes on."""
    PREFIX = "it_s81"

    def cids(self):
        return [self.PREFIX, f"{self.PREFIX}_a", f"{self.PREFIX}_b"]

    async def test_the_parent_launches_a_conversation_and_a_workflow(self):
        d, cid = self.d, self.PREFIX
        a, b = f"{cid}_a", f"{cid}_b"
        await self.reset_atelier()
        launch = lambda iid, child, pilot_ref, content: intention(iid, f"{AGENTIC}/pilot.launch", {   # noqa: E731
            "continuity_id": child, "pilot_ref": pilot_ref, "initial_intention": "Help your parent.", "parent_id": cid,
            "message": {"content": content}}, "user")
        await d.reset_world([
            {"when": {"continuity": cid, "stimulus": "active_wait_result"}, "then": {"output": say("deux pilotes lancés")}},
            {"when": {"continuity": cid, "message_id": f"{cid}_m1"},
             "then": {"output": awaited(launch("la", a, PILOT_REF, "dis bonjour"), launch("lb", b, PLAN_PILOT, {"client_id": "C-17"}))}},
            {"when": {"continuity": a}, "then": {"output": say("bonjour")}},
            {"when": {"step": "triage-commandes"}, "then": {"output": {"commande_active": "CMD-1042", "constats": ["CMD-1042 expédiée"],
                                                                       "prochaine_etape": "rien"}}},
        ])
        await d.create(cid, intention="Lancer des pilotes.")
        await d.send(cid, f"{cid}_m1", "lance un pilote de conversation et un workflow")
        await d.quiet(cid, a, b, timeout=60)
        await d.assert_invariants(cid, a, b)

        parent = await d.continuity(cid)
        self.assertEqual(sorted(parent["children"]), [a, b])                                # the parent's header references its daughters
        self.assertEqual([(await d.continuity(child))["parent_id"] for child in (a, b)], [cid, cid])
        self.assertEqual((await d.continuity(a))["pilot_ref"], PILOT_REF)
        self.assertEqual((await d.continuity(b))["pilot_ref"], PLAN_PILOT)

        # the first message of each daughter: a request from its parent
        first_a = next(m for m in await d.journal(a) if m["kind"] == "user")
        self.assertEqual((first_a["from"], first_a["content"], first_a["id"]), (f"continuity:{cid}", "dis bonjour", f"{a}_launch"))
        self.assertEqual([m["content"]["content"] for m in await d.journal(a) if m["kind"] == "pilot"], ["bonjour"])      # the conversation went on by itself
        self.assertEqual([m["content"]["step"] for m in await d.journal(b) if m["kind"] == "prompt"], ["triage-commandes"])   # and the workflow ran its plan

        # the parent's own journal says it launched them: the answer of each launch is the RESULT of the daughter (she finished the task before
        # the answer came), read from her journal, not a field she had to aim at the parent
        answers = {r["payload"]["continuity_id"]: r for r in (m["content"]["response"] for m in await d.journal(cid) if m["kind"] == "capacity_response")}
        self.assertEqual(sorted(answers), [a, b])
        self.assertTrue(all(r["status"] == "ok" and r["payload"]["created"] and r["payload"]["treated"] for r in answers.values()))
        self.assertEqual(answers[a]["payload"]["result"]["text"], "bonjour")
        self.assertEqual([m["content"]["content"] for m in await d.journal(cid) if m["kind"] == "pilot"], ["deux pilotes lancés"])


if __name__ == "__main__":
    unittest.main()
