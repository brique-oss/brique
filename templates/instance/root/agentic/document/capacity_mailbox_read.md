# Capacity — `mailbox.read`

## Intention

Select and claim a Continuity's next eligible message in one guarded mailbox write.

`mailbox.read` does not process the message. It only identifies the next normal work item that a given run may consume and transfers ownership to that run.

It guarantees that two runs can never read the same message concurrently.

## Input

### `mailbox.read`

| Field | Description |
|---|---|
| `continuity_id` | Identifier of the relevant Continuity. |
| `run_id` | Identifier of the run requesting the read. |
| `kind` | Optional. `user`: absorption read (see “Absorption read”). |

## Output

When a message is available:

- `continuity_id`
- `message_id`
- `priority`
- `seq`
- `status = claimed`
- `claimed_by = run_id`

When no eligible message is available:

- `continuity_id`
- `message_id = null`

## Preconditions

- The `continuity` must exist.
- Its associated `mailbox` must exist.
- Its associated `runtime` must exist.
- `runtime.run_id` must match the supplied `run_id`.

The calling run must therefore be the current owner of the Continuity.

## Selection

`mailbox.read` only considers entries that are:

- `status = pending`;
- eligible for normal processing;
- linked by `message_id` to a durable journal message (an entry without a durable message is a pre-commit reservation and is never eligible).

Selection order is deterministic:

1. functional priority;
2. message `seq` in the journal.

Priority order is `control`, then `normal`.

`expected` messages already correlated with an active `wait_group` are not selected by `mailbox.read`. They are claimed directly by `mailbox.send` at the start of routing, after commit.

### `control`

`control` entries take precedence over `normal` messages. They are mainly read at safe points.

A pending control must therefore be returned before any eligible normal message.

### `expected`

`mailbox.read` never consumes an `expected` message.

Two situations exist:

- `expected` + active `wait_group` → claimed directly by `mailbox.send` after commit;
- no matching `wait_group` → the message should have been classified as `normal` during `send`.

A pending `expected` entry exists only briefly between the reservation and the claim performed by `mailbox.send`. It must never remain durably without an associated consumer: if the `wait_group` is no longer active during routing, the entry becomes `normal`.

### Absorption read (`kind = user`)

With `kind = user` and `priority = normal`, only `normal` entries whose durable message has kind `user` are eligible. Other normal messages (returns from child Continuities, tool responses that fell back to normal) are not claimed and wait for their normal processing turn. The selected entry is claimed as usual (`claimed`, `claimed_by`), but the run does **not** make it `current_message_id`: it is absorbed into the chain in progress (see `capacity_pilot_activate`). Ordering still follows `seq`.

### FIFO within a priority

Among entries of equal priority, the message with the lowest `seq` is selected.

The journal provides `seq`. The mailbox maintains no independent FIFO ordering.

## Single guarded write

Selection and claim form one write to the `mailbox` Matter, guarded by `expected_revision`; it is atomic for that Matter. On a revision conflict, the mailbox is read again and selection is recomputed before another bounded attempt.

Conceptually:

```
identify the eligible entry
  ↓
status = claimed
  ↓
claimed_by = run_id
  ↓
return message_id
```

There must be no observable state in which two readers can select the same entry before it is claimed.

## Claim

After success:

- `entry.status = claimed`
- `entry.claimed_by = run_id`

From then on, only that run may complete or release consumption through `mailbox.ack` or `mailbox.release`.

## Message content

`mailbox.read` does not necessarily return a full copy of the message.

The durable truth remains in the journal. Given `message_id`, the runtime can read the complete message from the `continuity`.

The Capacity may nevertheless return useful minimum information such as `message_id`, `priority`, and `seq`, according to implementation needs.

## `current_message_id`

For a normal message intended for a reasoning chain, after a successful `mailbox.read` the runtime may set:

```
current_message_id = message_id
```

This mutation belongs to the `pilot` protocol, not to the semantic responsibility of `mailbox.read` itself.

## Controls and safe points

`mailbox.read` does not decide when a safe point exists.

The `pilot` protocol decides when to call `mailbox.read` to look for a priority control. `mailbox.read` only applies mailbox selection rules.

## `suspended` Continuity

A suspended Continuity should not normally have an active run capable of calling `mailbox.read`.

The `runtime.run_id` precondition therefore naturally prevents a normal read in this state.

## `idle` Continuity

An idle Continuity has no `run_id`.

`mailbox.read` must therefore not be used before `pilot.activate` acquires the runtime.

## Idempotence

`mailbox.read` is not functionally idempotent.

A successful read changes the entry state by claiming it. A second call from the same run selects another pending message, if any. The already claimed message is not selected again.

## Concurrency

Even if several `mailbox.read` calls are technically made in parallel, only one operation can claim a given entry.

The single-run invariant of a Continuity normally reduces this concurrency, but the Capacity must remain atomic independently of that invariant.

## Errors

The Capacity returns an error in particular when:

- the `continuity` does not exist;
- the `mailbox` does not exist;
- the `runtime` does not exist;
- `run_id` does not match the current owner;
- mailbox state is inconsistent.

The absence of an eligible message is not an error. It simply returns no message.

## What `mailbox.read` does not do

- It does not cognitively read the message content.
- It does not build the LLM context.
- It does not process `expected` responses.
- It does not create a `wait_group`.
- It does not change the current Intention.
- It does not launch a pilot.
- It does not acknowledge any message.
- It does not release any message.
- It does not reorder messages.
- It maintains no global cursor.

## Central invariant

`mailbox.read` guarantees:

```
a valid owner run
  → one deterministic next eligible message
  → a claim in one guarded write
  → one consumption owner
```

## Implementation constraint

The Continuity is managed in a Matter (the journal) for persistence and duplicated in an in-memory wrapper object for performance. This is an implementation concern rather than a formalization concern; see `agentic_core_concurrency_model`. For this Capacity, eligible-message existence and ordering (`seq`) are read from the in-memory object, rebuilt from the journal when absent.
