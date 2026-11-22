# Capacity — `mailbox.ack`

## Intention

Permanently complete the consumption of a mailbox message owned by a run.

`mailbox.ack` means:

- the message was successfully processed by the protocol that owned it;
- its presence in the mailbox is no longer required;
- the entry may be removed.

The durable message remains in the journal.

## Input

### `mailbox.ack`

| Field | Description |
|---|---|
| `continuity_id` | Identifier of the relevant Continuity. |
| `message_id` | Identifier of the message to acknowledge. |
| `run_id` | Identifier of the run requesting the acknowledgement. |

## Preconditions

- The `continuity` must exist.
- Its associated `mailbox` must exist.
- The entry matching `message_id` must exist in `mailbox.entries`.
- This entry must have:
  - `status = claimed`
  - `claimed_by = run_id`

The calling run must therefore be the current owner of the consumption.

## Effect

When the preconditions are satisfied, the entry matching `message_id` is removed from `mailbox.entries`.

The corresponding journal message is never removed.

## After `ack`

The message is historically consumed. It cannot be inserted into the mailbox again with the same `message_id`.

If equivalent processing must be triggered later:

- a new message must be created;
- with a new `id`;
- and, optionally, a correlation to the previous message.

## Relationship with `current_message`

When `mailbox.ack` completes the processing of a run's `current_message`, the `pilot` protocol may then set:

```
runtime.current_message_id = null
```

This mutation belongs to the `pilot` cycle. `mailbox.ack` does not directly modify `current_message_id` unless the implementation groups these operations at protocol level.

## Atomicity

Removing the entry is a single write to the `mailbox` Matter, guarded by `expected_revision`; it is therefore atomic for that Matter. On a revision conflict, the mailbox is read again and claim ownership is verified again before another bounded attempt.

The journal is never modified: after `ack`, the durable message remains in the journal, and the absence of an entry means “accepted, then consumed.”

There must never be an intermediate state in which the entry is considered acknowledged but remains selectable, or the reverse.

## Ownership

Only the run that claimed the message may `ack` it.

For example, with `claimed_by = R17`:

```
mailbox.ack(message_id, R17)  → success
mailbox.ack(message_id, R99)  → rejected
```

## Acknowledging a `pending` entry

An entry with `status = pending` cannot be acknowledged directly. It must first be claimed through the appropriate mechanism.

## Acknowledging an absent entry

If `message_id` no longer exists in `mailbox.entries`, the operation must not recreate it.

The exact behavior may be:

- a `not_found` error; or
- an idempotent response indicating `already_acknowledged`;

according to the selected technical contract. In every case, no new effect is produced.

## Durable message

`mailbox.ack` never alters:

- the message content;
- its `seq`;
- its `from`;
- its `to`;
- its `kind`;
- its `correlation`;
- its `created_at`.

The journal remains the complete historical truth.

## Traceability

An `ack` does not need to systematically produce a new durable message.

The important fact is already represented by the entry disappearing from the mailbox and by the protocol continuing.

If a detailed audit of the exact acknowledgement time becomes necessary later, a system event may be added. It is not required by the current core.

## Errors

The Capacity returns an error in particular when:

- the `continuity` does not exist;
- the `mailbox` does not exist;
- the entry does not exist under the selected policy;
- the entry is not `claimed`;
- `claimed_by` does not match the supplied `run_id`.

## What `mailbox.ack` does not do

- It does not remove the durable message.
- It does not cognitively process the message.
- It does not choose the next message.
- It does not `release` an entry.
- It does not wake a Continuity.
- It does not modify a `wait_group`.
- It does not replay any effect.

## Central invariant

`mailbox.ack` guarantees:

```
an owned and completed consumption
  → permanent removal of the mailbox entry
  → complete preservation of the message in the Continuity history
```
