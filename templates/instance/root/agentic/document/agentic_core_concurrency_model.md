# Concurrency and consistency model of `agentic_core`

## Intention

State once and for all what Brique actually guarantees, and what `agentic_core` builds on top of it, so that the contracts of the Capacities never claim more than the guarantees of the engine.

## Principle

Brique provides no multi-Matter transaction.

Every agentic Capacity that modifies several Matters is a **distributed operation made of several local commits**. Each local commit is atomic on a single Matter. Global consistency is obtained by:

- optimistic concurrency with `expected_revision`;
- a deterministic write order;
- idempotent steps;
- reconstruction after an interruption.

The vocabulary of the contracts therefore distinguishes:

| Wording | Scope |
|---|---|
| atomic mutation | a single `matter.write` of a single Matter, guarded by `expected_revision` |
| recoverable multi-Matter operation | several local commits, without rollback, reconstructible |

## Boundary of responsibilities

| Level | Role |
|---|---|
| engine | local atomicity of one Matter and the revision guard |
| wrapper library | what is generic to every wrapper: retry of a busy callee, critical sections, Matter client, typed errors, settings, diagnostic (see "What the wrapper library provides") |
| `agentic_core` | the commit protocols of the continuity, recovery |
| Matter | persistent truth |

## What the wrapper library provides

Several mechanisms that `agentic_core` built first, and that turned out to be generic, now live once in the wrapper library (`wrapper/`, Python, Go and C++, same behavior, shared vectors in `wrapper/vectors/`; see `Wrapper.md` §14.1). `agentic_core` uses them and no longer carries its own copy:

| Mechanism | Where in `agentic_core` |
|---|---|
| retry of a call refused "busy", with exponential backoff and jitter | none: `emit_and_wait` of the library does it; `call_capacity_response(busy_attempts=)` passes the number of attempts of a wake |
| keyed critical sections whose wait gives the execution slot back | `substrate/locks.py` (a table of `KeyedLocks`) |
| read, recompute, write guarded by `expected_revision`, with a random pause after a lost race | `substrate/cas.py` (`retry_on_conflict` of the library) |
| Matter client: inline or lease read, guarded write, journal range read and append | `BriquePort.matter_*` delegate to `MatterClient` |
| typed errors by reason (`busy`, `revision_mismatch`, `line_count_mismatch`...) | `errors.from_engine_error` asks the library for the kind |
| settings registry (`BRIQUE_<NAME>`), log of the settings not at their default | `substrate/settings.py` declares `BRIQUE_AGENTIC_<NAME>` in it |
| `running` heartbeat (`runtime.keepalive()`) | `substrate/keepalive.py` re-exports it |
| dump of every task on `SIGUSR1`, `scheduler.stats()` | `diagnostic.py` adds the inference windows |

What stays in `agentic_core`: the commit protocols, the in-memory continuity, the inference window, run liveness, the idempotence fingerprint of messages, and the release of the run (`#settle`).

## A single wrapper process

`agentic_core` assumes **a single wrapper process per instance**. The locks, the liveness of runs (`liveness`), the in-flight response tasks and the in-memory continuities are local to the process. Two processes on the same Matters would not see each other: a run alive in one would be taken for an orphan by the other (`pilot.recover`, a wake-up), and two runs could process the same continuity. The `expected_revision` and `expected_line_count` guards prevent corruption of the Matters, not this double processing. It is a limit that has been set, not a defect to fix as long as there is only one wrapper.

## The errors of agentic keep their type through the engine

`agentic_core` calls its own capacities through the engine (`DIRECT_CALLS` is false). An error that a capacity raises (`Refused`, `ConsistencyError`, `ConcurrencyError`, `InvalidRequest`, `IdentityConflict`) leaves for the engine with its `code` and its `reason`. The caller finds them again: the error is rebuilt identically from its `reason`. Without this, an `except Refused` would catch nothing in a real run (everything would arrive as a generic `EngineError`), while it catches everything in a test on a fake engine that raises the raw classes. The errors that the engine raises itself, with its own `reason`, remain `EngineError`.

## Concurrency model

- Mutations of the Matters of a continuity are serialized by **short critical-section locks**, indexed by `continuity_id` within one wrapper process. A lock is held only around "read, recompute, write with a guard". It is never kept during an LLM inference, an external invoke or a wait. It is never the only guarantee. The inference window (next section) is not a lock of this kind: it concerns only the write door of the journal, and the state Matters (`mailbox`, `runtime`) remain writable, each under its own guard.
- The uniqueness of the cognitive run is guaranteed durably by `runtime.status`, `run_id` and `expected_revision`, not by a lock held for the whole life of the run.
- Every write of a mutable Matter uses `expected_revision`, read again just before the write. The same `expected_revision` guard applies to `structure.patch` for a Structure. Created Matters therefore carry a tracked `brique.revision`.
- The journal of a continuity is a Matter in JSON Lines, append-only: writing it is a `matter.append` guarded by `expected_line_count` (not a rewrite guarded by `expected_revision`). The guard on the number of lines acts as the concurrency guard: two appends that read the same number of lines cannot both succeed.
- A revision conflict causes: re-read, recompute the content, retry, with a bounded number of attempts (`BRIQUE_CAS_ATTEMPTS`) and a short random pause that grows with the attempt (`BRIQUE_CAS_PAUSE_BASE_MS`, `BRIQUE_CAS_PAUSE_CAP_MS`), so that writers that lost the same round do not come back together. Adding a value that is already present or removing a value that is absent changes nothing: the operations are derived from what has just been read. Beyond the attempts, the failure is explicit (concurrency failure).
- The lock applies at least to the critical sections of `mailbox.send`, `mailbox.read`, `mailbox.ack`, `mailbox.release`, to the ownership transitions of `pilot.activate`, and to `continuity.create` / `continuity.delete`.
- Locks held simultaneously are acquired in the deterministic order of the `continuity_id`s (the library sorts the keys). The lock of a parent is never held around a recursive call on a child: the state is revalidated on return.
- `revision` is opaque concurrency metadata (a timestamp in nanoseconds). It is never an application sequence.
- `message.seq` is the line number in the journal: the number of lines already committed plus one, written with `expected_line_count` equal to that number of lines.

Example of two concurrent `mailbox.send`: A and B read a journal of 8 lines. A appends `seq = 9` with `expected_line_count = 8` and succeeds. B appends `seq = 9` with `expected_line_count = 8` and gets a conflict; it re-reads the tail, sees 9 lines, produces `seq = 10` and appends with `expected_line_count = 9`.

## Content of a Matter

An agentic Matter is an element (descriptor) that carries its content in its **substance** (`data`), not in its descriptor.

| Part | Role |
|---|---|
| `brique` | `kind: matter`, `substance_mode: brique`, `substance_type: text`, `revision`; for the journal, `substance_framing: lines` |
| `objective` | name, type (`continuity`, `journal`, `mailbox`, `runtime`, `pilot_definition`) and description |
| `functional` | what the element does: `format: json` (or `jsonl` for the journal) and `schema_ref` to the data schema (for the journal: the schema of **one line**) |
| substance (`data`, file `<matter_id>.json`) | the business content: a JSON document conforming to the data schema or, for the journal, one message per line |

Two schemas per type of Matter: the element schema (`continuity`, `journal`, `mailbox`, `runtime`, `pilot_definition`) describes the descriptor, the data schema (`continuity_data`, `message`, `mailbox_data`, `runtime_data`, `pilot_definition_data`) describes the substance.

A continuity is carried by a **header** (`continuity_<id>`, a small JSON document that rarely changes) and a **journal** (`journal_<id>`, append-only JSON Lines) to which the header points through `journal_ref`. `mailbox` and `runtime` remain distinct state Matters: the journal says what was true, they say what is true now and who owns what.

The `semantic_patch` of the engine never reaches the substance. For document Matters (header, `mailbox`, `runtime`), `agentic_core` derives its operations (`add`, `remove`, `set`, `delete`) from the content it has just read, applies them locally and writes the complete document in a single `matter.write(data, expected_revision)`. The atomicity is that of a write on one Matter, unchanged. For the journal there is no rewrite: a `matter.append` adds a block at the tail.

## Writing a Matter

```
read the Matter (read_mode = functional|data|brique)
→ get revision and parse the content (data)
→ compute the new content
→ validate the content against the data schema
→ matter.write(data = complete content, expected_revision = revision)
→ on conflict: re-read, recompute, retry (bounded)
→ otherwise explicit concurrency failure
```

For the journal, the sequence is:

```
read the current number of lines (matter.stat, or the tail read by matter.read in lines)
→ compute the line or lines to append (seq = lines + 1, then the following ones)
→ validate each line against the line schema
→ matter.append(data = lines terminated by LF, expected_line_count = lines)
→ on conflict: re-read the tail, recompute, retry (bounded)
→ otherwise explicit concurrency failure
```

A single `matter.append` commits a contiguous block atomically: several related lines can therefore enter together, or not at all.

The engine applies no schema. `agentic_core` therefore validates every content it writes against the data schema referenced by `functional.schema_ref` of the Matter.

`matter.read` serves the substance inline up to 256 KiB; beyond that it provides a single-use HTTP lease, which the Matter client of the library fetches (and reads again if the Matter was written in the meantime). The partial read of the journal by line range avoids reaching that threshold to read its tail.

## Size of intentions

No intention exceeds the frame limit of the WebSocket listener of the engine (4 MiB by default, adjustable only at the root). A voluminous piece of data that is only an intermediate product travels as transit (see "Persistent and transient"): the runtime of the wrapper makes it travel by value, `@object` or `@file` depending on the address, and deletes it after reading.

For the journal there is no size limit imposed by `agentic`: a message, a tool response or an LLM response is recorded as it is. There is no 64 KiB threshold and no overflow Matter. This is an implementation detail: a `matter.append` accepts 256 KiB inline, and a larger append goes through the HTTP lease of the engine (`http_data`), like `matter.write`.

The frame limits are performance settings, set by hand: the listener on the engine side (4 MiB, root), the transport of the Python wrappers (1 MiB, in the generic runtime). No auto-configuration is expected, since no healthy flow comes close to these limits: large working contents travel as transit (`@file`), not in an intention. An overrun closes the connection of the wrapper, which no longer answers until it is restarted; it is the sign of a content that should have gone through transit.

## Persistent and transient

What must exist in a Matter and what may circulate without persisting is decided by a single criterion.

| Nature of the data | Form | Examples |
|---|---|---|
| A **durable state** that already exists, or that must survive the call or be consulted later | a Matter, addressed by identifier and read explicitly | the journal, the header, `mailbox`, `runtime` |
| An **intermediate product** made by one step to be consumed by the next | **transit**: value, `@object` or `@file`, chosen by the runtime depending on the address, then deleted after reading | the prompt, the LLM request (`request`), the view returned by `continuity.project`, the context built by the pilot |

Consequences:

- A Capacity that **modifies a Matter** (`mailbox.*`, `runtime` transitions, `continuity.create` and `delete`) takes the identifier of the Matter as input and reads it itself: its `expected_revision` or `expected_line_count` guard must bear on what it has read, not on a copy supplied by the caller.
- A Capacity that **transforms without mutating** takes the value as input, in transit, when that value is an intermediate product. When its input is an already durable state, it addresses it and reads it: `continuity.project` reads the journal itself, now by line range.
- A transit is never persisted in the journal or in a Matter. The journal keeps the fact (the model's response, the inference step), not the assembled request that produced it. The prompt of a Capacity pilot step (`prompt` and `completion` of `pilot.infer`) is, however, a durable fact: memoization and recovery rely on it.
- The caller goes through the address-oriented call of the wrapper runtime (`calls.call`), which chooses the form of the parameter. The callee reads its parameters with `await params.value(name)`, which resolves a transit reference; a plain `params.get(name)` would return the unresolved reference. The contract of the Capacity does not change: it declares a name and a schema, not the way the parameter arrives.
- Calls between Capacities of the same wrapper accept `direct`. `direct = false` goes through the engine and stays **traced**; `direct = true` is a function call, with no engine trace, valid only for a Capacity bound in the same wrapper. It is a setting, not an architectural decision: `false` during tests, `true` once the tests are green and performance is wanted. The value is set in a single place (`BriquePort`) so that it can be changed without going through the code.

## Long capacities: the `running` acknowledgement

A capacity that may last (waiting for a run, querying a model, walking a lineage) does not leave its caller in silence: it sends `running` from the start, then a heartbeat every 10 seconds (`BRIQUE_HEARTBEAT_MS`), before its single final response. The runtime of the wrapper handles `running` natively: the caller sees only the final response, and its delay is an **inactivity budget** that each `running` renews. A caller can therefore no longer time out on a live capacity, and only a truly silent capacity times out. The heartbeat is `async with port.keepalive():`, that is the `runtime.keepalive()` of the library; it is never automatic, and the capacity asks for it only around work that can last.

| Capacity | Acknowledgement |
|---|---|
| `pilot.activate` | yes, except a wake-up (`wake`), which answers at once |
| `pilot.infer` | yes |
| `continuity.succeed` | yes |
| `continuity.delete` | yes |
| `infer.*` (model providers) | yes |
| `mailbox.send` | only when it waits for the inference window (see "Inference window") |
| `mailbox.read`, `mailbox.ack`, `mailbox.release`, `continuity.create`, `continuity.project` | no: short |

The acknowledgement is not declared in the descriptor of a capacity: it is the hosted code that decides.

## Bounded journal

The journal of a continuity is not infinite: compaction by tokens (document `continuity_succession`) makes work continue in a new continuity, and the old one becomes an archive. The journal Matter therefore stays bounded in size by the window of the model, as long as compaction does its job.

Since the journal is appended at the tail, the size of the journal no longer affects the cost of an append: it is proportional to the line appended. Only the complete read (projection, replay fallback) grows with the journal, and it is limited to a range of lines when it does not need everything.

## The in-memory continuity (implementation constraint)

The continuity (the thread the LLM reads) is managed in a **Matter** (the journal), which makes it persistent, and **duplicated in an object of the wrapper** for performance. The journal is the truth; the object is a derived copy. This duplication belongs to the implementation and not to the formalization: it has no element or contract of its own, and it is recalled as a note in each Capacity that reads or writes the continuity.

- **Write**: an append to the journal (`matter.append`, guarded by `expected_line_count`) is followed, under the critical-section lock of the continuity, by the update of the object with the same lines. If the append is refused, the object is not touched: the tail of the journal is re-read, the object is completed, then recomputed and retried.
- **Read**: a projection reads the object, without re-reading the journal. The line sizes are computed there once.
- **Views maintained at write time**: the LLM thread and the set of identifiers of already emitted intentions are updated by each applied line, not recomputed at each read. The messages of the object are shared and read-only: a projection filters and applies the limit and the budget without copying, then copies only the retained messages.
- **Recovery and loss**: at wrapper startup, or if the object is absent or evicted, it is rebuilt from the journal (read by line range). It does not need to survive the process.
- **Eviction**: the wrapper keeps at most 512 continuities in memory; beyond that, the least recently used is evicted, archived ones included, and is rebuilt on demand. A concurrent append to an evicted object remains safe: the `expected_line_count` guard refuses the append of a stale object, which is completed from the tail of the journal.
- **What it contains**: the lines retained for the LLM thread (see `schema_matter_continuity`, "Characterization of lines"). The tracking facts stay in the journal.
- Like the locks and the liveness, it is infrastructure local to the wrapper and not business state: a Capacity remains without state of its own, it reads a reconstructible object.

### Reads of the state Matters

`mailbox` and `runtime` remain states read in the engine, never cached: their `expected_revision` guards bear on what has just been read. On the other hand, several independent reads at one decision point go out in **a single request** (`matter.read_batch`), in the desired order: the header, `mailbox` and `runtime` at the entry of `mailbox.send` and of `pilot.activate`, `runtime` and `mailbox` for `mailbox.read`, `mailbox` then `runtime` in the release-and-recheck of `#settle` (this order is the one that guarantees that no wake-up is lost). When what has just been read shows that there is nothing to claim, `mailbox.read` answers empty without writing.

## A wait does not keep the execution slot of the wrapper

The wrapper executes at most **sixteen** jobs at a time (`BRIQUE_MAX_CONCURRENCY`): these are its slots. The library gives the slot back while a job waits for the **response to a call** to the engine; it does not give it back during any other wait. Now, a job that waits for a lock or for the inference window, and that keeps its slot, prevents the holder of what it waits for from taking back its own slot after its next call to the engine. From seventeen simultaneous waits on, no slot is free any more: the wrapper freezes for good (observed: twenty simultaneous calls on a single lock).

Rule: **every wait for a critical-section lock or for the inference window gives the slot back** (`port.yielding()`, which is `scheduler.yielding()` of the library). It is `critical(..., yielding=port.yielding)` for the locks, and the `yielding` parameter of `inference_window.hold` and of the journal door. A wait that does not take place (free lock, free window) gives nothing back. Since the keyed locks live in the library (`KeyedLocks`), the mechanism is the same for every wrapper.

## Inference window

**Rule.** An inference is a contiguous unit of the journal: from the reading of its input to the writing of its decision line (for a plan step, from the `prompt` to the `completion`), no other line is written in the continuity and no other inference runs for it. Without this, the response of a tool could slip in between what the model read and what it decided, and the journal, read back as a conversation, would say that the decision saw what it did not see. The reasoning and the prohibitions are in `user_message_in_flight`.

**What it is.** An in-memory state of the wrapper, per continuity, served FIFO in order of arrival. Since the wrapper is unique, the one that holds it and those that wait for it disappear together: there is nothing to repair after a stop. The journal keeps in any case the trace of an interrupted inference (a `prompt` without `completion`, which `pilot.infer` knows how to resume).

**Who holds it.** The loop of the Matter pilot for one step (reading the projection, inference, writing the decision), `pilot.infer` for one step (from the `prompt` to the `completion`), and a user request (see below). It is always released at the end of the step, including on error.

**The door.** Every write to the journal (`continuity_store.append`) waits for the window to be free, except that of its holder. The wait precedes taking the critical-section lock: a writer must never wait for the window while holding that lock, since the holder needs it to write its decision. `mailbox.send` therefore waits for the window at the append step (step 4 of its commit point), after the reservation of the entry, and sends a `running` acknowledgement during the wait; its output contract does not change (`seq` is known on return).

**Who writes while it is closed.** Its holder only. When it is *reserved* for a user request waiting to be absorbed, it closes the door to outside writers but **not to the loop of the run**: the latter must keep writing (traces, launching the effects), otherwise it would wait for the reservation to expire. **This also holds for the loop already in the queue** (November 11): the loop may have asked for the window *before* the reservation existed and find itself behind an outside writer; the queue then does not stop on the reservation, the loop goes first, and the outside writers wait until it has absorbed the request or until the reservation expires. Without this, `mailbox.send` (outside writer) and `pilot.activate` (the loop) waited for each other until the reservation expired (30 s). The identity of the loop is not transmitted: a background task launched by it (the delivery of the response of an awaited tool) is an outside writer, and a capacity called by the engine is one too. A `user` message that arrives while an inference is running waits its turn FIFO, with the other writers.

**What it does not cover.** Launching the effects: launching an action takes only a few milliseconds (awaited intentions run in the background) and a launched action is not recalled.

**Opening by a user request.** A message of kind `user` addressed to a continuity with a Matter pilot whose run is `running` or `waiting` opens the window in the very operation that writes it, before the append, then hands it to the loop that absorbs it. It cannot be opened later by the loop: the reservation keeps only the fingerprint of the message, and a stop between the two would lose the request. If the loop does not take it (suspension, deletion, error), it expires after a few tens of seconds, and at the suspension and deletion of the continuity; the request, already in the journal, becomes a normal message again, processed at the end of the chain.

**No refusal.** The engine does not distinguish a human from their parent: "no message is addressed to a Capacity pilot" is a convention of use, which `mailbox.send` does not check.

## Journal and working index

- The journal (`journal_<id>`) is the durable journal of accepted messages. It is immutable and append-only.
- `mailbox.entries` represents only the consumption state that is still active.
- `mailbox.ack` removes the entry from the mailbox and never modifies the journal.

Idempotence of `mailbox.send`, read on the state of the message and of the entry:

| Message in the journal | Mailbox entry | Interpretation |
|---|---|---|
| absent | absent | never accepted, new message |
| absent | present | send interrupted before the commit: pre-commit reservation, the retry takes it over |
| present | present | accepted message still active, or post-commit routing to finish |
| present | absent | message accepted then consumed, reinsertion forbidden |

These four lines hold for the **same fingerprint**. The same `message_id` with another fingerprint is an identity conflict in the three cases where the message already exists: reserved only (the fingerprint is in the reservation), accepted and still active, or consumed (the fingerprint is read back from the durable message).

## Commit point of `mailbox.send`

```
1. validation
2. idempotence decision
3. reservation of the mailbox entry (pending)
4. append to the journal               ← SEMANTIC COMMIT (matter.append, expected_line_count)
5. finalization of the routing (normal, control, wait_group capture)
6. possible wake / activation (non-blocking)
```

Before step 4, the operation can be abandoned or cleaned up. After it, the message is durably accepted: a crash never leads to appending it again, and all routing steps are idempotent and repairable from the durable message and the runtime state.

The reservation is a `pending` entry, with no new business status. An entry whose `message_id` matches no durable message is never eligible for a claim (`mailbox.read` ignores it). The identity of a message is its `message_id` **and the fingerprint of its content** (`message_fingerprint`: a SHA-256 of `from`, `kind`, `content` and `correlation`, in canonical JSON with ordered keys; not `seq`, nor `to`, nor `created_at`, which are assigned or optional). The pre-commit reservation carries the fingerprint. The same `message_id` with the same fingerprint is an idempotent retry; with another, it is an **identity conflict**, explicitly refused. Without this, an identifier collision, a client bug or a malicious actor would make it look as if a different operation had already been accepted: the first message would win silently.

## Operations on a single Matter

- `mailbox.read`: selection and claim in one mailbox write under `expected_revision`.
- `mailbox.ack`: removal in one mailbox write under `expected_revision`.
- `mailbox.release`: return to `pending` in one mailbox write under `expected_revision`.
- Acquisition of the run: `idle` to `running` with `run_id` in one runtime write under `expected_revision`. Of two concurrent activations, one wins; the other re-reads `running` and becomes a no-op.

These operations can legitimately be called atomic, for that single Matter.

## Idempotent multi-step protocols

`continuity.create`, in this order: `journal_<id>`, `continuity_<id>` (the header, which points to the journal: it is written once the journal is present, never the reverse), `mailbox_<id>`, `runtime_<id>`, parent relation, Structure relation.

`continuity.delete`, in this order: children (cascade or detachment), parent relation, Structure references, `runtime_<id>`, `mailbox_<id>`, `journal_<id>`, `continuity_<id>` last: the header carries `journal_ref`, so it remains the existence marker as long as the journal exists.

For each step:

- already done in the expected state: idempotent success;
- present but contradictory: consistency failure;
- on deletion, consistent absence: idempotent success.

Running the complete operation again repairs the missing steps. No rollback is promised.

## A claimed message that the dead run had not started

A run takes a message in two writes: `mailbox.read` **claims** it (the entry becomes `claimed`), then the run **records** that it is processing it (`runtime.current_message_id`). A brutal stop between the two leaves a claim with no processing begun: no inference took place, so no effect.

When a new run takes over a continuity whose run is dead, it adopts the claims of the old one. Rule for this case: **if no current message is recorded, a claim of priority `normal` has not been started; it is put back to waiting (`pending`), and the message is processed as if it had never been taken.** Without this, the entry would stay claimed by a run that no longer exists, nobody would take it up, and the continuity would never return to rest. Claims of priority `expected` (responses of a wait group) are adopted as before: their group consumes them. A recorded current message keeps the existing behavior (processing resumes, nothing that was emitted is replayed).

## Release of the run (`#settle`)

The simple transition `running` to `idle` is insufficient. `#settle` does a release-and-recheck:

```
1. check that there is no more immediate work
2. write runtime.status = idle and remove run_id (expected_revision)
3. re-read mailbox and runtime
4. if the runtime is still idle and activatable work is present:
     try idle → running with a new run_id (expected_revision), and continue
5. otherwise: stay idle
```

The second test comes after the publication of `idle`, which closes the lost-wake-up window against a concurrent `mailbox.send`. If another `pilot.activate` has won, nothing more is done.

## Controls applied immediately

- Deferred control: `pending`, claim by `pilot.activate`, application, `ack`.
- Control applied immediately by `mailbox.send`: application, then explicit removal of its entry by the send operation itself (no run owns it, hence no `mailbox.ack`). The durable message is kept.
- `delete`: terminal, the mailbox disappears with the continuity.

## Expected messages

- The entry of an `expected` message is reserved `pending`, then claimed by `runtime.run_id` at the start of the routing, after the commit.
- If the wait group is no longer active or no longer matches at that moment, the message falls back to `normal` routing: the priority of the entry becomes `normal` in the same guarded write, and the entry stays `pending`.

## Awaited capacity responses

`pilot.activate` transforms the Response of an awaited invoke into a canonical Message before `mailbox.send`: `id` derived deterministically from `intention.id` (repeated delivery = idempotent retry), `kind = capacity_response`, `content.response`, `correlation.intention_id = intention.id`. The raw Response is never sent to `mailbox.send`.

## Non-blocking activation

`mailbox.send` invokes `pilot.activate` without waiting for its response (`await_response` false). A cognitive run can last a long time; the caller of `mailbox.send` must never wait for it.

## A "busy" refusal is transient: it is asked again, and a wake-up is never silently lost

**Finding** (bursts of 200 and 300 simultaneous sends, November 11, 2026; the measurements are summarized in `performance_settings.md`). Two stages refuse work instead of making it wait: the Matter loop of the engine (beyond its simultaneous treatments, it answers `unavailable` at once with the reason "loop busy") and the queue of pending jobs of a wrapper (beyond its limit, it answers `refused` with the reason "busy"). In both cases the refusal takes place **before any execution**: nothing was done, asking again is harmless, even for a call that is not idempotent. And the wrapper answers a refusal **only if the call waits for a Response**: a wake-up sent without waiting, and refused, disappears without a signal. This is how continuities stayed at rest with a message in their mailbox.

**Rules.**
- **A "busy" refusal is an error of its own** (`BusyError`, code `unavailable`, reason `busy`), distinct from `Refused` (a refusal of substance, which `mailbox.send` and the delivery of responses interpret as "already done"). It is recognized for both forms of refusal (reasons `busy` and `loop busy`); the classification is the one of the library (`wrapper.errors`, kind `busy`).
- **The runtime of the wrapper asks again for a call refused "busy"**: at most `BRIQUE_BUSY_ATTEMPTS` attempts (8), with exponential backoff (`BRIQUE_BUSY_BASE_MS`, 20 ms, doubled at each attempt, capped at `BRIQUE_BUSY_CAP_MS`, 1 s) and randomness (from half to one and a half times) so that callers refused together do not come back together. The pause does not hold an execution slot. This applies to the calls that raise the error (`call_capacity`, operations on the Matters) and to those that return the entire Response (`call_capacity_response`, used for the awaited tools of the pilots). A call that timed out (`TimeoutError`) is never asked again: it is not known whether it was executed. An error of another nature is never asked again. `agentic_core` has no retry loop of its own.
- **A wake-up insists longer** than an interactive call (`WAKE_ATTEMPTS`, 40 attempts, of the order of 35 s, passed to the library as `busy_attempts`): its message is already accepted and the request costs only a waiting task. Measured: with 8 attempts, 7 wake-ups out of 300 simultaneous ones were abandoned during a saturation of 10 s.
- **If the refusal persists after the attempts**, the error goes up to the caller, which fails like for any other engine error; it is not masked.
- **A wake-up no longer waits without a response.** `mailbox.send` requests the activation through the port (`wake`): the call waits for a Response, in a task of its own (the run of a pilot can last); a "busy" refusal is asked again as above. If the wake-up is refused after all the attempts, the continuity receives in its journal an error line `wake_refused` (reason and number of attempts): the message is in the mailbox, and what could not wake it can be seen.
- **What this does not settle**: a wake-up lost because the process stopped (nothing restarts an orphan run by itself: see "A claimed message that the dead run had not started"). A periodic restart of the continuities that have an unprocessed entry is an evolution to measure before deciding it.
