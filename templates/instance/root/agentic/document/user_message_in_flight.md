# User messages accepted at any time

## Status

Under discussion; nothing is implemented. This document states the Intention, current behavior, forbidden states, the currently selected design, and open questions. It distinguishes what the **architect established**, what is **observed in code**, and what is **inferred and awaiting validation**.

## Intention

A user must be able to speak to a Continuity **at any time** while its pilot is working: to redirect it, ask for something else, or ask a question. What the user says and what the model answers remain in the Continuity because later work may depend on that guidance.

The user is a **second writer of the Continuity**, acting in parallel with the pilot's own movement through inference, tool requests, tool responses, and further inference.

## Established by the architect

- The conversational pilot is the **Matter pilot**: a loop of inferences and tool requests in which user conversation can join at every inference boundary.
- A **Capacity pilot is a child Continuity**, an uninterrupted worker. Its return to the parent is a tool return: a `continuity` message awaited by a `continuity` wait-group item. Its current behavior remains unchanged.
- **Messages are not addressed to a Capacity pilot.** The user always interacts with a Matter pilot. A Capacity pilot Continuity receives only what its parent sends.
- If a user writes while a Matter pilot awaits a tool, **the request proceeds**, and **the tool response is held** until the user response has been written. Journal order is request, response, then tool response.
- This protection applies to **every inference**, including pilot inference. Nothing else enters the Continuity between reading the journal and writing the decision line, and no other inference runs.
- The user request **joins the active chain** as a stimulus. Its inference may request tools whose responses arrive later.
- Framing and line semantics shown to models belong in prompts.

## Observed in the code

- `mailbox.send` writes the user line to the journal **on arrival**, which is its commit point, with a `pending` entry at `normal` priority. Message content exists only in the journal; the reservation stores only its fingerprint.
- `_drive` handles one message at a time through `current_message_id` and considers `normal` priority only when selecting work. During a wait, `_wait_for_resumption` wakes for a satisfied group or a control, never for a normal message. A user message therefore waits for processing to end.
- A Capacity pilot is a plan executed as one blocking `call_capacity` up to `timeout_s`, with no safe point inside it.
- Every journal write goes through `continuity_store.append` under the Continuity critical-section lock. Writers include `mailbox.send`, including tool Responses delivered by the `_awaited` background task; `append_trace` for decisions, events, and errors; `pilot.infer` prompt and completion lines; and measurements.
- A Matter pilot writes no request line before inference: its input is a journal projection read at one instant. Only `pilot.infer` steps write `prompt` and then `completion` in two writes.
- The agentic wrapper is **unique**: the process holding an in-memory guard and every waiter die together. The journal already reveals interrupted inference through a `prompt` without a `completion`.
- `mailbox.send` is a short Capacity without `running` acknowledgements. The runtime can emit `running` when hosted code requests it through `keepalive`.
- Waitable group items are `intention`, `continuity`, and `event`, matched through correlation. A user message is none of these.

## Forbidden states

The following proposed states must never occur and require individual validation:

1. The journal never misrepresents knowledge: a decision follows the lines it observed.
2. Two voices of one Continuity do not contradict each other unknowingly.
3. No line is inserted between a request and response, between what the model read and its decision, or between a plan step's `prompt` and `completion`.
4. The user never reads partially written state.
5. A message is never lost, left unanswered, answered twice, or attached to the wrong message. S44 already accepts duplicate response after abrupt shutdown.
6. User response time is bounded and independent of plan or tool duration.
7. Protection never freezes the Continuity indefinitely.
8. Nothing said disappears from the Continuity.
9. The two flows do not overwrite pilot state.

These reduce to two invariants: an inference unit is contiguous in the journal, and a response is bounded in time.

Rejected invariant: “no effect is emitted after a message accepted by an older decision.” A launched action cannot be recalled, and launch itself takes only milliseconds. True journal order matters more than cancellation: the pilot sees the message at its next inference and reacts by compensating or warning.

## Proposed form

This form is inferred and still requires validation:

1. **One FIFO inference window per Continuity**, held in memory and shared by paths that infer: the Matter-pilot loop and a user request. It spans from reading input through writing the decision or response line. It does not include effect launch: `_apply` only launches, while awaited Intentions run in background and return later through the mailbox, so it lasts only milliseconds and a launched action cannot be recalled.
2. **The journal gate waits.** Except for the window holder, `continuity_store.append` waits for the window to become free **before** acquiring the Continuity lock. `mailbox.send` gains no new contract: it waits while reporting `running`, then writes, and `seq` remains known on return.
3. **The window opens when the request arrives**, in the same operation that writes it through `mailbox.send`, then passes to the loop. The loop cannot write the request later because the reservation does not retain content and a crash would lose it. The window has a safety expiry if the loop never starts the step.
4. **The loop wakes for the user** during a wait, as it does for a control, and takes the request as a stimulus in the current chain. Multiple wait groups may remain active; the chain ends when nothing is awaited.
5. **FIFO by arrival:** a tool response arriving before a second message proceeds first, preventing starvation.

## Why close the window rather than invalidate inference

Discarding inference whenever lines arrive during it may never finish when several tools answer during a long inference, and it pays for another inference on every attempt.

## Detailed decisions

Accepted but still to formalize:

- **Window expiry.** A sender-opened window releases itself after a few dozen seconds if the loop does not acquire it, and also on Continuity suspension or deletion. The request is already in the journal and becomes a normal message processed at chain end, as today.
- **Acknowledgement.** An absorbed message remains claimed through chain completion and is acknowledged with the primary message. A mid-chain crash returns it to pending, in the S43 family.
- **Inference bound.** Every absorbed message renews the chain allocation.
- **`user_message` block.** Absorbed message identifiers enter a `user_message` block in current state, separate from the active stimulus. A stimulus may already be pending, so adding to or replacing it would lose one of them.
- **Recovery.** After abrupt shutdown, a `normal` entry claimed by the dead run that is not its current message returns to `pending` and is absorbed again.
- **Writer identity.** A window reserved for a request remains open to writes from the run loop; background tasks for awaited Intentions do not inherit it.
- **Wake-up.** While waiting, the loop reads pending-message kinds from the journal and wakes only for a `user` message.
- **No refusal in `mailbox.send`.** “Do not message a Capacity pilot” is a usage convention: the engine cannot distinguish a human from a parent sending a `user` message to its child.

## Formalized contracts

- `agentic_core_concurrency_model`: “Inference window,” clarification of the critical-section lock, and `mailbox.send` `running` acknowledgement.
- `capacity_mailbox_send`: wait for the window during append, open it for a `user` message, and wake the loop.
- `capacity_mailbox_read` and its `mailbox.read` descriptor: optional input `kind = user` for absorption reads.
- `capacity_pilot_activate`: “User message during an active chain,” including absorption, `user_message` stimulus, acknowledgement, and bounds; wake-up during waits; inference window under “Concurrency.”
- `capacity_pilot_infer`: the step holds the window from its `prompt` through its `completion`.
- Descriptors for `mailbox.send`, `pilot.activate`, and `pilot.infer`, including effects and invariants, plus the `user_message` schema.

Code and tests have not yet been changed.

## Open point

- Delegating to a child still has an ergonomic cost for the model: create, write, then await a `continuity` item. A one-call facade would be convenient.

## Control-tower relationship

`pilot_launcher_chat.md` describes the chat that finds, launches, and follows pilots. Intervention through an in-flight message depends on this design; monitoring through journal-line events does not.
