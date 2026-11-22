# Matter schema — `runtime`

## Intention

Represent the present operational state of a continuity.

The `runtime` Matter contains neither the history of the continuity, nor its intention, nor its cognitive identity.

It carries only the minimal state needed to know:

- whether a cognition is active,
- which run currently owns the continuity,
- which message is being processed,
- which external waits are still open,
- how to resume execution correctly.

## Element and data

This Matter is described by two schemas, which must not be confused:

| Schema | Describes | Content |
|---|---|---|
| `runtime` (element) | the Matter as a Brique element | `brique.kind: matter`, `substance_mode: brique`, `substance_type: text`; `objective.type: runtime`; `functional`: `format: json` and `schema_ref` |
| `runtime_data` (data) | the **substance** of the Matter | the JSON document described by the tables below |

The content is the substance of the Matter (`<matter_id>.json`), never its descriptor. The `functional.schema_ref` field of the descriptor designates `/root/agentic/schema/runtime_data`.

## Schema (`runtime_data`)

### `runtime`

| Field | Description |
|---|---|
| `continuity_id` | Identifier of the associated continuity. |
| `status` | Current operational state of the continuity. Expected values: `idle`, `running`, `waiting`, `suspended`. |
| `run_id` | Identifier of the run that currently owns the continuity. Present only when `status = running` or `waiting`. |
| `current_message_id` | Identifier of the message currently being processed. Present during the whole cognitive chain opened by this message. May remain present when `status = waiting`. |

The context size of the last inference is not a state of `runtime`: it is a measurement, recorded as an `event` line (`inference_measured`) of the journal (see `schema_matter_continuity`). The compaction pressure reads it in the in-memory object.
| `active_wait_groups[]` | Set of the wait groups still needed by the runtime. |

## Semantics of `status`

### `idle`

The continuity exists but no cognitive run is active.

- `run_id = null`
- `current_message_id = null`, except for an internal atomic transient state that cannot be observed.

A new normal message can cause `pilot.activate`.

### `running`

A run owns the continuity. The cognition can currently advance.

- `run_id` is mandatory.
- `current_message_id` is present when a mailbox message is being processed.

### `waiting`

A run still owns the continuity. The cognition cannot currently advance without waiting for an external event.

- `run_id` remains present.
- `current_message_id` remains that of the message that opened the current cognitive chain.

### `suspended`

The continuity exists but no cognition must be activated automatically.

- `run_id = null`.
- Messages can keep arriving in the mailbox.

## Ownership invariant

A continuity never has more than one active cognitive run.

`run_id` represents the current logical owner of the continuity.

The transition `idle → running` takes possession of the runtime in a single write of the `runtime` Matter, guarded by `expected_revision`.

If two concurrent activations try to acquire the continuity, only one can succeed. The other gets a revision conflict, re-reads the runtime, finds that the continuity is no longer `idle` and launches no second run. This guarantee is durable: it does not rely on a lock held during the life of the run.

## `current_message_id`

`current_message_id` designates the causal message currently being processed.

Typical cycle:

```
mailbox.read
  → claim of message M42
  → runtime.current_message_id = M42
  → running
  → possible inferences and Capacities
  → possible waiting
  → resumption
  → complete end of the processing
  → mailbox.ack(M42)
  → current_message_id = null
```

The current message therefore stays stable during the whole cognitive chain it triggered.

## Wait group

A wait group represents an operational waiting condition. It contains no cognitive logic.

### `wait_group`

| Field | Description |
|---|---|
| `id` | Unique identifier of the group. |
| `condition` | Deterministic satisfaction condition. Expected values: `all`, `any`, `first_success`, `n_of_m`. |
| `n` | Minimal number of results needed. Present only when `condition = n_of_m`. |
| `expected[]` | Set of the expected events. |
| `received[]` | Set of the events already received. |
| `status` | Operational state of the group. Expected values: `waiting`, `satisfied`, `completed`. |

### `expected_item`

| Field | Description |
|---|---|
| `type` | Nature of the expected element. Expected values: `intention`, `continuity`, `event`. |
| `ref` | Reference to the expected element. |
| `correlation` | Information needed to recognize without ambiguity the corresponding event. |

### `received_item`

| Field | Description |
|---|---|
| `expected_ref` | Reference to the element of `expected[]` to which this result corresponds. |
| `message_id` | Reference to the durable message present in the journal. |
| `outcome` | Operational result. Expected values: `success`, `error`. |

No business content of a response is duplicated in `runtime`. The real content stays in the journal.

## `expected` and `received`

`expected[]` describes what is expected. `received[]` describes what has actually arrived.

The *pending* state of an element is not stored. It is derived by:

```
expected - received
```

This avoids maintaining several representations of the same state.

## Satisfaction conditions

### `all`

The group becomes `satisfied` when all the elements of `expected[]` have a corresponding `received_item`.

### `any`

The group becomes `satisfied` as soon as the first result is received. The result can be a `success` or an `error`.

### `first_success`

The group becomes `satisfied` as soon as the first `received_item` with `outcome = success`.

If all the expected elements have been received with `outcome = error`:

- the group nevertheless becomes `satisfied`,
- the resumption explicitly indicates that no success was obtained.

### `n_of_m`

The group becomes `satisfied` when:

```
number of received_item >= n
```

The other events can keep producing their results after the group is satisfied.

## Life cycle of a wait group

```
waiting          The condition is not yet satisfied.
  ↓ condition satisfied
satisfied        The group has provided enough information
                 to allow a cognitive resumption.
  ↓ associated cognitive resumption
completed
  ↓
removal from active_wait_groups[]
```

`completed` is therefore a conceptual transient state before removal.

Finished groups do not stay durably in `runtime`. Their history stays in the journal.

## Relation between `status` and wait groups

`runtime.status = waiting` means:

- no cognition can currently advance,
- at least one external wait is still open.

When a wait group becomes `satisfied`, `runtime.status` goes back to `running`. The cognition can then resume.

## Several wait groups

Several wait groups can exist simultaneously.

A `pilot_output` creates at most one new wait group at a time, but groups from previous decisions can still be active.

Example:

```
WG1 = waiting
WG2 = waiting
WG3 = waiting
```

If WG2 becomes `satisfied`:

- `runtime.status = running`,
- the cognition resumes on WG2,
- WG1 and WG3 keep waiting.

## Several satisfied groups

If several wait groups become `satisfied` before being processed:

- they are resumed one by one,
- in the stable order of `active_wait_groups[]`.

Only one wait-group result is injected as the active stimulus of a given inference.

Example:

```
WG1 = satisfied
WG2 = satisfied
WG3 = waiting
  ↓
resumption on WG1
  ↓
WG1 completed then removed
  ↓
resumption on WG2
  ↓
WG2 completed then removed
  ↓
WG3 stays waiting → runtime.status = waiting
```

## Active result

When a group becomes `satisfied`, its result is presented to the pilot as `active_wait_result`.

`active_wait_result` is not an additional durable state of `runtime`. It is built from the wait group, but it shows the pilot only what is useful to it: that the wait is over and how many responses it holds (`outcome`, `received`, `expected`). Neither the identifier of the group, nor its condition, nor the identifiers of the messages: the responses are in the conversation, each with the identifier of its intention. It is injected into the resumption inference then considered consumed.

The corresponding messages remain durably accessible in the journal.

## Late responses

For `any`, `first_success` and `n_of_m`, some events can arrive after the group is satisfied.

These results:

- are added to the journal,
- never reopen a finished wait group,
- do not automatically trigger a new cognitive resumption.

They simply become durable facts available for future projections.

## Errors

A `received_item` with `outcome = error` counts as a received event.

The runtime does not decide cognitively what to do about this error. It applies only the condition of the group.

The pilot will then decide whether to:

- retry,
- change approach,
- continue,
- or give up.

## Transition to `idle`

The transition `running → idle` follows a release-and-recheck:

```
check that there is no more immediate work
  ↓
write status = idle, run_id = null   (expected_revision)
  ↓
re-read mailbox + runtime
  ├─ runtime still idle AND activatable work present
  │     → retry idle → running with a new run_id (expected_revision)
  └─ otherwise → stay idle
```

The second test takes place after the publication of `idle`. If a `mailbox.send` saw `running` just before, the run sees it on the re-read and reactivates; if the `send` sees `idle`, it activates itself, and the guarded acquisition guarantees that only one wins.

## Suspension

When `suspend` is applied at a safe point:

- `status` becomes `suspended`,
- `run_id` becomes `null`.

The continuity is no longer automatically activatable by normal messages. Messages nevertheless keep being stored in the mailbox.

## Resume

When a valid `resume` is applied: `suspended → idle`.

If work is present in the mailbox, `pilot.activate` can then acquire the runtime normally.

## What does not belong to this schema

- No complete messages.
- No initial intention.
- No current intention.
- No `pilot_ref`.
- No pilot definition.
- No Capacity response content.
- No history of finished wait groups.
- No wait-group timestamps.
- No generic metadata.
- No LLM projection.
- No cognitive cache.
- No Structure placement.

The runtime carries only the minimal operational state needed for the present execution and its resumption.
