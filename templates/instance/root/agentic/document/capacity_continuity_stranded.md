# Capacity — `continuity.stranded`

## Intention

Report which Continuities cannot proceed on their own: either their run died, or work is waiting with no run to take it. This Capacity is read-only.

## When a Continuity is stranded

| Observed state | Stranded? |
|---|---|
| `runtime` is `running` or `waiting`, with a `run_id` that is not live in this process | yes |
| `runtime` is `idle` and the journal contains a `pending` message | yes |
| `runtime` is `idle` with a `current_message_id` | yes |
| `runtime` is `idle` with an active wait group | yes |
| archived, suspended, or `idle` without work | no |

“Live” means live in this process according to `substrate/liveness`. No run is live after a restart, so every `running` or `waiting` Continuity is stranded. A quarantined `failed` entry is not pending work.

## Input and output

- Optional input `continuity_ids` limits the examination to those Continuities. When absent, the whole `continuities` Structure is examined in order.
- Output contains `continuity_ids` for stranded Continuities, `reasons` for each one, `examined`, and `skipped`. A Continuity whose Matters cannot be read is named under `skipped` and does not stop examination of the others.

## Why this is a separate Capacity

The DSL orchestrates but does not compute, and it has no string operation that could derive a Continuity identifier from a Structure reference such as `continuity_<id>`. Deciding which Continuities are stranded is therefore a transformation with its own contract; `pilot.recover` merely orchestrates that decision followed by wake-up calls.
