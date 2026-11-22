# Matter schema — `mailbox`

## Intention

Represent the consumption state of the incoming messages of a continuity.

The mailbox is not a secondary memory. It never duplicates the business content of messages.

It references only messages already persisted in the journal and carries the state needed for their selection, their claim and their consumption.

## Element and data

This Matter is described by two schemas, which must not be confused:

| Schema | Describes | Content |
|---|---|---|
| `mailbox` (element) | the Matter as a Brique element | `brique.kind: matter`, `substance_mode: brique`, `substance_type: text`; `objective.type: mailbox`; `functional`: `format: json` and `schema_ref` |
| `mailbox_data` (data) | the **substance** of the Matter | the JSON document described by the tables below |

The content is the substance of the Matter (`<matter_id>.json`), never its descriptor. The `functional.schema_ref` field of the descriptor designates `/root/agentic/schema/mailbox_data`.

## Schema (`mailbox_data`)

### `mailbox`

| Field | Description |
|---|---|
| `continuity_id` | Identifier of the continuity this mailbox belongs to. A continuity owns exactly one mailbox. |
| `entries[]` | Set of the entries currently present in the mailbox. |

## Schema of an entry

### `entry`

| Field | Description |
|---|---|
| `message_id` | Reference to an existing message in the journal. |
| `priority` | Functional priority of the entry. Expected values: `control`, `expected`, `normal`. |
| `status` | Current consumption state. Expected values: `pending`, `claimed`, `failed` (set aside after three failed treatments: never taken again, it no longer blocks the queue). |
| `attempts` | Optional. Number of treatments of the message that have failed. |
| `claimed_by` | Identifier of the run that currently owns the consumption. Present only if `status = claimed`; never on a `pending` or `failed` entry. |

## Responsibility of the mailbox

The mailbox represents only what remains to be processed or what is temporarily owned by a run.

It does not keep:

- the content of the message;
- its `from`;
- its `to`;
- its `kind`;
- its `content`;
- its `created_at`;
- its `correlation`;
- its history after `ack`.

All this information stays in the journal.

## Priority

The priority is determined at the time of `mailbox.send`. It is then stored explicitly in the entry.

Semantics:

- **`control`**: control message that must be processed before any normal work.
- **`expected`**: message that actually matches an active `wait_group` at the time of its arrival.
- **`normal`**: any other incoming message.

`expected` is not a durable parallel queue. If a message matches no active `wait_group` at the time of its arrival, it enters as `normal`.

## Selection order

The physical order of `entries[]` has no semantics.

The selection relies on:

1. the functional priority;
2. the `seq` of the message in the journal.

Priority order:

```
control > expected > normal
```

At equal priority: smaller `message.seq` first.

## Life cycle of an entry

Normal cycle:

```
pending
  → mailbox.read
claimed
  → successful treatment
  → mailbox.ack
entry deleted
```

In case of failure before termination:

```
claimed
  → mailbox.release
pending
```

There is no additional durable status such as:

- `acked`
- `consumed`
- `failed`
- `completed`

`ack` simply removes the entry from the mailbox.

## Claim

`claimed_by` contains only a `run_id`. It represents the logical owner of the consumption.

It never contains a `wait_group_id`. The relation to the `wait_group` stays carried by the correlation of the message and by the runtime.

## `mailbox.read`

`mailbox.read` performs, in a single write of the mailbox guarded by `expected_revision`:

- the selection of the eligible entry;
- its transition from `pending` to `claimed`;
- the assignment of `claimed_by = run_id`.

Two concurrent reads can therefore never claim the same message.

## Expected

When a message arrives and matches an active `wait_group`:

- a mailbox entry is first reserved `pending`;
- the message is persisted in the journal (semantic commit);
- the correlation with the `wait_group` is recognized;
- the entry then becomes `claimed`;
- `claimed_by` receives the active `run_id`;
- the message is attached to the `wait_group`.

It does not go through `mailbox.read`. It can therefore never be taken again afterwards as a normal message.

If no active `wait_group` matches, or if it is no longer active at the time of routing, it enters as `normal` and its entry stays `pending`.

## Control

A `control` message received during `running` or `waiting`:

- stays `pending`;
- never interrupts the current operation;
- is processed at the next safe point.

A control received during `idle` can be applied directly without launching any LLM cognition.

`resume` has a particular behavior in `suspended`:

- it can be applied immediately;
- `suspended` becomes `idle`;
- the control is `ack`ed;
- if work remains in the mailbox, `pilot.activate` can be triggered.

## Uniqueness

The same `message_id` can appear only once in `entries[]`.

A durable message therefore has at most one mailbox consumption in progress.

## Pre-commit reservation

A `pending` entry whose `message_id` matches no durable message of the journal is the reservation of a send interrupted before its commit. It is never eligible for a claim.

## Idempotence

`mailbox.send` is idempotent for a message already present in the mailbox.

A second send of the same `message_id`, as long as its entry exists:

- does not create a new entry;
- does not cause a second consumption;
- simply returns the existing state.

## Message already acked

A message already consumed and removed from the mailbox can never be reinserted.

If a new functional processing is wanted:

- a new message must be created;
- it can be correlated with the old one.

## `mailbox.ack`

`mailbox.ack` is allowed only if:

- the entry is `claimed`;
- `claimed_by` matches the calling `run_id`.

Effect: the entry is deleted from the mailbox.

## `mailbox.release`

`mailbox.release` is allowed only if:

- the entry is `claimed`;
- `claimed_by` matches the calling `run_id`.

Effect:

- `status` becomes `pending` again;
- `claimed_by` becomes `null` again.

## Invariants

- A continuity owns exactly one mailbox.
- The mailbox is identified only by `continuity_id`.
- The mailbox contains no business content of the message.
- Each `message_id` appears at most once in `entries[]`.
- An `ack` entry disappears.
- A `release` entry becomes `pending` again.
- Only the owning run can `ack` or `release` a `claimed` entry.
- The processing order never depends on the physical position in `entries[]`.
- `expected` exists only when a message really matches an active wait.
- A message already `ack`ed can never be replayed implicitly.

## What does not belong to this schema

- No message content.
- No history.
- No `created_at` of the mailbox's own.
- No counter.
- No cursor.
- No `last_read_seq`.
- No generic metadata.
- No runtime state.
- No complete `wait_group`.

The mailbox carries only the minimal state needed to schedule and consume incoming messages.
