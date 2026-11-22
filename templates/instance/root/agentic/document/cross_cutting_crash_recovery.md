# Cross-cutting document — Recovery after a crash

## Intention

Define how an agentic continuity finds a consistent state again after the brutal disappearance of a process, a wrapper or a run.

Recovery must never rely on a blind replay.

The fundamental principle is:

- the effects already materialized are facts,
- the `continuity` carries the durable history,
- the `runtime` carries the current operational state,
- the `mailbox` carries the consumptions still open.

Recovery must rebuild the execution from these facts without implicitly re-emitting what may already have produced an effect.

## Central principle

After a crash:

- never assume that an unconfirmed action did not take place;
- never automatically re-emit an Intention already sent;
- never arbitrarily recreate the cognitive state from a simple hypothesis.

Always prefer:

1. observe,
2. correlate,
3. rebuild,
4. then resume explicitly.

## Sources of truth

Recovery relies on three main sources: the journal (that the `continuity` header designates by `journal_ref`), `mailbox` and `runtime`.

### The journal of the `continuity`

Carries:

- the durable messages,
- the Intentions already emitted,
- the Responses already received,
- the errors,
- the intention changes,
- the `wait_group` events,
- the cognitive history.

### `mailbox`

Carries:

- the messages still `pending`,
- the messages still `claimed`,
- their previous owner.

### `runtime`

Carries:

- the `status`,
- the `run_id`,
- the `current_message_id`,
- the `active_wait_groups`.

No additional hidden state must be needed to understand what was in progress.

## Who triggers the recovery

Recovery is **explicit**. Nothing restarts a continuity by itself at wrapper restart: a `running` or `waiting` continuity whose run is dead resumes when a message arrives for it (the wake-up of `mailbox.send` detects the orphan), or when the architect calls `pilot.recover`. This capacity asks `continuity.stranded` which continuities cannot continue by themselves, and wakes them with `pilot.activate` (`wake`), without creating a message or replaying an intention. A restart therefore incurs no inference cost that nobody asked for.

## Detection of an orphan run

After a restart, a `runtime` can contain `status = running` or `status = waiting` with a `run_id` that no longer really exists.

This run is then considered **orphan**.

Recovery must first establish this situation before modifying anything.

## Reconstruction of the current message

If `runtime.current_message_id` is present:

- recovery checks that the message still exists in the journal;
- it also checks that a corresponding entry still exists in the `mailbox`.

If the entry is `claimed` by the old `run_id`, this claim now belongs to a vanished run. It cannot stay owner indefinitely.

Recovery must then determine whether the processing can continue from the existing durable state.

### Simple case: no effect produced yet

If the current message was claimed but no durable effect has yet been produced in its chain, recovery **does not release it**: the claim is transferred to the new run (see "Resumption of ownership") and the cognition resumes the same chain, with a recovery stimulus that says no intention was emitted. Releasing then re-reading a message that can be resumed directly would be one more write for no gain, and would put a message already started behind those that arrived during the crash. Release is reserved for a chain that cannot be resumed.

### Case: Intentions already emitted

If the journal shows that the run had already emitted one or more Intentions:

- these Intentions are never automatically re-emitted, even if no Response is visible yet;
- the external effect may have been produced before the crash.

Recovery must therefore examine what is known about each of these Intentions.

## Known state of an Intention

### Intention with a Response already present

If a Response corresponding to an Intention is already present in the journal:

- it is considered received;
- if it belongs to a `wait_group`, `received[]` can be rebuilt or verified from this Response;
- the condition of the `wait_group` can then be re-evaluated;
- if it is satisfied, the runtime can resume the cognition with `active_wait_result`.

### Intention still waiting and transport observable

If the infrastructure makes it possible to know that an Intention is still actually in progress:

- the system keeps the wait;
- the `wait_group` stays `waiting`;
- the runtime can be rebuilt in a state that allows continuing to wait.

No new emission is needed.

### Intention whose state is unknown

If no Response is present and it is impossible to determine whether the Intention:

- was executed,
- is still in progress,
- failed before execution,
- or produced an effect without a response,

then the state is **explicitly unknown**.

The system must not choose arbitrarily between success and failure. Nor must it automatically replay the Intention.

This uncertainty becomes an explicit recovery fact. For example:

```
execution_state = unresolved
```

A cognitive resumption can then be triggered with this information. The pilot can then decide:

- to check the state of the world,
- to launch a diagnostic Capacity,
- to produce a new explicit Intention,
- or to abandon this branch.

## Wait groups

The `active_wait_groups` of the runtime must be able to be rebuilt or verified from:

- their durable state present in `runtime`,
- the correlated Intentions in the journal,
- the Responses already present,
- the returns from continuities already received.

For each `wait_group`:

- `expected[]` describes what was to arrive;
- `received[]` can be verified against the durable messages;
- any durable result already present must be recognized;
- no result already received must be lost.

### `expected` claims

An `expected` message already arrived before the crash can be:

- persisted in the journal,
- claimed in `mailbox`,
- referenced in `received[]`.

After a crash:

- the new runtime must recognize that it already belongs to the `wait_group`;
- it must never go back into the normal flow of `mailbox.read`.

If necessary, the claim can be reassigned to the new owning run at the time of the recovery.

### `wait_group` `satisfied` before the crash

If a `wait_group` is marked `satisfied` but the crash occurs before or during its cognitive resumption:

- the group must not be considered lost;
- it remains eligible for resumption.

The new run can rebuild `active_wait_result` from the `message_id`s received. The cognition then resumes from this point.

### `wait_group` `completed`

A `completed` `wait_group` must not be recreated as an active wait.

Its history stays in the journal. If it no longer exists in `active_wait_groups`, it is considered finished.

## A wait that never ends

A wait (`wait_group`) does not last indefinitely. After `limits.wait_timeout_s` (3600 s by default), each response still awaited becomes a `timeout` error response, delivered like any response, and the cognition resumes. It is the same principle as the `unresolved` state: the uncertainty is stated, never turned into a success. This covers in particular a delegated continuity that does not answer, whose wait has no other bound.

## A message whose processing always fails

A message whose processing fails is counted (`attempts` on its entry). At the third attempt, the entry is set aside (`failed`): it is no longer taken up and no longer blocks the following messages. The resumption of a message set aside is a decision of the architect.

## Orphan states

### Orphan `running`

If `runtime.status = running` but no LLM or Capacity operation can really continue:

- the previous run is abandoned;
- a new `run_id` will have to be created.

Before this, the system rebuilds:

- the state of the `current_message`,
- the effects already carried out,
- the possible Intentions in flight,
- the `wait_groups` still active.

Then it chooses the correct operational state: `running`, `waiting` or `idle`.

### Orphan `waiting`

If `runtime.status = waiting` with a vanished `run_id`, the continuity is not simply put back to `idle`. It must first check its waits.

- If `wait_groups` are still really `waiting`: a new run can take over ownership of the `continuity` while keeping the `waiting` state.
- If a `wait_group` is already `satisfied`: the new run must go back to `running` and perform the cognitive resumption.
- If no valid wait exists any more: the state must be recomputed from the `current_message` and the `mailbox`.

## Resumption of ownership

An old orphan `run_id` never stays owner durably.

Recovery assigns a new `run_id`. The mailbox claims that legitimately belonged to the resumed processing can be transferred to this new run. This transfer must be explicit: a mailbox write guarded by `expected_revision`, atomic for this Matter.

A message must not be artificially `release`d then re-`read` if the processing can be resumed directly in a safe way.

## `current_message`

- **Recoverable cognitive chain**: `current_message_id` stays the same. The new run resumes the same causal chain.
- **Not recoverable, but no effect makes the resumption ambiguous**: the message can be `release`d and become `pending` again.
- **Effects have already taken place**: recovery must preserve this information before any new cognitive decision.

## Recovery `execution_result`

When the crash has left a partial execution, recovery builds a structured stimulus (`kind: recovery`) that says **what the journal knows**, and nothing more: the identifier of the resuming run, the message resumed, and for each intention emitted for this message its identifier, whether a response was awaited and whether a response is present. The lost intentions for which a response was awaited have first received a response of unknown state (`unresolved`): they therefore appear as answered. The stimulus never proposes to replay.

It describes in particular:

- the actions known as successful,
- the responses already received,
- the waits still open,
- the actions whose state is unknown,
- the known errors.

This stimulus is injected into a new recovery inference.

Like the other active stimuli, it is consumed once. The underlying facts remain durable in the journal.

## No implicit replay

The following are never automatically replayed after a crash:

- Brique Intentions already emitted,
- messages already sent,
- continuity creations already carried out,
- controls already applied,
- external effects already observed.

If the pilot decides that an action must be repeated:

- it produces a new explicit action,
- with a new identifier,
- and a new trace.

## Idempotence

The Capacities of the core must favor idempotence when possible. In particular, `mailbox.send` on a message already present creates no duplication.

But technical idempotence never replaces causal reconstruction. A business Capacity may have produced a non-idempotent effect.

Recovery must therefore never assume that a retry is safe simply because the agentic protocol restarted.

## Pending controls

The `pending` controls in the mailbox remain valid after a crash. After reconstruction, they must be processed with their usual priority.

For example, `delete`, `suspend`, `set_intention`, `set_pilot` can modify what the recovery must do next.

### `delete` pending

If a `delete` was already `pending` before the crash, after the minimal reconstruction needed for consistency, the deletion must be applied at the next available safe point.

A long cognition must not be uselessly resumed before processing this priority control.

### `suspend` pending

If `suspend` was `pending`, recovery must not launch a new normal cognitive chain before having applied it at the first available safe point.

## Normal mailbox

The `normal` messages that arrived during the crash stay `pending`.

- They are not lost.
- They are not processed before the resolution of the recovered `current_message` or of the priority controls.

## Interrupted multi-Matter operations

Brique offers no multi-Matter transaction: a crash can leave an operation between two local commits. Recovery reads it from the durable states, without replaying anything blindly.

### Interrupted `mailbox.send`

| Message in the journal | Mailbox entry | Reading |
|---|---|---|
| absent | absent | never accepted |
| absent | present | pre-commit reservation of a send interrupted before its commit; inert, never claimed; the retry of the same `message.id` takes it over |
| present | present | accepted message; the post-commit routing (classification, capture in a `wait_group`, control, wake) is finished or repaired idempotently |
| present | absent | accepted then consumed; never reinserted |

After the commit point (the append), the message is never appended again.

### Lost wake-up

Activatable work left in the mailbox while the runtime is `idle` is taken up by the release-and-recheck or by a new activation; the `idle → running` acquisition guarded by `expected_revision` guarantees that only one run starts.

### Interrupted `continuity.create` and `continuity.delete`

These are ordered idempotent protocols. A continuity whose Matters are incomplete (interrupted creation) or whose `continuity` remains without `mailbox` or `runtime` (interrupted deletion) is a detectable partial state. Running the complete operation again repairs the missing steps; a step already carried out in the expected state is an idempotent success, a contradictory state is a consistency failure.

## Transition to `idle`

Recovery puts a `continuity` back to `idle` only when it has established that there is:

- no `current_message` still owned,
- no cognitive resumption to perform,
- no `wait_group` requiring a wait,
- no priority control to apply.

The final transition to `idle` follows the same release-and-recheck as in a normal run: `idle` is published (write guarded by `expected_revision`), then the mailbox and the runtime are re-read, and the continuity is reactivated if activatable work has appeared.

## Traceability of the recovery

The significant events of the reconstruction can be written in the journal. For example:

- orphan run detected,
- `wait_group` rebuilt,
- external state `unresolved`,
- resumption with a new `run_id`,
- claim reassigned,
- message `release`d after recovery.

The goal is not to trace every technical detail. It is to make understandable the state changes that really influence the cognitive continuity.

## What recovery does not do

- It does not rewrite the journal.
- It does not delete the effects already produced.
- It does not assume that an absence of Response means an absence of effect.
- It does not automatically re-emit an Intention.
- It does not create a second parallel history.
- It does not turn an uncertainty into a success or an error without proof.
- It does not automatically restart the whole `current_message` from zero.

## Conceptual algorithm

At restart:

1. read `runtime`
2. detect a possible orphan `run_id`
3. read a possible `current_message_id`
4. check its mailbox claim
5. read the `active_wait_groups`
6. reconcile `expected[]` with the messages actually present
7. rebuild `received[]`
8. identify the Intentions already emitted without a known final state
9. distinguish:
   - responses present
   - operations still observable
   - unknown states
10. apply the priority controls recovered
11. choose the new operational state:
    - `running`
    - `waiting`
    - `idle`
    - `suspended`
12. assign a new `run_id` if a cognition must resume
13. transfer the necessary claims
14. resume without replaying the past effects

## Central invariant

Recovery after a crash guarantees:

```
persistence of the facts
  + reconstruction from the durable state
  + no implicit replay
  + explicit uncertainty when an effect cannot be determined
  → recoverable cognitive continuity without falsifying the real history of the system
```
