# Capacity — `continuity.delete`

## Intention

Delete a continuity and the operational states directly associated with it.

`continuity.delete` removes:

- the `continuity` Matter (the header),
- the journal Matter (`journal_<id>`),
- the associated `mailbox` Matter,
- the associated `runtime` Matter,
- the placement references that point to this continuity.

The deletion never rewrites the history already present in other continuities.

## Input

### `continuity.delete`

| Field | Description |
|---|---|
| `continuity_id` | Identifier of the continuity to delete. |
| `cascade` | Optional. Default value: `false`. |

## Output

| Field | Description |
|---|---|
| `continuity_id` | Identifier of the continuity concerned. |
| `deleted` | `true` if the deletion was actually performed. |
| `cascade` | Value applied. |
| `detached_children[]` | Children kept but detached when `cascade = false`. |
| `deleted_children[]` | Children deleted when `cascade = true`. |

## Preconditions

- The `continuity` must exist.
- The associated `mailbox` must exist.
- The associated `runtime` must exist.

## Simple deletion

When `cascade = false`, the `continuity` is deleted without automatically deleting its children.

For each direct child: `child.parent_id = null`.

The downward relation of the parent disappears with the deletion of the parent. The children keep existing normally.

## Relation to the parent

If the deleted `continuity` has a parent, `continuity_id` must be removed from `parent.children[]`.

This update is part of the consistent deletion.

## Cascade

When `cascade = true`, `continuity.delete` recursively deletes the continuity and all the descendant continuities linked by `children[]`.

For each `continuity` of the subtree, the following are deleted:

- the `continuity`,
- its **read cursors** (the entries of the `reading_cursors` Matter that designate it, for all readers: removed before its header, so that resuming an interrupted deletion finishes the cleanup),
- its `mailbox`,
- its `runtime`,
- its Structure references.

The recursive deletion follows the same safe-point rules for active continuities.

### No implicit cascade

`cascade = false` remains the default behavior.

Deleting a parent `continuity` must never silently cause the deletion of its cognitive subtree.

## Depending on the runtime state

### `idle` continuity

If `runtime.status = idle`, the deletion can be applied immediately.

### `suspended` continuity

If `runtime.status = suspended`, the deletion can be applied immediately.

### `running` or `waiting` continuity

If `runtime.status = running` or `waiting`, the deletion does not abruptly cut:

- an LLM inference,
- a Capacity in progress,
- a Matter mutation (individually atomic),
- a cognitive resumption in progress.

The deletion request is applied at the next safe point.

## Safe point

At the next safe point:

- the run stops taking new work,
- no new inference is launched,
- no new normal message is claimed,
- then the deletion is performed.

## `delete` takes priority

A `delete` request is a priority control.

Once applicable, it must prevent the run from starting a new, useless cognitive cycle before the deletion.

## Wait groups

Deleting a `continuity` also deletes its `runtime` and therefore its active wait groups.

External operations already launched are not automatically cancelled. They can keep producing effects in the world.

If a result arrives later for a deleted `continuity`:

- it must not automatically recreate the `continuity`;
- the handling of this case belongs to the routing and error protocol.

## Effects already emitted

The deletion never cancels the real effects already produced by the `continuity`. This includes in particular:

- Brique Intentions already emitted,
- messages already sent to other continuities,
- creation of external resources,
- transformations already executed.

The deletion bears on the future existence of the `continuity`, not on the retroactive cancellation of its history.

## Messages in other continuities

If the deleted `continuity` had previously sent a message to another `continuity`, that message stays entirely in the journal of the target continuity.

The deletion of the sender does not modify the history of the target.

## Messages of the continuity itself

The messages contained in the journal of the deleted continuity disappear with that Matter.

No additional independent archive is defined in the current core.

## Structure

All the Structure references pointing to the deleted `continuity` must be removed.

The `continuity` itself stores no `structure_path`. The deletion must therefore search for and clean the external references concerned through the Structure mechanisms available.

## Deletion protocol

Brique offers no multi-Matter transaction. `continuity.delete` is a recoverable multi-Matter operation, executed in this order:

```
children (cascade or detachment)
  ↓
cleanup of the parent relation
  ↓
cleanup of the Structure references
  ↓
runtime_<id>
  ↓
mailbox_<id>
  ↓
journal_<id>
  ↓
continuity_<id>   (the header, last)
```

- Each step treats a target already absent or already cleaned as an idempotent success, if the remaining state is consistent.
- The absence of the `continuity` Matter (the header) marks the completion of the deletion. It carries `journal_ref`: as long as it exists, the journal and the contents it references remain findable.
- A `continuity` present without its `mailbox`, its `runtime` or its journal is a detectable partial deletion: running the deletion again finishes it.
- Deleting the `runtime` first prevents any new activation, since `pilot.activate` and `mailbox.send` fail on a missing runtime.
- No rollback is promised.

The writes on the parent, on the detached children and on the Structure (all guarded by `expected_revision`) are preceded by a revision read; on conflict, the target is re-read and the content recomputed before a new bounded attempt.

For a given `continuity`, at the end of a successful deletion:

```
continuity (header) deleted
journal deleted
mailbox deleted
runtime deleted
parent reference cleaned
Structure references cleaned
```

No orphan `mailbox` or `runtime` must durably remain; an intermediate state remains detectable and recoverable.

## Examples

### Deletion with children, without cascade

```
C1
├ C2
└ C3
```

`continuity.delete(C1, cascade = false)` produces:

- C1 deleted
- `C2.parent_id = null`
- `C3.parent_id = null`

C2 and C3 keep existing.

### Deletion with cascade

`continuity.delete(C1, cascade = true)` produces:

- C1 deleted
- C2 deleted
- C3 deleted
- as well as their own possible descendants.

## Concurrency

During a deletion, no new run must be able to acquire the target `continuity`: the `runtime` is deleted first, and the `idle → running` acquisition is guarded by `expected_revision`.

The critical-section locks per `continuity_id` are short. The lock of a parent is never held around the recursive call of a cascade deletion on a child; the state of the parent is revalidated on return. Locks held simultaneously are acquired in the order of the `continuity_id`s.

## Idempotence

A second deletion of the same `continuity` already entirely deleted is an idempotent success (`deleted = false`) and produces no side effect and no recreation.

## Traceability

The deletion request can be present in the journal as a `control` before the Matter disappears.

The other continuities keep the messages they received from this `continuity`.

No additional global trace is imposed by this contract.

## Lineage of successions

A continuity that belongs to a lineage (it has a `predecessor_id` or a `successor_id`) is deleted only with `cascade`, which deletes the whole lineage, from the most recent generation to the oldest. Without `cascade`, the deletion is refused with the neighboring identifiers.

## What `continuity.delete` does not do

- It rolls back no external effect.
- It does not automatically cancel the Capacities already launched.
- It does not delete the messages already present in other continuities.
- It does not cascade by default.
- It does not create a `closed` state.
- It does not turn a deleted `continuity` into an implicit archive.
- It never recreates a `continuity` following a late result.

## Central invariant

`continuity.delete` guarantees:

```
a deleted continuity
  → no more continuity Matter
  → no more associated mailbox
  → no more associated runtime
  → no more Structure reference
  → consistent parent/child relation
    without ever rewriting the history already materialized elsewhere
  → recoverable operation: a partial state is detectable and finishes by running again
```

## Implementation constraint

The continuity is managed in a Matter (the journal) for persistence, and duplicated in an in-memory object of the wrapper for performance. It is not a question of formalization but of implementation; see `agentic_core_concurrency_model`. For this Capacity: the in-memory object of the deleted continuity is evicted with the deletion of the Matters.
