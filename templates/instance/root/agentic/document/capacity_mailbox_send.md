# Capacity — `mailbox.send`

## Intention

Send a message to a continuity.

`mailbox.send` is the normal entry point of everything that comes from outside a continuity.

It guarantees that the message first becomes a durable fact in the journal, then becomes consumable through the associated mailbox.

It also handles:

- the qualification of priority,
- the possible correlation with an active `wait_group`,
- the claim, after the commit, of `expected` messages,
- the possible wake-up of the continuity.

## Input

### `mailbox.send`

| Field | Description |
|---|---|
| `continuity_id` | Identifier of the target continuity. |
| `message` | Message to send. |

The message contains at least:

- `id`
- `from`
- `to`
- `kind`
- `content`
- `created_at`
- `correlation`

The `seq` is not supplied by the caller. It is assigned at the time of the append to the journal.

## Preconditions

- The target `continuity` must exist.
- The associated `mailbox` must exist.
- The associated `runtime` must exist.
- The `message.id` must be valid.
- The `message.to` must match the target continuity or a destination consistent with the operation.

## Recoverable multi-Matter operation

`mailbox.send` modifies several Matters (the journal of the continuity, `mailbox`, `runtime`). Brique offers no multi-Matter transaction: the operation is composed of individually atomic local commits, each guarded by `expected_revision` (or, for the journal, by `expected_line_count`), and its global consistency is rebuilt by idempotent steps. The complete model is in `agentic_core_concurrency_model`.

Order of the steps:

1. validation of the message;
2. idempotence decision;
3. reservation of the entry in `mailbox.entries` (`pending`);
4. append to the journal — **semantic commit**;
5. finalization of the routing (`normal`, `control`, `expected` / capture in the `wait_group`);
6. possible wake.

Before step 4, the message does not exist and the operation can be abandoned or cleaned up. After it, the message is durably accepted: it is never appended again, and the routing steps are idempotent and repairable from the durable message and the runtime state.

```
message sent
  =
durable message in the journal of the continuity
  +
consistent mailbox entry (or consumed since)
```

## Reservation of the entry

The entry is reserved before the append. It is an ordinary `pending` entry, with no provisional status. An entry whose `message_id` matches no durable message is inert: `mailbox.read` never claims it, and a retry of the same `message.id` takes over the reservation.

## Append to the journal

The message is added at the tail of the journal (`matter.append`). The `seq` is the line number of the message:

```
seq = number of lines already committed + 1
```

The append is guarded by `expected_line_count` equal to that number of lines, under the critical-section lock of the continuity. In case of conflict, the tail of the journal is re-read and `seq` recomputed. The cost of the append is that of the line, independent of the size of the journal. Once written, the message becomes immutable. The `revision` is never used as `seq`.

## Inference window

The journal is the conversation of the model: a tool response written while an inference is running would be placed between what the model read and what it decided. The write of step 4 therefore waits for the **inference window** of the continuity (see `agentic_core_concurrency_model`) when an inference holds it, unless the writer is the holder:

- the wait comes **after** the reservation of the entry (step 3): a retry of the same `message.id` takes over the reservation, as before;
- it precedes taking the critical-section lock of the journal;
- during the wait, `mailbox.send` sends a `running` acknowledgement (the delivery of a tool response by `pilot.activate` must not time out);
- the waiting callers are served in order of arrival;
- the output contract does not change: the `seq` is known on return, the commit point remains the append.

**Opening the window.** A message of kind `user` addressed to a continuity whose pilot is a `pilot_definition` (Matter) and whose `runtime.status` is `running` or `waiting` opens the window in the operation that writes it, before the append. It is handed to the loop of the pilot, which absorbs the request (see `capacity_pilot_activate`). If the loop does not take it, it expires after a few tens of seconds, and at the suspension and the deletion of the continuity; the message, already in the journal, becomes a normal message again. An `idle` or `suspended` continuity opens no window.

`mailbox.send` does not refuse a `user` message addressed to a continuity with a Capacity pilot: usage wants only the parent to write to it, and the engine does not distinguish a human from their parent.

## Priority

`mailbox.send` determines the priority of the entry. Possible values:

- `control`
- `expected`
- `normal`

### `control`

A control message receives `priority = control`.

### `expected`

A message receives `priority = expected` only if it actually matches an active `wait_group` at the time of its arrival.

The correlation must be unambiguous. If no active `wait_group` matches: `priority = normal`.

### `normal`

Any other message receives `priority = normal`.

## Content of the entry

The reserved entry contains:

- `message_id`
- `priority`
- `status`

It is created `pending`, without an owner. `claimed_by` appears only when a run claims the entry.

## Archived continuity: redirection

A continuity that names a `successor_id` no longer receives anything. `mailbox.send` follows the chain to the active continuity of the lineage and accepts the message there with its original identifier. The response carries `redirected_to`.

- An identifier already accepted **and consumed** by the archive is never reinserted into the next one: refusal of reinsertion.
- A message accepted by the source while `continuity.succeed` switches the routing is **relayed** to the next one after its commit, never left orphaned in the archive.

## Kinds reserved for history

Messages of kind `summary`, `prompt`, `completion`, `event`, `projection` and `error` are history: `agentic_core` records them itself, they have no mailbox entry and `mailbox.send` refuses them if they come from outside.

## Voluminous content

A message is written as it is in a journal line, whatever its size. There is no 64 KiB threshold and no carrying by reference.

## `expected` case

If the message matches an active `wait_group`, its entry is reserved `pending` like the others. After the commit, at the start of the routing, it is claimed by the active run:

- `status = claimed`
- `claimed_by = runtime.run_id`

If the `wait_group` is no longer active or no longer matches at that moment, the message falls back to `normal` routing: the priority of the entry becomes `normal` in the same guarded write, and the entry stays `pending`.

The message is recorded as received in the corresponding `wait_group`. `received[]` is updated with:

- `expected_ref`
- `message_id`
- `outcome`

The message never goes through `mailbox.read`.

### Evaluation of the `wait_group`

After attaching the result, `mailbox.send` evaluates the deterministic condition of the `wait_group`.

- **If the condition is not satisfied**: the runtime stays `waiting` if no other cognition can advance.
- **If the condition becomes satisfied**:
  - `wait_group.status = satisfied`
  - `runtime.status = running`

The existing run is then woken or made resumable according to the execution mechanism of the wrapper.

### A single `wait_group`

An `expected` message can satisfy only one `wait_group`.

If several groups seem able to consume it, the correlation is considered invalid or ambiguous. The message must never be consumed simultaneously by several groups.

## `control` case

If `priority = control`, the behavior depends on the current runtime.

### If `status = running` or `waiting`

The control stays `status = pending`. It causes no immediate interruption. It will be processed at the next safe point by the active run.

### If `status = idle`

The control can be applied directly without launching an LLM inference.

Depending on the control:

- `set_intention` → explicitly modify `current_intention`
- `set_pilot` → explicitly modify `pilot_ref`
- `suspend` → `runtime.status = suspended`
- `delete` → trigger the deletion according to `continuity.delete`
- `resume` → functional no-op if the continuity is not `suspended`

After application, the entry is removed from the mailbox by the send operation itself (no run owns it, hence no `mailbox.ack`). The durable message is kept. Exception: `delete` is terminal, the mailbox disappears with the continuity.

### If `status = suspended`

The ordinary controls (`set_intention`, `set_pilot`, `suspend`) stay `pending`. `resume` and `delete` are the special cases:

- `resume`: see below;
- **`delete` is applied immediately** (November 11). A suspended continuity has no run: nothing is in progress, deleting it is safe. Deferring it would have condemned it to stay there forever (no run will ever reach a safe point), and the person who suspended a conversation could no longer delete it. The deletion goes through `continuity.delete` as in the `idle` state; the mailbox disappears with the continuity.

### `resume` in `suspended`

If the control is `resume` and `runtime.status = suspended`:

- `resume` is applied immediately;
- `suspended → idle`;
- `run_id` stays `null`;
- the entry of the control is removed by the send operation, the durable message is kept.

Then, if consumable work remains in the mailbox, `pilot.activate` can be triggered.

## Wake logic

The activation requested is marked `wake: true`: it answers at once and never waits for another run. An explicit activation of a pilot, on the other hand, waits for the run in progress (see `capacity_pilot_activate`).

For a normal message:

- `runtime.status = idle` → `mailbox.send` triggers `pilot.activate` without waiting for the end of the run: the activation is non-blocking for `mailbox.send`, but its request is not lost. It goes through the port (`wake`), in a task of its own that waits for the Response; if the callee is busy ("busy"), the request is asked again by the runtime of the wrapper a bounded number of times (more attempts than for an interactive call), and a persistent refusal is written in the journal of the continuity (`wake_refused`). A timeout or a transport loss is never replayed blindly, because the activation may have started: the durable state of the runtime and of the mailbox is re-read; if the work is still `idle` and `pending`, a `wake_failed` line with `outcome: unknown` makes the incident visible. If the work has started or no longer waits, no false failure is written. See "A "busy" refusal is transient" in `agentic_core_concurrency_model.md`.
- `runtime.status = running` → no new run is created.
- `runtime.status = waiting` → no new run is created.
- **A deferred control** (a deletion, a suspension… requested during a run) **wakes the continuity if that run is dead**: a `running` or `waiting` whose run is no longer alive in this process is an orphan (the wrapper restarted); without a wake-up, nobody would ever reach the safe point that applies the control. The activation takes over the orphan and applies the control. A live run is only notified (`liveness.notify`).
- For a `user` message addressed to a Matter pilot whose run is `running` or `waiting`: the inference window is opened (see above) and the loop is woken (`liveness.notify`) to absorb the request, without waiting for the end of the chain.
- `runtime.status = suspended` → no automatic wake-up.

For an `expected` message: the existing run is used. No new `pilot.activate` is launched.

## Idempotence

Idempotence is read on the state of the message in the journal and of the entry in `mailbox.entries`:

| Durable message | Mailbox entry | Interpretation |
|---|---|---|
| absent | absent | never accepted, new message |
| absent | present | send interrupted before the commit: pre-commit reservation, the retry takes it over |
| present | present | accepted message still active, or post-commit routing to finish: idempotent retry |
| present | absent | message accepted then consumed: reinsertion forbidden |

This table holds for the **same fingerprint** of content. The identity of a message is its `id` and the fingerprint of `from`, `kind`, `content` and `correlation` (see `agentic_core_concurrency_model`). The same `id` with another fingerprint is an identity conflict, explicitly refused, whether the first message is only reserved, accepted and active, or already consumed.

An idempotent retry adds no new append, no new entry, no second capture and no new wake. The existing state is returned. No additional trace is needed: the journal is the durable source and `mailbox.entries` is only the active working index.

## Message already consumed

A message already present in the journal but absent from `mailbox.entries` was accepted then consumed: it cannot be reinserted.

An attempt to reuse this same `message.id` must be refused.

If a new processing is needed, a new message must be created with a new `id`. It can be correlated with the previous message.

## `from` and `to`

`from` and `to` must be explicitly present in the durable message.

The caller can optionally omit their technical construction if the execution context lets the runtime infer them. But before persistence, `from` and `to` must be materialized explicitly.

## Correlation

`mailbox.send` uses `correlation` to recognize in particular:

- a response to an Intention,
- a return from a continuity,
- a `wait_group`,
- a causal relation.

It creates no implicit cognitive correlation. Any correlation used for special routing must be explicit and verifiable.

## Outcome

When an expected message represents the result of an operation, `mailbox.send` determines or receives its operational `outcome`: `success` or `error`.

This fact is stored in `received[]` of the `wait_group`. The detailed content of the result stays only in the journal.

## FIFO order

`mailbox.send` does not depend on the physical order of `entries[]`.

The next normal processing will be determined later by `mailbox.read` according to `priority`, then `message.seq`.

## Traceability

The message itself constitutes the main durable trace of the arrival.

The significant operational changes produced by `mailbox.send` can also be written in the journal when necessary to understand the course.

Examples:

- `wait_group` satisfied
- `resume` applied
- `suspend` applied in `idle`
- control applied directly

## What `mailbox.send` does not do

- It does not cognitively process a normal message.
- It never launches two runs for the same continuity.
- It does not duplicate the content of the message in the mailbox.
- It never reinserts a message already `ack`ed.
- It does not cognitively decide the relevance of an event.
- It does not consume an `expected` message in several `wait_group`s.
- It does not wake a `suspended` continuity except through `resume`.

## Output

The output must let one know at least:

- `continuity_id`
- `message_id`
- `seq`
- `priority`
- `status`
- possible `claimed_by` (present only after the claim of an `expected` entry)
- wake triggered or not
- possible `wait_group_id`

## Central invariant

`mailbox.send` guarantees:

```
an external event
  → a unique mailbox entry reservation
  → a unique durable message (semantic commit)
  → a deterministic qualification
  → zero or one consumption owner
  → zero or one consistent wake-up
```

## Implementation constraint

The continuity is managed in a Matter (the journal) for persistence, and duplicated in an in-memory object of the wrapper for performance. It is not a question of formalization but of implementation; see `agentic_core_concurrency_model`. For this Capacity: after the `matter.append` of the message, the same lines are applied to the in-memory object, under the lock of the continuity. A refused append leaves the object intact: the tail of the journal is re-read, the object completed, `seq` recomputed. The idempotence decision reads the object, never the whole journal. The `kind` of the message is its characterization, set by the sender who knows its origin; `mailbox.send` keeps it as is.
