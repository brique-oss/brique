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

This directory provides a minimal generic C++ wrapper runtime for Brique.

It mirrors the Python wrapper model at a smaller surface:

- one `main.cpp`
- one static bindings file in code
- one wrapper runtime that:
  - loads `BRIQUE_*` execution environment
  - resolves the boundary websocket from `context.json`
  - connects to the boundary context
  - emits `wrapper_ready`
  - receives `wrapper_stop`
  - dispatches wrapper-backed user capacities from a static registry
  - returns Brique `response` envelopes

## Layout

- `code/main.cpp`
- `code/bindings.hpp`
- `code/bindings.hpp.example`
- `code/wrapper/*.hpp`

The implementation is header-only on purpose so a minimal compiled wrapper can be built with a simple command such as:

```bash
c++ -std=c++17 -O2 main.cpp -o sandbox_cpp
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

## Current Intent

This first C++ wrapper is intended to support `Execution` `N6` build/run testing with:

- one real compiled artifact
- one real wrapper process
- one real readiness signal
- one or two real wrapper-backed capacities

It does not yet implement the full Python wrapper surface such as:

- matter bindings
- outbound intentions
- correlator wait API
- subscription/event routing

Those can be added later if C++ wrappers need the same functional depth as Python wrappers.
