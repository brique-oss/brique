# Capacity — `continuity.create`

## Intention

Create a new reasoning Continuity with all minimum state required for it to operate.

`continuity.create` triggers no reasoning. It only materializes a new Continuity ready to receive messages.

## Input

### `continuity.create`

| Field | Description |
|---|---|
| `pilot_ref` | Reference to the `pilot_definition` to use. |
| `initial_intention` | Founding Intention of the Continuity. |
| `parent_id` | Optional identifier of the parent Continuity. |
| `structure_path` | Optional location in a Brique Structure in which to reference the new Continuity. |

## Output

| Field | Description |
|---|---|
| `continuity_id` | Identifier of the created Continuity. |

## Effects

Through a recoverable multistep protocol (see “Creation protocol”), `continuity.create` creates:

- the journal Matter (`journal_<id>`, JSON Lines), whose first line is the pilots' reasoning framework (`kind: framework`);
- the `continuity` Matter (the header, which points to the journal);
- the associated `mailbox` Matter;
- the associated `runtime` Matter;
- the optional reference in a Structure.

## Initializing the `continuity` header

The `continuity` Matter is the Continuity header: a small document that no longer contains the messages. It is initialized with:

- `id = continuity_id`
- `created_at` = creation time
- `parent_id` = supplied value or `null`
- `children[] = []`
- `initial_intention` = supplied Intention
- `current_intention = initial_intention`
- `pilot_ref` = supplied value
- `journal_ref` = reference to `journal_<id>`

## Initializing the journal

The `journal_<id>` Matter is created empty, with `brique.substance_framing = lines`, `functional.format = jsonl`, and `functional.schema_ref` pointing to the schema of one line (`message`). It is created **before** the header, so `journal_ref` never points to an absent Matter.

## Initializing `mailbox`

The `mailbox` Matter is initialized with:

- `continuity_id = continuity_id`
- `entries[] = []`

## Initializing `runtime`

The `runtime` Matter is initialized with:

- `continuity_id = continuity_id`
- `status = idle`
- `run_id = null`
- `current_message_id = null`
- `active_wait_groups[] = []`

## Parent/child relationship

If `parent_id` is supplied:

- the parent Continuity must exist;
- the new `continuity_id` is added to `parent.children[]`;
- the new Continuity receives `parent_id = parent_id`.

Updating the parent is a creation-protocol step: the parent is read, then its full content is rewritten with `expected_revision` (an idempotent addition of the child to `children[]`, derived from the content just read).

Without `parent_id`, the new Continuity is a root Continuity.

## Structure

If `structure_path` is supplied, a reference to the new `continuity` is added to the relevant Structure.

Placement in the Structure is independent of the parent/child relationship. The `continuity` does not store its `structure_path`.

## The reasoning framework: first journal line

A Continuity journal is not empty at creation: its first line (`seq` 1) is the pilots' **reasoning framework**, a short text explaining how a pilot acts in the Brique world (retrieve rather than remember, read a contract before calling, route by family, read statuses). See `pilot_framework`.

- The text is read from the `/root/agentic/prompt/pilot_framework` Matter and **copied** into the line: `kind: framework`, `content: { text, source }`, `id: framework_<continuity_id>`, emitted by the `agentic` context. History retains what was provided regardless of later changes to the Matter, just as for a `prompt` line.
- It is read **before any effect**: an absent Matter rejects creation with `NotFound`; an empty Matter rejects it with `InvalidRequest`; nothing is created.
- The line is written once the Continuity exists, outside the critical-section lock (journal append acquires it). Its identifier is deterministic, so retrying creation does not write it twice.
- `framework` is history-only (never accepted externally and never entered into a mailbox) and **pinned**: every projection places it first, regardless of filters, limits, or budgets (see `continuity.project`). A Continuity created by succession therefore has its own framework before its summary; compaction never loses it.

## Creation protocol

Brique provides no multi-Matter transaction. `continuity.create` is a recoverable multi-Matter operation: each Matter is created or changed by a local atomic commit, and overall consistency comes from idempotent steps executed in this order:

```
journal_<id>
  ↓
continuity_<id>   (header, with journal_ref)
  ↓
mailbox_<id>
  ↓
runtime_<id>
  ↓
reasoning framework (first journal line)
  ↓
parent relationship
  ↓
Structure relationship
```

For every step:

- already completed in the expected state: idempotent success;
- present but contradictory: consistency failure;
- missing: create it.

Retrying creation completes or repairs missing steps. No rollback is promised. A `continuity` is complete only when its four Matters (journal, header, `mailbox`, `runtime`) exist; `mailbox.send` and `pilot.activate` explicitly fail on an incomplete Continuity.

Document Matters (header, `mailbox`, `runtime`) carry:

- `functional.format = json` and `functional.schema_ref` pointing to their content data schema (`continuity_data`, `mailbox_data`, `runtime_data`): the engine does not apply schemas; `agentic_core` validates every written content;
- their content as substance (`payload` of `matter.create`), never in the descriptor;
- `brique.substance_mode = brique` and `brique.substance_type = text`;
- a tracked `brique.revision`, since every later write is guarded by `expected_revision`.

Writes to the parent relationship and to the `continuities` Structure (both guarded by `expected_revision`) are preceded by reading the revision. On conflict, the target is read again and content recomputed before another bounded attempt. Per-`continuity_id` critical-section locks are acquired in identifier order when several are held simultaneously.

## Creation by succession

`continuity.create` accepts the lineage fields `predecessor_id`, `lineage_id`, `generation`, and `current_intention`, which only `continuity.succeed` supplies. An ordinary creation opens a new lineage: `lineage_id` is its own identifier, `generation` is 1, and `status` is `active`. `generation` must be a positive integer.

## `pilot_ref` form

`pilot_ref` designates exactly one of two forms: a Matter `{ context, matter_id }` (context must be `/root/agentic`) or a Capacity `{ context, capacity, limits? }` (context is the Capacity's context). Supplying both or neither is an error. Matter or Capacity existence is verified at creation; a Capacity is checked through the context's `read.meaning`.

**A `pilot_ref` designates a pilot, not merely any existing element** (November 11). In both forms, a Capacity executes: for a Matter it is `pilot.activate`, the generic launcher, which reads the Matter as its definition; for a Capacity it is the plan itself. Validation therefore depends on what can be known about the Capacity that will run:

| Form | What is verified | Where it is read |
|---|---|---|
| `{ matter_id }` | the Matter is a **pilot definition**: `objective.type = pilot_definition` | `matter.read` in `meaning` mode |
| `{ capacity }` | the Capacity **declares itself a pilot**: `objective.agentic.pilot` is present in its descriptor (the same path used to find pilots semantically: `agentic.pilot.subject`) | `read.meaning`, `objective` section |

Launching an ordinary tool as a pilot by mistake makes the child call that tool with the message as parameters, fail three times, and park the message, as observed on November 11. It is now rejected **at creation**, with a message explaining what is missing (“this is an ordinary tool: invoke it; a pilot plan declares `objective.agentic.pilot`”). The same validation applies to `set_pilot`. See `capacity_defined_pilot`.

## No reasoning

`continuity.create` does not:

- launch `pilot.activate`;
- call the LLM;
- send the first message;
- automatically trigger processing.

After creation, `runtime.status = idle`.

The first reasoning act must be triggered separately by `mailbox.send`.

## Delegation

Delegation can be composed as:

```
continuity.create
  ↓
mailbox.send to the new Continuity
```

`continuity.create` therefore has no delegation-specific semantics.

## Validating `pilot_ref`

`pilot_ref` must reference an existing, valid `pilot_definition`.

A Continuity cannot be created without an associated pilot.

## Validating `initial_intention`

`initial_intention` is required and becomes immutable after creation. `current_intention` is initialized with the same value.

## Identifier

`continuity_id` must be unique. It may be generated by `continuity.create` or supplied according to the selected technical convention.

An existing `continuity` must never be overwritten.

## `created_at`

`created_at` is set at creation time. It represents the birth of the `continuity` itself, not the date of its first message.

## Messages

Immediately after creation, the journal contains only the reasoning framework described above and no exchange message.

Creating a `continuity` is not automatically represented as a message in its own history unless that decision is explicitly added to the protocol later.

## Children

`children[]` is empty at creation.

Future descendant relationships are added when other Continuities are created with this `continuity` as parent.

## Creation failure with a parent

- If `parent_id` references no existing `continuity`, creation fails.
- If the parent relationship cannot be updated because of a persistent revision conflict or contradictory state, creation fails explicitly and may be retried.

## Creation with a Structure

If `structure_path` is supplied but referencing the Continuity in the Structure fails, creation explicitly fails when that reference was part of the request. Matters already created remain; retrying creation completes the missing step.

A `continuity_id` designating an identically created Continuity is an idempotent success; if its state contradicts the request, it is a consistency failure.

## What `continuity.create` does not do

- It produces no message.
- It starts no run.
- It performs no inference.
- It creates no `wait_group`.
- It discovers no Capacity.
- It modifies no `pilot_definition`.
- It creates no secondary Intention.
- It processes no mailbox.

## Central invariant

`continuity.create` guarantees:

```
a founding Intention + a pilot
  → a consistent Continuity
    + its mailbox
    + its idle runtime
    + any structural relationships
  → ready to receive its first message
```

## Implementation constraint

The Continuity is managed in a Matter (the journal) for persistence and duplicated in an in-memory wrapper object for performance. This is an implementation concern rather than a formalization concern; see `agentic_core_concurrency_model`. For this Capacity, the journal contains only the reasoning-framework line at creation; the in-memory object is built from it immediately or lazily on first use.
