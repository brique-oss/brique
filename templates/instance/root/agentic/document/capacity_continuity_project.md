# Capacity — `continuity.project`

## Intention

Build a deterministic view of a continuity from its durable memory.

`continuity.project` does not cognitively summarize the continuity. It selects, filters, orders and structurally aggregates the messages needed for a given view.

It produces a projection intended in particular for building the LLM context.

## Input

### `continuity.project`

| Field | Description |
|---|---|
| `continuity_id` | Identifier of the continuity to project. |
| `projection` | Definition of the projection to apply. |
| `mode` | Optional. Expected values: `merge`, `replace`. `merge` means that the requested projection complements the default projection of the pilot. `replace` means that the requested projection temporarily replaces the default projection. |
| `budget` | Optional. Maximum budget granted to the projection. May be expressed according to the mechanism chosen by the LLM subsystem. |
| `default_projection` | Optional. The default projection of the pilot, supplied by the caller **as a value** (transit). Used by `merge`, ignored by `replace`. Absent: `merge` composes the request with an empty base. `continuity.project` never reads the `pilot_definition`. |

## Output

| Field | Description |
|---|---|
| `continuity_id` | Identifier of the projected continuity. |
| `projection` | Resulting structured view. |
| `selected_message_ids[]` | List of the messages actually included. |
| `omitted_count` | Possible number of messages not included. |
| `budget` | Budget applied. |

## Source of truth

`continuity.project` reads only the header (`continuity_<id>`) and the journal (`journal_<id>`, found through `journal_ref`) as the main durable sources. Its input is a durable state: it addresses it by identifier and reads it itself, it does not receive the messages as a parameter.

The projection reads the **continuity**, that is the in-memory object of the wrapper rebuilt from the journal (see `agentic_core_concurrency_model`, "The in-memory continuity"). It re-reads the journal only to rebuild this object or to complete it with its tail, by line range (`matter.read` with `data_range` in lines, `matter.stat` for the number of lines). By default, the projection takes the whole continuity, which is the LLM thread; the projection criteria (including `include_kinds` and `exclude_kinds`, the `kind` being the characterization of a line) serve to include or exclude elements of it. The tracking kinds (`event`, `projection`, `error`, `control`) are not part of it by default; a projection may ask for them, they are then read in the journal.

It can use the explicit relations of the continuity if the projection asks for it.

The Matters of explicitly linked continuities are derived from their identifiers (`continuity_<id>`, then `journal_ref`) and read read-only. It creates no parallel memory. It does not modify the journal.

The view produced is an **intermediate product**: its consumer (the pilot, which places it in the model's request) receives it in transit. It is not persisted by `continuity.project`.

## Default projection

The `pilot_definition` can supply a `default_projection`. This projection serves as the usual base for building the LLM context. It is not `continuity.project` that reads it: the caller (`pilot.activate`, which has already read the `pilot_definition` once for the run) passes it as a parameter, as a value. A Capacity thus receives no Matter as input for the sole reason of re-reading part of it.

A dynamic projection can then:

- complement this projection,
- or explicitly replace it.

## `merge` mode

In `merge` mode:

```
effective projection
  =
default_projection
  +
requested projection
```

The dynamic projection adds or refines criteria without implicitly removing the default base.

## `replace` mode

In `replace` mode, the dynamic projection becomes the effective view for the inference concerned.

It does not modify the `default_projection` supplied, nor that of `pilot_definition`.

## Temporality

A dynamic projection is temporary.

It applies to the inference or the short sequence explicitly concerned. It does not accumulate automatically with the previous dynamic projections.

A new dynamic projection replaces the previous one, unless the protocol explicitly decides otherwise.

## Allowed operations

`continuity.project` can in particular:

- filter by `kind`
- filter by `from`
- filter by `to`
- filter by `seq`
- filter by time range
- filter by `correlation`
- select certain messages
- exclude certain messages
- limit to a number of messages
- select the last relevant messages
- order according to the canonical order
- structurally group related elements
- select events linked to a parent or child `continuity` if explicitly requested

## Forbidden operations

`continuity.project` must not:

- cognitively summarize
- interpret the content
- decide what is important according to LLM reasoning
- reformulate the meaning
- synthesize several messages into a conclusion
- produce an evaluation
- produce a recommendation

Any operation of this type belongs to an explicit cognitive Capacity.

## Order

The canonical order of the messages is determined by `message.seq`.

A projection does not arbitrarily reorder history. It can select a subset, but the relative order of the selected messages remains that of the continuity unless a structural request is explicitly defined.

## Pinned lines: the cognitive framework

A line of a **pinned** kind (today `framework`, the cognitive framework written by `continuity.create`) is always carried by the projection, first, whatever its filters:

- it escapes `include_kinds`, `exclude_kinds`, `from`, `to`, `min_seq`, `max_seq`, `message_ids` and `correlation`;
- it is never counted by `limit`, nor by `selection` (`limit: 2` returns two exchange messages, plus the framework);
- it is never counted by the budget, nor removed by it: the reduction bears only on the rest;
- it appears only once, even if a filter designates it explicitly.

`selected_message_ids` contains it, first. The consumer of the view (`pilot.activate`, `pilot.infer`) places its text at the head of the system text and does not repeat it in the journal view it sends.

## Context budget

`continuity.project` can receive a maximum budget.

The runtime first reserves the space needed for the other blocks of the LLM context:

- `pilot_definition`
- `initial_intention`
- `current_intention`
- `available_capacities`
- `current_message`
- a possible `active_wait_result`
- a possible `execution_result`

The remaining budget is granted to `continuity_projection`.

### What the budget counts

The budget is expressed in characters and measures **what the conversation carries to the model**: the size of the text that each line says (its content as `substrate/conversation.py` turns it into a message), never the complete journal line. The identifiers, dates, correlations and other metadata of a line are not counted, because the model does not see them. A line that the conversation does not say counts zero.

A consumer that does not send certain lines (the `pilot` and `capacity_intention` effect lines, already said by the decision that produced them) excludes them from the projection by `exclude_kinds`, and they are then neither selected nor counted. `pilot.activate` does this.

### Protected lines

The succession summary (`summary`) is **never removed by the budget**: it is the anchor of the history of a continuity, without which the successor no longer knows what was decided. It is counted, but the reduction bears on the rest. It differs from the pinned framework: it does not escape the filters, it escapes only the budget.

### Reduction

If the projection exceeds the budget, `continuity.project` applies its deterministic rules to reduce the view: it removes the oldest messages, one by one, until it fits, outside pinned and protected lines.

This budget is a **hard cut**, not the normal regime of a continuity. The size of the context is bounded by compaction, which triggers on the tokens actually measured (`context_tokens` and its ratios, see `continuity_succession.md`). A projection budget smaller than what `context_tokens` lets in cuts the history before compaction takes place: it must be set only for a reason specific to the pilot, and then at least at the level of the window converted into characters. A pilot without a budget is not cut.


It must not arbitrarily truncate text in the middle of a content. It reduces the selection, never the pinned lines.

For example:

- fewer messages
- narrower `seq` range
- exclusion of instrumental `kind`s
- limitation to recent or explicitly relevant events according to the projection rules

The reduction remains entirely deterministic.

## Default locality

By default, the projection concerns only the `continuity` requested.

The messages of parent or child continuities are not included automatically.

## Cross-continuity projection

A projection can explicitly ask for elements linked to:

- `parent_id`
- `children[]`
- a specific `continuity`

In this case, `continuity.project` can build a composed view. This inclusion remains explicit. It never turns the whole tree of continuities into an automatic context.

## Instrumental messages

The default projection can exclude certain messages that are too instrumental, for example:

- projection calls
- projection results
- detailed traces of semantic discovery
- very verbose results of Capacities

if the `default_projection` provides for it.

These messages nevertheless remain fully present in the journal.

## `current_message`

The `current_message` does not need to be included by `continuity.project` to be available to the LLM. The runtime injects it as a separate block in the composite context.

The projection can nevertheless contain this same message if the projection rules include it.

This logical duplication is allowed because the two blocks do not have the same function:

- `continuity_projection` = context
- `current_message` = active stimulus

## `initial_intention`

`initial_intention` stays injected explicitly as a block of the LLM context. It therefore does not depend on the projection to be visible.

## `current_intention`

`current_intention` also stays injected explicitly. It does not depend on `continuity.project`.

## Capacities

`available_capacities` is built separately. `continuity.project` does not decide which Capacities must be offered to the LLM.

## Traceability

When a dynamic projection is explicitly requested by the pilot, the call to `continuity.project` and its result are recorded in the journal.

The effective projection built for the context does not need to be persisted as a new durable state.

## Determinism

For the same `continuity`, the same state, the same projection definition and the same budget, `continuity.project` must produce the same view.

It must not depend on any internal LLM inference.

## Errors

The Capacity returns an error in particular if:

- the `continuity` does not exist,
- the projection definition is invalid,
- a requested filter is inconsistent,
- an explicitly referenced `continuity` does not exist,
- the budget supplied is invalid.

An empty projection can be a valid result if the filters select no message.

## What `continuity.project` does not do

- It does not modify the `continuity`.
- It does not modify the `runtime`.
- It does not modify the `mailbox`.
- It does not launch the LLM.
- It does not produce a cognitive summary.
- It does not discover a Capacity.
- It does not change `current_intention`.
- It does not persist a new condensed memory.
- It never replaces the continuity as the source of truth.

## Central invariant

`continuity.project` guarantees:

```
a complete durable memory
  + a deterministic projection rule
  + a possible budget
  → a focused, reproducible and non-cognitive view
    without ever altering the source
```

## Implementation constraint

The continuity is managed in a Matter (the journal) for persistence, and duplicated in an in-memory object of the wrapper for performance. It is not a question of formalization but of implementation; see `agentic_core_concurrency_model`. For this Capacity: the projection reads the in-memory object, rebuilt from the journal if it is absent or stale, and applies its criteria (including the characterization of lines) on it. The line sizes are those computed once at insertion into the object.
