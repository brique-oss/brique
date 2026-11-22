<!--
Copyright 2026 Nicolas Cassan

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

# C++ Wrapper

## Scope

This directory provides the generic C++ wrapper runtime for Brique.

It mirrors the Python wrapper's protocol and execution guarantees:

- one `main.cpp`
- one static bindings file in code
- one wrapper runtime that:
  - loads `BRIQUE_*` execution environment
  - resolves the boundary websocket from `context.json`
  - connects to the boundary context
  - emits `wrapper_ready`
  - receives `wrapper_stop`
  - dispatches wrapper-backed user capacities from a static registry
  - dispatches wrapper-backed matter reads, writes and subscriptions
  - emits fire-and-forget or awaited outbound intentions with correlation
  - bounds global work while preserving FIFO order per matter and per serial capacity
  - returns Brique `response` envelopes

## Layout

- `code/main.cpp` (per wrapper)
- `code/bindings.hpp` (per wrapper)
- `code/bindings.hpp.example`
- `lib/wrapper/*.hpp` (runtime library, shared by every C++ wrapper of the instance at `<instance>/wrapper/cpp/lib/`)
- `lib/tests/runtime_test.cpp`

The implementation is header-only on purpose so a minimal compiled wrapper can be built with a simple command such as:

```bash
c++ -std=c++17 -O2 -pthread -I<instance>/wrapper/cpp/lib main.cpp -lcurl -o sandbox_cpp
```

## Binding Model

Bindings are static and code-defined.

The canonical shape is:

```cpp
inline wrapper::BindingsFragment build_bindings(wrapper::Runtime&) {
    wrapper::BindingsFragment fragment;
    fragment.capacities.push_back(wrapper::CapacityBinding{
        "",
        "cpp.echo",
        [](const wrapper::ParamsView& params) {
            return std::string("{\"echo\":") + wrapper::json::quote(params.string_value("message")) + "}";
        },
    });
    return fragment;
}
```

The key is:

- relative context
- Brique capacity name

The value is:

- a live callable compiled into the wrapper process

A wrapper-owned matter uses the same static fragment:

```cpp
auto state = std::make_shared<std::string>();
fragment.matters.push_back(wrapper::MatterBinding{
    "", "state",
    [state] { return *state; },
    [state](const std::string& value_raw) { *state = value_raw; },
    true,
});
```

Hosted code receives the `Runtime&` passed to `build_bindings` and can call `emit_intention(...)` or `emit_and_wait(...)`. Outbound params and matter lists are supplied as JSON strings, while the runtime fills source address and parent/root correlation from the currently executing capacity when omitted.

The runtime strictly unwraps `brique.wrapper/1` and carries its opaque
delegation handle in the worker's `thread_local ExecutionContext`. Responses,
matter operations, notifications, and child calls reuse the frame; child
identity is empty and root/parent correlation is forced from the current call.
The handle never enters hosted parameters. Raw envelopes remain available for
handle-free local traffic.

## Runtime guarantees

- current Brique response/status and structured-error envelopes
- `BRIQUE_MAX_CONCURRENCY` (default `16`) and `BRIQUE_QUEUE_LIMIT` (default `256`); all the numeric settings are declared once (`settings.hpp`, `describe_settings`, `declare_setting`) and are the same as in the other runtimes (Wrapper.md §14.1.3)
- responses and `wrapper_stop` outside the bounded work scheduler
- FIFO ordering per matter; capacities run their calls in parallel unless a binding sets `serial = true`, which runs one call at a time in arrival order
- strict binding validation before connection
- 4 MiB control-message limit, fragmented WebSocket reads and bounded startup retries
- matter commit before notification, relative notification origins and owner-checked subscriptions
- outbound correlation plus `running`-renewed inactivity waits; a capacity sends its own with `runtime.notify_running()` or, for a heartbeat until the end of the scope, `wrapper::Keepalive alive(runtime);` (`BRIQUE_HEARTBEAT_MS`; never automatic)
- a call refused `busy` is asked again with bounded backoff and jitter (`emit_and_wait`; `OutboundParams::busy_attempts` to insist longer)
- an error response is a `CallError` (`code()`, `details()`, `reason()`, `kind()`, `outcome_unknown()`)
- `SIGUSR1` writes `$TMPDIR/brique_wrapper_tasks_<pid>.txt` (slots and settings not at their default; threads have no portable stack to read)
- generic internal errors carrying a public `error_id`, with details kept in local logs

Build and run the runtime test with:

```bash
cd lib
c++ -std=c++17 -pthread -Wall -Wextra -pedantic -I. tests/runtime_test.cpp -lcurl -o runtime_test
./runtime_test
```

## Shared mechanisms

Wrapper.md §14.1 describes the mechanisms the three runtimes share; in C++:

- `wrapper::CriticalSection section(runtime.scheduler(), {"key", ...});` holds the lock of every key for its scope (sorted, a wait gives the execution slot back)
- `wrapper::MatterClient::over(runtime, context)` (`#include "wrapper/matter_client.hpp"`) reads and writes the Matters of a context with raw JSON values: `read`, `read_text`, `read_many`, `read_lines`, `stat`, `exists`, `objective`, `create`, `write(id, content, expected_revision)`, `append_lines`, `remove`, `update(id, compute)`; the errors are `CallError` with `is_revision_mismatch`, `is_line_count_mismatch`, `is_busy`, `is_not_found`, `is_outcome_unknown`
- `runtime.scheduler().stats()` is a snapshot of the execution slots

## Placement and transit

`ParamsView::value_raw(name)` resolves inline JSON, wrapper-local `@object`, shared-filesystem `@file`, and transient cross-instance `@matter`; `string_value(name)` uses that same resolution. The destructor releases references the capacity did not consume. Semantic Matters remain explicit and are never consumed by this path: `ParamsView::matter(name)` returns a `MatterRef` for an incoming semantic Matter (`intention.matters`, delivered under its `repr`, or its `id` when `repr` is missing) or for a parameter or result field holding exactly the wire form `{"context","id"[,"mode"]}`, and throws `TransitError` with reason `not_found` or `type_mismatch`. `value_raw(name)` of an incoming Matter returns that wire form, and `MatterRef::json()` produces it for a returned payload. `Runtime::call(target, params, matters)` sends each `MatterRef` in `intention.matters` with its name as `repr`. A `MatterRef` nested in a structured value is data (Wrapper.md §10.1).

Hosted code calls `Runtime::emit(value_raw, TransitTarget{...})` or `Runtime::call(target, params)` with ordinary JSON fragments. The runtime selects the carrier from the address, creates transient destination Matters through HTTP or HTTPS leases when necessary, verifies `brique.transient`, revision and size, and guarded-deletes after successful decoding. The C++ runtime links to libcurl (`-lcurl`) so HTTPS keeps normal certificate verification. `emit_intention` and `emit_and_wait` remain available for raw non-capacity intentions. C++ calls currently stay engine-routed; the Python-only optimization is direct in-process execution, not parameter transit.
