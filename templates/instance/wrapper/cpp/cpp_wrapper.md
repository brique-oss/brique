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
  - bounds global work while preserving FIFO order per binding or matter
  - returns Brique `response` envelopes

## Layout

- `code/main.cpp`
- `code/bindings.hpp`
- `code/bindings.hpp.example`
- `code/wrapper/*.hpp`

The implementation is header-only on purpose so a minimal compiled wrapper can be built with a simple command such as:

```bash
c++ -std=c++17 -O2 -pthread main.cpp -lcurl -o sandbox_cpp
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

## Runtime guarantees

- current Brique response/status and structured-error envelopes
- `BRIQUE_MAX_CONCURRENCY` (default `16`) and `BRIQUE_QUEUE_LIMIT` (default `256`)
- responses and `wrapper_stop` outside the bounded work scheduler
- FIFO ordering per capacity or matter with unrelated parallelism
- strict binding validation before connection
- 4 MiB control-message limit, fragmented WebSocket reads and bounded startup retries
- matter commit before notification, relative notification origins and owner-checked subscriptions
- outbound correlation plus `running`-renewed inactivity waits
- generic internal errors carrying a public `error_id`, with details kept in local logs

Build and run the runtime test with:

```bash
c++ -std=c++17 -pthread -Wall -Wextra -pedantic tests/runtime_test.cpp -lcurl -o runtime_test
./runtime_test
```

## Placement and transit

`ParamsView::value_raw(name)` resolves inline JSON, wrapper-local `@object`, shared-filesystem `@file`, and transient cross-instance `@matter`; `string_value(name)` uses that same resolution. The destructor releases references the capacity did not consume. Semantic Matters remain explicit and are never consumed by this path.

Hosted code calls `Runtime::emit(value_raw, TransitTarget{...})` or `Runtime::call(target, params)` with ordinary JSON fragments. The runtime selects the carrier from the address, creates transient destination Matters through HTTP or HTTPS leases when necessary, verifies `brique.transient`, revision and size, and guarded-deletes after successful decoding. The C++ runtime links to libcurl (`-lcurl`) so HTTPS keeps normal certificate verification. `emit_intention` and `emit_and_wait` remain available for raw non-capacity intentions. C++ calls currently stay engine-routed; the Python-only optimization is direct in-process execution, not parameter transit.
