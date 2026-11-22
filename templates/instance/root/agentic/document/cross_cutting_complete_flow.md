# Cross-cutting document — Complete flow of the agentic context

## Intention

Reassemble the whole agentic model into a coherent end-to-end flow.

This document defines no new concept. It checks how:

- `continuity`,
- `mailbox`,
- `runtime`,
- `pilot_definition`,
- `pilot.activate`,
- the Brique Intentions,
- the `wait_group`s,
- the controls,
- the projections,
- the recovery

fit together in a single cycle.

## 1. Creation of a continuity

A new continuity is created by `continuity.create`.

Main inputs:

- `pilot_ref`
- `initial_intention`
- possible `parent_id`
- possible `structure_path`

The Capacity creates `continuity`, `mailbox` and `runtime` through a recoverable idempotent protocol (continuity, mailbox, runtime, parent relation, Structure relation). Each Matter is created by an atomic local commit; there is no multi-Matter transaction.

The `continuity` is initialized with:

- `initial_intention`
- `current_intention = initial_intention`
- `pilot_ref`
- `journal_ref` to a journal that contains only the cognitive framework of the pilots (its first line, `kind: framework`, see `pilot_framework`): no exchange message

The `mailbox` is empty.

The `runtime` is:

- `status = idle`
- `run_id = null`
- `current_message_id = null`
- `active_wait_groups[] = []`

No cognition is triggered by the creation itself.

## 2. First message

A first event is sent by `mailbox.send`.

The message can come from:

- a user,
- another continuity,
- an external system,
- a delegation,
- a control.

`mailbox.send` is a recoverable multi-Matter operation, whose commit point is the append to the journal:

1. validates the message;
2. decides idempotence (durable message? mailbox entry?);
3. reserves the `pending` mailbox entry;
4. adds the message to the journal with its `seq` (line number, `matter.append` under lock and `expected_line_count`) — **semantic commit**;
5. finalizes the routing: determines the priority, evaluates a possible correlation with a `wait_group`, applies a control;
6. decides whether the continuity must be woken, without waiting for the run (non-blocking activation).

Before the commit, the message does not exist; after it, it is never appended again and the routing is repairable.

## 3. Activation

If the message is `normal` and `runtime.status = idle`, `mailbox.send` causes `pilot.activate(continuity_id)`.

`pilot.activate` tries, in one `runtime` write guarded by `expected_revision`:

```
idle → running
run_id = new run
```

If the acquisition fails because another run is already active, no second run is created.

## 4. Start of the `pilot` loop

The active run becomes the sole cognitive owner of the continuity.

Before processing a normal message, it checks:

1. the applicable priority controls,
2. the `wait_group`s already satisfied,
3. then the normal mailbox.

If a normal message must be processed, `mailbox.read` selects and claims the next eligible message in one mailbox write guarded by `expected_revision`. `runtime.current_message_id` receives this `message_id`.

## 5. Building the LLM context

Before the inference, `pilot` builds a composite context.

Logical order:

1. the stable system text, marked `cache: true`: the cognitive framework, `pilot_definition` (prompt, rules, guide), its stable `available_capacities` and the `initial_intention`
2. the conversation: the projected journal, one line per message with its role (the last message marked `cache: true`)
3. what is new, in one last message: `current_intention`, `current_message` (designated by `id`, `kind`, `from` when the conversation contains it), possible active stimulus

The active stimulus can be:

- `active_wait_result`
- `execution_result`
- `validation_error`

The conversation comes from the projection built by `continuity.project`, a deterministic view of the complete memory, converted line by line into messages (see `capacity_pilot_activate`, "The journal is the conversation"). It never replaces the journal as the source of truth.

## 6. Inference

The LLM receives:

- the context,
- the model,
- the parameters,
- the expected protocol,
- the schema of `pilot_output`.

The LLM carries the cognition. It decides what it wishes to do. The engine imposes no mandatory phases of reflection, planning or evaluation.

## 7. `pilot_output`

The result of the LLM is structured. It can contain in particular:

- `intention_update`
- `projection_request`
- `intentions[]`
- `wait_group`
- `messages[]`

An empty output is invalid.

## 8. Validation

Before any effect, the whole `pilot_output` is validated.

The validation checks in particular:

- the structure,
- the allowed combinations,
- the references,
- the destinations,
- the targeted Capacities,
- the known inputs,
- the `wait_group`s.

If the output is invalid, no effect is applied.

A single correction inference is allowed. It receives the previous output and the validation errors.

If the correction remains invalid, the current processing fails explicitly.

## 9. Application of the `pilot_output`

Once valid, the output is applied in a deterministic order:

1. `intention_update`
2. possible `projection_request`
3. possible `wait_group`
4. `intentions[]`
5. `messages[]`

The `wait_group` is created before the invokes of the awaited intentions it references.

## 10. Intention change

If `intention_update` is present:

- `current_intention` is modified explicitly;
- the change is traced in the journal.

The new intention becomes effective for every following inference. `initial_intention` remains unchanged.

## 11. Projection request

If `projection_request` is present, it constitutes a request for a new view before any further decision.

It does not coexist with `intentions[]` nor `wait_group`.

The runtime:

1. executes `continuity.project`,
2. rebuilds the context,
3. launches an inference again.

This new inference is not artificial: it is explicitly requested by the cognition in order to have a different context.

## 12. Emission of Brique Intentions

The `pilot` can emit several Brique Intentions in the same output.

Each Intention is immediately added to the journal before or at the time of its actual emission.

The transformations can run in parallel. Parallelism belongs to the transformations. The cognition of the continuity stays serialized.

## 13. Fire-and-forget and awaited intentions

Each `pilot_intention` carries `await_response`, chosen by the pilot, and possibly `target_type` (family of the target, `user` by default).

- `await_response = false`: fire-and-forget. The invoke is non-blocking, no Response is awaited or tracked, and the intention can never appear in a `wait_group`. The pilot does not go to `waiting`.
- `await_response = true`: the Response is awaited. It is transformed into a canonical Message (`kind = capacity_response`, `correlation.intention_id = intention.id`, `id` derived deterministically from `intention.id`) then delivered by `mailbox.send`, which captures it in the `wait_group` or admits it as a normal message.

A `wait_group` that references an intention with `await_response = false` is invalid.

## 14. Creation of a `wait_group`

If the `pilot` wishes to wait for certain events, it creates a `wait_group`.

The group describes:

- `condition`
- `expected[]`
- `received[]`
- `status`

The `expected` can reference:

- Intentions of this same output with `await_response = true` (`expected.ref = intention.id`),
- Intentions already in progress,
- returns from continuities,
- correlatable external events.

Only one new `wait_group` can be created per `pilot_output`.

## 15. Transition to `waiting`

If the condition of the `wait_group` is not yet satisfied and nothing else lets the cognition advance:

- `runtime.status` becomes `waiting`;
- the same `run_id` stays owner;
- `runtime.current_message_id` stays unchanged;
- the current message is not `ack`ed.

## 16. Arrival of an awaited Response

The Response of an Intention `await_response = true` is never consumed directly: it enters through `mailbox.send` as a `capacity_response` message, correlated by `intention_id`.

- It is added to the journal (semantic commit).
- The `wait_group` concerned receives its `received_item`.
- Its condition is re-evaluated.

A Response without a corresponding `wait_group` enters as a normal message.

## 17. Arrival of an awaited external event

A result coming for example from a child continuity, another pilot or an external event enters through `mailbox.send`.

If its correlation matches an active `wait_group`:

- the message is qualified `expected`,
- its entry, reserved `pending` then committed, is claimed by the active run at the start of the routing (if the `wait_group` is no longer active at that moment, it falls back to `normal`),
- added to `received[]`,
- and never goes through `mailbox.read`.

## 18. Condition not satisfied

If the new event does not yet satisfy the condition, the group stays `waiting`.

If no other cognition is available, `runtime.status` stays `waiting`.

## 19. Condition satisfied

When the condition becomes satisfied:

- `wait_group.status = satisfied`
- `runtime.status = running`

An `active_wait_result` is built. It says that the wait is over and how many responses it holds; the responses are in the conversation, each with the identifier of its intention.

## 20. Resumption after a wait

A new inference is performed with:

- the same `current_message`,
- the same causal course,
- `active_wait_result` as an additional stimulus.

The pilot thus knows why the cycle exists, and which new event now lets it advance.

`active_wait_result` is consumed after this inference.

## 21. Completion of the `wait_group`

After the associated cognitive resumption:

- the `wait_group` goes `completed`,
- then is removed from `active_wait_groups`.

Its history stays in the journal.

## 22. Several `wait_group`s

Several `wait_group`s can exist in `runtime`.

If several become `satisfied`, they are processed one by one. The order follows the stable order of `active_wait_groups`. A single `active_wait_result` is injected per cognitive resumption.

## 23. `any`, `first_success` and `n_of_m`

Some `wait_group`s can be satisfied before all the awaited operations have answered.

The remaining operations continue normally. Their late results:

- are added to the journal,
- do not reopen the `wait_group`,
- do not automatically trigger a new inference.

## 24. Execution error

Once a `pilot_output` is validated, some effects can succeed before a later action fails.

There is no global transaction with rollback, and no multi-Matter transaction either.

At the first failure:

- the remaining actions are not executed;
- the previous effects remain acquired.

An `execution_result` is built with:

- the successful actions,
- the failed action,
- the error obtained.

## 25. Resumption after an error

`execution_result` becomes the active stimulus of a new inference.

The pilot can then decide:

- to retry,
- to check the world,
- to change approach,
- to continue,
- or to give up.

The actions already successful are never replayed implicitly. A retry is always a new explicit decision.

## 26. Sending messages

The `pilot_output` can contain `messages[]`.

Each message is addressed explicitly. An output toward a user is simply a message addressed to `user`.

A message toward another continuity goes through `mailbox.send(target_continuity)`.

The source continuity also keeps the trace of its emission.

## 27. Delegation

A delegation can be built by composition: `continuity.create`, then `mailbox.send` to the new continuity.

The child `continuity` has:

- its own intention,
- its own pilot,
- its own mailbox,
- its own runtime,
- its own history.

The parent can wait for its result through a `wait_group`.

## 28. Return of a child continuity

When the child has a result to transmit, it sends a message to the mailbox of the parent.

- If the parent explicitly waits for this return: the message satisfies the corresponding `wait_group`.
- Otherwise: it becomes a normal message of the mailbox of the parent.

## 29. Controls

The controls also go through `mailbox.send`.

Examples:

- `suspend`
- `resume`
- `set_intention`
- `set_pilot`
- `delete`

They have `priority = control`.

## 30. Control during `running`

A control arriving during `running`:

- is added to the mailbox,
- stays `pending`,
- does not interrupt the inference or the Capacity currently in progress.

At the next safe point, it is processed before a new normal message.

## 31. Control during `waiting`

A control arriving during `waiting` follows the same rule. A wait already engaged is not abruptly interrupted.

When an awaited event arrives and produces a safe point, the priority controls can be applied before a new cognition.

## 32. Control during `idle`

If the continuity is `idle`, a purely operational control can be applied directly without launching the LLM.

For example: `set_intention`, `set_pilot`, `suspend`, `delete`.

After application, if the continuity remains activatable and normal work exists, `pilot.activate` can be triggered.

## 33. `resume`

`resume` has a particular behavior.

If `status = suspended`:

- `resume` is applied directly;
- `suspended → idle`.

Then, if work waits in the mailbox, `pilot.activate` can be triggered.

## 34. `suspend`

When `suspend` is applied:

- `runtime.status = suspended`
- `runtime.run_id = null`

The `continuity` stays existing. The mailbox can keep receiving messages. No normal message triggers `pilot.activate` as long as the continuity stays `suspended`.

## 35. `delete`

`delete` is applied at the next safe point if a run is active. Once applicable, no new work is started.

`continuity.delete` removes:

- `continuity`
- `mailbox`
- `runtime`
- Structure references

and maintains the consistency of the parent/child relations.

## 36. End of the processing of the `current_message`

The `current_message` is finished when no cognitive resumption associated with its chain remains. In particular:

- no `projection_request`,
- no `validation_error`,
- no active `execution_result`,
- no `wait_group` blocking the current chain.

*Fire-and-forget* actions do not keep the message open.

## 37. Ack

Once the processing is finished, `mailbox.ack(current_message_id, run_id)` removes the mailbox entry, in one write guarded by `expected_revision`. The message stays in the journal, which is the durable journal: "message present without entry" means "accepted then consumed".

Then `runtime.current_message_id = null`.

## 38. Next message

The run does not necessarily end after one message. It can immediately:

- process controls,
- resume a `satisfied` `wait_group`,
- read the next normal message.

The same `pilot.activate` can therefore process several messages successively. They remain nevertheless strictly serialized.

## 39. Transition to `idle`

When no immediate work exists any more, the run releases the continuity through a release-and-recheck:

1. last check of the mailbox, the available resumptions and the controls;
2. write `runtime.status = idle`, `run_id = null` (guarded by `expected_revision`);
3. re-read the mailbox and the runtime;
4. if the runtime is still `idle` and activatable work is present: new `idle → running` acquisition with a new `run_id`, and the run continues;
5. otherwise it stays `idle`.

The second test takes place after the publication of `idle`, which closes the lost-wake-up window against a concurrent `mailbox.send`.

`pilot.activate` returns.

## 40. Crash

If the process brutally disappears:

- The journal remains the durable truth;
- `mailbox` keeps the `pending` or `claimed` messages;
- `runtime` keeps the known operational state.

At restart, a possible orphan `run_id` is detected.

## 41. Recovery after a crash

Recovery rebuilds:

- the `current_message`,
- the actions already performed,
- the Intentions already emitted,
- the Responses already present,
- the `wait_group`s,
- the pending controls.

No Intention already emitted is replayed automatically.

## 42. Unknown state

If an Intention was emitted but its real state cannot be determined, the system represents this uncertainty explicitly. It assumes neither success nor failure.

The pilot can then reason from `unresolved` and decide on a check or a new action.

## 43. Resumption of the run

If the cognition can be resumed:

- a new `run_id` takes possession of the continuity;
- the necessary claims can be reassigned, each by a guarded mailbox write;
- the same `current_message` can be kept;
- the cognition resumes from the real durable state.

## 44. Global invariants

- A `continuity` always has an `initial_intention`.
- A `continuity` always has a `current_intention`.
- A `continuity` always has a `pilot_ref`.
- A `continuity` has exactly one `mailbox`.
- A `continuity` has exactly one `runtime`.
- A `continuity` never has more than one active cognitive run.
- A durable message is immutable.
- The order of the messages is canonical.
- A normal message is cognitively processed only once at a time.
- An `expected` message does not then join the normal FIFO.
- A message accepted then consumed (present in the journal, without a mailbox entry) cannot be reinserted.
- Brique offers no multi-Matter transaction: multi-Matter operations are recoverable (atomic local commits, `expected_revision`, deterministic order, idempotent steps).
- A mailbox entry without a durable message is a pre-commit reservation, never eligible.
- A real effect already produced is never automatically rolled back.
- An Intention already emitted is never replayed implicitly.
- The complete memory stays in the journal.
- The `mailbox` carries only the consumption state.
- The `runtime` carries only the present operational state.
- The `pilot_definition` carries only the configured cognition.

The LLM decides. Brique materializes, orchestrates, waits, traces and resumes.

## 45. General movement

The complete movement can be summarized thus:

```
intention
  ↓
continuity
  ↓
incoming message
  ↓
mailbox
  ↓
pilot.activate
  ↓
projection of the useful past
  ↓
LLM
  ↓
pilot_output
  ↓
Intentions / messages / waits
  ↓
transformations of the world
  ↓
Responses / events
  ↓
enriched continuity
  ↓
possible new cognition
  ↓
end of the message
  ↓
next message or idle
```

The cognition is therefore not locked in an artificial agentic loop. It evolves in a persistent continuity in interaction with an explicit Brique world.

## 46. Architectural result

The agentic context finally produces a model in which:

- the cognition can stay open,
- the execution stays deterministic where it must be,
- the memory is durable,
- the LLM context stays projected,
- the delegation is fractal,
- the concurrency is controlled,
- the effects are traceable,
- a technical interruption does not require falsifying or replaying history.

The system does not seek to represent thought itself. It represents the conditions that allow a thought to keep acting in time.
