# Matter schema — `continuity`

## Intention

Represent the durable semantic state of a cognitive continuity.

A continuity is carried by **two Matters** that contain no operational run state:

- the **header** (`continuity_<id>`), a small JSON document, which carries:
  - the direction of the continuity,
  - its possible relation with other continuities,
  - the pilot associated with it,
  - the pointer to its journal (`journal_ref`);
- the **journal** (`journal_<id>`), a JSON Lines substance, which carries the complete and ordered history of its course.

The header says *who* the continuity is and *where* it goes; the journal says *what happened*. The two are separated because their rhythms differ: the header rarely changes and stays small, the journal grows with each fact. Rewriting the whole journal at each append would cost a write proportional to the entire history: appending at the tail (`matter.append`) costs the size of the line appended.

## Element and data

These Matters are described by schemas that must not be confused:

| Schema | Describes | Content |
|---|---|---|
| `continuity` (element) | the header as a Brique element | `brique.kind: matter`, `substance_mode: brique`, `substance_type: text`; `objective.type: continuity`; `functional`: `format: json` and `schema_ref` |
| `continuity_data` (data) | the **substance** of the header | the JSON document described by the table below |
| `journal` (element) | the journal as a Brique element | `brique.kind: matter`, `substance_mode: brique`, `substance_type: text`, `substance_framing: lines`; `objective.type: journal`; `functional`: `format: jsonl` and `schema_ref` |
| `message` (line) | **one line** of the journal | the message described below. For a JSON Lines Matter, `functional.schema_ref` designates the schema of a single line. |

The content is the substance of the Matter (`<matter_id>.json` for the header, a file in lines for the journal), never its descriptor. The `functional.schema_ref` field of the header descriptor designates `/root/agentic/schema/continuity_data`; that of the journal designates `/root/agentic/schema/message`.

> The `journal` Schema elements and the new form of `continuity_data` remain to be created or adapted: this document describes the validated intention, not yet the state of the elements.

## Schema of the header (`continuity_data`)

### `continuity`

| Field | Description |
|---|---|
| `id` | Unique identifier of the continuity. |
| `created_at` | Creation date of the continuity. |
| `parent_id` | Identifier of the parent continuity. Can be `null` for a root continuity. |
| `children[]` | Explicit list of the identifiers of the child continuities. |
| `initial_intention` | Founding intention of the continuity. Mandatory. Immutable after creation. |
| `current_intention` | Active direction of the continuity. Mandatory. Non-null. Can evolve explicitly over time. Remains interpreted as a local derivation of `initial_intention`. |
| `pilot_ref` | What decides for this continuity: a `pilot_definition` Matter `{ context, matter_id }` or a Capacity `{ context, capacity, limits? }`, never both. Mandatory. |
| `journal_ref` | Reference to the journal Matter of the continuity `{ context, matter_id }`. Mandatory. Written at creation, never modified. |
| `status` | `active` as long as the continuity can be activated; `archived` (read-only) once a following one continues it. Mandatory. |
| `lineage_id` | Identifier of the first continuity of the chain of successions: the one a delegating parent knows. Mandatory. |
| `generation` | Rank in the lineage: 1 for the first, incremented at each succession. Mandatory. |
| `predecessor_id` | Continuity that this one continues. Absent for the first of the lineage. |
| `successor_id` | Continuity that continues this one. Present if and only if it is archived (or being archived). |

## Schema of a journal line

### `message`

Each line of the journal is a JSON document on a single line, terminated by LF.

| Field | Description |
|---|---|
| `id` | Unique identifier of the message. |
| `seq` | Monotonic sequence number specific to the continuity: the **line number** of the journal (`1` for the first line). It equals the number of lines already committed plus one, and the append is guarded by `expected_line_count`, so that two concurrent appends cannot get the same `seq`. Never derived from `revision`. Materializes the canonical order of the messages. |
| `from` | Explicit address of the sender. |
| `to` | Explicit address of the recipient. |
| `kind` | Stable high-level category of the message. |
| `content` | Extensible content depending on the `kind`. |
| `created_at` | Creation date of the message. |
| `correlation` | Explicit correlation structure between this message and other events. |

## Message kinds

The first level of `kind` remains deliberately stable.

Expected values:

- `user`
- `pilot`
- `capacity_intention`
- `capacity_response`
- `continuity`: a message coming from another continuity (delegation, response of a child).
- `control`
- `projection`: request and result of a projection. History, never a stimulus.
- `error`: an error recorded by the machinery. History, never a stimulus.
- `event`: an internal tracking fact of the continuity (`wait_group_created`, `wait_group_completed`, `run_recovered`, `intention_update`, `inference_measured`). History, never a stimulus. It distinguishes these facts from the messages between continuities, which the `continuity` kind alone used to carry.
- `framework`: the cognitive framework of the pilots, **first line of every continuity** (`seq` 1), copied at its creation from the Matter `/root/agentic/prompt/pilot_framework`. History, never a stimulus, and **pinned**: every projection carries it, whatever its filters, its limit and its budget.
- `summary`: the synthesis written by the previous continuity, first exchange message of a continuity resulting from a succession (after its framework). It is history, never a stimulus.
- `prompt` and `completion`: the two halves of an inference step of a pilot defined by a Capacity (`pilot.infer`). History, never a stimulus.
- `completion` (`step: decision`) also written by `pilot.activate` for a Matter pilot: its decision as the model returned it (free values decoded), recorded **before** its effects (`capacity_intention`, `pilot`, which remain facts of the journal). It is what the model re-reads of itself.

The functional detail belongs to `content`.

### The `kind` is the characterization of a line

The `kind` of a line says whether it belongs to the LLM thread (the continuity) or to the tracking of the machinery. It is set by the tool that writes the line, which knows where it comes from; the LLM never sets it.

| Belongs to | Kinds | Role |
|---|---|---|
| **LLM thread** (retained by default in the continuity) | `framework`, `user`, `pilot`, `capacity_intention`, `capacity_response`, `continuity`, `summary`, `prompt`, `completion` | the exchanges, the reasonings (including the self-prompt), the tool calls and their results |
| **Tracking** (stays in the journal, outside the continuity by default) | `event`, `projection`, `error`, `control` | what the machinery records to follow and resume |

The projection already filters by `kind` (`include_kinds`, `exclude_kinds`): no additional criterion is needed.

## Voluminous content

A `content` is carried as it is in the journal line, whatever its size: the response of the LLM or of a tool is recorded, that is all. There is no carrying by reference, no overflow Matter, no size limit imposed by `agentic`. Writing a large append is an implementation matter (see `agentic_core_concurrency_model`).

Examples:

- `kind = control` → `content`: `action`, possible parameters.
- `kind = error` → `content`: `type`, error details.

## Correlation

`correlation` can carry the necessary links without requiring that they all be present.

Expected minimal structure:

| Field | Description |
|---|---|
| `causation_id` | Message that directly caused this one. |
| `intention_id` | Brique intention to which the message is linked. |
| `wait_group_id` | Wait group with which the message is correlated. |
| `continuity_id` | Linked continuity when this information is needed. |

Unneeded fields stay absent or null.

## Invariants

- `initial_intention` is immutable.
- `current_intention` is always present.
- `pilot_ref` is always present.
- `journal_ref` is always present and never changes.
- The journal constitutes the complete durable memory of the continuity.
- A message already written is immutable: the journal is append-only.
- Any correction or cancellation produces a new message correlated with the previous one.
- The order of the journal lines is canonical.
- `seq` is strictly monotonic within a continuity and equal to the line number.
- `created_at` is never used to reorder messages.
- Control events, Capacity calls, their responses, projections, errors and significant system events belong to the same stream of messages.
- The runtime can produce messages with `from = system` when a significant operational fact must be kept.

The continuity contains no field of the type:

- operational `status` (idle, running, waiting, suspended; not to be confused with the `active`/`archived` `status` of the header)
- `run_id`
- `current_message_id`
- operational `wait_group`
- `claimed_by`
- mailbox state

This information belongs to the runtime or mailbox Matters.

## Durable journal

The journal is the durable stream of the accepted messages, immutable. The mailbox is only a working index that is still active; `ack` never modifies the journal.

The journal is a Matter in JSON Lines (`substance_framing: lines`). The engine knows only the line boundaries (LF) there and never parses the JSON.

- **Append**: `matter.append` adds a contiguous block at the tail, atomically. A block can contain several lines: several related facts can therefore enter in a single commit. Each line, and therefore each block, ends with LF.
- **Guard**: the append carries `expected_line_count`. A conflict causes the same loop as for any mutable Matter: re-read the tail, recompute, retry, with a bounded number of attempts.
- **Read**: `matter.read` with `data_range` in lines (`offset`/`length` or `tail`). `matter.stat` gives `revision` and `total_lines` without reading content.
- **Size**: no imposed limit. There is no longer a 64 KiB threshold or an overflow Matter (see "Voluminous content").

### The continuity: a Matter, duplicated in memory

The **continuity** is everything that lets the LLM reason: the set of exchanges (user messages, LLM responses and reasonings, tool calls and their results). The journal is implicitly inside it: it is its durable form, and it also keeps the tracking facts that do not serve reasoning.

- **Formalized: the Matter.** The journal (with the header) carries persistence. If the wrapper stops, work resumes from it. It is the only source of truth.
- **Implementation constraint, not formalized: the in-memory copy.** For performance, the continuity is duplicated in an object of the wrapper, rebuilt from the journal and kept up to date at each write. It is neither a Brique element nor a state: it is reconstructible, and never a source of truth. This constraint is recalled, as a note, in each Capacity that reads or writes the continuity (see their "Implementation constraint" section).

It is **when the continuity is rebuilt** from the journal that its content is decided: the retained lines form the LLM thread, the others (the tracking of the machinery) stay in the journal without entering the object by default.

### Characterization of lines

The characterization of a line is its **`kind`**. It serves to **include or exclude** the line from the continuity, and from the projection: it is the only filter mechanism for now, with no additional field.

- **Who sets it**: the tool that writes the line, not the LLM. It knows where what it records comes from (who answered, what sort of Capacity it is).
- **By default**, the continuity takes up the whole LLM thread (table above). A projection criterion can remove kinds from it (`exclude_kinds`) or add tracking facts to it (`include_kinds`), which are then read in the journal.

### Tracking facts

The journal does not contain only stimuli: the execution tracking facts (wait group created or completed, run resumed, current intention changed, projection requested, error…) stay there, as durable facts of the continuity, under the kinds `event`, `projection`, `error` and `control`.

The measurement of the context size of an inference is one of these facts: an `event` line `inference_measured` (`message_id` of the current message, `input_tokens`: the total context, and `estimated` when the provider reported nothing). When the provider reported the usage, the line also carries the detail, **each field only if known**: `cache_read_tokens` (part of the context read back from the cache), `cache_creation_tokens` (part written to the cache by this inference), `uncached_input_tokens` (the rest: `input_tokens` minus the two previous ones, never negative; absent if the provider says nothing about the cache), `output_tokens`, `model` (`provider/model` that answered), `duration_ms` (duration of the call to the model) and `window_tokens` (`limits.context_tokens` of the pilot when given). `input_tokens` keeps its meaning: everything that reads it (context pressure, compaction) is unchanged; the lines written before this detail remain valid and have only the three original fields. It goes out in the same append as the other lines of the step and is read in the in-memory object, which keeps the last measurement; `runtime` does not have to carry it. They do not belong to the trace mechanism of the engine, which can be enabled and disabled and serves to debug: these are not the same things. These lines are messages without a mailbox entry, never consumed.

## Parent / child relation

`parent_id` and `children[]` are both stored explicitly.

They represent the cognitive relation between continuities and are fields of the header. Their consistency is maintained by the idempotent protocols of `continuity.create` and `continuity.delete` (writes guarded by `expected_revision`, without multi-Matter transaction).

This relation is independent of the placement in a Brique Structure.

## Placement

The `continuity` Matter contains no `structure_path` or equivalent information.

Its placement is entirely external and carried by a Structure. The same continuity can therefore be organized or referenced independently of its parent/child relation.

## Open points

Nothing that follows is decided; the current state remains in force until it is settled.

- **Content of `prompt` and `completion`**: they carry the prompt of the step of a Capacity pilot (`instructions`, `input`, `model`), needed for memoization and resumption: it is a durable fact. The assembled request sent to the provider is, however, an intermediate product (transit) and does not enter the journal.

## What does not belong to this schema

- No runtime.
- No mailbox.
- No current projection.
- No discovered capacities cached as a mandatory state.
- No generic metadata.
- No `updated_at` in the core.
- No `closed` state: the continuity exists as long as its Matter exists.

The `error` line `validation_error` (invalid pilot output) keeps what is needed to diagnose it afterwards: `errors`, **`output`** (the output as the model wrote it, bounded to 2,000 characters) and **`finish_reason`** (why the provider stopped) when known.

The `pilot` line (`message_out`) carries `correlation.causation_id` (the message the pilot was processing); the last one of a step that ends the chain also carries `ends_chain: true` in its content: see `capacity_pilot_activate.md`, "Exactly one response per message".
