# Chat that searches for, launches and follows pilots

## Status

Under discussion. Nothing is implemented. This document lays out what exists, what is missing, the framing of the first version and the decisions to take.

## Intention

A chat where the user **searches for a pilot**, **launches it in a conversation** and **follows everything** that happens, including child continuities, waits, failures and cost.

## What exists

- **Launching**: `continuity.create` with a Matter `pilot_ref` (`pilot_definition`), then `mailbox.send` to talk. The chat launches and converses only with Matter pilots: a Capacity pilot is a child continuity, a worker that is not interrupted, and only its parent writes to it. The continuity is the conversation; its **lineage** (`lineage_id`) follows it through the successions, which change the identifier (`_g2`, `_g3`).
- **Following**: the journal (append-only, readable by `seq`), the runtime (`idle`, `running`, `waiting`, `suspended`), the mailbox (including the `failed` messages set aside), the tree of children (`children`), `continuity.stranded` for continuities that do not restart by themselves, `pilot.recover` to restart them.
- **Intervening**: the controls `suspend`, `resume`, `set_intention`, `set_pilot`, `delete`.
- **Measuring**: the exact token profile (`token_measurement.md`).
- **Interfaces**: a WebSocket interface wrapper already serves the test bench; the MCP reader and architect profiles exist.

## What is missing

1. **Finding a pilot.** Solved for the guide: pilots are qualified by a nesting of meaning `objective.agentic.pilot.subject` (see `capacity_pilot_launch.md`). The existing pilots of the workshop remain to be qualified.
2. **A notion of conversation** that follows the lineage, not the identifier of a continuity.
3. **The view itself**: a UI block that assembles the index of continuities, the Matter subscriptions, the states, the tree and the alerts.
4. **A launch path**: choose, create, present the intention, send the first message.

## First version (framing by the architect)

We start simple.

**Set by the architect**
- One **conversation window per continuity**, which displays its journal like a classic chat.
- A **list of continuities** with a notion of **new message**.
- A **new chat** starts with a **prompt pilot (Matter) that does only Brique search**, and that begins by saying how Brique works. Each new chat is a root continuity with this pilot; it is not a child of a permanent continuity.
- A **hierarchical view** of the continuities.
- The chat is tied to a **lineage**: it follows the successions (`_g2`, `_g3`), and the window sends to the head of the lineage.
- The journal is displayed **with what makes sense to the user**, not all the technical lines.
- **A sub-continuity is launched only from a continuity**, that is by its pilot (it creates the child with `continuity.create`). The interface creates only root chats. A child is created by the pilot of a chat: the guide of the first version, which only searches, creates none.
- The **discovery pilot** is called `guide_brique` (Matter `/root/agentic/prompt/guide_brique`), it is written, qualified (`objective.agentic.pilot.subject = brique`) and tested with a real model (`RealGuideBrique`). It **speaks English**. Its "how Brique works" stays short: it serves to **launch and explore**, not to develop or architect, so the complete Brique framework is not carried in it.
- **The model is associated with the pilot**: it is in its `pilot_definition`, not a choice of the interface.
- **A continuity can call other pilots** (a general rule of the Brique world, not a function of the guide), and that produces the sub-continuities that the hierarchical view shows.
- **The launcher** creates a continuity, activates it as needed and calls the pilot, Matter or Capacity. It does not deal with the return: the child reports to its parent through its own pilot.
- **What separates the two pilots is interaction.** A Matter pilot is a conversation: one can talk to it while it launches tools. A Capacity pilot is a workflow with inference: one does not converse with it.
- **Outside the first version: the input request of a workflow.** When a Capacity pilot needs an input, it is not a conversation message. It would be an explicit request outside the chat window (for example a pop-up "such pilot asks you to fill in something"), the user enters it and the workflow restarts. It cannot be expressed today: a user message is not an element that a wait group knows how to wait for.
- **Everything said to the user is submitted to the architect**: the labels, what counts as "new", the messages of the interface.

**Observed consequences**
- All the engine needed exists: `continuity.create`, `mailbox.send` (which redirects to the head of a lineage), the following of the journal by line events, the tree in the header (`parent_id`, `children`, `lineage_id`, `successor_id`).
- A parent that receives a child is never surprised: it is the one that created it.
- A Matter child can receive a message from the user like any Matter pilot; a Capacity child cannot.

**To build**
- The UI block of a `chat` context (list, window, tree).
- ~~The discovery pilot~~: done (`guide_brique`).
- The "read up to such line" state per user and per continuity, in a Matter (the framework wants the persistent state not to be in the interface).

**To review with the architect (everything said to the user)**
- The text of the discovery pilot, written in `guide_brique`: to be re-read.
- What counts as "new" in the list.
- The way each kind of journal line is displayed, and what stays hidden.
- The labels of the states (at rest, running, waiting, suspended, failed).

## Decisions to take

1. **Who is the chat**: an interface (WebSocket), the MCP profile, or a pilot itself?
2. **Following**: subscription (push) or incremental read by `seq` (pull)? Since the journal is append-only, incremental reading is the simplest and safest.
3. **Granularity of "everything"**: the whole journal, or chosen events (messages, intentions, responses, errors, successions, set-asides)?
4. **Where it lives**: a separate `chat` context, with its capacities and its interface?
5. **Access**: which security principals launch and follow, and can they act on the continuities of others?
6. **Several conversations** in parallel and their list.

## Spark, Pulse and the UI block: the natural path

Brique already has the interface building block (`ui_block/UIBlock.md`). **Spark** is the host (Electron) that builds the view of a context on demand; **Pulse** (`@spark/pulse`) is the runtime that the view uses to talk to the engine over WebSocket; the **UI block** is a React component per context, declared in `context.json` (`brique.ui_config` and an interface `type: ui`), whose source is `<context>/ui/<RootView>.tsx`.

What this changes for the chat and the tower:

- **Following live, without adding anything to the engine.** `useMatterSubscription` subscribes to a Matter. The Matters of a continuity are already there: `runtime_<cid>` (state, wait groups), `mailbox_<cid>` (pending messages, set aside), `continuity_<cid>` (header, lineage, children), `journal_<cid>` (the thread). The "event stream" that I thought was missing is the subscription to the Matters.
- **The global index exists.** The `continuities` structure references each continuity (`structure.read` returns its `matter_id`s). A tower reads it to list, then subscribes to the Matters of each continuity.
- **Intervening through intentions.** `emitUIIntention` calls the agentic capacities as they are: `continuity.create`, `mailbox.send` (messages, controls), `pilot.recover`. The test bench already does this through a WebSocket interface route.
- **No business logic in the view.** The block observes and triggers; it decides nothing, which is exactly the intended role of the chat.
- **Composition.** A block can mount the block of a child context: the tower can mount one view per pilot or per family of pilots.

Consequence for my first proposal: the capacities `conversation.start`, `conversation.send`, `conversation.follow` and `conversation.list` are not needed. The block composes `continuity.create` then `mailbox.send`, and follows by subscription. A `chat` (or `tour`) context would therefore mostly contain only a view; a capacity would be added there only for a need that reading and the primitives do not cover (for example the search for a pilot through the semantic funnel, which is already `vocabulary.query` then `meaning.query`).

### What the code of Pulse and Spark establishes (read in `Developer/Published/brique/pulse` and `spark`)

- **Spark** (`spark/`): `SparkRuntime` launches the engine (`BriqueLauncher`), `ContextUIBuilder` builds the view of a context (esbuild-wasm), `ContextUILauncher` mounts it, and hosts execute it: **Electron** and **VS Code** (`hosts/electron`, `hosts/vscode`), connected to the engine by a local WebSocket relay. A control tower can therefore be an Electron window or a VS Code panel, with the same view.
- **Pulse** (`pulse/`): `PulseRuntime` (intentions, responses, flows), `MatterRuntime` (read, subscription, byte stream), `FlowRegistry` / `SubscriptionRegistry`, and the React hooks (`useMatter`, `useMatterSubscription`, `useMatterEvent`, `useFlow`, `useBriqueAction`, `usePathFlow`).
- **The large journal is followed by line ranges.** `MatterDataRange` accepts `{unit: "lines", offset, length}` or `{unit: "lines", tail: N}`: read the last N lines of the journal, without reloading it. A `matter_appended` event carries `lineOffset`, `lineLength`, `totalLines` and `previousTotalLines`: the tower knows which lines were added. The other events are `matter_written`, `matter_deleted` and `matter_truncated`.
- **A subscription has a state**: `status` (`idle`, `loading`, `streaming`, `ready`, `error`, `stale`, `deleted`), `lastEvent`, `subscribed`, bytes received. `MatterSubscribeInput` accepts `durable` and a `subId`; events are acknowledged by `event_id`, so a durable subscription does not lose an event.
- **The example** (`spark/test/sandbox/root/app_test`) contains a Matter flow context (`stream_context`), a flow and projection inspector (`inspector_context`), and Matter observation blocks: a base to reuse for the tower.

### Verified on the real engine: the agentic journal is followed by line events

Test done with a `ui` interface (the integration test pilot): subscription to `journal_<cid>` by `matter.subscribe`, then a message sent to the continuity.

- The subscription is accepted (`substance_mode: brique`, not durable).
- The engine emitted **one `matter_appended` per append**, that is 4 events for the 4 lines written (message, measurement, decision, response), consecutive and without duplicate: `line_offset` 1, 2, 3, 4, `line_length` 1, `total_lines` 2 to 5, `byte_length` for the size. No content byte is sent: the tower then reads the useful line range (`unit: "lines"`).
- The cause: agentic writes its journal by `matter.append` and its framing is `lines` (`substance_framing: lines`), the only case in which the engine adds the line coordinates. Nothing to change on the agentic side for the tower to follow a journal.

### What remains to be verified
1. The behavior **at scale**: a journal of several thousand lines, several continuities subscribed at the same time, grouped appends (`line_length` greater than 1).
2. The events of the **other Matters** of the continuity: `runtime_<cid>` and `mailbox_<cid>` are written by `matter.write` (expected event `matter_written`), not tested.
3. The **Pulse** side: the React hooks on these events, not executed; this test goes through the integration pilot, not through Pulse.
4. The **rights** of the principal of the `ui` interface: the subscription was accepted in this configuration, but the complete access policy (who can follow or act on which continuities) remains to be defined.
5. The **billed cost** per continuity is not in the journal (only the context size): to be added on the agentic side if the tower must display it.

## Horizon: the agentic control tower

The chat is the first step toward a **control tower**: a graphical interface that sees all the pilots, Matter and Capacity (the child continuities appear there with their parent), what they are doing at this moment and what they have done, and that allows intervening with the Matter pilots. The chat is a subset of it (one conversation, one pilot); the tower is the same reading over all the continuities.

**What the journal already provides, in structured lines**: the messages, the intentions and their responses, the decisions of the pilot, the wait groups (`wait_group_created`, `wait_group_completed`, `wait_groups_abandoned`), the failures (`treatment_failed`, `message_parked`, `validation_error`), the resumptions (`run_recovered`), the code repairs (`output_repaired`), the context measurements (`inference_measured`), the successions and the lineage. A tower can be built on it without the pilots changing.

**What the tower would show**
- the state of each continuity (at rest, running, waiting for what, suspended, failed) and the tree of children;
- the live thread of a continuity: who said what, which capacities were called, with which outcomes;
- the alerts: continuities that do not restart (`continuity.stranded`), messages set aside, repaired outputs, policy refusals;
- the cost: tokens per inference, per continuity, per lineage, with the fixed part and the replayed history (the exact measurement exists).

**What would allow intervening**: suspend, resume, delete, change the intention or the pilot, restart (`pilot.recover`), send a message at any time to a Matter pilot: the request goes through at once and joins the chain in progress (see `user_message_in_flight.md`, formalized and implemented).

**What a tower is missing**
1. A "what is it doing now" field readable at a glance: current message, awaited group, last intention (the runtime already carries `current_message_id` and the groups; the last intention is read in the journal).
2. The **billed cost** per continuity: `inference_measured` carries only the context size, not the billed tokens.
3. The **access model**: who sees and acts on what (security principals of the `ui` interface).
4. The billed cost per continuity and the access policy of the interface (see the points to verify above).

The global index (`continuities` structure) and the stream (Pulse subscription to the Matters) exist. None of this changes the engine: they are reads and calls to the existing primitives, in a UI block.
