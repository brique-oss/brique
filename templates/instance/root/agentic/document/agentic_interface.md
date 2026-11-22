# Agentic interface (UI block)

## Status

Specification of the first version. At the time of writing nothing was implemented; it has been built since (see "To build", "Wiring done" and the verification sections). This document says what to do and how, before any code, and lists what the architect must validate because it is said to the user. The starting framing is in `pilot_launcher_chat.md`; the mechanism of a user message in the middle of work is in `user_message_in_flight.md`; the launcher is in `capacity_pilot_launch.md`.

## Intention

An interface that one starts to **converse with Matter pilots**, see one's conversations and their hierarchy, and start new ones. It decides nothing: it observes the Matters of agentic and triggers its capacities.

## Scope of the first version

- A **hierarchical list** of the conversations, with the state of each and the notion of new message.
- A **conversation window** per lineage, which displays the journal like a chat.
- **Start a chat**: a root continuity with the guide (`guide_brique`).
- A single user (`user:1`).

Outside the first version: the input request of a workflow (pop-up), the tools in progress, the cost, the controls (suspend, delete), the search for a pilot other than the guide.

## Where it lives and how it is wired

It is the interface of the `/root/agentic` context: a single block, root component `AgenticInterface`, source `ui/AgenticInterface.tsx`. The wiring follows that of the block of the `mcp` context (`IntentionRepl`):

- in `context.json`, `engine_config.communication.interfaces` gains `{ driver: "ws", name: "agentic", type: "ui", config: { path: "/agentic/ws", egress_enabled: true, ingress_enabled: true } }`;
- `brique.ui_config`: `{ adapter: "react", host: "electron", name: "agentic", root_view: "AgenticInterface", ws_path: "/agentic/ws" }` (the `ws_path` is that of the interface);
- the skeleton is that of `ui_block/UIBlock_LLM.md`: the outer component builds the `PulseRuntime` (`maxMessagesPerFlow: 20`, `terminalFlowTtlMs: 30000`), the inner component does everything through the hooks; styles as inline objects with the `--syn-*` variables; no business logic, no access to substances outside `pulse.matter`.

## The three views

**1. The list.** The root conversations (the continuities of the `continuities` structure without parent), and under each one its children through `children[]`. A **lineage** (a continuity and its successors `_g2`, `_g3`) is a single entry: that of its head (the generation without `successor_id`). Each entry shows a title (the initial intention), the runtime state, the nature of the pilot (conversation for a Matter, workflow for a Capacity) and the "new" badge.

**2. The window.** The journal of the lineage, generations in order, with a separator at the summary of each succession. The end of the journal is read (the last N lines) and one goes back up on demand. The input field sends to the original continuity through `mailbox.send` (which redirects to the head). For a continuity with a Capacity pilot, there is no free input.

**3. Start a chat.** A button that calls `pilot.launch` without parent or message: `pilot_ref` of the guide, a new `continuity_id` chosen by the interface, an initial intention. The user then writes.

## What each view reads and calls

| Need | Means |
|---|---|
| List the continuities | `structure.read` of `continuities`, then the header (`continuity_<id>`) of each by `useMatter` |
| State of a continuity | `runtime_<id>` by `useMatter` with `{ subscribe: true, refetchOnEvent: true }`; the content is JSON in `data.bytes` |
| Follow a journal | `useMatter` with `{ subscribe: true, refetchOnEvent: true, dataRange: { unit: "lines", tail: N } }`: the end of the journal is re-read at each append. The content arrives in `data.bytes` (text, one JSON line per line) |
| Send | `emitUIIntention` to `mailbox.send` (through `useBriqueAction`) |
| Start | `emitUIIntention` to `pilot.launch` |

Only the open window subscribes to its journal. The list reads the headers and the states; it does not follow all the journals.

## What the user sees of the journal

The complete journal is kept; the window shows what makes sense to the user. Proposal, submitted to the architect:

| Kind of line | Display |
|---|---|
| `user` | user bubble |
| `pilot` | pilot bubble |
| `continuity` (message from a child) | bubble labeled with the name of the child |
| `capacity_intention` and its `capacity_response` | a tool card: the name of the capacity, its state (in progress, succeeded, in error), expandable |
| `summary` | separator "the conversation continues from a summary", expandable |
| `error` | a discreet alert note |
| `control` | a note ("suspended", "resumed") |
| `framework`, `prompt`, `completion`, `event`, `projection` | hidden |

A decision of the pilot (`completion`) repeats its messages and its intentions, already displayed: it stays hidden.

## Stop, resume, delete

The header of the window carries two buttons. **Stop** (becomes **Resume** when the conversation is suspended) and **Delete** (with confirmation, and mention of the sub-continuities that go with it). These are control messages (`suspend`, `resume`, `delete` with `cascade`) sent by `mailbox.send` to the head continuity: the engine applies them at a safe point, the interface writes nothing else. After a deletion, the selection is emptied.

## "New message"

A **read cursor** per lineage: up to which line the user has read. It is a persistent state, therefore in a Matter and not in the interface. The capacity `reading.mark` (monotonic: the cursor never goes back, see `capacity_reading_mark.md`) writes the `reading_cursors` Matter; the interface calls it when the window is open and the end of the journal is visible, and reads the Matter through a subscription. The "new" messages are those of the pilot (`pilot`), of a child (`continuity`) and the errors (`error`) placed after the cursor. The badge of the list counts these lines.

## The labels submitted to the architect

| Runtime state | Label |
|---|---|
| `idle` | idle |
| `running` | running |
| `waiting` | waiting for a tool |
| `suspended` | suspended |
| does not restart (`continuity.stranded`) | stranded |
| archived (has a successor) | superseded by the rest of the conversation |

The texts of the interface (buttons, empty states, separators) are in English, like the guide; they are gathered in the model (`STATE_LABELS`, `METRIC_LABELS`, `INPUT_LABELS`) or in the components.

## To build

- ~~The reading-mark capacity and its Matter~~: done (`reading.mark`, contract, 8 contract tests, scenario S82 with 60 simultaneous marks).
- ~~The UI block and its wiring~~: done. Hierarchical list, window, new conversation, stop/resume/delete, wait indicator. The texts remain to be validated.
- ~~A pure module that turns a journal into a conversation view, builds the tree and computes the "new"~~: done, in `ui/model/` (`journal.ts`, `tree.ts`, `state.ts`), tested by `tests/ui_pulse/model.test.ts` on real journals captured from the engine (`fixtures/`, regenerable by `capture_fixtures.py`). The state labels are gathered in `STATE_LABELS`, to validate.

## Verified on the real engine

Done with the Pulse hooks in a test render, against the started engine (scripts in `code/agentic_core/tests/ui_pulse/`):

1. **The events arrive for the four Matters of a continuity.** The journal emits one `matter_appended` per line appended, with consecutive `lineOffset` and `totalLines`; `runtime_<id>` and `mailbox_<id>` emit `matter_written` (about four runtime writes and three mailbox writes per message processed); the header does not move as long as nothing is written to it.
2. **`useMatterSubscription` notifies without refreshing**: the status goes to `stale` and the data stays old. It is `useMatter` with `subscribe` and `refetchOnEvent` that re-reads. The data read is an object `{ bytes, byte_range, kind, size, total_bytes }`, the content is in `bytes`.
3. **Journal scale**: the last 50 lines of a journal of 3,001 lines (512 KiB) are read in 23 ms (8.5 KiB transferred) and refreshed in 31 ms after an append; writing 3,000 lines takes 80 ms.
4. **List scale**: 40 continuities followed at once (80 subscriptions, runtime and header) are read in less than 2.5 s; sending a message to each at the same time takes 0.4 s, the engine finishes them in 2.5 s, and the interface then shows all 40 at rest.
5. **Rights**: no security profile is defined in this instance (`root/security` does not exist), so the principal of the interface is not restricted; all the intentions of an `agentic-user` principal (writes, subscriptions, creation, send, deletion) succeeded. If profiles are applied later, the interface will need read and subscription on the Matters of agentic, and the call of `mailbox.send`, `pilot.launch`, `structure.read` and of the marking capacity.

**An anomaly observed, not explained.** For one minute, 38 `matter.append` calls of `agentic_core` timed out at 60 s (error journal of the wrapper), during a simultaneous send to 40 continuities; three runs in a row then took 61 s instead of 2.5 s. It was not reproduced afterwards, neither by six following runs, nor after a brutal disconnection of an interface with 18 subscriptions (hypothesis ruled out). The stack stops in the call to the engine, not in the inference window. To watch: the interface does not send 40 messages at once.

## Live update and waiting

Two defects found in use, fixed, and verified by `tests/ui_pulse/smoke_ui.mjs` (the complete block rendered against the real engine with a fake model: new conversation, response without reloading, second message, stop, resume, delete).

- **The response appeared only at refresh.** Cause: Pulse discards an event when **several read keys follow the same Matter** (it does not know which one it is addressed to). The head journal was followed twice, by the list (60 lines) and by the window (300). Rule: **a single key per Matter**, hence a common `JOURNAL_TAIL`. With one key, the response appears in about a hundred milliseconds. Two protections in `useMatters`: an event that arrives during a read in progress causes a second one (the first ends with the old content), and a safety net re-reads the followed Matters every 5 s.
- **Nothing said that we were waiting.** The window adds a line at the end of the stream: "The pilot is working…" (running state), "Waiting for a tool response…" (waiting state), "Message received, waiting for the response…" (last message of the conversation coming from the user and not yet answered). These three texts are to be validated.

## The defect seen in a real browser

The user reported: the conversation does not update, and the input field loses focus and its text. The test render (`react-test-renderer`) could not see it: it has neither a real DOM nor focus. Reproduced in Chrome (bench `tests/ui_pulse/browser/`), the cause was: **Pulse empties the data of a Matter at the start of each read** (`setLoading` sets `data` to `null`), including a re-read. Each event and each 5 s safety net therefore emptied all the headers; the list of conversations became empty, `Conversation` was unmounted, and its input field recreated empty. Fix: `useMatters` keeps the last valid read while re-reading (nothing blinks, nothing is unmounted).

## The bench: the real block, in a real browser, facing the real engine

`tests/ui_pulse/browser/`: `node run.mjs` (the scenarios with the fake model), `node run.mjs real` (with the real guide, tokens), `node run.mjs all`, or scenario names.

**What is tested.** The code of `ui/AgenticInterface.tsx`, without any modification, compiled and mounted in Chrome as Spark mounts it: without parameters, with its default transport and guide. The page of the bench (`page.tsx`) only mounts it. The only concession: in the fake-model scenarios, the property `guideRef` is passed (a documented property of the block) so that its conversations start with the test pilot. The scenario presses real buttons, types in the real field, reads the screen and takes captures.

**What the bench provides to the scenario**, on a separate connection that the block does not see: write the script of the fake model, open a gate (`wait_gate`), delete what the scenario created. The engine accepts only one client per route: the relay of the bench (a small local server that talks to the engine without an `Origin` header, like Electron; the engine refuses other web origins) makes the block and the scenario share a single connection.

**The scenarios.**
- `chat`: start a conversation, see the response arrive without reloading, type slowly for more than ten seconds (the field must keep its element, its focus, its text), a second message, reload the page, delete.
- `waiting`: the pilot is held on a gate; the screen must say that we are waiting ("The pilot is working…", "running" state), then show the response when the gate opens.
- `hierarchy`: a pilot launches a child; indented line, new-message badge on the one that nobody opened, which disappears at reading; deleting the parent deletes the child.
- `guide_search` (real guide): tool cards, visible wait, response naming the capacity, return to rest.
- `guide_child` (real guide): the guide launches a child, the indented line appears, the child reports, the parent says so.
Each also fails on the slightest console error. The bench was verified by removing the fix above: the `chat` scenario then fails.

**Limits.** It is neither Electron nor Spark itself: the `--syn-*` theme variables are absent (fallback values). The default route of the block (`/agentic/ws`) is redirected by the bench to the test route of the same engine: on the working instance, the `/agentic/ws` route closes again at once (probably because another application, Spark, already holds the single connection allowed there).

## Load and performance

Measured with `browser/load.mjs` (fake model: several conversations in parallel that make tool calls and launch sub-continuities) and `browser/loadreal.mjs` (the real guide, in parallel, some of which launch a child). They measure the screen (event-loop lag, long tasks, memory), the traffic of the block (requests, kilobytes) and the engine (dates of the journals).

- **The engine and the screen were not at fault.** 6 conversations that launch 12 children and make 24 tool calls: everything at rest in 4 s, each journal finished in less than 0.9 s, no screen blocking (maximum lag 28 ms). With the real guide, 7 conversations in parallel are at rest in 24 s; the wait before an inference is that of the model (median 2.4 s, maximum 6 s); no error.
- **The block, however, talked too much.** For these 18 conversations: 962 requests and 2.6 MB received, including 188 unsubscriptions and 248 subscriptions, and 123 requests per 10 s at rest. Two causes. First `useMatters` undid and redid all its subscriptions at each change of the list (the cost of an added conversation grew with the number of conversations). Then the safety net re-read all the Matters every 5 s. A burst of reads on the engine can be paid for in exceeded timeouts.
- **Fixed.** Only what enters or leaves the list is subscribed or released; a line of the list is re-read at most once per 400 ms (`throttleMs`); the net (15 s) now concerns only the journal of the open conversation (`watchdogMs`). Same conversations: 322 requests, 0 unsubscriptions, 4 requests per 10 s at rest. Traffic stays high with large journals (2.2 MB for 7 conversations with the real guide, whose tool responses are voluminous): each append re-reads the end of the journal (300 lines). An incremental read by `lineOffset` would be the next step; not done.

## What the bench did not see, and the cuts

The user reported: at times, a prompt must be sent again, and both responses arrive at once. Three gaps between the bench and Spark were filled.

- **Spark does not let the block open its connection.** It creates its own Pulse (`createRendererPulse`: an IPC bridge to a relay of the main process) and passes it to the block with `rootContextId`, `transportUrl`, etc., through the root of the React adapter. The bench now mounts the block that way (`page.tsx`, "spark" mode, with the `createRendererPulse` of Spark and the same root); the "direct" mode (the block alone) remains available through `MODE=direct`.
- **The page of the bench is that of Spark** (November 11): the body with its default margin and a `#root` without a height of its own (`spark/hosts/electron/renderer/index.html`). This is what showed the global scrolling: `height: 100%` of a parent without height gives nothing, the block grew with its content and it was the whole page that scrolled (top bar included). The block is now sized on the remaining height of the window (`useRemainingHeight`: from its position to the bottom of the window, minus the body margin, recomputed at resize) and does not scroll itself: the **top bar stays**, and **three zones each scroll** (the conversation, the metrics, the list of conversations). Bench scenario `scrolling`: conversation of 25 messages, metrics open; the page does not scroll, the top bar does not move when scrolling.
- **The relay of Spark is the real one** (November 11). The bench no longer copies the relay into the page: it compiles `spark/hosts/LocalWebSocketRelay.ts` for Node and runs it in the process of the bench, like the main process of Electron; the page joins it only through messages (`__sparkOpen`, `__sparkSend`, `__sparkClose` one way, `__sparkIpc` for the messages and the states the other way), exactly the bridge of `installElectronTransportBridge`. The relay talks to a grouping server that stands for the engine route (the engine admits only one client per route). A cut of the bench (`drop`) therefore closes the real socket of the real relay, which reconnects with its real rules (attempts, code 4001). What is still not covered: the preload of Electron itself (real IPC) and the Electron windows.
- **The Spark relay tries only 5 times to reconnect** (about 6 s, `LocalWebSocketRelay.ts`), then gives up; it starts again when something is sent.
- **A cut link makes the subscriptions lost, and Pulse does not restore them.** What is written during the cut is never announced: the screen stays late until the next event, that is until the next message sent, which then makes both responses appear. Pulse also keeps a read launched during the cut as "in progress", with no possible response, and hands it to all the following calls.

Fixed in `hooks.ts`: at each return of the link (`subscribeToConnectionState` of Pulse), the block resubscribes each followed Matter, once, and re-reads it **by its own request** (`readDirect`, which puts the result into the snapshot without going through the pending read of Pulse). As long as the link is cut for more than 1.5 s, a banner says so: "Engine connection lost. Reconnecting…" (text to validate). (These workarounds were later removed from `hooks.ts` once Pulse fixed these defects: it now resubscribes and keeps the data during a re-read.)

Scenarios added to the bench: `cut` (the link of the block is cut then comes back), `long_cut` (10 s, beyond the 5 attempts of the relay) and `answer_during_cut` (the response is written during the cut: the screen must show it alone, without a new message). The bench can cut the link of the block alone, the engine and the channel of the scenario staying active. The last scenario failed before the fix.

`stress.mjs` (30 short conversations, sends at random moments, comparison with the journal of the engine) and `stressreal.mjs` (the real guide, a second message in the middle of work) showed no gap without a cut: the defect does come from the cuts.

## Calls refused "busy" and interface settings

When the callee is busy (the Matter loop of the engine answers "loop busy", the full queue of a wrapper answers "busy"), it refuses **before** executing: asking again is harmless. The block therefore asks again for its calls (message, start of a conversation, read mark, controls, reads), at most `busyAttempts` times, with growing and random pauses; beyond that, the person sees the refusal and their text is kept. All the performance constants of the block are properties of the block (`settings`), with unchanged default values: see `performance_settings.md`, §4.

Bench scenarios: `busy_transient` (the relay answers "busy" to the first two attempts of the start of a conversation and to the first three of sending a message: everything goes through anyway, no error on screen; detects the absence of a retry) and `busy_persistent` (the refusal lasts longer than the attempts: it is displayed, the text of the person is kept, the next attempt succeeds). Pure tests: `tests/ui_pulse/settings.test.ts` (7 tests).

## Wiring done

The `/root/agentic` context carries the `agentic` interface (route `/agentic/ws`) and the `ui_config` of the `AgenticInterface` block; the context was reloaded and its wrappers restarted. Verified:
- the `/agentic/ws` route accepts Pulse end to end (creation, subscriptions, events, as on the test route);
- the resolver and the builder of Spark applied to the context read the configuration, find that `ws_path` equals the path of the interface, and build the block.

The block is `ui/AgenticInterface.tsx` (list and hierarchy), `ui/ConversationWindow.tsx` (conversation, draft), `ui/hooks.ts` and `ui/model/`.

## Still to verify

The block opened in Spark (Electron), for real. Spark launches its own engine: I did not start it next to yours.

## What the real journals taught

- A user request transferred by a succession is in two journals: it is shown once, where it was written first, and the summary of each generation serves as a separator.
- A processing that fails three times then is set aside writes four error lines: it is a single alert, with the number of attempts.
- A message that a child sends to its parent is, in the journal of the child, a `pilot` line addressed to a continuity: it is not "new" for the user.
- The real guide was wrong once (a `read.capacity` without `cap_name`) before correcting itself: the tool card shows the error.

## Tests

- The pure module (journal to view, tree, "new") is tested with real recorded journals: each kind of line, a succession, a child, an error.
- The marking capacity has its contract tests like the other agentic capacities.
- The block is verified by starting it through Spark against the real engine, with a real conversation of the guide.

## The metrics of a conversation

**Decision of the architect: tokens, not money, and tokens that take the cache into account.** No prices, no cost in currency.

**Why not the sum of the contexts.** At each inference the pilot sends back the whole conversation. Adding the `input_tokens` of all the inferences gives an enormous and misleading figure: almost everything is read back from the cache, for a marginal cost. What really costs is what is **new** at each inference. The central metric is therefore the cost per inference, broken down:

| Per inference | Meaning |
|---|---|
| **Read from cache** | tokens of the prefix already in cache (cheap) |
| **Written to cache** | tokens added to the prefix and cached at this inference (the new part of the conversation) |
| **Uncached input** | tokens processed at full price (in principle the very end of the request: the novelty of this turn) |
| **Output** | tokens produced by the model |

For the conversation (and cumulated with its children):
- **Real cost in tokens** = written to cache + uncached input + output. This is the figure to look at, not the sum of the contexts.
- **Read from cache**, shown separately: it is what the cache spares; with the **cache rate** (read ÷ total context of the inference).
- **Current context** of the last inference and its share of the model window (context pressure), with the curve over the inferences: it says when a compaction is near.
- **Inferences**: number, model duration (average, maximum). **Time**: elapsed, and the share in the model, in the tools, waiting for a person or a child. **Tools**: calls per capacity, failures, average duration. **Messages and structure**: messages of the person and of the pilot, generations (successions), errors, children and their state.

The data comes from the journal; the token detail comes from the `inference_measured` event (see `schema_matter_continuity.md`). The summary line sits under the title of the window ("12 inferences · 3.1 k new tokens · 54 k read from cache · 41 s"); the "Metrics" button opens the panel on the right; the list shows nothing more. The pure module is `ui/model/metrics.ts`, the panel `ui/Metrics.tsx`.

## Open decisions

- **A single client per route (engine).** The local WebSocket routes (wrappers, Spark, the local block) keep one connection per route: a new connection replaces the old one, which receives the close code 4001 and does not reconnect. One local interface at a time needs nothing more. Several simultaneous clients are a web-mode need, and the public web entry (`communication/public_websocket.go`) and the browser bridge already give each connection its own channel, with limits and cleanup at close. Not needed for local use; to revisit only if a scenario uses the local routes with several clients.
- **The texts of the interface** (state labels, wait lines, connection banner, buttons, separators, metrics and input labels) are provisional and wait for the validation of the architect.
- **A periodic restart of the continuities that have an unprocessed mailbox entry**: to decide after measuring (a wake-up lost because the process stopped is today recovered only by a new message or by `pilot.recover`).
