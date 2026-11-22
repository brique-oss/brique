# Pilot defined by a Capacity

## Status

Implemented and validated on the real engine (scenarios S26 and S27). Clarifications from the implementation:

- the existence of a Capacity is checked by `read.meaning` of the context: `read.capacity` knows only the native capacities of the engine;
- `>for_each` does not bind its result to an alias: the loop of a plan is its own Capacity, which the plan invokes to receive `{ items }`;
- the engine refuses a loop alias that carries the same name as a `with` key (`order_id`): the test plan uses `item`.

## Intention

A pilot is defined in two ways: by a **prompt** (a `pilot_definition` Matter) or by a **`dsl` Capacity** that expresses more finely what a prompt alone does not allow: a sequence of deterministic actions and prompts, where the output of each prompt is used by the rest of the plan.

`pilot.activate` handles both. The rule that links them: **the continuity records the LLM calls, not the actions.** Tool calls, loops and conditions run without a trace in the journal; each prompt, with its response, enters it, because it is the cognition of the continuity.

## Two kinds of pilots

| | Matter pilot | Capacity pilot |
|---|---|---|
| defined by | `pilot_definition`: prompt, rules, known capacities | a Capacity that carries a plan |
| who decides | an LLM, through a generic inference | the plan: deterministic, with prompts at useful places |
| output | `pilot_output` | `pilot_output` |
| application of effects | `pilot.activate` | `pilot.activate`, identically |

The mailbox, the wait groups, the run ownership, the succession, the controls and the resumption are common. Only the **decision function** varies.

## Reference of the pilot

`pilot_ref` designates exclusively one of the two:

| Form | Fields |
|---|---|
| Matter | `{ context, matter_id }` (unchanged: `context` is `/root/agentic`) |
| Capacity | `{ context, capacity, limits? }`, `context` being the context of the Capacity, `capacity` its name |

`limits` (optional, Capacity pilot): `max_inferences_per_message` (ceiling of prompts for a message, 100 by default, never raised beyond) and `timeout_s` (maximum duration of a turn, 600 by default).

At the creation of the continuity, and at `set_pilot`, the reference is checked: the Matter or the Capacity exists.

## Contract of a pilot Capacity

A pilot Capacity is a pure, stateless transformation: it receives everything it needs and returns a decision.

**Inputs**

| Field | Description |
|---|---|
| `continuity_id` | the piloted continuity |
| `run_id` | the owning run, to pass on to the `pilot.infer` steps |
| `message` | the current message, complete |
| `stimulus` | the active stimulus of the resumption (`active_wait_result`, `execution_result`, `projection`, `recovery`), absent at a first turn |
| `context_pressure` | if the `pilot_ref` declares `limits.context_tokens`: the fill level of the context from the advised level on. The plan alone decides the succession (it has no automatic compaction) |

**Output**: a canonical `pilot_output`. It is validated exactly like that of a Matter pilot, with the same exclusivity of fields.

**Re-entrance.** The plan is stateless. When it decides to wait (it returns a `wait_group` and intentions), `pilot.activate` invokes it again at the resumption with `stimulus`. It is the continuity that carries the memory, never the plan.

**Deterministic actions.** An `invoke` of a tool in the plan runs without a trace in the continuity. For the resumption after a crash to be safe, an action with effects must be idempotent or declare `compensated_by`.

## Capacity `pilot.infer`

A direct `invoke` to `/root/llm/infer` would bypass the continuity. The "prompt" step of a plan is therefore `pilot.infer`, which is the cognition phase of `pilot.activate` made callable: a single cognition path for both kinds of pilots.

**Inputs**

| Field | Description |
|---|---|
| `continuity_id`, `run_id` | the continuity and its owning run |
| `step` | stable name of the step, literal of the plan |
| `instructions` | the prompt of the step, as a single text. Required, unless `prompt_parts` or `prompt_matters` is given; never with them |
| `prompt_parts` | the prompt as an **ordered list of pieces** (list parameter): a text as is, or any other JSON value (object, list, number, boolean), written as **compact JSON** with ordered keys; `null` is not a piece. This is how the data of a step enters its prompt: there is no `input` parameter |
| `prompt_matters` | the prompt as an **ordered list of Matters**, bound under this name in `@matter` (slot `cardinality: many`); each one is read as text |
| `prompt_order` | `matters_first` or `parts_first`: which comes first when both lists are given. Required in that case only |
| `output_schema` | schema of the structured output; this schema is the contract between the reasoning and the deterministic continuation |
| `projection` | optional: a projection of the journal added to the context of the step (otherwise the step sees only its prompt). With it, the cognitive framework of the continuity opens the system text |
| `framework` | optional, false by default: without `projection`, still adds the cognitive framework at the beginning of the system text. A step thus decides whether it is "with" or "without" the continuity |
| `model` | `{ provider, model, parameters }`: required, since a Capacity pilot has no `pilot_definition` |

**Output**: `output`, the structured output validated against `output_schema`, plus `usage`.

**Effect, in order**

0. if the prompt is given in pieces, assemble it (read the Matters); any error occurs here, before any effect;
1. compute the key of the step: fingerprint of `(message_id, step, prompt, output_schema, model)`, where `prompt` is the text sent, however it was given;
2. if the journal already contains the response of this key, return it **without calling the LLM** (memoization);
3. otherwise record the prompt (`kind: prompt`), call the LLM, validate the output, record the response (`kind: completion`), return it.

The identifiers of the two messages derive from the key: a replay is an idempotent retry, never a duplicate.

### Assembled prompt

`pilot.infer` is not intelligent: it concatenates what it is given. The prompt is either `instructions` (a text), or one or two lists:

| List | Where it travels | Content |
|---|---|---|
| `prompt_matters` | `@matter`, several Matters under the same name, in order | each Matter is its whole substance, read as **text** |
| `prompt_parts` | list parameter, each element travels as a parameter | non-empty texts, or any JSON value (data of a previous step, for example `$weather.payload`), put into compact JSON |

- Each list keeps its order; between the two, `prompt_order` says which comes first. The pieces are separated by **a blank line** (fixed, not configurable). Nothing is interpreted, extracted or rewritten: a JSON Matter is read raw, there is no `field`.
- To mix a text and a Matter in an order that does not reduce to "some then the others", one assembles **upstream** (a Matter that carries the concatenation, or a capacity that assembles it): it is not the job of `pilot.infer`.
- A malformed list, a missing or empty Matter, both lists without `prompt_order`, `instructions` with a list, an `input` given (the parameter no longer exists: the data is a piece of the prompt), or no prompt: error before any effect (no call to the model, nothing in the journal).
- The memoization key and the journal use the **assembled text**. A modified Matter is therefore a new step, and the journal keeps the text actually sent (field `instructions` of the `prompt` line), never the references. The same text given as `instructions` is the same step.
- A Matter pilot keeps its prompt in its `pilot_definition`; the two kinds of pilots do not share the text, they share the inference path.
- The transport of these lists (a list of Matters under one name, a list parameter walked element by element) is that of the wrappers, described in `Wrapper.md` §10.1 and §17.3.

**Constraints**

- The run must own the continuity (`run_id`).
- The ceiling `max_inferences_per_message` applies: beyond it, explicit refusal.
- If the continuity disappears, the step fails with `NotFound` and the plan is interrupted.
- An invalid output is not corrected by a second inference: it is a defect of the plan, reported.
- `output_schema` must respect the limits of the providers (closed objects, few optional fields): the design imposes it, `pilot.infer` checks it.

## A plan whose model asks for intentions

A plan can let the model decide to **call tools**: the `pilot.infer` step has as `output_schema` a form of `pilot_output` (`intentions`, `wait_group`, `messages`), and the plan returns this output as is. `pilot.activate` applies it like that of a `pilot_definition`: it sends the intentions, waits for the responses, then invokes the plan again with the `stimulus`. The loop is carried by the continuity (its successive recalls), not by the plan. Validated for real by the `search_pilot` capacity (`test` context) and the `brique_search` prompt.

What this case imposes:

- **`target_type` mandatory** in the schema of the step (with its values and a description), since the output schema of a plan is written by its author, without the notes of the `pilot_intention` schema.
- **The free values** (`parameters`, `content`) are JSON in text, decoded by `pilot.activate` before the validation (see `capacity_pilot_activate`). The schema of the step is limited to closed objects and few unions.
- **The step name changes at the resumption.** The memoization of `pilot.infer` is computed on `(message, step, prompt, schema, model)`: a plan called again with the same step name and the same prompt would return the output already recorded, therefore the same intentions, in a loop. The plan names the step differently when it receives a `stimulus` (`resume`), and its prompt is that `stimulus`, which changes at each turn.
- **The continuity is read** by the step (`projection` with the kinds `user`, `prompt`, `completion`, `capacity_response`): the model sees its conversation, its own prompts, its outputs and the responses of the capacities. The starting prompt (the task and `brique_search`) is therefore sent only **once**, at the first step; the resumptions send only the `stimulus`, and the cache of the provider reads back the rest. The continuity then carries the cognitive framework, as system text.

## Journal: new kinds

| `kind` | Content |
|---|---|
| `prompt` | `{ step, instructions, model }`, sender the continuity |
| `completion` | `{ step, output, usage }`, with `correlation.causation_id` to the prompt |

These two kinds are history, never stimuli: they have no mailbox entry. They follow the projection rules like the others (`include_kinds` / `exclude_kinds`).

## Context pressure

The measurement (`event` line `inference_measured` of the journal) follows only the inferences whose context contains the journal projection: it is what expresses the size of the history. A step that sees only its prompt does not modify it. A Capacity pilot without a declared window is not automatically compacted; compaction remains voluntary, through a `succession` decision of the plan.

## Resumption after a crash

The Capacity pilot that was in progress is invoked again by the handling of an orphan run. The `pilot.infer` steps already recorded are **returned at no cost**: the plan replays, the responses already obtained are neither paid again nor divergent. Non-idempotent actions with effects declare their compensation, following the doctrine of the engine.

## Controls and duration

The controls (`suspend`, `delete`, `set_*`) apply at the safe points, that is between two turns of the Capacity pilot, never in the middle of a plan. The duration of a turn is bounded by `timeout_s` and by the bounds of the DSL (`limit` of the loops, `max_iterations`).

## Sub-continuity: ordinary Capacities

A sub-continuity is not a separate mechanism: a pilot (Matter or plan) opens it with `continuity.create`, sends it its work with `mailbox.send`, then waits for `pilot.activate` like any Capacity, which the waiting contract of `pilot.activate` allows (see `capacity_pilot_activate`). The child is piloted by its own `pilot_ref`, Matter or Capacity, which makes the composition recursive. A prompt with a structured output inside a plan remains `pilot.infer`.

## What the engine imposes, verified

- Nested values resolve their references: `$item` (whole object), `$input.doc`, nested objects, literals.
- `>for_each` returns `{ items: [payload…] }`, with a concurrency `limit`.
- `$item_index` is rejected by the engine despite its documentation: the design does not depend on it, the key of a step is a content fingerprint.

## Invariants

- A pilot is a Matter or a Capacity, never both.
- The continuity contains the prompts and the responses of the steps, never the actions of a plan.
- A replayed step does not call the LLM again and adds nothing to the journal.
- The output of a Capacity pilot goes through the same validation and the same application as that of a Matter pilot.
- A plan contains no state: every state is the journal of the continuity.

## Expected tests

- **Contract of `pilot.infer`**: one per line of the contract, including memoization, run ownership, the ceiling, output validation and recording.
- **Contract of `pilot.activate`**: Capacity pilot branch, application of a `pilot_output`, re-entrance with `stimulus`, plan error.
- **Integration on the real engine**: a real `dsl` Capacity with a loop and a fake LLM, including resumption after a wrapper restart.
- **Real**: same scenario with a real model, compared with the Matter pilot (tokens, inferences, reliability).

## Out of scope

- A plan interpreter in `agentic_core`: the engine already executes the DSL.
- Writing of plans by the LLM.
- Automatic compaction of a Capacity pilot without a declared window.
