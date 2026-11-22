"""Real-LLM scenarios (R01..R06). Run with BRIQUE_REAL_LLM=1; BRIQUE_REAL_RUNS=N repeats each scenario.

The judgement is structural: capacity called, parameters used, facts present in the final answer,
number of inferences. Wording is never asserted exactly.
"""
import re
import unittest

from ..integration.driver import DriverError
from .harness import (PILOT_ID, QUIET_TIMEOUT, REAL, RUNS, SKIP_REASON, Run, RealDriver, contains_any, measure, pilot_texts, summarize,
                      write_report)

ALL_RUNS = []


def called(run: Run, tool: str, **params) -> int:
    return sum(1 for c in run.facts["tool_calls"]
               if c["tool"] == tool and all(str((c["params"] or {}).get(k, "")).upper() == str(v).upper() for k, v in params.items()))


@unittest.skipUnless(REAL, SKIP_REASON)
class RealScenarios(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.d = await RealDriver().__aenter__()

    async def asyncTearDown(self):
        try:
            await self.d.cleanup()
        finally:
            await self.d.__aexit__()
            if ALL_RUNS:
                write_report(ALL_RUNS)

    async def play(self, scenario: str, messages, judge, pilot_ref=None):
        """messages: list of user texts sent one after the other in ONE continuity; judge(run, i) after each."""
        runs = []
        for k in range(RUNS):
            d, run = self.d, Run(scenario, k)
            cid = f"real_{scenario.lower()}_{k}"
            await d.cleanup(cid)
            await d.reset_world()
            await d.create(cid, pilot_ref=pilot_ref)
            seen = 0
            for i, text in enumerate(messages, 1):
                await d.send(cid, f"{cid}_m{i}", text)
                await d.quiet(cid, timeout=QUIET_TIMEOUT)
                run.facts.setdefault("turns", []).append({"user": text})
                await measure(d, run, cid, first_call_index=0)
                await judge(run, i)
            ALL_RUNS.append(run)
            runs.append(run)
            await d.assert_invariants(cid)
        failed = [r for r in runs if not r.passed]
        self.assertFalse(failed, f"{scenario}: " + "; ".join(f"run {r.index} failed {[c for c, ok in r.checks.items() if not ok]}" for r in failed))

    # -- R01 -------------------------------------------------------------------------------------------------
    async def test_r01_no_tool_needed(self):
        async def judge(run, i):
            run.check("no_tool_call", not run.facts["tool_calls"])
            run.check("answered_once", len(run.facts["pilot_texts"]) == 1 and len(run.facts["pilot_texts"][0]) > 0)
            run.check("single_inference", run.facts["inferences"] == 1)
        await self.play("R01", ["Bonjour ! Qu'est-ce que tu peux faire pour moi ?"], judge)

    # -- R02 -------------------------------------------------------------------------------------------------
    async def test_r02_lookup_then_answer(self):
        async def judge(run, i):
            text = " ".join(run.facts["pilot_texts"])
            run.check("order_looked_up_once", called(run, "world.get_order", order_id="A100") == 1)
            run.check("no_other_tool", len(run.facts["tool_calls"]) == 1)
            run.check("answer_has_eta", contains_any(text, ["2026-10-14", "14 octobre", "14/10"]))
            run.check("answer_has_status", contains_any(text, ["expédi", "en cours d'acheminement", "en cours de livraison", "shipped"]))
            run.check("two_inferences", run.facts["inferences"] == 2)
        await self.play("R02", ["Où en est ma commande A100 ?"], judge)

    # -- R03 -------------------------------------------------------------------------------------------------
    async def test_r03_two_independent_lookups_together(self):
        async def judge(run, i):
            text = " ".join(run.facts["pilot_texts"])
            run.check("customer_looked_up", called(run, "world.get_customer", customer_id="C1") == 1)
            run.check("order_looked_up", called(run, "world.get_order", order_id="A100") == 1)
            run.check("answer_has_tier", contains_any(text, ["gold", "or "]))
            run.check("answer_has_status", contains_any(text, ["expédi", "en cours d'acheminement", "en cours de livraison", "shipped"]))      # the wording is the model's
            run.check("parallel_one_emission", run.facts["inferences"] == 2)
        await self.play("R03", ["Je suis la cliente C1. Quel est mon niveau de fidélité et où en est ma commande A100 ?"], judge)

    # -- R04 -------------------------------------------------------------------------------------------------
    async def test_r04_tool_failure_is_reported_honestly(self):
        async def judge(run, i):
            text = " ".join(run.facts["pilot_texts"])
            run.check("inventory_attempted", 1 <= called(run, "world.get_inventory", sku="KB-77") <= 2)
            run.check("says_unavailable", contains_any(text, ["indisponible", "impossible", "pas disponible", "pas pu", "pas été en mesure", "momentanément", "problème", "erreur", "temporairement", "n'ai pas accès"]))
            run.check("invents_no_stock_figure", not re.search(r"\b\d+\s*(unités|exemplaires|pièces|en stock|claviers)", text.lower()))
            run.check("bounded_inferences", run.facts["inferences"] <= 4)
        await self.play("R04", ["Combien reste-t-il de claviers KB-77 en stock ?"], judge)

    # -- R05 -------------------------------------------------------------------------------------------------
    async def test_r05_follow_up_uses_the_history(self):
        async def judge(run, i):
            if i == 1:
                run.check("t1_order_looked_up", called(run, "world.get_order", order_id="A100") == 1)
            else:
                text = run.facts["pilot_texts"][-1]
                run.check("t2_answers_carrier", "colisexpress" in text.lower().replace(" ", ""))
                run.check("t2_no_redundant_lookup", called(run, "world.get_order", order_id="A100") == 1)
                run.check("t2_two_replies", len(run.facts["pilot_texts"]) == 2)
        await self.play("R05", ["Où en est ma commande A100 ?", "Et c'est quel transporteur ?"], judge)

    # -- R06 -------------------------------------------------------------------------------------------------
    async def test_r06_unknown_order(self):
        async def judge(run, i):
            text = " ".join(run.facts["pilot_texts"])
            run.check("order_looked_up", 1 <= called(run, "world.get_order", order_id="Z999") <= 2)
            # structural first: it names the order and invents no delivery information; the wording is a second, wide net
            run.check("names_the_order", "z999" in text.lower())
            run.check("invents_no_delivery_information", not re.search(r"expédi|colisexpress|postnord|\d{4}-\d{2}-\d{2}|\d{1,2} (janvier|février|mars|avril|mai|juin|juillet|août|septembre|octobre|novembre|décembre)|total de|€", text.lower()))
            run.check("says_not_found", contains_any(text, ["introuvable", "n'existe", "aucune commande", "pas trouvé", "ne trouve", "trouver", "not found",
                                                           "pas de commande", "inconnu", "ne figure", "aucun résultat", "pas reconnu",
                                                           # the pilot's own rule: a failing capacity is reported as unavailable information
                                                           "indisponible", "temporairement", "ne peux pas accéder"]))
            run.check("bounded_inferences", run.facts["inferences"] <= 4)
        await self.play("R06", ["Où en est ma commande Z999 ?"], judge)


    # -- R07: the same request, decided by the generic pilot and by a Capacity pilot ---------------------------------------
    @staticmethod
    def _orders_answer(run, text_checks=True):
        text = " ".join(run.facts["pilot_texts"])
        run.check("customer_looked_up_once", called(run, "world.get_customer", customer_id="C1") == 1)
        run.check("each_order_looked_up_once", called(run, "world.get_order", order_id="A100") == 1 and called(run, "world.get_order", order_id="A300") == 1)
        run.check("answer_names_both_orders", "a100" in text.lower() and "a300" in text.lower())
        run.check("answer_flags_the_delayed_order", contains_any(text, ["retard", "douane", "risque", "décal", "21 octobre", "2026-10-21"]))
        run.check("invents_no_stock_figure", not re.search(r"\bstock\b", text.lower()))

    async def test_r07_generic_pilot_all_open_orders(self):
        async def judge(run, i):
            self._orders_answer(run)
        await self.play("R07G", ["Je suis la cliente C1 : où en sont toutes mes commandes ouvertes ?"], judge)

    async def test_r07_capacity_pilot_all_open_orders(self):
        async def judge(run, i):
            self._orders_answer(run)
            run.check("prompts_are_in_the_journal", run.facts["kinds"].count("prompt") == 4 and run.facts["kinds"].count("completion") == 4)
            run.check("actions_are_not", "capacity_intention" not in run.facts["kinds"])
            run.check("one_inference_per_prompt", run.facts["inferences"] == 4)
        await self.play("R07C", ["Je suis la cliente C1 : où en sont toutes mes commandes ouvertes ?"], judge,
                        pilot_ref={"context": "/root/agentic/test", "capacity": "review_orders_real"})


    # -- R08: ordinary capacities, prompts and a sub-continuity in one Capacity pilot --------------------------------------
    async def test_r08_plan_with_lookup_triage_subcontinuity_and_conclusion(self):
        kid = "real_r08_0_kid"

        async def judge(run, i):
            text = " ".join(run.facts["pilot_texts"])
            run.check("customer_looked_up_once_by_the_plan", called(run, "world.get_customer", customer_id="C1") == 1)
            # the plan's loop looks each order up once; only the chosen one is looked up again, by the sub-continuity
            run.check("plan_fetched_both_orders", called(run, "world.get_order", order_id="A100") >= 1 and called(run, "world.get_order", order_id="A300") >= 1)
            run.check("child_investigated_the_delayed_order_only", called(run, "world.get_order", order_id="A300") >= 2 and called(run, "world.get_order", order_id="A100") == 1)
            run.check("parent_journal_holds_only_prompts_and_answer", run.facts["kinds"] == ["user", "prompt", "completion", "prompt", "completion", "pilot"])
            run.check("answer_names_the_order", "a300" in text.lower())
            run.check("answer_flags_the_delay", contains_any(text, ["retard", "douane", "bloqu", "risque", "21 octobre", "décal"]))
            child = await self.d.journal(kid)
            run.facts["child_kinds"] = [m["kind"] for m in child]
            run.check("sub_continuity_has_its_own_history", "pilot" in run.facts["child_kinds"] and run.facts["child_kinds"][0] == "user")
        # the plan needs the identifiers of its child in the message: it cannot build them itself
        await self.play("R08", [{"customer_id": "C1", "child_id": kid, "child_message_id": f"{kid}_task"}], judge,
                        pilot_ref={"context": "/root/agentic/test", "capacity": "investigate_orders_real"})


if __name__ == "__main__":
    unittest.main()
