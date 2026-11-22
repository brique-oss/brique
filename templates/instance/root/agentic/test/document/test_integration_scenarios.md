# End-to-end integration test of the `agentic` context

## Intention

Verify, on the **real Brique engine** and the **real `agentic_core` wrapper**, that the agentic mechanics work end to end: creation of continuities, sending of messages, wake-up, cognition, waiting, delegation to a sub-continuity, return of results, projection, controls, resumption.

This test does **not verify the reasoning of an LLM**. The LLM is replaced by a fake that answers what the scenario decided. Only the mechanics are under test.

The contract unit tests (`code/agentic_core/tests/contract/`) verify each capacity in isolation against its contract. This document describes what the integration adds: the real chaining, the real transport, the real persistence of the Matters.

> Status: scenarios S01 to S20 are implemented and pass on the real engine (see "Implementation" at the end of the document). The design decisions were taken with the architect.

## Principle

```
test driver
   │ real intentions (mailbox.send, continuity.create, ...)
   ▼
Brique engine ── agentic_core wrapper (real code)
                      │ call /root/llm/infer
                      ▼
                fake LLM  ◄── scenario rules (Matter)
                      │ pilot_output decided by the scenario
                      ▼
                fictitious tools (test sub-context)
```

Only two elements are fictitious: the LLM and the tools that the pilot addresses. Everything else is real.

## Elements of the test

### Sub-context `/root/agentic/test`

It contains only what serves the test, so as to mix nothing into the `agentic` context itself.

| Element | Nature | Role |
|---|---|---|
| `test.echo` | capacity | answers immediately with its parameters |
| `test.slow` | capacity | waits for a gate (`gate`) to be opened, then answers |
| `test.open_gate` | capacity | opens a named gate |
| `test.fail` | capacity | answers an error |
| `infer.fake` | capacity | fake LLM: picks a rule, returns a canonical `response` |
| `llm_script` | matter | rules of the current scenario |
| `llm_calls` | matter | journal of the requests received by the fake LLM (for assertions) |
| `gates` | matter | state of the gates of `test.slow` |

The fictitious capacities are hosted by a dedicated wrapper, `test_tools`, declared by the sub-context itself. Their code lives in the sub-context, under `code/test_tools/`.

### Test pilot

A Matter `pilot_test` (`pilot_definition`):

- `prompt`: a scenario marker, readable by the fake LLM (for example `SCENARIO-DRIVEN TEST PILOT`); the prompt contains no reasoning instruction;
- `model`: `{ provider: fake, model: scripted }`;
- `known_capacities`: the fictitious tools;
- `default_projection`: chosen to be observable (e.g. exclusion of `projection` messages).

### Identifiers of the test

Everything a scenario creates carries the prefix `it_` followed by the name of the scenario (`it_s03_c1`, `it_s03_child`). Nothing else in the instance is touched. A final cleanup deletes with `continuity.delete` every `it_*` continuity, then verifies that no `it_*` Matter and no `it_*` reference remains in the `continuities` Structure.

## The fake LLM

### Rules

`llm_script` contains an ordered list of rules:

```
{ "when": { "message_contains": "...", "stimulus": "none | active_wait_result | execution_result | projection | recovery",
            "call_index": 1, "continuity": "it_s03_c1" },
  "then": { <pilot_output> } | { "invalid": <value> } | { "error": "..." } }
```

- **Matching:** the first rule whose present `when`s are all true, on the request received.
- **Determinism:** no applicable rule is a failure of the test, never an improvised response.
- **Observation:** each request is added to `llm_calls` with its number, the continuity, the current message, the stimulus and the journal view received. The request is a conversation: the fake LLM reads the new context in its last JSON message, the initial intention in the system text, and the continuity, the step and the identifiers of the view in `request.metadata`, which no provider reads.
- **Variables:** a rule can reference `{{continuity_id}}`, `{{message.id}}`, `{{intention_id:n}}` so that the `pilot_output` is valid whatever the identifier.

### Address of the fake LLM

`pilot.activate` calls `/root/llm/infer`, which routes on `request.provider`. The fake LLM is one more provider, `fake`: `infer` contains a `fake` case that invokes `/root/agentic/test/infer.fake`. The test pilot declares `model = { provider: fake, model: scripted }`. The real routing is therefore exercised, as for openai or anthropic.

## Driving and observation

- **Driver:** a test WebSocket client, on a dedicated UI route, which sends real intentions to the engine and reads the Matters (`matter.read`). It contains no agentic logic.
- **Waiting for the end:** a scenario is finished when the `runtime` is `idle`, without an active group, and the journal no longer grows; a global delay bounds each scenario.
- **Observation:** `matter.read` on `continuity_*`, `mailbox_*`, `runtime_*`, plus `llm_calls`.

### Invariants verified at the end of each scenario

1. Contiguous and unique `seq` in each continuity.
2. Empty mailbox, except an explicitly expected `pending` message.
3. `runtime`: `idle`, without `run_id`, without `current_message_id`, without an active group.
4. No message sent lost or duplicated in the journal.
5. No intention `await_response = false` appears in a `wait_group`.
6. Each mailbox message has a corresponding durable message (no durable orphan reservation).
7. No residual Matter after cleanup.

## Scenarios

Each scenario gives: the mechanical objective, the setup, the expected course, what is verified.

| # | Scenario | Mechanics exercised |
|---|---|---|
| S01 | Question and answer | creation, send, wake-up, inference, outgoing message, ack, idle |
| S02 | Fire-and-forget intention | non-blocking invoke, no wait, no return route |
| S03 | Wait for two tools (`all`) | wait group before the invokes, Responses through mailbox, resumption with `active_wait_result` |
| S04 | `any` condition and late result | early resumption, late result in history only |
| S05 | `first_success` with error | outcome `error`, result `no_success` |
| S06 | `n_of_m` | satisfaction threshold |
| S07 | Dynamic projection | `projection_request`, merge and replace mode, cognitive resumption point |
| S08 | Intention change | `intention_update`, immutable `initial_intention`, trace |
| S09 | Delegation to a sub-continuity | child creation, parent/child relation, `continuity` message, correlated return, `continuity` type wait |
| S10 | Parallel delegation | several children, `all`, aggregation of the returns |
| S11 | Deferred controls | suspend/resume during a wait, durability of the wait |
| S12 | Deletion | `delete` applied at a safe point, terminal, cascade on the children |
| S13 | Invalid output, then corrected | a single correction, no effect before validation |
| S14 | Invalid output twice | explicit failure of the processing, entry released, no loop |
| S15 | Execution failure | stop at the first failure, `execution_result`, no replay |
| S16 | Burst of messages | one message at a time, order by `seq`, none lost |
| S17 | Retry and consumed message | idempotence, reinsertion refused |
| S18 | Crash and resumption | wrapper killed in the middle of a wait, resumption of the orphan run, no re-emission |
| S19 | Lost wake-up | message arriving during the transition to idle |
| S20 | Cleanup | deletion of everything the test created |

### Detail

**S01 — Question and answer.**
Setup: `it_s01` created with `pilot_test`. Rule: on `hello`, answer a `user` message.
Expected: the journal contains `user`, then `pilot`; the mailbox is empty; `runtime` returns `idle`; `llm_calls` counts 1 call with the context blocks in order (pilot, initial intention, current intention, capacities, projection, current message).

**S02 — Fire-and-forget.**
Rule: an intention `test.echo` with `await_response = false`, then an end message.
Expected: `test.echo` called once; no Response in the journal; no group; the current message is acknowledged without waiting.

**S03 — Wait for two tools (`all`).**
Rule 1: `wait_group all` on two pending `test.slow` intentions. Rule 2 (stimulus `active_wait_result`): end message.
Expected: `runtime.status = waiting` observable during the wait with the group present; after the gates are opened, a single resumption; the stimulus contains the two results; the two `capacity_response` messages are correlated by `intention_id`; group removed; `expected` entries acknowledged.

**S04 — `any` and late result.**
Two tools, one fast, the other blocked. Expected: resumption as soon as the first; the gate is opened afterwards; the late result is in the journal, **without** a new inference and **without** a remaining mailbox entry.

**S05 — `first_success` with error.**
A single tool, `test.fail`. Expected: `received[0].outcome = error`, `outcome = no_success` in the stimulus.

**S06 — `n_of_m`.**
Three tools, `n = 2`. Expected: resumption after the second; the third is late.

**S07 — Dynamic projection.**
Journal previously filled with messages of several `kind`s. Rule 1: `projection_request` (`replace`, `include_kinds`, `limit`). Rule 2 (stimulus `projection`): end message.
Expected: the next request of `llm_calls` contains exactly the view requested; the `default_projection` of the pilot is not modified; the `projection` traces are in the journal.

**S08 — Intention change.**
Rule: `intention_update`. Expected: `current_intention` changed, `initial_intention` intact, trace present, and the next request exposes the new intention.

**S09 — Delegation to a sub-continuity.**
Parent `it_s09_p`. Parent rule 1: intention `continuity.create` (`await_response = true`, child `it_s09_c`, `parent_id` = parent) then, after its Response, a `continuity` message to the child and a `continuity` type `wait_group` on the child. Child rule: answer with a `continuity` message to the parent. Parent rule 2 (stimulus `active_wait_result`): end message.
Expected: `children` of the parent contains the child and `parent_id` of the child is the parent; the child processed its message with its own run; the response arrives in the mailbox of the parent as `expected` (correlation `continuity_id` = child), never as a normal message; the parent resumes once; the Structure references both.

**S10 — Parallel delegation.**
Two children, `wait_group all`. Expected: a single resumption of the parent, with the two returns; the two children each have their run.

**S11 — Deferred controls.**
During the wait of S03, send a `suspend` control. Expected: applied at the safe point, `status = suspended`, `run_id` removed, group kept; the Responses arrive and are captured without an inference; `resume`: the same chain resumes and ends.

**S12 — Deletion.**
On a parent/children tree, send a `delete` (with then without cascade). Expected: without cascade, the children survive detached; with cascade, everything disappears; Structure cleaned; a late Response recreates nothing.

**S13 — Invalid output, then corrected.**
Rule 1: invalid output. Rule 2 (correction call): valid output. Expected: exactly two calls; the correction request contains the errors; no effect produced by the first output.

**S14 — Invalid output twice.**
Expected: exactly two calls, trace `treatment_failed`, entry released `pending`, `runtime` returned `idle`, no automatic restart.

**S15 — Execution failure.**
Three intentions, the second of which is unaddressable. Expected: the first is executed once, the third never; the next stimulus is an `execution_result` listing succeeded, failed, remaining; no replay.

**S16 — Burst.**
Twenty messages sent without waiting. Expected: processed one by one, in the order of `seq`; `llm_calls` counts twenty calls, no duplicate.

**S17 — Retry and consumed.**
The same `message.id` sent twice before processing, then once after. Expected: a single durable message; reinsertion after consumption is explicitly refused.

**S18 — Crash and resumption.**
During the wait of S03, restart of the wrapper. Expected: after the restart, the orphan runtime is taken over by a new `run_id`; no intention is re-emitted (counter of `test.slow`); the chain ends; the claims are transferred.

**S19 — Lost wake-up.**
Send of a message at the moment the run publishes `idle`. Expected: the message is processed (release-and-recheck), without a second simultaneous run.

**S20 — Cleanup.**
Deletion of all the `it_*` continuities. Expected: no residual Matter or reference.

## Decisions taken

1. **Address of the fake LLM**: a `fake` provider added to `/root/llm/infer`, which routes to `/root/agentic/test/infer.fake`. It is the real routing path that is exercised.
2. **Fictitious tools**: hosted by a dedicated wrapper `test_tools`, declared by the sub-context `/root/agentic/test` (and not by `agentic_core`).
3. **Driver**: standalone WebSocket client, on a dedicated UI route `it_driver` declared by the `test` sub-context (the `main` route of the root context is shared with the editor, which cuts the other clients when it reconnects).
4. **Crash (S18)**: `wrapper.restart` of the `agentic_core` wrapper.
5. **`llm_script`, `llm_calls`, `gates`, `tool_calls`**: Matters of the `test` sub-context. Like any agentic Matter, their content is their substance (`<matter_id>.json`), with `functional.format = json`; only the descriptor is in the `.matter.json` file.

## Implementation

| Element | Location |
|---|---|
| Sub-context and wrapper `test_tools` | `/root/agentic/test` (context.json), code in `test/code/test_tools/` |
| Fictitious capacities | `test/capacity/`: `test.echo`, `test.slow`, `test.open_gate`, `test.fail`, `infer.fake` |
| Rule engine of the fake LLM | `test/code/test_tools/rules.py` (pure, without Brique access) |
| Test pilot | Matter `it_pilot` of the `agentic/test` context (the test pilots live in the test context) |
| `fake` provider | case added in `/root/llm/capacity/infer.json` |
| Driver and scenarios | `code/agentic_core/tests/integration/` |

Execution: the engine runs, the `agentic` and `agentic/test` contexts are started, the `agentic_core` and `test_tools` wrappers are started, the Matters `llm_script`, `llm_calls`, `gates`, `tool_calls` and `it_pilot` exist.

```
cd root/agentic/code/agentic_core
BRIQUE_IT=1 ../../../../envs/env1/venv/bin/python3 -m unittest discover -s tests -t .
```

Without `BRIQUE_IT=1`, the integration scenarios are skipped and only the unit and contract tests run.

### Gaps from the initial hypotheses

- **S11b** added: controls applied immediately at `idle` (`set_intention`, `suspend`, `resume`), no residual entry, waiting message woken by `resume`.
- **S12** is split into three: without cascade, with cascade, and `delete` applied at a safe point during a wait with a late result.
- **S15**: an emission failure is produced by a malformed Capacity reference (`/nowhere`), because a fire-and-forget emission to an absent Capacity fails only afterwards, with no return.
- **S18**: the restart triggers nothing by itself; it is a new message that makes the orphan run visible and causes its resumption. The intention whose Response is lost is resolved explicitly by an `unresolved` error Response, never replayed.

### What the integration revealed

- The content of a Matter is its substance (`data`), not its descriptor: the `semantic_patch` of the engine never reaches the substance, so the operations are derived from the content read then written in a single `matter.write(data, expected_revision)`.
- The engine rounds the revisions (timestamps in nanoseconds) read as JSON numbers: `expected_revision` must be sent as a decimal string.
- `structure.patch` read its guard under `expected_rev` whereas its descriptor documented `exp_rev`, with the refusal reason `revision not coherent`. The engine is fixed: canonical key `expected_revision` (the historical aliases remain accepted), reason `revision_mismatch`, and `agentic_core` now sends only `expected_revision`.
- The generic router of the wrapper answers any error of a hosted Capacity with the code `internal`; the application code is visible only in the message.
- A chain of endless projections was bounded only by a very high ceiling: it is now bounded to 50 inferences per message.

## Succession and voluminous content (S22 to S25)

| Scenario | Verifies |
|---|---|
| S22 | full context at the safe point: compaction inference, succession, waiting message transferred, redirection, rejection of a consumed identifier |
| S23 | succession decided by the pilot: the message in progress is processed by the next one; deletion of the whole lineage with `cascade` only |
| S24 | delegation: the parent does not see the succession of its child (`correlation.continuity_id` = lineage identifier, `children` points to the active one) |
| S25 | voluminous content: voluminous message and Response written as they are in the journal, no overflow Matter |
| S21 | journal beyond 256 KiB: read through HTTP lease, inline writes |

## Pilot defined by a Capacity (S26 and S27)

| Scenario | Verifies |
|---|---|
| S26 | a real `dsl` Capacity with a loop: deterministic lookups without trace, one prompt per command then a synthesis, recorded in the journal and correlated with the message; switch from the Matter pilot to a Capacity by `set_pilot` |
| S27 | a non-conforming step output: no `completion`, processing in failure, entry released; an unknown Capacity is refused at creation |

## An activation is a Capacity (S28 and S29)

| Scenario | Verifies |
|---|---|
| S28 | an explicit `pilot.activate` on a slow child **waits** for the end of the child and answers `idle`; it no longer answers "in progress" |
| S29 | a pilot opens a sub-continuity with `continuity.create`, `mailbox.send` then `pilot.activate`, all awaited, and resumes when the child has finished; the parent keeps neither the history of the child nor its steps |

## Combinations of plans, capacities and sub-continuities (S30 to S32)

| Scenario | Verifies |
|---|---|
| S30 | a plan combines ordinary capacities (including a loop), `pilot.infer` prompts and a sub-continuity: the records returned by the capacities feed the triage, the output of the triage feeds the message of the child, the response of the child feeds the conclusion prompt; the journal of the parent carries only the prompts and the response |
| S31 | a plan delegates to a sub-continuity that is itself piloted by a Capacity; the prompts of the child live in its journal, the prompt of the parent is fed by the last message of the child |
| S32 | the wrapper dies while the plan waits for its sub-continuity: a new activation takes over the two orphan runs, replays the plan, does not re-issue any recorded prompt, and the response is produced only once |

A design rule that S30 recalled: the prompt can decide only with the information it is given. A triage fed by a customer record without the status of the orders could not choose the late order; the orders must first be retrieved by an ordinary capacity, then given to the prompt.

## Concurrency (S33 to S36)

| Scenario | Verifies |
|---|---|
| S33 | width: eight pilots (four Matters held in a tool wait, four plans) woken together; each keeps its journal, its messages and its number of inferences, without crossing |
| S34 | depth: five levels that wait for each other, alternating Matter and plan (Matter → plan → Matter → plan → plan); each level keeps only its own prompts |
| S35 | width and depth: a Matter root opens three children of three kinds, one of which has a grandchild, sends them work together and waits for them to be all at rest |
| S36 | the wrapper dies in the middle of the tree: the resumption of the root resolves its lost activations to `unresolved` without replaying them; each orphan is taken over by its own activation; no task or tool replayed |

## Assembled prompt (S37 to S40)

| Scenario | Verifies |
|---|---|
| S37 | `prompt_matters` (two contexts, the same name) and `prompt_parts` assembled as they are, in the order requested (`matters_first`, `parts_first`, a single list); the journal keeps the text sent, never the references; the Matters are only read |
| S38 | Matters shared by six continuities, modified in the meantime: those that already asked keep their text, the next one sees the change |
| S39 | a missing or empty Matter, two lists without order, an empty text: no inference, neither prompt nor completion in the history, message released |
| S40 | a Matter pilot (its prompt is its definition) and a plan (assembled prompt) woken together: each queries the model with its own prompt, through the same path |

The real test `tests/real/test_parts_scenarios.py` (P1) does the same thing with a real model, for about 2,000 tokens.

## Recovery after a hard failure (S41 to S44)

The process of the wrapper is killed (`SIGKILL`, without a clean stop); the engine starts a new one, and the orphan run is taken over only at the next message. macOS only (the process is found by its environment with `ps eww`).

| Scenario | Verifies |
|---|---|
| S41 | the wrapper dies while the continuity waits for a tool: an orphan run, taken over by the next message |
| S42 | it dies during an inference of a plan: the prompt is in the journal, not the response; the step is asked again once, the history keeps one prompt, one completion, one response |
| S43 | it is killed at a random moment of a burst of messages: no message is lost, each receives a response, the continuity ends at rest |
| S44 | **known gap, expected failure**: between the writing of the response and the acknowledgement of the message, a `kill -9` leaves the response written and the message claimed; the resumption processes it again and the continuity answers twice. The responses carry no correlation to the message: nothing in the journal says that the processing was complete |

## The cognitive framework (S45)

| Scenario | Verifies |
|---|---|
| S45 | the first line of each journal is the framework (`kind: framework`, `seq` 1, text copied from the Matter `pilot_framework`); a Matter pilot has it at the head of its system text, once; a plan that asks for it (`framework`) receives it before its prompt, a plan that does not ask receives its prompt alone; the history keeps the prompt without the framework |
| S46 | `pilot.recover`, the explicit start | an orphan continuity and one with waiting work are woken, the quiet one is not; a second call wakes nothing more |
| S47 | the policy of the pilot | a forbidden context refuses the whole output before any effect (neither tool, nor intention update), the pilot is warned and decides again; an allowed context lets it through; a stubborn pilot is stopped after three refusals |
| S48 | a message that always fails | three attempts counted, set aside at the third (`failed`), the following messages are processed, never a fourth inference |
| S49 | a wait that nothing fulfills | a mute delegated continuity: after `wait_timeout_s`, a `timeout` error response ends the wait and the pilot decides |

The other scenarios read the journal without the framework (`Driver.journal` masks it by default, `frame=True` returns it), and the end-of-scenario invariants count it (`seq` contiguous from 1). S22 verifies that the successor opens with its own framework.

## Real tests of the search (R-SEARCH)

`tests/real/test_search_scenarios.py` launches the pilot capacity `search_pilot` (`test` context) with a real model (Haiku): a single `pilot.infer` step, whose prompt is a task text followed by the Matter `brique_search`. The model reads contracts (`read.capacity`), queries the vocabulary, then `meaning.query`, then reads the descriptors retained (`read.meaning`), and reports `[{kind, context, name, why}]`. The judgment is structural: the search went through the capacities of the engine, no `meaning.query` leaves before the vocabulary has answered, what is reported was returned by a query, `weather.fetch` is in the list. Four to seven inferences, 30,000 to 50,000 input tokens.

## Out of scope

- The quality of the responses of a real LLM.
- Performance and load beyond the burst of S16.
- The user interface.
