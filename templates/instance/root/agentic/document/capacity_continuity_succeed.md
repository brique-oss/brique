# Capacity — `continuity.succeed`

## Intention

Continue a Continuity's work in a new Continuity whose first message is a summary, then archive the source. This keeps every Continuity usable by an LLM and prevents Continuity Matters from growing without bound, without ever rewriting history. The complete design is documented in `continuity_succession`.

## Input

### `continuity.succeed`

| Field | Description |
|---|---|
| `continuity_id` | Required source Continuity. |
| `summary` | Required non-empty string or object written for the successor. |
| `run_id` | Optional run that owns the Continuity when succession is decided during that run. |

## Output

| Field | Description |
|---|---|
| `continuity_id` | Source Continuity. |
| `successor_id` | Successor Continuity: `<lineage_id>_g<generation + 1>`. |
| `archived` | `true` once the source is archived. |
| `transferred` | Identifiers of pending messages transferred to the successor. |

## Preconditions

- The Continuity exists and has no active wait group.
- Its runtime is `idle`, or `run_id` names the run that owns it.

## Protocol: recoverable multi-Matter operation

1. **Validate** the source and determine the successor identifier.
2. **Create the successor** through `continuity.create`, with the same founding Intention, pilot, and parent, plus the source's `current_intention`, `predecessor_id`, `lineage_id`, and `generation + 1`.
3. **Record the summary** as the first exchange message, immediately after the reasoning framework written by `continuity.create` as the successor's first line. It uses `kind: summary`, names the source as sender, and has a deterministic identifier. It is a journal entry without a mailbox entry: history, never a stimulus.
4. **Switch routing** by writing `successor_id` on the source *before* any transfer. From that instant, `mailbox.send` redirects.
5. **Transfer** pending messages in `seq` order while preserving identifiers, then consume them on the source. Entries claimed by a run are never transferred.
6. **Update the parent** so the successor replaces the source in `children`.
7. **Archive** the source with `status: archived`. This is the final write and the commit point.

Every step is idempotent; rerunning the operation repairs missing steps. A failure before archival leaves the source active. An already archived source returns idempotent success. No rollback is promised.

## Guarantees

- No message is lost: each message is either in the successor or already consumed.
- Succession never modifies the source journal.
- A lineage has exactly one active Continuity after the operation completes.
- Every Matter write is guarded by `expected_revision`; no lock is held around `continuity.create`.

## Errors

| Case | Error |
|---|---|
| missing or empty `summary` | invalid |
| unknown Continuity | not found |
| active wait group | refusal |
| non-`idle` without `run_id`, or `run_id` does not own the Continuity | refusal |
| existing Continuity conflicts with the expected successor, or source names another successor | consistency failure |
| revision conflicts exhausted | concurrency failure |

## What `continuity.succeed` does not do

- It does not write the summary: cognition, either the pilot or compaction inference, produces it.
- It deletes nothing: the archive remains readable through `continuity.project`.
- It never decides on its own when succession should occur.

## Implementation constraint

The Continuity is managed in a Matter—the journal—for persistence and duplicated in an in-memory wrapper object for performance. This is an implementation concern rather than a formalization concern; see `agentic_core_concurrency_model`. For this Capacity, pending messages to transfer are read from the source object; the summary and transferred messages are appended to the successor journal, whose object is updated in the same way. The archived source object may be evicted and rebuilt on demand.
