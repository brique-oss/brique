# Capacities — `input.request` and `input.answer`

## Status

**Contract approved by the architect on 11 November and implemented.** Descriptors: `capacity/input.request.json` and `capacity/input.answer.json`; code: `input_request.py`, `input_answer.py`, and `substrate/input_state.py`; 21 contract tests in `tests/contract/test_input_request.py`; real-engine integration scenario S84; pure module `ui/model/input.ts` with two tests; window `ui/InputRequest.tsx`; and two bench scenarios, `input_request` and `input_request_collapsed_child`. **All five points were approved as proposed**: a 24-hour lifetime, the request stays in the child window with a signal propagated to its collapsed parent, three answer kinds, every pilot may ask, and the proposed text. **Interface wording still requires validation** and is defined in `INPUT_LABELS` and `ui/InputRequest.tsx`.

The change takes effect after restarting the `agentic_core` wrapper, which has been done. This document states the Intention, the decisions derived from the Brique model, and the contract.

## Intention

A pilot, especially a workflow implemented as a Capacity pilot, sometimes needs an answer from a person: data, a choice, or confirmation. This is not free-form conversation. It is a **specific request**, displayed separately in the interface as a response window, whose answer resumes the pilot.

## Applying the Brique model

An Intention returns to its sender through that Intention's Response. A long-running Capacity reports progress through `running` Responses before its terminal Response. `pilot.activate` and `pilot.launch`, including a child's return, already work this way. Input requests use the same model without introducing another mechanism:

- **Asking** is an Intention sent to `input.request`. Its terminal Response **is the person's answer**. The pilot waits for it like the Response of any other tool, through a wait group on the Intention, which the engine and `pilot.activate` already support. The original obstacle—that a user message is not something a wait group can await—disappears because the pilot awaits an Intention Response, not a message.
- **Answering** is an Intention sent by the person, through the interface, to `input.answer`.
- **Request state lives in the requesting Continuity's journal** as three line types. All have kind `event`, are never consumed, and are never stimuli. The interface already follows this journal live, so it sees a request appear without a new mechanism.

## Journal: three events

All these lines have kind `event` and are written by the following Capacities, never by the model.

| Event | Content | Written by |
|---|---|---|
| `input_request` | `request_id`, textual `question`, `answer_kind` (`text`, `choice`, or `confirm`), `choices` for `choice`, `asked_at`, `expires_at` | `input.request` |
| `input_response` | `request_id`, `answer`, `answered_at` | `input.answer` |
| `input_closed` | `request_id`, `reason` (`timeout` or `cancelled`), `closed_at` | `input.request` |

A request is **open** while its `input_request` has neither an `input_response` nor an `input_closed` with the same `request_id`. The interface derives this state from the journal through a pure function, as it does for metrics.

## `input.request`

**Inputs**

| Field | Description |
|---|---|
| `continuity_id` | Requesting Continuity whose journal carries the request. |
| `question` | Required non-empty text shown to the person. |
| `answer_kind` | `text` by default, `choice`, or `confirm` for yes/no. |
| `choices` | Required for `choice`, with at least two distinct texts; refused otherwise. |
| `request_id` | Optional. When provided, makes the call replayable: the same identifier and parameters refer to the same request. Otherwise derived from the calling Intention. |
| `timeout_s` | Optional unanswered-request lifetime. Defaults to `INPUT_TIMEOUT_S`. |

**Effects, in order**

1. Verify that the Continuity exists and is neither archived nor deleted.
2. Write `input_request`, idempotently because the line carries `request_id`. If the request already exists and was answered, immediately return its answer; if it is closed, return the closing error.
3. Wait for `input.answer` to write the response, reporting `running` every `HEARTBEAT_S` seconds and yielding the execution slot. A write in the same process wakes it immediately; otherwise it rereads every `INPUT_POLL_S` seconds.
4. Return terminal Response `{ request_id, answer, answered_at }`.

**Errors** are Intention error Responses, never invented answers: `invalid` for malformed parameters or `choice` without choices; `not_found` for an unknown Continuity or one deleted during the wait; `refused` with reason `timeout` when the deadline is reached, after writing an `input_closed` line; or `refused` with reason `cancelled` when the Continuity is deleted or its pilot replaced while waiting, leaving the request without a recipient.

## `input.answer`

**Inputs:** `continuity_id`, `request_id`, and `answer`.

**Effect:** verify that the request is open; **validate the answer against its kind**—non-empty text for `text`, exactly one of `choices` for `choice`, and `yes` or `no` for `confirm`; write `input_response`; then wake the waiter.

**Idempotence:** answering the same request twice with the same answer returns the same result without another write. A different answer to an answered request is refused with reason `already_answered`. An answer to a closed request is refused with reason `closed`. An invalid answer is refused as `invalid`, and **the request remains open** so the person can correct it.

**Output:** `{ request_id, answered: true }`.

## Who sees and answers the request

- **The request appears in the conversation that carries it.** A child request is visible in the child window; its row in the list carries a signal, and a **collapsed** parent whose child awaits an answer carries the signal as already specified by hierarchy collapsing.
- **It is not propagated to the parent as a request.** The parent pilot is not asked; the person answers in the child window. The parent waits for the Response of its `pilot.launch` call with `wait`. While the child awaits the person, the launch remains in progress and reports `running`, then returns the child result.
- **There is one respondent:** the first valid answer closes the request.

## Interface

- Pure module `ui/model/input.ts` derives open requests for a conversation, oldest first, their state, and the number of open requests for a child or lineage. It is tested against hand-written and captured journals.
- **Conversation row:** displays an “awaiting your answer” signal while a request is open. A collapsed parent also shows the child signal.
- **Response window:** displays the question and the control appropriate to its kind—a text area, choice buttons, or yes/no buttons—plus “Answer.” An `invalid` refusal appears in the window without closing it. A request closed by timeout or cancellation disappears, and a conversation line reports why.
- Window calls use the same `busy` retry mechanism as the block's other calls.
- **Proposed wording:** “Your answer is required,” “Answer,” “Yes,” “No,” “The request has closed (deadline exceeded),” and “The request was cancelled.”

## Settings

| Variable (`BRIQUE_AGENTIC_…`) | Default | Min. | Controls |
|---|---:|---:|---|
| `INPUT_TIMEOUT_S` | 86,400 (24 h) | 1 | unanswered-request lifetime; call-level `timeout_s` takes precedence |
| `INPUT_POLL_S` | 1.0 | 0.05 | fallback while waiting; an answer written in this process wakes it first |

## Known first-version limits

- **Restarting the wrapper while waiting** loses the call and its wait because they share the process. The caller receives `connection_lost`, meaning an unknown outcome. The request remains open in the journal; replaying it with the same `request_id` resumes the wait or returns the answer already given. This matches every long-running Capacity.
- **A request is not addressed to a specific person:** the first valid answer wins.
- **No open-ended form:** only text, choice, and yes/no kinds exist. A multi-field form will be added only for a concrete need.

## Points that required a decision

All were accepted as proposed:

1. Default lifetime is 24 hours, since a person may take hours to answer while the usual wait timeout is one hour.
2. A child request stays in the child window rather than also appearing in the parent window.
3. Text, choice, and yes/no are sufficient initial answer kinds.
4. A workflow may call `input.request` from its plan, and a Matter pilot such as the guide may use it as another tool. It is an ordinary Capacity governed by pilot policy.
5. The wording listed above.

## Expected tests

- **Contract, fake engine:** a request is written and awaited; an answer closes it and returns the answer; both Capacities are idempotent; validation by kind; invalid response leaves the request open; timeout writes `input_closed` and refuses with `timeout`; deletion while waiting produces `cancelled`; `running` is reported and the slot yielded; replay after restart uses the same `request_id`.
- **Integration, real engine:** a workflow requests input, an interface response resumes it, and the parent receives the result through the launch Response.
- **Interface:** pure journal function; bench scenario in which the window appears, the response closes it, and the workflow resumes; badges on the row and collapsed parent.

## What implementation revealed

- An idempotent append does not return an event from a journal because `event` is not a retained kind. The Capacity rereads the journal to find its request or the winning answer.
- **Typed error reasons:** `typed()` used to replace the reason supplied in `details` with the class reason `refused`. Errors now have dedicated classes: `WaitTimeout` with reason `timeout`, `RequestCancelled` with `cancelled`, `RequestClosed` with `closed`, `AlreadyAnswered` with `already_answered`, and `MessageParked` with `message_parked`. The reason crosses the engine and returns as its original class, covered by `test_typed_errors.py`. The defect already affected waiting `pilot.launch` calls with `message_parked` and `timeout` and was fixed in the same change.
