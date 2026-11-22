# BRIQUE — Intention of the agentic context

## 1. General intention

The intention of the Brique agentic context is to allow a cognition carried by an LLM to persist over time, to act on the Brique world, to wait for the effects of its actions, to delegate part of its work and to resume its cognition from what actually happened.

It is not about building a generic agent framework on top of Brique.

It is on the contrary about making an agentic behavior emerge from the fundamental primitives of Brique:

- The **Matter** carries state.
- The **Capacity** carries a transformation.
- The **Intention** addresses a transformation.
- The **Structure** organizes Matters.
- The **semantic field** allows discovering the world by its meaning.

The agentic context adds mainly to this model the possibility for an LLM cognition to have an explicit continuity in which it can evolve.

## 2. Not moving cognition into the engine

The LLM carries the cognition.

Brique does not seek to reproduce its reasoning as a mechanical and mandatory succession of phases such as reflection, planning, evaluation, selection, critique or compaction.

These operations can exist if the cognition itself decides they are useful. They can also be delegated to other specialized pilots. But they are not mandatory primitives of the agentic engine.

The Brique runtime carries something else:

- persistence,
- addressing,
- transformation,
- orchestration,
- waiting,
- concurrency,
- resumption,
- traceability.

The boundary sought is simple:

> The LLM decides.
> Brique materializes and executes the decisions.

## 3. The pilot

A pilot represents a specialized cognition.

This specialization is not implemented by a different Capacity for each type of pilot.

- A generic Capacity `pilot` carries the common protocol.
- A Matter `pilot_definition` describes the particular cognition to mobilize.

The definition of the pilot can express in particular:

- its prompt,
- the model used,
- its inference parameters,
- its default continuity projection,
- the Capacities it knows explicitly,
- its specific cognitive rules or preferences.

The generic Capacity `pilot`, for its part, carries the common mechanisms needed to:

- build the context sent to the LLM,
- launch an inference,
- interpret its structured output,
- emit Brique Intentions,
- wait for their results,
- resume the cognition,
- process incoming messages,
- evolve the operational state of the continuity.

This separation keeps a single agentic protocol while building as many specialized cognitions as needed.

## 4. The continuity

A continuity is the durable space in which a cognition evolves.

It is not only a conversation. It represents a persistent cognitive course oriented by an intention.

- Each continuity has an **initial intention**. This intention constitutes its founding direction. It is **immutable**.
- The continuity also has a **current intention**. It represents the local direction in which the cognition is currently working. It can evolve over time, but is always interpreted relative to the initial intention.

A continuity can therefore change focus, go down in precision, explore a sub-problem then return to a more general direction without losing the reason why it exists.

The continuity also keeps the definition of the pilot associated with it and the ordered set of significant messages produced during its existence.

## 5. A complete memory

The continuity keeps everything.

There is no fundamental policy of retention or deletion of certain cognitive events.

User messages, pilot outputs, Brique Intentions, Capacity responses, communications between continuities, controls, intention changes, projections, errors and significant operational events belong to the same durable course.

The question of what the LLM must see is therefore not handled at the level of persistence. It is handled at the level of the **projection**.

The continuity represents the complete truth of the course. The context sent to the LLM represents only a view adapted to the inference in progress.

## 6. The message as the unit of the course

Each significant interaction is materialized in the form of a message.

A message has:

- an identity,
- an order in the continuity,
- a provenance,
- a destination,
- a nature,
- a content,
- a date,
- correlation information.

Once added to the continuity, a message is **immutable**. A correction, a cancellation or a complement produces a new message correlated with the previous one.

The order of the messages in the continuity is canonical. Each message also receives a monotonic sequence number specific to the continuity. This sequence explicitly materializes the order in which the facts became true for this continuity.

The runtime therefore never rewrites history. It adds new facts.

## 7. The projection

Keeping everything does not mean that everything must be presented to the LLM at each inference.

Before an inference, the runtime builds a composite context. This context distinguishes several roles:

- the cognitive definition of the pilot,
- the initial intention,
- the current intention,
- the Capacities currently available,
- a relevant projection of the continuity,
- the message currently being processed,
- possibly, a wait result or an error that caused the resumption.

The continuity projection is **deterministic**. It can filter, select, order and structurally aggregate the messages. It must not perform cognitive reasoning or autonomous semantic synthesis. When a cognitive synthesis is necessary, it must be produced explicitly by a specialized cognition or Capacity.

A pilot definition provides a default projection. The pilot can also ask for a more focused dynamic projection. This dynamic projection by default complements the normal projection and can explicitly replace it. It is temporary and does not accumulate indefinitely.

The projection also respects a context budget. If the whole continuity cannot be presented to the model, Brique reduces the view deterministically. It does not arbitrarily truncate history.

## 8. The available Capacities

A pilot can know certain Capacities explicitly from its definition. These Capacities form its initial field of action. It is however not limited to this list.

The pilot can explore the vocabulary and the semantic field of Brique in order to discover other relevant Capacities.

This discovery remains cognitive. Brique creates no intermediate Capacity in charge of automatically translating an intention into a semantic search. The LLM can read the vocabulary, query the meaning, examine the contracts found and decide itself what is relevant.

The discovered Capacities can remain available in the current continuity. They do not automatically modify the stable definition of the pilot.

## 9. Structured output of the pilot

The runtime must not freely interpret text to guess what the pilot wants to do. The result of a pilot inference therefore has a structured form.

A single inference can decide several actions. It can in particular:

- modify the current intention,
- request a different projection,
- emit one or more Brique Intentions,
- create a wait condition,
- send one or more addressed messages.

The messages sent are not necessarily intended for a user. They can be addressed to another continuity or to any other recipient recognized by the protocol. The user conversational model is therefore only a particular case of addressed communication.

## 10. Order of effects

When a pilot output contains several decisions, their application follows a deterministic order:

1. An intention change is applied before the other actions.
2. A projection request constitutes a cognitive resumption point and does not mix with the emission of Intentions that already require an execution decision.
3. The possible wait condition is created, before the invocations it references.
4. The Intentions are then emitted.
5. The addressed messages are finally sent.

The order is not left implicitly to the interpretation of the text produced by the LLM.

## 11. Validation before effects

A pilot output is fully validated before the slightest effect is produced.

The validation bears on:

- its structure,
- the consistency between its different parts,
- the references it contains,
- when possible, the compatibility of the inputs supplied with the contracts of the targeted Capacities.

An invalid output produces no effect.

The pilot has **a single automatic correction attempt**. This correction receives the previous invalid output as well as the validation errors. It can correct the whole decision, but no effect is produced until the new output is valid.

If the correction remains invalid, the current processing fails explicitly.

Each Intention carries an explicit choice of the pilot, `await_response`. With `false`, it is fire-and-forget: the invocation is non-blocking, no result is awaited or tracked, and it can never appear in a wait. With `true`, its Response is awaited: it comes back as a `capacity_response` message correlated by the identifier of the Intention, through the mailbox, and can be referenced by a wait group. An Intention can also specify the family of its target (`target_type`, `user` by default) to reach a native Capacity.

## 12. No global transaction on the world

A valid output represents a consistent decision. It does not mean that all its effects constitute a global transaction that can be cancelled. Brique also offers no multi-Matter transaction: the operations that touch several Matters are recoverable operations, made of atomic local commits, optimistic concurrency, a deterministic order and idempotent steps.

Once a real effect has taken place, it remains a fact.

If several Intentions must be emitted and some succeed before another operation fails, the effects already produced are not cancelled. The execution stops at the first failure. The continuity records what succeeded and what failed. A new inference then resumes from this real state.

The actions already carried out are never replayed implicitly. If the pilot wishes to repeat them, it must produce new explicit Intentions.

## 13. The mailbox

Everything that arrives from outside a continuity goes through its mailbox. This includes in particular:

- user messages,
- messages coming from other continuities,
- the results returned by a delegated continuity,
- the controls,
- the external events addressed to this continuity.

The mailbox is not a second memory. The complete message is written in the continuity. The mailbox keeps only a reference to this message and the state needed for its consumption.

- The continuity represents the **memory**.
- The mailbox represents the **scheduling of incoming work**.

## 14. A single active cognition per continuity

A continuity never has two active cognitive chains simultaneously.

Parallelism exists between different continuities. It can also exist between the transformations launched by the same continuity. But the cognition of a continuity remains serialized.

A mailbox message is taken in charge. The whole cognitive chain it triggers is carried to its termination point. Only then can another normal message be processed.

This invariant guarantees a clear local causality.

## 15. The runtime

The runtime represents only the present operational state of a continuity. It constitutes neither its memory, nor its direction, nor its cognitive identity.

It indicates in particular whether the continuity is:

- **idle**: no cognitive run is active.
- **running**: the cognition can currently progress.
- **waiting**: a run exists but its progress depends on an awaited external event.
- **suspended**: the continuity still exists, can receive messages, but does not reactivate automatically.

The runtime also keeps the identity of the active run, the message currently being processed and the operational waits still open.

## 16. Activation and ownership

When a message arrives on an idle continuity, the mailbox can cause the activation of the pilot.

The activation takes possession of the runtime in a single write guarded by an expected revision. An identified run then becomes the sole cognitive owner of the continuity.

If several activations are requested almost simultaneously, only one can acquire this ownership. The others get a conflict, find that a run is already active and create no concurrent cognition.

The same guarantee is applied at the end of the run. The return to idle is published, then the mailbox and the runtime are re-read: if activatable work has appeared in the meantime, the run reactivates. This prevents a message from being lost between a last mailbox read and the release of the continuity.

## 17. Processing of a message

A normal message is *claimed* by the run. It stays owned during the whole cognitive chain it causes. This chain can contain several LLM inferences and several Brique transformations.

For example:

1. incoming message,
2. inference,
3. emission of a Capacity,
4. wait,
5. response,
6. new inference,
7. new action,
8. end of the processing.

The message is acknowledged only once this chain is finished.

In case of failure before this termination, the claim can be released so that the message becomes available again. A message already acknowledged can never be reinserted into the mailbox. A new functional processing requires a new message, possibly correlated with the previous one.

## 18. Waiting and wait groups

A pilot can launch several transformations or delegations and decide to wait for their results. This wait is represented by a **wait group**.

A wait group describes:

- the expected events,
- the events already received,
- the condition needed to resume,
- its current state.

A wait can concern heterogeneous elements: a Capacity response, a return from a continuity or any other correlatable event.

The main conditions provided are:

- **all**: resumes when all the expected events are resolved.
- **any**: resumes at the first result, whether it represents a success or an error.
- **first_success**: resumes at the first success. If all the events end in error, the wait ends nevertheless indicating that no success was obtained.
- **n_of_m**: resumes when the requested number of events is resolved.

## 19. Expected results and mailbox

External events keep entering through the mailbox even when they match an active wait.

When a message arrives and matches an active wait group without ambiguity, it is immediately recognized as an expected result and claimed by the active run. It does not join the normal FIFO flow of messages. Its entry is reserved before the commit of the message, then claimed after; if the wait is no longer active at that moment, the message falls back to normal.

If no active wait group matches, the message is processed as a normal message.

*Expected* is therefore not a durable parallel queue. It is the qualification of a message that actually matches an active wait at the time of its arrival.

## 20. Cognitive resumption

When a wait group becomes satisfied, the runtime makes the cognition executable again.

The result of the group is explicitly injected into the context of the resumption inference. The initial message that opened the cognitive chain remains the current message. The wait result represents only the new event that allows this chain to progress.

This active result is consumed once. It remains present in the complete history of the continuity, but is not reinjected as news in all the following inferences.

If several wait groups become satisfied, they are processed one by one in a deterministic order. The other waits can keep living while a cognitive resumption is processed.

## 21. Late results

Certain wait conditions allow resuming before all the operations engaged are finished. This is notably the case of `any`, `first_success` or `n_of_m`.

The remaining transformations are not cancelled automatically. When they produce their results, these are added to the continuity. They never reopen a wait group already finished and do not automatically cause a new cognitive resumption.

They simply become new facts available in the history of the continuity.

## 22. Communication between continuities

A continuity can communicate with another continuity. This communication always goes through the mailbox of the target continuity.

There is no implicit cognitive channel shared between two pilots. Each continuity therefore keeps its own causality and its own processing rhythm.

An inter-continuity communication becomes a message in the target continuity exactly like a user message or another external event.

## 23. Delegation

Delegation does not need to be a fundamental primitive of the agentic engine. It can be built from simpler primitives.

A delegation conceptually consists of:

1. creating a new continuity,
2. associating it with a derived intention,
3. assigning it a pilot,
4. establishing the parent/child relation,
5. sending it its first message through its mailbox.

The child continuity then works independently. It can perform several transformations, several inferences and several internal delegations.

When it produces a result intended for its parent, it simply sends it a message. If the parent explicitly waits for this result, the message can satisfy a wait group. Otherwise, it becomes a normal message of its mailbox.

## 24. Cognitive fractality

This ability to open a new continuity allows the cognition to go down in precision without indefinitely filling its main context.

A continuity can therefore meet a problem rich enough to create a new continuity focused on a derived intention. This sub-continuity carries out its own course. It keeps its own complete history. It can itself delegate.

The useful result can then go back up in a much more condensed form to the parent continuity.

The global cognition can thus emerge from a set of distributed transformations without requiring a gigantic central conversation.

## 25. Parent / child relation and placement

The parent/child relations represent an explicit cognitive relation between continuities. They must not be confused with their placement in a Brique Structure.

A Structure can freely organize references to continuities according to the needs of the project:

- by domain,
- by experiment,
- by working version,
- by research,
- or according to any other useful organization.

The Structure placement is external to the continuity. A continuity does not know its location in this organization.

The parent/child relation and the semantic placement are therefore two independent dimensions.

## 26. Controls

Certain operations are not cognitive acts: suspending a continuity, resuming it, changing its pilot, changing its current intention or deleting it are **controls**.

These controls also enter through the mailbox. They benefit from a priority higher than normal messages.

When a cognition is already in progress, a control never interrupts an inference or a transformation in the middle of its execution. It is applied at the next **safe point**. The safe points are consistent boundaries of the protocol, for example after an inference, after an awaited response or after the complete processing of a message.

A control received while the continuity is idle can be applied directly without launching an LLM cognition.

`resume` has a particular behavior in suspended, since no active run could come to process it: it can directly reactivate the continuity.

## 27. Suspension

Suspension destroys nothing.

A suspended continuity keeps existing. It keeps its intention, its pilot, its messages and the messages that arrive in its mailbox. These messages can accumulate. They simply cause no automatic activation.

`resume` makes the continuity available again for processing.

## 28. Deletion

A continuity does not need a `closed` state. It exists as long as its Matters exist.

Deletion removes the continuity, its mailbox, its runtime and its placement references. If it is active, the deletion is applied at the next safe point.

Deleting a parent does not automatically delete its children. The children can keep existing and simply lose their upward relation. A recursive deletion can be requested explicitly.

The messages already sent into other continuities are never removed from their history.

## 29. Errors and resumption

An error is not necessarily the end of a cognition.

- An output validation error causes a bounded correction attempt.
- An error during the execution of an effect causes the remaining actions of this output to stop and a cognitive resumption from the real state reached.

The resumption context specifies both the actions already carried out and the one that failed. The error thus becomes new information to reason about.

Like a wait group result, it is injected as the active stimulus for the resumption then stops being considered new after this inference. It nevertheless stays definitively present in the continuity.

## 30. End of a cognitive act

The processing of a message does not necessarily end with a response intended for the user. The pilot can perform transformations, send messages, delegate or modify its direction without producing a conversational response.

The processing of the current message is finished when no immediate or future cognitive resumption linked to this act is still open.

There is no artificial `done` field needed to express this termination. It is deduced from the protocol.

When there is no longer:

- a projection request to resume,
- a wait group blocking the chain,
- an error requiring a resumption,
- an output correction to perform,

the current message can be acknowledged. The run can then process the next mailbox message or return to idle.

## 31. Scripted cognition and open cognition

Brique allows two complementary forms of cognition.

**The first is open.** The pilot receives a situation, reasons and dynamically decides which Capacities to mobilize and which delegations to perform.

**The second is crystallized in a DSL Capacity.** A Capacity can explicitly combine:

- LLM inferences,
- deterministic transformations,
- loops,
- parallelism,
- conditions,
- other Capacities.

An exploratory cognition can therefore gradually stabilize as a deterministic workflow without changing paradigm. Brique thus allows moving gradually from an open cognition to a scripted cognition.

## 32. Traceability

The agentic context must make the real course of the cognition visible.

The calls to Capacities, their responses, the delegations, the messages, the intention changes, the projections requested, the errors and the significant controls are kept in the continuity.

The runtime can produce system messages when an operational fact deserves to be durably recorded.

The continuity thus becomes a verifiable memory of the course, and not an approximate reconstruction made after the fact.

## 33. Robustness and resumption

The system must be designed to survive the disappearance of an execution process.

The continuity contains the durable truth. The runtime contains only the minimal operational state needed to know what was in progress.

An intention already emitted must never be automatically emitted again after a crash, because it may have produced a real effect before the process disappeared.

At a resumption, the system must prefer to rebuild the state from the existing facts:

- if a response is already present, it can be used;
- if an operation is still identifiable as in progress, the system can keep waiting for it;
- if its state is unknown, this uncertainty must become explicit.

Robustness must never be obtained by a blind replay of effects.

## 34. Minimal core

The agentic context must remain built on a small number of fundamental Capacities. The core envisaged is:

- `pilot.activate`
- `mailbox.send`
- `mailbox.read`
- `mailbox.ack`
- `mailbox.release`
- `continuity.create`
- `continuity.project`
- `continuity.delete`

The more expressive operations, such as delegation or the return to a parent continuity, can be built as DSL compositions of these primitives.

The goal is to keep the engine conceptually small.

## 35. Architectural intention

The agentic architecture of Brique does not seek to define what an agent must think. It seeks to define the conditions under which a cognition can become durable, addressable, distributed and operative.

It does not formalize reasoning. It formalizes its relation to the world.

It provides a cognition with:

- a direction,
- a memory,
- transformations,
- means of discovery,
- means of communication,
- means of waiting,
- means of delegation,
- a continuity in time.

The LLM remains free to reason. Brique makes this reasoning able to act in an explicit, traceable and composable world.

The schemas of the Matters and the contracts of the Capacities that follow this document therefore do not constitute the conceptual model itself. They are the operational materialization of this intention.
