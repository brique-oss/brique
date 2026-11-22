# Capacity — `pilot.launch` (pilot launcher)

## Status

Formalized, with child return through `wait` formalized on 11 November. The descriptor is `capacity/pilot.launch.json`, implementation is `pilot_launch.py`, and policy extension is in `pilot.activate` through `_refusal_of`. Tests include 12 `PilotLaunchContract` contract tests, two `PilotActivateContract` policy tests, and integration scenario S81 in which a parent launches both a Matter pilot and a Capacity pilot. In a real-model test, the guide launches a child—another guide—which reports back through `RealGuideBrique`. The guide no longer has an allowlist of Capacities and therefore uses open policy. Its child reports through a `continuity` message addressed to the parent, which the prompt must state explicitly; otherwise a model chooses `user` or `pilot`, neither of which delivers to anyone. This document states the Intention, accepted decisions, contract, and detailed choices.

## Intention

Launch a pilot by creating a Continuity associated with a Matter or Capacity pilot, giving it a task, and activating it. This is the missing layer between an interface or pilot that wants to call another pilot and the agentic primitives `continuity.create`, `mailbox.send`, and `pilot.activate`.

One launcher serves two uses:

- **The interface** starts a chat by creating a root Continuity with the discovery pilot.
- **A pilot** calls another pilot by creating a child Continuity attached through `parent_id` and referenced in its header.

## Established by the architect

- The launcher creates a Continuity, activates it correctly, and calls the pilot. **The child's return is the Response to the launch Intention.** This 11 November decision replaces the earlier statement that the launcher did not handle returns. An Intention returns to its sender, and a long-running Capacity reports `running` Responses before its terminal Response, as `pilot.activate` does. The parent therefore receives its child's result like the result of any other tool.
- A Matter pilot is a conversation and can receive messages while working. A Capacity pilot is an inference workflow and is not conversational.
- Only a Continuity pilot may launch a child Continuity. The interface creates roots only.
- **The parent knows about its children through the conversation:** the launcher call and response are journal lines. Nothing is prepended to requests or added to the current-state block, preserving the cache.
- **The child does not need permanent awareness of its parent:** it receives a specific Intention, and when a response is required the caller says so in the message. The parent-child relation belongs to Continuity configuration in `parent_id` and `children[]`, not to the model conversation.
- Pilots will be qualified by semantic fields designed by the architect so they can be found by meaning.

## Observed in the code

- `continuity.create` creates the four Matters—journal, header, mailbox, and runtime—verifies that `pilot_ref` names an existing Matter or Capacity, and attaches the child to its parent through `parent_id` and `children[]`. It triggers no cognition.
- `mailbox.send` writes the message and wakes the Continuity through non-blocking activation. An explicit activation is therefore unnecessary: activation follows the first message.
- No Capacity previously wrapped these steps. The workshop pilot exposed `continuity.create` among its known Capacities, forcing its model to know the parameters.

## Contract

**Inputs**

| Field | Description |
|---|---|
| `continuity_id` | New Continuity identifier supplied by the caller, making the call replayable. |
| `pilot_ref` | Pilot reference: `{ context, matter_id }` or `{ context, capacity, limits? }`, as in `continuity.create`. |
| `initial_intention` | Reason for opening this Continuity. |
| `parent_id` | Optional launching Continuity; absent for an interface-created root. |
| `message` | Optional first message containing `content` and optional `id`: the task. When absent, the Continuity waits for its first message, as in a chat where the user writes later. |
| `wait` | Optional boolean. Wait until the pilot finishes processing the first message and return its result. **Defaults to true when both `parent_id` and `message` are provided**, meaning a parent delegates a task, and false otherwise, so an interface opening a chat does not wait. |

**Effects, in order**

1. Call `continuity.create` with `pilot_ref`, the Intention, and the parent. The operation is idempotent: the same identifier and parameters resolve to the same Continuity.
2. When `message` is provided, send it to the new Continuity through `mailbox.send`. The message wakes the pilot.
3. When the call waits, wait until the child is **settled** on the task, reporting `running` every `HEARTBEAT_S` seconds and yielding the execution slot while waiting. Settled means execution is `idle`, the mailbox is empty, no wait group is active, **no awaited Intention lacks its Response**, and all those conditions remain true after `LAUNCH_SETTLE_S`, 0.3 seconds by default, so the final tool Response is written before its entry. Acknowledging the first message is insufficient: a pilot may emit an awaited Intention without a wait group, finish its turn, and receive the continuation through new messages that open new turns. This was observed with a real guide on 11 November, where the Response arrived on the first turn with an empty result. If succession replaces the Continuity during the wait, the wait follows the successor.

**Output:** `continuity_id`, `created`, and the message `seq` when a message exists. Without waiting, the launcher returns immediately and reports no result. With waiting, it adds `treated: true` and `result: { text, messages: [{ to, kind, content }] }`, representing **what the pilot said while processing the message**.

**The result is read from the child journal, not from a field the model must target.** It consists of `pilot` messages written after the first message, regardless of recipient and in order. `text` concatenates their content with one blank line between messages. The child may respond to `user:1`, to nobody, or to any recipient; the result is unchanged. A silent child produces an empty result. Known limit: a person's message arriving during processing belongs to the same thread.

**Failure.** A message the pilot could not process after three attempts and then parked produces a `refused` error Response with reason `message_parked`. Its message and `details.last_error` explain the cause using the final-attempt error read from the child journal. Without this information the caller could only guess, for example that it launched an ordinary tool as a pilot and omitted a required parameter. The wait is bounded by `BRIQUE_AGENTIC_LAUNCH_WAIT_TIMEOUT_S`, 3,600 seconds by default; reaching it refuses with reason `timeout`.

**Idempotence and recovery.** Every step is idempotent. Replaying the call with identical parameters completes anything missing without duplication, and a child that already completed the message returns its result immediately. A wrapper restart while waiting loses both the call and its wait because they share a process; the caller receives a connection-loss error and replaying the call returns the result.

**Errors.** A missing pilot, missing parent, or identity conflict where the same identifier is used with different parameters follows `continuity.create` and `mailbox.send` behavior.

## What the launcher does not do

- Without `wait`, it neither waits for completion nor returns the pilot response.
- It does not conduct conversation; speaking to a Matter pilot still uses `mailbox.send`.
- It does not select the pilot; the caller has already found it by meaning.
- A launched Capacity pilot receives its task in the first message and is not freely messaged afterward. Its possible input requests are a separate concern.

## Usage

- **Chat:** call `pilot.launch` with the discovery pilot and without a parent or message; the user then writes through `mailbox.send`.
- **Pilot calling a pilot:** send an Intention to `pilot.launch` with the semantically discovered `pilot_ref`, the caller's own identifier as `parent_id`, and a message stating the task and intended recipient.

## Decisions

The remaining points are resolved here as agent choices submitted to the architect, who may reverse them.

- **Name: `pilot.launch`**, consistent with `pilot.activate`, `pilot.infer`, and `pilot.recover`.
- **The first message** has kind `user`: it is a request to the pilot and is treated as an instruction. The framework makes `user` messages direct a pilot, and plans already do this with `from: plan:…`. Its sender is `continuity:<parent_id>` for a pilot launch and `user:1` for an interface-created root. When its identifier is absent, it is derived from `continuity_id` so replay does not duplicate it.
- **Only a pilot can be launched**, as decided on 11 November. `pilot_ref` must name a pilot rather than any existing element: either a `pilot_definition` Matter through `objective.type` or a Capacity declaring itself as a pilot through `objective.agentic.pilot`, also used for semantic discovery. Creation refuses an ordinary tool and explains that it must be invoked instead; see `capacity_continuity_create.md`. The architect must correct the twelve existing plans in the test world and workshop by declaring a subject, `test` for the test world and `commande` for the workshop.
- **Parent policy also applies to the named pilot.** Before any effect, an Intention to `pilot.launch` checks `pilot_ref` against the calling pilot's policy like every other target. It interprets the reference as `<context>/<capacity>` for a Capacity pilot or `<context>/<matter_id>` for a Matter pilot. A parent whose allowlist does not name a pilot cannot launch it. This adds no mechanism; it extends the existing `pilot.activate` policy check to this parameter.
- **Semantic pilot qualification is nested meaning.** The domain is `agentic`, followed by the pilot element and its subject: `objective.agentic.pilot.subject`, with value `brique` for the guide or a domain subject for another pilot. Already-known information is not repeated: the descriptor provides the **element type**, Matter or Capacity, which determines its nature—conversation or workflow. Finding pilots means searching for `objective.agentic.pilot.subject` on the desired element type and filtering by `sujet`. Further qualifications can be added below the same path when needed, for example workflow properties under `agentic.pilot`. Verified behavior: the vocabulary exposes `agentic > pilote > sujet`, and `meaning.query` finds the guide through this path. `objective.type` remains the Matter schema type `pilot_definition`.

## Expected tests

- Contract tests with the fake engine: both steps in order, idempotence, a root without a message, a child with parent and message, and errors.
- Real-engine integration: a Matter pilot launches another and receives its response as a message; a Capacity pilot launches as a workflow.
- Real-model test: the guide launches a pilot found by meaning.
