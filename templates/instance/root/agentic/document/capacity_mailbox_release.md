# Capacity — `mailbox.release`

## Intention

Release consumption of a mailbox message currently claimed by a run.

`mailbox.release` means:

- the run cannot correctly complete message processing;
- the current consumption must be abandoned;
- the message must become available for later processing again.

The durable message remains unchanged in the journal.

## Input

### `mailbox.release`

| Field | Description |
|---|---|
| `continuity_id` | Identifier of the relevant Continuity. |
| `message_id` | Identifier of the message whose claim must be released. |
| `run_id` | Identifier of the run requesting the release. |

## Preconditions

- The `continuity` must exist.
- Its associated `mailbox` must exist.
- The entry matching `message_id` must exist in `mailbox.entries`.
- This entry must have:
  - `status = claimed`
  - `claimed_by = run_id`

The calling run must therefore be the current owner of the consumption.

## Effect

When the preconditions are satisfied:

- `status = pending`
- `claimed_by = null`

The entry remains in `mailbox.entries` and becomes eligible for future consumption again.

## Ownership

Only the run that claimed the message may release it.

For example, with `claimed_by = R17`:

```
mailbox.release(message_id, R17)  → success
mailbox.release(message_id, R99)  → rejected
```

## Durable message

`mailbox.release` never changes the journal message, including its:

- `id`
- `seq`
- `from`
- `to`
- `kind`
- `content`
- `created_at`
- `correlation`

Release concerns only consumption state in the mailbox.

## Relationship with `current_message`

When the released message is the run's `current_message`, the `pilot` protocol must then restore:

```
runtime.current_message_id = null
```

The run no longer cognitively owns that message.

## Main use case

`mailbox.release` is primarily used when a run fails before it can complete and acknowledge the current message.

Example:

```
message M42 claimed
  ↓
inference or technical processing
  ↓
unrecoverable run failure before cycle completion
  ↓
mailbox.release(M42)
  ↓
M42 becomes pending again
```

A new run may resume the message later.

## Effects already produced

`release` never means rollback.

If message processing has already produced external effects or Brique Intentions, those effects remain real.

The next processing attempt must be able to reconstruct actual state from the journal and persistent `runtime`.

No previously emitted Intention may be replayed automatically merely because the message was released.

## Release conditions

A release is appropriate only when the protocol considers message processing not definitively complete.

- If processing is complete, use `mailbox.ack`.
- If an execution error was converted into a reasoning-resumption stimulus in the same run, the message remains `claimed`. It must not be released while that recovery remains possible.

## Failed processing: attempts and parking

`mailbox.release` accepts `failed` (boolean). With `failed: true`, the release says message processing failed and increments the entry's `attempts`. On the **third** failed attempt (3 is hard-coded, not configurable), the entry is not restored to `pending`; it is parked with `status = failed`. A failed entry is never selected by `mailbox.read` and no longer blocks following messages: `pilot.activate` continues with the next message instead of ending its run.

Without this rule, a message that always fails would be released and selected first at every wake-up, paying for an inference each time while blocking the entire queue. The durable message remains in the journal; the `message_parked` event records what happened. A failed entry cannot be released: `mailbox.release` rejects it. Resuming a parked message is an architect decision.

The response carries `attempts` and `parked`.

## Releasing a `pending` entry

An already pending entry cannot be released. The operation must be rejected or reported as inconsistent.

## Releasing an absent entry

If `message_id` does not exist in `mailbox.entries`:

- no entry may be recreated;
- the operation returns an error or a result indicating that the entry does not exist.

## Atomicity

The transitions `claimed → pending` and `claimed_by = run_id → claimed_by = null` occur in one write to the `mailbox` Matter, guarded by `expected_revision`. On conflict, the mailbox is read again and claim ownership is verified again before another bounded attempt.

There must be no observable state in which the entry is pending while retaining an owner.

## Ordering after `release`

Once pending again, the entry returns to its logical position under the normal selection rules.

The next `mailbox.read` uses `priority`, then `message.seq`. No new ordering or `seq` is assigned.

## `expected` and `release`

An `expected` message normally captured by an active `wait_group` is owned by the current run.

If a run failure requires reconstruction, exact claim handling depends on runtime recovery.

`mailbox.release` must not arbitrarily turn an `expected` result already recorded in `received[]` into a `normal` message.

Consistency between mailbox, `wait_group`, and runtime must be restored as a whole.

## Traceability

A release does not need to systematically produce a new durable message.

The `continuity` already preserves:

- the original message;
- actions performed;
- any errors;
- significant run events.

A specific system event may be added later if detailed release auditing becomes necessary.

## Errors

The Capacity returns an error in particular when:

- the `continuity` does not exist;
- the `mailbox` does not exist;
- the entry does not exist;
- the entry is not `claimed`;
- `claimed_by` does not match the supplied `run_id`.

## What `mailbox.release` does not do

- It does not remove the entry.
- It does not remove the durable message.
- It does not create a new message.
- It does not reset the Continuity history.
- It does not roll back any external effect.
- It does not re-emit any Intention.
- It does not automatically start new reasoning.
- It does not change entry priority.

## Central invariant

`mailbox.release` guarantees:

```
consumption abandoned by its owner
  → claim removal in one guarded write
  → message returns to pending
  → complete preservation of everything that has actually happened
```
