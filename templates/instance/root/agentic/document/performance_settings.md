# Performance settings

## Status

Principle established by the architect on 11 November 2026: **performance adapts through configuration, not code**. Every limit, timeout, queue size, or attempt count that affects load must be configurable for an instance without source changes or recompilation. This document lists those settings layer by layer: where they are configured, their defaults, what they protect, what they cost, and what measurements revealed. A setting that remains a constant is identified as such with the decision still required.

The measurements below come from the load investigation of November 11, 2026 (scripts in `code/agentic_core/tests/load/`, with the results summarized in its `README.md`).

## 1. Engine settings

These live in `root/context.json` and each context's `context.json`. The engine reads configuration **at startup**, so changes apply after restart.

| Setting | Location | Default | Bound | Measurement |
|---|---|---:|---|---|
| `ingress_buffer` | per-context `engine_config.communication` | **4096**, formerly constant 16 | slots in the channel fed by every context interface and drained by one goroutine | 16 slots represented 5.5 ms at 2,900 messages/s and caused wrapper disconnection at 100 concurrent sends. At 4096, occupancy remained below 75% through 400 concurrent sends |
| `ingress_wait_ms` | root `engine_config.communication.websocket_listener` | **5000**, formerly immediate close | time a route waits for a slot before closing the connection | waiting applies backpressure through the sender's TCP window instead of losing all in-flight work; no wait observed at 400 concurrent sends |
| `max_handlers` | `engine_config.matter` | **512**, formerly constant 64 | concurrent Matter handlers; excess receives immediate `busy` | 64 was reached, causing 31 `loop busy` refusals in 11 s at 100 concurrent sends. At up to 256 KiB inline per handler, 512 permits a 128 MiB worst case |
| `max_concurrent_jobs` | `engine_config.execution` | 1024 | concurrent user jobs in the execution family | not reached during measurement |
| `ping_interval_ms`, `write_timeout_ms`, `read_limit` | `engine_config.communication.websocket_listener` | 20,000; 5,000; 4 MiB | connection health and writes | the 5 s write bound is a possible close cause, not observed |
| `inline_max_bytes`, `max_upload_bytes`, `lease_ttl_ms` | `engine_config.matter` | 256 KiB; 256 MiB; 30 s | inline serving size and HTTP lease beyond it | a pilot journal quickly exceeds 256 KiB and then uses a lease |
| `default_invoke_timeout_ms`, `ready_timeout_ms`, `stop_timeout_ms` | wrapper entries under `engine_config.execution` | 0, meaning none; 2000; 1000 | wrapper call, startup, and shutdown timeouts | `agentic_core` starts on demand without supervision |
| trace `enabled`, `level`, `ring_capacity`, `flush_every_n`, `flush_every_interval`, `segment_max_bytes` | `engine_config.trace` | disabled; `normal`; 64; 8; 20; 1 MiB | recorded trace volume | **a 64-entry buffer loses lines under bursts**. Load measurement uses `ring_capacity` 65535 and `flush_every_n` 1, producing about 636 MiB of segments in one morning |

## 2. Wrapper library settings

These are settings of the wrapper library, shared by every wrapper of the instance and identical in the Python, Go and C++ runtimes. They are set in wrapper `run.env` inside `context.json`. The table is generated from `wrapper/vectors/settings.json` (`python3 -m wrapper.settings table settings.json`, from `wrapper/python/lib`); an invalid value of a **strict** setting stops the wrapper at start, of a **tolerant** one it is ignored. `Wrapper.md` §14.1.3 describes them.

| Variable | Default | Min. | Unit | Invalid value | What it sets |
|---|---|---|---|---|---|
| `BRIQUE_MAX_CONCURRENCY` | 16 | 1 | works | stops the start | Engine-routed capacity and Matter works that run at the same time in the wrapper (the execution slots). |
| `BRIQUE_QUEUE_LIMIT` | 256 | 1 | works | stops the start | Works that may wait for a slot; beyond it a call is refused 'busy' before it runs. |
| `BRIQUE_TRANSIT_MAX_BYTES` | 67108864 | 1 | bytes | stops the start | Largest serialized parameter sent as a transit file; above it a transient Matter is used. |
| `BRIQUE_TRANSIT_TTL_SECONDS` | 3600 | 1 | seconds | stops the start | Lifetime of an abandoned transit reference before it is swept. |
| `BRIQUE_BUSY_ATTEMPTS` | 8 | 1 | attempts | ignored | Attempts in all (the first included) of a call refused because its callee is busy. |
| `BRIQUE_BUSY_BASE_MS` | 20 | 0 | ms | ignored | Pause before the second attempt of a call refused 'busy'; doubled at each attempt, times a random factor from 0.5 to 1.5. |
| `BRIQUE_BUSY_CAP_MS` | 1000 | 0 | ms | ignored | Ceiling of the pause between two attempts of a call refused 'busy'. |
| `BRIQUE_LEASE_TIMEOUT_MS` | 30000 | 1 | ms | ignored | Time allowed to one HTTP transfer through a Matter lease. |
| `BRIQUE_LEASE_RETRIES` | 5 | 1 | reads | ignored | Reads of a Matter whose lease went stale (the substance changed meanwhile) before giving up. |
| `BRIQUE_CAS_ATTEMPTS` | 12 | 1 | attempts | ignored | Attempts of a read-recompute-write that keeps losing the race on the revision. |
| `BRIQUE_CAS_PAUSE_BASE_MS` | 2 | 0 | ms | ignored | After a lost race, a pause of random length up to this, doubled at each attempt. |
| `BRIQUE_CAS_PAUSE_CAP_MS` | 50 | 0 | ms | ignored | Ceiling of the pause after a lost race: writers that lost the same round do not come back together. |
| `BRIQUE_HEARTBEAT_MS` | 10000 | 50 | ms | ignored | Period of the `running` acknowledgement of a long capacity (it renews the inactivity budget of whoever waits for it). |

What measurements showed:

| Setting | Measurement |
|---|---|
| `BRIQUE_MAX_CONCURRENCY` | Response waits yield their slot, and so do waits for a critical-section lock or the inference window; slots were not the bottleneck of `agentic_core`. `llm_llm` sets **64**: an inference holds its slot for the whole HTTP call to the provider, and the 17th simultaneous inference was refused "busy" |
| `BRIQUE_QUEUE_LIMIT` | 256, overridden to **4096 in `root/agentic/context.json`** after the next engine restart. 272 equals 16 + 256: at 300 concurrent sends, 28 were refused as `busy` and wake-ups were lost. At 4096, 400 concurrent sends pass |
| `BRIQUE_BUSY_*` | the retry of a call refused "busy" moved from `agentic_core` to the library: same defaults (8 attempts, 20 ms doubled up to 1 s, factor from 0.5 to 1.5). A wake-up insists longer (`BRIQUE_AGENTIC_WAKE_ATTEMPTS`) |
| `BRIQUE_CAS_*` | a read-recompute-write that loses the race starts again after a random pause; the `continuities` Structure is no longer written by racing, since a short lock serializes the writers of the wrapper, so these settings now cover external writers |

What a running wrapper really uses is written to its log at start (`settings_not_default`) and to the dump of `kill -USR1 <pid>`, so an environment variable that the engine did not pass on shows.

## 3. `agentic_core` settings

These variables share the wrapper environment and use the `BRIQUE_AGENTIC_<NAME>` prefix. They are declared in the settings registry of the library (`substrate/settings.py`), with **exactly the same defaults** as the former constants, so behavior is unchanged until configured. A value of the wrong numeric type or below its minimum is ignored. The table is generated from the declarations (`python3 -m substrate.settings`, from `code/agentic_core`).

| Variable | Default | Min. | Invalid value | What it sets |
|---|---|---|---|---|
| `BRIQUE_AGENTIC_DEFAULT_TIMEOUT_S` | 60.0 | 1.0 | ignored | inactivity budget of a call (renewed by every `running` of the callee) |
| `BRIQUE_AGENTIC_WAKE_ATTEMPTS` | 40 | 1 | ignored | attempts in all of a wake refused "busy": of the order of 35 s, a waiting request costs one task |
| `BRIQUE_AGENTIC_RESERVATION_TTL_S` | 30.0 | 0.0 | ignored | how long a reservation waits for the loop before the request becomes a normal message |
| `BRIQUE_AGENTIC_MAX_OBJECTS` | 512 | 1 | ignored | continuities kept in memory by one wrapper; the least recently used goes first, archived ones included |
| `BRIQUE_AGENTIC_CHUNK_LINES` | 200 | 1 | ignored | lines read per matter.read while (re)building the object |
| `BRIQUE_AGENTIC_STORE_MAX_ATTEMPTS` | 12 | 1 | ignored | attempts of a journal append that keeps finding the journal moved on, before it fails |
| `BRIQUE_AGENTIC_INPUT_TIMEOUT_S` | 86400.0 | 1.0 | ignored | how long a request lives without an answer; the timeout_s of the call takes precedence |
| `BRIQUE_AGENTIC_INPUT_POLL_S` | 1.0 | 0.05 | ignored | safety net while waiting: an answer written in this process wakes the wait first |
| `BRIQUE_AGENTIC_POLL_S` | 5.0 | 0.05 | ignored | safety net while waiting: liveness.notify wakes the run first, re-reading the Matters each second would buy nothing |
| `BRIQUE_AGENTIC_AWAIT_TIMEOUT_S` | 600.0 | 1.0 | ignored | bound of an awaited intention's Response |
| `BRIQUE_AGENTIC_WAIT_TIMEOUT_S` | 3600.0 | 1.0 | ignored | how long a wait group waits for answers that never come; limits.wait_timeout_s lowers or raises it |
| `BRIQUE_AGENTIC_DELIVERY_ATTEMPTS` | 3 | 1 | ignored | a Response that mailbox.send does not take is offered again this many times |
| `BRIQUE_AGENTIC_DELIVERY_BACKOFF_S` | 0.2 | 0.0 | ignored | pause before a Response that mailbox.send did not take is offered again (doubled at each attempt) |
| `BRIQUE_AGENTIC_MAX_CHAIN_STEPS` | 50 | 1 | ignored | inferences allowed in the chain of ONE message (projection / failure resumptions included) |
| `BRIQUE_AGENTIC_LAUNCH_WAIT_TIMEOUT_S` | 3600.0 | 1.0 | ignored | how long a launch waits for its pilot to finish the task; reaching it is a refusal |
| `BRIQUE_AGENTIC_LAUNCH_POLL_S` | 1.0 | 0.05 | ignored | safety net while waiting: a run of this process that ends wakes the wait first |
| `BRIQUE_AGENTIC_LAUNCH_SETTLE_S` | 0.3 | 0.0 | ignored | how long the continuity must stay at rest before the task counts as finished |

The settings that `agentic_core` used to carry for the retry of a busy callee (`BUSY_ATTEMPTS`, `BUSY_BASE_S`, `BUSY_CAP_S`), for the guarded write (`CAS_MAX_ATTEMPTS`, `CAS_PAUSE_BASE_S`, `CAS_PAUSE_CAP_S`), for the Matter leases (`LEASE_TIMEOUT_S`, `LEASE_RETRIES`) and for the heartbeat (`HEARTBEAT_S`) no longer exist: those mechanisms moved into the wrapper library, and their settings are the `BRIQUE_BUSY_*`, `BRIQUE_CAS_*`, `BRIQUE_LEASE_*` and `BRIQUE_HEARTBEAT_MS` of section 2 (durations there are in milliseconds). Setting one of the old `BRIQUE_AGENTIC_` names has no effect.

Per-pilot settings belong in the pilot definition rather than the environment: `limits.max_inferences_per_message`, `limits.context_tokens`, `limits.wait_timeout_s`, and access policy.

## 4. Interface settings

The interface receives settings through the block's `settings` property in `ui/settings.ts`. With no property supplied, defaults match the former constants. A non-finite number or value below its minimum is ignored. Spark may pass them as it passes `rootContextId` and `transportUrl`.

| `settings` property | Default | Min. | Controls |
|---|---:|---:|---|
| `listThrottleMs` | 400 | 0 | at most one read of each list Matter during this interval |
| `journalTailBytes` | 122,880 | 4,096 | journal tail bytes read (not lines: a line can be tens of kilobytes and the browser bridge carries at most 180 KiB per answer); Pulse drops events when two keys follow the same Matter, so there is one value per Matter |
| `watchdogMs` | 15,000 | 1,000 | reread of the open conversation even without an event |
| `listPollMs` | 3,000 | 500 | reread interval for the Continuity list |
| `busyAttempts` | 6 | 1 | attempts for a call refused because the callee is busy, including the first |
| `busyBaseMs`, `busyCapMs` | 100; 2,000 | 0; 0 | delay before the second attempt, doubled to the cap with a random factor from 0.5 to 1.5 |

The block retries a call refused as `busy`: sending a message, starting a conversation, marking a read cursor, controls, list reading, and rereading after interruption. A call that did not answer is never retried because it may have executed; every other error is shown to the person. When refusal persists after all attempts, it is displayed and **the person's text remains in the field**.

## 4a. Spark relay and Pulse transport

| Option | Default | Min. | Controls |
|---|---:|---:|---|
| `LocalWebSocketRelay.maxRetries` | **12**, formerly 5 or about 6 s | 0 | reconnection attempts after interruption before giving up. The count resets after every successful connection; 12 attempts with delays starting at 0.2 s and doubling to 30 s tolerate about 2 min 45 s of engine unavailability |
| `baseRetryDelayMs`, `maxRetryDelayMs` | 200; 30,000 | 0; 0 | delay before first attempt, doubled and capped |
| `WebSocketTransport` in direct Pulse mode outside Spark: same settings | 5; 200; 30,000 | | unchanged, because the architect selected only the Spark relay increase |

Neither reconnects after close code **4001**, meaning the route was taken by another client. Invalid settings are ignored.

## 5. Test infrastructure

The fake test model used to record each call in one Matter through read-rewrite with conflict retry. Forty concurrent calls took 62–92 s instead of 2 s. **Fixed:** calls arriving together are written in one operation by the first arrival, and calls waiting for that write yield their execution slots. Without yielding, 16 waiting calls occupied all slots and blocked the writer. On the real engine with recording enabled: 40 concurrent calls complete in 1.9 s and 100 in 5.8 s. The `llm_calls` journal format, numbering, and guarantee that a call is in the journal before its Response leaves remain unchanged. Remaining limit: a Matter above the engine's 256 KiB `inline_max_bytes` limit requires a lease, which the fake model cannot read; each entry is about 5 KiB.

## 6. Applying a setting

1. **Engine:** modify `root/context.json` or the context's `context.json`, then restart the engine.
2. **Wrapper and `agentic_core`:** add the variable to wrapper `run.env` in the context's `context.json`, for example `"BRIQUE_BUSY_ATTEMPTS": "12"` (library) or `"BRIQUE_AGENTIC_MAX_CHAIN_STEPS": "80"` (`agentic_core`), then restart the engine. It retains configuration read at startup; `wrapper.restart` reloads code but not the environment.
3. **Verify:** the log of the wrapper at start lists the settings that are not at their default (`settings_not_default`), and `kill -USR1 <wrapper pid>` writes them again in `$TMPDIR/brique_wrapper_tasks_<pid>.txt` (or `agentic_core_tasks_<pid>.txt` for `agentic_core`); `ps eww -p <wrapper pid>` displays the raw environment of the process.

## 7. Rule for future work

Every new load-related limit, timeout, or attempt count **begins as a setting** with a default, a name, a row in this table, and a test covering the default and rejection of invalid values. A constant that bounds load without configuration is a defect to report.
