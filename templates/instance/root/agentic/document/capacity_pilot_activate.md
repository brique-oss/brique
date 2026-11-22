# Capacity — `pilot.activate`

## Intention

Activate the cognition of a continuity.

`pilot.activate` is the Capacity that takes operational possession of an `idle` continuity and launches the generic cognitive protocol associated with its `pilot_ref`.

It does not define the cognition itself. It uses:

- the `continuity` Matter (the header) and its journal (`journal_ref`),
- the `runtime` Matter,
- the `mailbox` Matter,
- the `pilot_definition` Matter,
- the Brique Capacities needed for the unfolding of the cycle.

## Input

### `pilot.activate`

| Field | Description |
|---|---|
| `continuity_id` | Identifier of the continuity to activate. |

## Output

Success:

- `continuity_id`
- `run_id`
- `status`

The Capacity can return after:

- the run is finished and the continuity has returned `idle`,
- the continuity has become `suspended`,
- the continuity has been deleted,
- or the execution ended with an unrecoverable error.

## Preconditions

- The `continuity` must exist.
- The associated `mailbox` must exist.
- The associated `runtime` must exist.
- The `pilot_ref` of the `continuity` must reference a valid `pilot_definition`.

## Acquisition of the runtime

`pilot.activate` begins by trying, in a single write of the `runtime` Matter guarded by `expected_revision`:

```
runtime.status = idle
  → runtime.status = running
  → creation of a run_id
  → runtime.run_id = run_id
```

This operation represents the exclusive acquisition of the continuity. Of two concurrent activations, one succeeds; the other re-reads the runtime, observes `running` and becomes a no-op.

The uniqueness of the run is guaranteed durably by `runtime.status`, `run_id` and `expected_revision`, not by a lock held for the whole life of the run.

## An activation is a Capacity like any other

`pilot.activate` is called and awaited like any Capacity, including by a pilot: a **sub-continuity** is only `continuity.create`, `mailbox.send` then `pilot.activate`, three Capacities already known to the pilot. No delegation primitive exists outside these.

The waiting contract is uniform:

| Situation | Response |
|---|---|
| the continuity is `idle` | `pilot.activate` takes the run, makes it run and answers **when it has returned to rest** |
| another live run owns it | `pilot.activate` **waits for the end of this run**, then answers with the state in which the continuity is left |
| an orphan run owns it | it is resumed (behavior unchanged) |
| `suspended` | immediate response |
| the call is a **wake-up** (`wake: true`) | immediate response, without waiting: it is the activation that `mailbox.send` triggers after each message |

The wait is bounded by the wait delay of an intention (600 s). At the end, the response carries the current state.

A continuity cannot wait for its own activation: an awaited intention of `pilot.activate` on its own continuity fails explicitly (it would wait for the run that requests it).

The result of a child has no field: the child answers in its journal (or to the parent by a `continuity` message), and the parent reads it with `continuity.project`.

## If the continuity is not `idle`

- **`status = running`**: no new run is created. The Capacity returns without effect.
- **`status = waiting`**: no new run is created. The existing run stays owner of the continuity.
- **`status = suspended`**: no new run is created. The continuity must be resumed explicitly before activation.

### Invariant

A continuity never has two simultaneous cognitive runs.

## Main loop

After acquisition of the runtime, `pilot.activate` unrolls a loop.

Conceptually:

```
acquire runtime
  ↓
process applicable priority controls
  ↓
look for a cognitive resumption already available
  ↓
otherwise read the next normal message
  ↓
execute its complete cognitive chain
  ↓
ack of the message
  ↓
start again
  ↓
if no more work is available:
  publication of idle, then re-read (release-and-recheck)
  ↓
return
```

## Processing of controls

At the safe points, the `pending` controls are processed before the normal messages. Each control is claimed by the run (`mailbox.read` in `control` priority), then its durable content is loaded from the journal.

Actions applied:

- `suspend`
- `resume`
- `set_pilot`
- `set_intention`
- `delete`

A deferred control follows: `pending`, claim by `pilot.activate`, application, `mailbox.ack`.

`delete` is terminal: `continuity.delete` is called, the run stops, and no `ack` is emitted since the mailbox disappears with the continuity. The controls already applied immediately by `mailbox.send` have their entry removed by the send and never arrive here.

A control never cuts:

- an LLM inference in progress,
- a Capacity in progress,
- a Matter mutation in progress (individually atomic).

## Current message

When a normal message is obtained through `mailbox.read`:

- `runtime.current_message_id` receives its `message_id`.
- The message stays `current_message` during the whole cognitive chain it opens.

This chain can contain:

- several inferences,
- several Brique Intentions,
- several responses,
- several wait groups,
- several cognitive resumptions.

## Building the LLM context

Before each inference needed, `pilot.activate` builds the request as **the conversation of the continuity** (see "The journal is the conversation").

Order:

1. the **stable system text**, marked `cache: true`: the cognitive framework of the continuity (its first line, `kind: framework`), the prompt of `pilot_definition`, its rules, the output guide, its stable `available_capacities` and the `initial_intention` (immutable)
2. the **conversation**: the projection of the journal, one line per message with its role; the last message is marked `cache: true`
3. **what is new**, in one last message: `continuity_id` (the identifier of the continuity that decides: a pilot cannot delegate, nor be designated as a parent, without knowing its own; after a succession, it is that of the successor), `current_intention`, `current_message`, possible active stimulus. The current message is already in the conversation when the projection contains it: the last message then **designates** it (`id`, `kind`, `from`) without repeating it; it carries it in full only if the projection does not contain it. The observer of the request always has the whole message in `metadata.current_message` (no provider reads it)

The active stimulus can be in particular:

- `active_wait_result`: only that the wait is over and how many responses it holds (`outcome`, `received`, `expected`). The responses themselves are in the conversation
- `execution_result`
- `validation_error` of correction

No additional inference is created solely to rebuild the context. The context is built at the moment when an inference is already needed.

## The journal is the conversation

A request to the model is not a text that is re-assembled at each turn: it is the **conversation of the continuity**. Each line of the journal becomes a message with its role, in the order of the journal, through a single function (`substrate/conversation.py`) shared by `pilot.activate` and `pilot.infer`. A request is therefore: the stable system text, the conversation so far, then **only what is new**. What was sent at the previous turn is the beginning, byte for byte, of what is sent now: it is what the cache of the provider reads.

| Journal line | Message sent |
|---|---|
| `framework` | the system text (the framework), cacheable; not a message |
| `user`, `continuity`, `summary` | user |
| `prompt` (the assembled prompt of a step) | user |
| `completion` (the output of a step of a plan, or the decision of a Matter pilot) | assistant |
| `pilot`, `capacity_intention` (the effects of a decision) | assistant in the conversation of a plan that includes them; **nothing** in that of a Matter pilot: the decision that produced them is already there, through its `completion` line |
| `capacity_response` | user, with the identifier of the intention to which it answers (`intention_id`): it is the only link between a response and its question, it is said in the response |
| `event`, `projection`, `error`, `control` | nothing: the tracking of the machinery is not told to the model |

**A single form of history for all pilots.** The model re-reads what it wrote, not a reconstruction. A plan does it through the `prompt` and `completion` lines of `pilot.infer`. A Matter pilot records its **decision** (the output it returned and that the code accepted, with its free values decoded, but without what the code adds to it: wait group identifier, correlations) as a `completion` line (`step: decision`), **before** applying the slightest effect of it. The effects (`capacity_intention`, `pilot`) stay in the journal as facts, but the conversation of the Matter pilot does not show them: it would say the same thing twice, in a broken-up form (one intention per message, without its wait group, without its intention update). Before, it saw bare intention objects whereas it had to produce a single object. If a crash occurs between the decision and its effects, the decision is in the history and the recovery stimulus says which intentions were emitted.

The text of a line is written as compact JSON with ordered keys (a text is sent as is): the same line always gives the same bytes. The request carries a `metadata` field (the continuity, the step or the message, the identifiers of the view) that **no provider reads**, for whoever observes the request.

Consequences:

- A prompt enters the conversation **once**. The prompt of the first step of a plan, with its Matters (`brique_search`, for example), is a `prompt` line; the following steps send only what is new and re-read the rest from the journal.
- The cache is **append-only**: compaction itself reads the cache (it is one more turn of the conversation), but the succession that follows it starts from a new conversation and pays again, which is intended. An exceeded projection budget removes the oldest lines and does the same.
- Two cache markers per request: on the system text (the framework, or the stable text of a Matter pilot) and on the **last message of the history**. The new message is never marked. The Anthropic adapter turns the marker of a message into a block with `cache_control`.
- The provider silently ignores a prefix shorter than its minimum (4,096 tokens for Haiku 4.5, 1,024 for Sonnet 4.6, 512 for the 5.x models): `cached_input_tokens` then stays at 0. It is the only measurement.

### `pilot_definition`

`pilot.activate` reads `pilot_ref` from `continuity`.

The `pilot_definition` provides in particular:

- `prompt`
- `model`
- `parameters`
- `default_projection`
- `known_capacities`
- `rules`

The cognitive core of the pilot is always present in the context.

### The cognitive framework

The first line of the journal is the cognitive framework of the pilots, written by `continuity.create` (see this document and `pilot_framework`). It is a **pinned** line: every projection carries it first, whatever its filters, its limit and its budget. `pilot.activate` extracts its text, places it at the **very beginning of the system text** (before the prompt of the pilot, its rules and the output guide), and does not repeat it in the journal view it sends. It is therefore in the cacheable prefix, written once per continuity and never re-assembled.

For a Capacity pilot, `pilot.activate` does not build the prompt: it is the plan that assembles it through `pilot.infer`, and it is the step that decides to carry the framework (`projection`, or `framework`).

### Projection

The complete continuity is not sent automatically to the LLM.

`pilot.activate` uses `continuity.project` to produce a deterministic view adapted to the context budget. It has read the `pilot_definition` only once for the run (`#resolve_pilot`): it passes to `continuity.project` only its `default_projection`, as a value, not the Matter. `continuity.project` therefore does not re-read the `pilot_definition`.

`pilot.activate` requests the projection of a `pilot_definition` pilot without the effect lines (`pilot`, `capacity_intention`), which the decision already says. The projection budget of the pilot (`default_projection.budget`) is a hard cut in characters of the text actually said, not the normal regime: it is compaction, triggered on the measured tokens, that bounds the context. A pilot that has no reason of its own to cut sets none; if it sets one, it is worth at least the window `limits.context_tokens` converted into characters, otherwise the history is cut before compaction takes place. The succession summary is never cut.

The projection can evolve between two inferences already needed. A dynamic projection requested by the pilot can complement or temporarily replace the default projection.

### `available_capacities`

The available Capacities come from two sources:

- the **stable** Capacities of `pilot_definition` (`ref`, `description`) go into the **system text**, after the output guide. They grow with the pilot, not with the conversation: it is what makes the prefix cacheable and useful;
- the Capacities **discovered** in the continuity are volatile: they would go into the volatile context, under `discovered_capacities`, never in the prefix.

`pilot.activate` no longer adds `/root/read.capacity` and `/root/read.meaning` by default: a `ref` and a short description do not say how to call a Capacity, its contract does, and it is the **cognitive framework** (first line of the journal, at the beginning of the system text) that gives these two calls as they are. `read.capacity` returns the contract of a **engine** Capacity: inputs, outputs and family (`target_type`). It does not know the user Capacities, whose contract is the descriptor returned by `read.meaning`, addressed to the context of the element. The policy of the pilot allows them without listing them (see below).

The LLM can explicitly discover new Capacities through the vocabulary and the semantic field.

## Inference

`pilot.activate` calls the LLM subsystem with:

- the model,
- the parameters,
- the context built,
- the expected schema of `pilot_output`.

### Transit of the request and of the view

The request sent to the model (system, projection, current message, stimulus) and the view returned by `continuity.project` are **intermediate products**: they are made for one step and consumed by the next. They travel as transit (value under 64 KiB, otherwise `@file` to another wrapper, `@object` in the same wrapper), and the runtime of the wrapper deletes them after reading. The request is not written in the journal; the latter keeps the facts that result from it (intentions emitted, messages, projection requested and its retained identifiers). The context stays reconstructible at any time from the journal and the state Matters. See "Persistent and transient" in `agentic_core_concurrency_model`.

The output must be structured. It is requested as `json_schema`: the schema sent to the provider is a **strict projection** of the `pilot_output` schema, generated from it (closed objects, free values `parameters`, `content` and `intention_update` carried as JSON text). Its style depends on the provider that actually answers (`parameters.upstream_provider` of the pilot definition, otherwise `model.provider`): for `anthropic`, which limits union parameters to 16, the properties are optional and absent when not used, with no union at all; for the other providers, all the properties are required and nullable. The guide added to the system text states the corresponding convention (omit or use `null`). The response is brought back to the canonical form before validation: the `null`s of absent values are removed and the JSON text decoded. A textual response is read tolerantly (raw JSON, JSON in a code block, or a JSON object contained in the text). The canonical `pilot_output` schema does not change.

## Validation of the `pilot_output`

### What the code writes in place of the model

Principle: **what is deterministic is code of the pilot; the model analyzes and produces information, and it is not asked to write what the code knows how to write.** The schema that the model fills therefore contains neither an identifier nor a family. Before the validation, `pilot.activate` normalizes the output (`normalize_output`):

- an intention **without an identifier** receives one (`i1`, `i2`, …, free in the continuity); an identifier **already used** in the continuity or earlier in the same output is renamed (`x_2`), and the references that target it in the following wait group follow. The model no longer has to remember which identifiers are taken;
- the **family** (`target_type`) is written by the code when it knows it with certainty: the family declared by the `pilot_definition` for a capacity that it lists, otherwise the one the name of an engine capacity says (`read.*`, `meaning.*`, `vocabulary.*`, `edit.*`, `trace.*` reflexive; `matter.*`, `structure.*` matter; `wrapper.*` execution; `context.start|restart|stop` control). A wrong family from the model is corrected, an absent one is added; an unknown user capacity stays `user`.

- a **succession is alone**: if the model writes one next to other fields (`intentions`, `wait_group`, `messages`, `projection_request`), the code knows what it decided. With a non-empty summary, it decided to succeed and nothing has yet been executed: the succession is kept, the rest is dropped (the successor resumes the current message, and the summary says what remains to be done). Without a summary, it wanted to work: the succession is dropped. The gap is traced (`output_repaired`, `succession_alone`). Observed: a real pilot mixed a succession with work this way seven times in a day, and each processing failed.

The decision kept in the journal (`completion`) is the **normalized** decision: the model re-reads there the identifiers that the responses will carry (`intention_id`). The text of the cognitive framework no longer asks the model for a family or an identifier, which also lightens the fixed prefix of each request. A duplicated identifier is therefore no longer a validation error to correct: it is repaired.

**Detecting what the model still writes wrong.** The model no longer receives these fields: `id` and `target_type` are not in the schema it fills (they carry the note "Assigned by agentic_core"). The model does not designate its intentions: a wait group waits for **all the awaited intentions of its output**, and its `expected` serve only to wait for a delegated continuity or an event. An intention reference that designates nothing in the output is dropped (`expected_dropped`) and the default applies. **Waiting for "the continuity" of a child launched with a task** (`pilot.launch` with `parent_id` and `message`) **is waiting for the launch**: its Response is the result of the child. A `continuity` element whose reference is the `continuity_id` of such a launch of the same output is therefore dropped (`continuity_wait_is_the_launch`), the launch is awaited, and the default applies. Without this the pilot waited for a message that never comes, and forever if the launch had failed (observed on November 11 with the guide). The code always writes them; it is not a repair. If an output nevertheless carries some (a scripted pilot, a plan, an old context) and it is wrong, the correction is said in the journal by an `output_repaired` event (`id_renamed` with the old and the new identifier, `family_corrected` with what was written and what the code put). A model that writes nothing wrong produces no event; the count of these events per run measures what it still gets wrong.

Before any effect, `pilot.activate` fully validates the `pilot_output`.

The validation bears in particular on:

- its structure,
- the allowed combinations,
- the Brique references,
- the destinations,
- the wait groups,
- the Capacity refs,
- the inputs of Capacities when their contract is known.

The free values of a `pilot_output` (`parameters`, `content`, `intention_update`, `projection`, `correlation`) are written by the model as **JSON in text form**, since a strict output schema cannot describe an open object. `pilot.activate` decodes them before the validation, for both kinds of pilots: the output of a Capacity pilot (a plan whose `pilot.infer` step asks the model for the intentions) is decoded exactly like that of a `pilot_definition`. The decoding is idempotent: a plan that already returns objects is not modified. It takes place here, where we know that the output is a `pilot_output`, and not in `pilot.infer`, whose `output_schema` is that of the plan and does not say which strings are JSON.

If the output is invalid:

- no effect is produced;
- a single automatic correction attempt is allowed.

The correction receives:

- the invalid `pilot_output`,
- the validation errors.

If the second output remains invalid, the current processing fails explicitly.

## Application of the `pilot_output`

A valid output is applied in a deterministic order:

1. possible `intention_update`
2. possible `projection_request`
3. possible `wait_group`
4. `intentions[]`
5. `messages[]`

The `wait_group` is materialized **before** the invokes of the awaited intentions it references: a fast Response can thus never arrive before its wait exists.

### `projection_request`

A `projection_request` constitutes a cognitive resumption point.

It does not coexist with:

- `intentions[]`
- `wait_group`

It can coexist with `intention_update`.

The runtime:

1. possibly applies `intention_update`,
2. executes the projection,
3. rebuilds the context,
4. performs the next inference.

### Intentions

`pilot.activate` can emit one or more Brique Intentions. Each `pilot_intention` carries:

- `id`: chosen by the pilot, referenced by a `wait_group` of the same `pilot_output`;
- `target_capacity`: absolute reference of the Capacity targeted;
- `target_type`: family of the target (`user` by default; `matter`, `reflexive`, `execution` or `control` for a native Capacity);
- `parameters`;
- `await_response`: boolean chosen by the pilot.

Each emission is traced in the journal.

- **`await_response = false`**: fire-and-forget. The invoke is non-blocking, no Response is awaited or tracked, and the intention can never be referenced by a `wait_group`. The pilot does not go to `waiting` because of it.
- **`await_response = true`**: the Response is awaited. The intention can be referenced by a `wait_group` through `expected.ref = intention.id`. A `wait_group` without `expected` waits by default for the intentions of the same output whose `await_response` is true: the model writes only the condition. `expected` is listed only to wait for a delegated continuity or a part of the intentions.

A `wait_group` that references an intention `await_response = false`, or an intention absent from the same `pilot_output`, is invalid.

#### Delivery of an awaited Response

The Response of an awaited invoke is first transformed into a canonical Message, then delivered by `mailbox.send`:

- `id` derived deterministically from `intention.id` (a repeated delivery is therefore an idempotent retry);
- `from`: the Capacity targeted; `to`: the emitting continuity;
- `kind = capacity_response`, `content.response` = the Response;
- `correlation.intention_id = intention.id`.

`mailbox.send` captures it in the corresponding `wait_group`, or admits it as a normal message if there is none. The raw Response is never sent directly to `mailbox.send`.

### Messages

`messages[]` represent addressed outputs.

They can target in particular:

- `user`
- another `continuity`
- a recipient recognized by the protocol

A message intended for another continuity goes through `mailbox.send` of the target continuity.

## Cost of an inference

The schema and the guide travel with each inference, so they are kept short, which also serves the less capable models:

- the output guide says only what the field names do not say; the provider schema carries no top-level descriptions;
- the free values (`parameters`, `content`, `intention_update`) are said once, in the guide;
- the stable system text (prompt, rules, guide) carries the hint `cache: true`, which the Anthropic adapter translates into `cache_control`. A provider without explicit cache ignores it; below the minimum cacheable length of the model, the provider ignores it too. The part served by the cache is reported by `usage.cached_input_tokens`.

Reference measurement (Haiku 4.5, R01): 2,339 input tokens before reduction, 2,080 after.

## Pilot defined by a Capacity

When `pilot_ref` designates a Capacity (`{ context, capacity, limits? }`) instead of a `pilot_definition`, the decision function is no longer a generic inference: it is the invocation of this Capacity, a plan that mixes deterministic actions and prompts. Everything else is common: mailbox, run ownership, wait groups, validation and application of the `pilot_output`, succession, controls, resumption.

At each turn, the Capacity receives `continuity_id`, `run_id`, `message` (the current message) and, at a resumption, `stimulus` (`active_wait_result`, `execution_result`, `projection`). It returns a `pilot_output`. The plan is stateless: a wait is a returned `wait_group`, and the resumption invokes the Capacity again with the stimulus.

- Each prompt of the plan goes through `pilot.infer` (capacity `pilot.infer`): it is recorded in the journal (`prompt` then `completion`), memoized, validated. The **actions** of the plan (tools, loops, conditions) leave no trace in the continuity.
- An invalid output is **not** corrected by a second inference: it is a defect of the plan. The processing fails explicitly (`pilot_output_invalid`, then `treatment_failed`) and the claim is released.
- A missing Capacity makes the processing fail, not the continuity.
- A Capacity pilot has no `pilot_definition` nor model of its own: its compaction is never automatic. But if its `pilot_ref` declares `limits.context_tokens` (and, optionally, the two ratios), `pilot.activate` computes the pressure as for a `pilot_definition` and gives it to the plan (`context_pressure`) from the advised level on. The succession remains a decision of the plan.
- `limits.max_inferences_per_message` (100 by default, never raised) and `limits.timeout_s` (600 by default) bound a turn.
- The pilot can change nature along the way: the control `set_pilot` accepts both forms. It takes effect from the next inference of the same run: after a control applied at the safe point, the run reloads the pilot and its inference limit, instead of continuing with the old one until the next activation.

## The policy of the pilot

The definition of the pilot (or, for a Capacity pilot, its `pilot_ref`) can carry two lists of capacity reference patterns, where `*` replaces any text, `/` included:

- `forbidden_capacities`: what the pilot will never invoke. It always wins.
- `allowed_capacities`: what it can invoke. Absent, everything is allowed; present, only that, **plus** `/root/read.capacity` and `*/read.meaning`, which `agentic_core` exposes to every pilot (a pilot cannot call what it has not read the contract of), unless `forbidden_capacities` names them.

The policy is stated in two ways, which combine: by **capacity** (`allowed_capacities`, `forbidden_capacities`: an exact reference, or a pattern with `*`) and by **context** (`allowed_contexts`, `forbidden_contexts`: a context path, for example `/root/music`). A context covers any capacity addressed to it **and to the contexts it contains** (`/root/music` covers `/root/music/album.enrich` and `/root/music/streaming/encode`): contexts are nested and a boundary of responsibility extends to what it contains. The forbidden, by capacity or by context, always wins; the allowed, as soon as one of the two lists is present, is satisfied by a listed capacity **or** by a listed context.

**Launching a pilot.** An intention to `pilot.launch` is also checked against the policy of the pilot that requests it, **on the pilot it names**: its `pilot_ref` is read as a reference (`<context>/<capacity>` for a Capacity pilot, `<context>/<matter_id>` for a Matter pilot) and goes through the same lists. A parent whose `allowed_capacities` does not name a pilot therefore cannot launch it. No new mechanism.

The policy is applied **by code**, before any effect of the output, never by a request made to the model: the whole output is refused if a single intention targets a forbidden or unallowed target. No intention, no wait group, no intention update, no message of this output takes place. The pilot receives an `execution_result` that says which targets were refused and why, and decides again. A target that is not a canonical absolute reference (`/root/./x`, `//`, `..`, trailing slash) is refused, so that a writing detour does not bypass a pattern. The model is warned in the stable system text ("Never invoke…", "You may invoke only…"). Without either list, nothing is restricted. A pilot that asks three times in a row for what its policy refuses fails its processing (the failure counts as an attempt on the entry of the message): without this bound, a stubborn model would chain up to the limit of the chain, fifty paid inferences, for the same request.

## What a message can cost, at most

A cognitive chain is bounded to `limits.max_inferences_per_message` inferences per processing (50 at most). A message whose processing fails is resumed at once in the same run and set aside at the third attempt: it therefore costs no more than three times this bound, and the messages that follow it wait only for these three attempts. The bound is that of a run; a crash followed by a resumption counts it again, which is accepted (a resumption is a decision, not a loop).

## The wake-up of a wait

A run that waits is woken by notification (`mailbox.send` notifies when a response or a control arrives). The re-read of the durable state is only a safety net, every 5 seconds: re-reading two Matters per second and per waiting continuity would serve no purpose.

## Failures are said, never lost

- A message whose processing fails is traced and its entry released with one more attempt. **The run does not stop**: it immediately takes up again the oldest waiting message, which is this one, until the third attempt; at the third failure, the entry is set aside (see `mailbox.release`) and the run continues with the next message. Thus a message that fails blocks those that follow it only for the time of its three attempts, and nothing waits for a wake-up. Transient failures of the provider are already retried below, by the adapter, with a wait. Each attempt has its own inference bound. If the entry cannot be released (the run no longer owns it), the run ends at rest as before.
- A message **set aside** takes with it the wait groups of its chain: they belonged to this message, which will no longer be processed. They are closed (event `wait_groups_abandoned`, with their identifiers) and do not stay in the runtime. Without this, a group already satisfied but never consumed would be given as a stimulus to the next message, which did not ask for it, and compaction, which requires that no group be active, would be blocked forever. As long as the message is not set aside (one or two attempts), its groups stay: the resumption replays the same stimulus.
- A `NotFound` is read as "the continuity was deleted" only if its header really no longer exists. A provider or a capacity not found is a failure of the processing.
- A wait (`wait_group`) that never receives a response ends after `limits.wait_timeout_s` (3600 s by default): each missing response becomes a `timeout` error response delivered by `mailbox.send` like the others, with an identifier `timeout_<continuity>_<ref>` different from the real response. If the real one arrives late, it enters the history as a late result of a resolved wait.
- The start of a continuity whose run is dead is **explicit**: the capacity `pilot.recover` (see `continuity.stranded`); nothing starts by itself at restart.

## Context pressure and compaction

**The model of a `pilot_definition` pilot does not see the context pressure.** Since compacting is a threshold of measured tokens, it is code: compaction triggers by itself at the required level, with the fixed summarization instruction, and `continuity.succeed` adds to the summary the exact register of the calls (see `continuity_succession.md`). Showing the advised level to the model invited it to succeed too early, and each successor started its work over. A succession decided by the pilot remains valid (the message in progress continues in the successor), but nothing in its context solicits it. The level is still given to a **Capacity pilot** (parameter `context_pressure`), whose plan is code.

The definition of the pilot declares `limits.context_tokens` (window of the model), `compaction_advised_ratio` (0.60) and `compaction_required_ratio` (0.85). Without a window, no pressure is computed.

- **Measurement:** at each inference, `usage.input_tokens` of the provider is recorded, with the detail of the usage (read from cache, written to cache, uncached input, output, model, duration, window: see `schema_matter_continuity.md`), in an `event` line `inference_measured` of the journal, in the same append as the other lines of the step, and read in the in-memory object (estimation characters ÷ 4 as long as the provider has reported nothing). `runtime` does not carry this measurement.
- **Level `advised`:** no effect on a `pilot_definition` pilot (the model does not see the pressure). A Capacity pilot receives `context_pressure` (`tokens`, `window`, `ratio`, `level`) and its plan decides.
- **Level `required`:** at the safe point (no message in progress, no wait group) and only if normal work is waiting, `pilot.activate` executes a compaction inference then calls `continuity.succeed`. This inference is one more turn of the conversation: same system, same history and same schema as the other turns, hence read from the provider cache; only the last message changes and carries the summarization instruction. A compaction failure is traced (`compaction_failed`), leaves the continuity unchanged, and is not asked again by the same run.

## Succession decided by the pilot

A `pilot_output` carrying `succession: { summary }` is exclusive of `intentions`, `wait_group`, `messages` and `projection_request`. The message in progress is released (`mailbox.release`) so that the next continuity processes it, then `continuity.succeed` is called with the `run_id` of the run. The run ends with the status `archived`.

## Archived continuity

An `archived` continuity is never activated: `pilot.activate` answers `status: archived` without modifying anything.

## Delegation and lineage

A `continuity` message sent to another continuity carries by default `correlation.continuity_id = lineage_id`, the identifier that the delegating parent knows, whatever the generation of the sender.

## Voluminous responses

The Response of an awaited invoke is written as it is in the `capacity_response` line of the journal, with its status, whatever its size. There is no overflow Matter and no threshold.

The delivery of this Response to `mailbox.send` takes place in a background task that nobody reads: it must never be lost silently. If `mailbox.send` does not take it (conflict, delay), it is offered again (3 attempts, with a growing wait; the delivery is idempotent). If it is still not taken, the loss is written in the journal (`error`, `response_delivery_failed`, with the identifier of the intention). A continuity deleted in the meantime or a Response already delivered end the delivery without a trace.

## Waiting

If the cognitive progress depends on an external condition not satisfied:

- `runtime.status = waiting`.
- The same `run_id` stays owner of the continuity.
- The `pilot` Capacity stays logically associated with this run.

When an event satisfies a `wait_group`:

- `runtime.status` goes back to `running`.
- The cognition resumes.

The cognition also resumes when a `user` message is waiting (see "User message in the middle of a chain"): the wait groups then stay open.

## User message in the middle of a chain

This section concerns the Matter pilot. The user always interacts with a Matter pilot; the continuity of a Capacity pilot receives only what its parent sends it. The reasoning and the prohibitions are in `user_message_in_flight`.

A `user` message that arrives while the run is `running` or `waiting` does not wait for the end of the chain:

- `mailbox.send` writes it on arrival and opens the inference window (see `capacity_mailbox_send`).
- **During a wait**, the loop wakes up for this message as for a control (in addition to a satisfied group). The mailbox entries do not carry the kind of the message: the loop reads it in the journal and wakes up only for a `user` message.
- **At a safe point** (after an inference, after the execution of a `pilot_output`), and only when a chain is in progress (`current_message_id` set), it claims the waiting `user` messages through `mailbox.read` with `kind = user`, and **absorbs** them: they do not open another processing, they join the chain in progress.
- **The `user_message` block.** The news block of the next inference carries a `user_message` block (schema `user_message`) with the identifiers of the absorbed messages, in order of arrival. It is not a stimulus: a stimulus (execution result, projection, resumption, wait result) may already be pending at that moment, and the block is added to it without replacing it. The wait groups stay open; the inference can create others.
- **The window** is held by the loop from the opening (taking over the reservation) to the writing of the decision line. It does not cover the launching of the effects of this decision. Reserved to a request, it stays open to the writes of the loop (see `agentic_core_concurrency_model`); the background tasks of the awaited intentions do not have this identity.
- **After a brutal stop**, the resumption puts back to `pending` every `normal` entry claimed by the dead run that is not its current message: an absorbed message is absorbed again by the new run (at most one more time, like S44).
- **Acknowledgement.** An absorbed message stays `claimed` until the end of the chain, then is acknowledged with the main message. A stop in progress puts it back to `pending` (same rule as the resumption of a dead run).
- **Inference ceiling.** Each absorbed message renews the allocation of the chain.
- **End of chain.** It ends when nothing is awaited any more: no open wait group, no cognitive resumption, no absorbed message unprocessed.
- If the window expires without the loop having taken it, the request becomes a normal message, processed at the end of the chain.

## Satisfied wait group

When a `wait_group` becomes `satisfied`:

- `pilot.activate` builds an `active_wait_result`.
- This result is injected only once into the resumption inference.

After the associated cognitive resumption:

- the group goes `completed`,
- then is removed from `active_wait_groups`.

If several groups are `satisfied`, they are processed one by one, in the stable order of `active_wait_groups`.

## `execution_error`

If an action of a valid `pilot_output` fails after some effects succeeded:

- the execution of the `pilot_output` stops immediately;
- the effects already carried out stay acquired.

`pilot.activate` builds an `execution_result` describing:

- the actions already successful,
- the action that failed,
- the error obtained.

A new inference is launched in the same cognitive chain. The actions already successful are never replayed implicitly.

## End of the processing of the current message

The current message is considered entirely processed when no cognitive resumption linked to its immediate chain remains. In particular:

- no `projection_request` to resume,
- no `validation_error` to correct,
- no `execution_error` to resume,
- no `wait_group` blocking the current chain.

*Fire-and-forget* Intentions can still be in progress in the world without keeping the current message open.

## Ack

When the cognitive chain of the `current_message` is finished, `mailbox.ack` is called (one mailbox write guarded by `expected_revision`) with:

- `message_id = current_message_id`
- `run_id = runtime.run_id`

Then `runtime.current_message_id = null`.

## Next message

After `ack`, `pilot.activate` checks:

1. the priority controls,
2. the wait groups already satisfied,
3. the next available messages.

A single normal message is cognitively processed at a time.

## Transition to `idle` (release-and-recheck)

The simple transition `running → idle` is insufficient. The release of the run follows:

1. check that there is no more immediate work (mailbox, available resumptions);
2. write `runtime.status = idle` and remove `run_id`, in one write guarded by `expected_revision`;
3. re-read the mailbox and the runtime;
4. if the runtime is still `idle` and activatable work is present: retry `idle → running` with a new `run_id` (`expected_revision` guard) and continue;
5. otherwise: stay `idle`.

The second test takes place after the publication of `idle`, which closes the lost-wake-up window against a concurrent `mailbox.send`. If another activation has already won, nothing more is done.

## Suspend

When an applicable `suspend` is met at a safe point:

- `runtime.status = suspended`
- `runtime.run_id = null`

The run ends. The messages stay in the mailbox.

## Delete

When an applicable `delete` is met at a safe point:

- the run stops any new cognition;
- the deletion is then applied according to the contract of `continuity.delete`, without `ack`: the run ends.

## Run error

In case of a technical failure before the termination of the `current_message`:

- if a mailbox entry is still `claimed` by this run, it must be `release`d when this can be done without compromising the effects already produced;
- no Intention already emitted must be automatically re-emitted;
- a `NotFound` is read as "the continuity was deleted" only if the header of the continuity really no longer exists. A provider or a capacity not found is a failure of the processing (traced, claim released), not a deletion.

## Safe points

The main safe points are:

- after an LLM inference,
- after the resolution of a wait,
- after the execution of a `pilot_output`,
- after the complete processing of a message.

They are never located:

- in the middle of an inference,
- in the middle of a Capacity,
- in the middle of a Matter mutation.

## Concurrency

The mutations of the Matters of the continuity are protected by short critical-section locks, per `continuity_id`, and by `expected_revision`. No lock is held during an LLM inference, an external invoke or a wait. During an inference, the **inference window** of the continuity closes the write door of the journal to the other writers (it does not lock the state Matters). The complete model is in `agentic_core_concurrency_model`.

## Traceability

The significant events produced by `pilot.activate` are written in the journal. The `kind` of each line written is its characterization, set by the tool that writes it and that knows where it comes from: it serves to include it in the continuity or exclude it from it (see `schema_matter_continuity`). The internal tracking facts (wait group, run resumption, intention change) are written in kind `event`. This includes in particular:

- Intentions emitted,
- Responses received,
- intention changes,
- messages produced,
- projection requests and results,
- creation and satisfaction of wait groups,
- `validation_error`,
- `execution_error`,
- significant controls.

## What `pilot.activate` does not do

- It carries no domain-specific cognition.
- It contains no hard-coded business prompt.
- It does not decide which Capacities are semantically relevant.
- It creates no mandatory reflection or evaluation system.
- It does not maintain a memory parallel to `continuity`.
- It does not create several concurrent cognitive chains in the same continuity.
- It never automatically replays an Intention already emitted.

## Central invariant

`pilot.activate` guarantees:

```
a continuity
  → a single cognitive owner at a time
  → a normal message processed at a time
  → as many parallel transformations as necessary
  → as many parallel continuities as necessary
  → a single durable history in the journal
```

## Implementation constraint

The continuity is managed in a Matter (the journal) for persistence, and duplicated in an in-memory object of the wrapper for performance. It is not a question of formalization but of implementation; see `agentic_core_concurrency_model`. For this Capacity: every read of the continuity (controls, current message, history, `_used_intention_ids`) goes through the in-memory object; every write is an append to the journal followed by the update of the object. A run keeps no private copy: after a loss or a resumption, the object is rebuilt from the journal.

## Exactly one response per message, even if the wrapper dies between the writing of the response and the acknowledgement (November 14)

**The gap** (S44). A run writes the last response of a message, then acknowledges that message. Killed between the two, it leaves the message claimed while its response is in the journal; the resumption presented it again to the pilot ("the previous run was interrupted, decide from the real state") and nothing in the journal said that it already had its response (the messages of the pilot were not correlated with the message processed): a second response.

**The rule** (decision of the architect: option A).
- Each `pilot` line (`message_out`) carries `correlation.causation_id` = the identifier of the message it processes.
- The **last message of a step that ends the chain** (the output has no intention, no wait group, and no group is waiting) also carries `ends_chain: true` in its content. It is the proof that the processing is complete.
- At the resumption (`_recover`), if the journal contains, after the message in progress, a `pilot` line with its `causation_id` and `ends_chain`, the processing was complete: the resumption **acknowledges** the message (without replaying it), empties the current message and writes `run_recovered` with `already_answered: true`.
- **Chain cut before its last message**: no proof, the message is presented again. Accepted risk: a possible **duplicate**, never a lost ending (a duplicate is seen and corrected; a lost ending is not seen).
