# Continuity succession and token-based compaction

## Status

Implemented and validated on the real engine through scenarios S22–S25. Implementation clarified two points:

- The summary is a successor journal entry with `kind: summary` and **no mailbox entry**. It is history, never a stimulus, and comes after the reasoning framework that `continuity.create` writes as the first line of every Continuity, including successors. Compaction therefore never loses the framework.
- `successor_id` is written on the source **before** transfer, and after that commit `mailbox.send` relays every message captured by the switch.

## Intention

Ensure a Continuity remains usable by an LLM and no Continuity Matter grows forever, without ever rewriting or truncating history.

A Continuity has a **bounded lifetime**. When its context approaches the model window, cognition summarizes it and **continues in a new Continuity** whose first exchange message is that summary. The old Continuity remains as an archive linked to its successor.

## Two distinct concepts

| | Compaction | Binary size |
|---|---|---|
| nature | cognitive | storage |
| unit | tokens | bytes |
| role | succession trigger | consequence, never a trigger |

Continuity Matter size is bounded **because** token volume is bounded, provided every message remains reasonable. Large content belongs in an existing Matter substance referenced by the message, not inline in the message.

## Token measurement

- The measurement is `usage.input_tokens` returned by the provider for the latest inference: exactly the size of the sent context.
- Before the first inference, it is estimated as characters divided by four.
- The measurement is recorded in an `inference_measured` `event` journal line with the current message identifier that produced it. The latest value is read from the in-memory object and is not stored in `runtime`.

## Thresholds

Declared in the pilot definition under `limits`:

| Field | Role | Default |
|---|---|---|
| `context_tokens` | model context window in tokens | required to enable compaction |
| `compaction_advised_ratio` | fraction of the window from which compaction is advised | 0.60 |
| `compaction_required_ratio` | fraction from which succession is mandatory | 0.85 |

Without `context_tokens`, no pressure is computed and compaction remains voluntary.

## Pressure levels

| Level | Condition | Effect |
|---|---|---|
| `none` | measurement below advised threshold | none |
| `advised` | advised threshold ≤ measurement < required threshold | no effect for a `pilot_definition` pilot: **the model never sees pressure** because compaction is a measured token threshold and therefore code. A Capacity pilot receives `context_pressure`; its plan is code and can decide |
| `required` | measurement ≥ required threshold | succession is mandatory at the next safe point |

`context_pressure` is an inference-context block containing `tokens`, `window`, `ratio`, and `level`, never a journal message.

The advised level used to be shown to the model as an invitation to decide on succession. With a real model, it was interpreted as an order: succession happened too early and every successor restarted completed discovery. This produced six lineages over twenty-nine inferences and repeatedly invoked one domain call. Compaction requires no analysis, so the decision was removed from the model. It is automatic at `required`; pilot-decided succession remains possible, but the context no longer solicits it.

## Succession decision

Pilot output `pilot_output` includes `succession: { summary }`. It is mutually exclusive with `intentions`, `wait_group`, `messages`, and `projection_request`, as those already are with one another.

- At `advised`, the pilot may produce `succession` when useful.
- At `required`, at the next safe point—runtime `idle` with no wait group—`pilot.activate` performs a **compaction inference** using the same model with output constrained to `succession`. Compaction is one more and the largest conversation turn: the same system text, history, and output schema as other turns, therefore available in the provider cache. Only the final message changes and carries the fixed summarization instruction. The resulting summary triggers succession. The same run does not retry a failed compaction inference.

The model is no longer asked to list previously used Capacities, effects and identifiers, or failed calls and reasons; the deterministic call ledger below carries them. Without these facts, a real successor restarted discovery, repeated work, and made several errors in one message.

## Call ledger: what code writes and what the model writes

Principle: **code writes what is deterministic; the model writes what requires analysis.** Calls already made by a Continuity, their parameters, and results exist in its journal. Asking the model to include them in a summary makes it copy the journal with opportunities for omission and invention. The model therefore writes the analysis—current progress, decisions and rationale, pending work, and remaining work—while **`continuity.succeed` appends an exact call ledger to the summary**, built from the source journal. The successor reads it as the end of its first message.

For each source call in order, the ledger records:

- target Capacity and compact parameters truncated to 160 characters;
- outcome: `ok`, `error <code> : <message>` with a truncated message, `unanswered` when no Response arrived, or `interrupted` when a failure lost the Response and outcome is unknown, which must not be replayed without a decision;
- for a successful call to a **non-reflexive** Capacity, up to six identifiers carried by the Response in fields named `id` or ending in `_id`. This tells the successor, for example, that draft `BR-0001` exists. Free-form result excerpts are never included because external data must not enter a summary the successor reads as its own.

Reflexive calls such as contract reads and searches are listed without results. Identical calls with the same outcome are grouped, for example `x3`. The ledger is bounded to the 40 most recent calls and states how many older calls were omitted. It is deterministic: the same journal produces the same ledger, and recovery of an interrupted succession does not write a second summary because the message identifier is deterministic.

The model no longer needs to list effects or used Capacities. Compaction and voluntary-succession instructions are shorter and state that code appends the ledger and the model must not repeat it.

## Capacity `continuity.succeed`

A recoverable multi-Matter operation in this order:

1. Validate the source: active, runtime `idle`, no wait group, and `successor_id` absent or equal to the expected successor on retry.
2. Determine successor identifier `<lineage_id>_g<generation + 1>`.
3. Create the successor with the same `initial_intention`, `pilot_ref`, and `parent_id`; source `current_intention`; `predecessor_id`; `lineage_id`; and `generation + 1`.
4. Append the summary as the successor's **first exchange message**, after its reasoning framework, with `kind: summary`, source in `from`, and a deterministic identifier.
5. Transfer pending source-mailbox messages to the successor in `seq` order with deterministic identifiers, making the operation idempotent, then consume them on the source.
6. Archive the source with `status: archived` and populated `successor_id`. This final step is the succession commit point.

Every step is idempotent. Rerunning repairs missing steps. No rollback is promised.

## Archived Continuity

- It is read-only: no run and no activation through `pilot.activate`.
- Its journal remains intact and readable, including through `continuity.project`.
- `mailbox.send` to an archived Continuity **redirects** to its successor, following the chain to the active Continuity, and returns `redirected_to`.
- `continuity.delete` on one member of a lineage deletes the whole lineage only with `cascade`; otherwise it refuses while successors exist.

## Lineage and delegation

- `lineage_id` identifies the first Continuity in the chain; `generation` is the position in that chain.
- A parent awaits a child's Response by identifier. After child succession, Responses use `correlation.continuity_id = <identifier known by the parent>`, meaning the identifier under which the parent created the child—its `lineage_id`. The parent does not observe succession.
- `parent_id` and `children` point to the active Continuity in a lineage. On succession, the parent `children` entry replaces the source identifier with the successor identifier.

## Message size

A message is written directly as one journal line regardless of size. There is no threshold, reference transport, or overflow Matter.

## Invariants

- An active Continuity never has `successor_id`; an archived one always does.
- A lineage has exactly one active Continuity.
- An archived Continuity journal is never modified.
- Succession loses no message: every pending message exists in the successor or was already consumed.
- Succession occurs only while `idle` and without a wait group.

## Recovery

| Interruption | State | Recovery |
|---|---|---|
| before successor creation | active source | nothing to repair |
| successor created, summary absent | successor without first exchange message | retry appends the summary |
| pending messages partially transferred | deterministic identifiers prevent duplicates | retry finishes the transfer |
| everything complete except archival | two active Continuities | retry archives the source |

`mailbox.send` to a source with incomplete succession routes to the successor as soon as the successor exists and carries the summary.

## Out of scope

- Summary-writing strategy, which belongs to the pilot prompt and compaction inference.
- Compacting several Continuities into one.
- History rewriting; none occurs.
