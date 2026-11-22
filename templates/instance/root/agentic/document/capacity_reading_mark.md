# Capacity — `reading.mark`

## Intention

Track how far each reader has progressed in each conversation so the interface can identify what is **new**. `reading.mark` advances a reader's cursor on a lineage. This is the interface's only write to reading state: the interface calls the Capacity rather than writing the Matter directly, because persistent state does not belong in the interface and the logic belongs in a Capacity.

## Input

| Field | Description |
|---|---|
| `lineage_id` | The Continuity being read, specifically the head at the time of reading. Cursors are kept per generation, so a replaced generation keeps its cursor. The field name is historical. |
| `seq` | Sequence number of the last line read, as a non-negative integer. |
| `reader` | Optional reader identifier; defaults to `user:1`. |

## Output

| Field | Description |
|---|---|
| `lineage_id`, `reader` | The cursor that was marked. |
| `seq` | Cursor value after marking. |
| `changed` | True when the cursor advanced. |

## The `reading_cursors` Matter

A Matter in `/root/agentic`, of element type `reading` with content described by the `reading_data` schema. It contains entries shaped as `{ reader, lineage_id, seq }`, one per reader and lineage. A missing entry means nothing has been read. The Capacity creates the Matter empty on first use as part of the same operation as the first mark. The interface reads it like any other Matter, through a subscription.

## Rules

- **The cursor never moves backward**: it becomes the greater of the stored value and `seq`. A mark that does not advance it writes nothing.
- A mark changes **neither other lineages nor other readers**.
- Concurrent marks are **all preserved**: writes are guarded by `expected_revision` and retried on conflict.
- **The Continuity must exist.** On 14 November the architect replaced the earlier rule that a cursor survived its conversation, because that rule allowed the file to grow without bound with one cursor per never-removed conversation. Marking a nonexistent Continuity is refused and writes nothing; a late interface mark after conversation deletion therefore does not recreate a cursor. `continuity.delete` removes a Continuity's cursors.
- The definition of what counts as “new” does not live here. It belongs to the interface module `ui/model/state.ts`, which compares lines with the cursor.

## Errors

An empty `lineage_id`, a negative or non-integer `seq`, or an empty `reader` makes the request invalid and writes nothing.

A `lineage_id` naming a nonexistent Continuity returns `not_found` and writes nothing.
