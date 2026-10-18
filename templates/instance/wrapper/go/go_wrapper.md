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

# Go Wrapper

## Scope

This directory provides the generic Go wrapper runtime for Brique.

It mirrors the Python wrapper's protocol and execution guarantees:

- one `main.go` entry point
- one `bindings.go` for user-defined capacity and matter bindings
- one `wrapper/` package that implements the full runtime membrane

## Layout

```
code/
  main.go
  bindings.go
  bindings.go.example
  wrapper/
    constants.go
    models.go
    transport.go
    correlator.go
    registry.go
    executor.go
    scheduler.go
    errors.go
    outbound.go
    router.go
    runtime.go
```

## Binding Model

Bindings are static and code-defined.

The canonical shape is:

```go
func BuildBindings(rt *wrapper.Runtime) wrapper.BindingsFragment {
    return wrapper.BindingsFragment{
        Capacities: []wrapper.CapacityBinding{
            {
                Context: "",
                Name:    "my_capacity",
                Handler: func(params *wrapper.Params) (any, error) {
                    return map[string]any{"result": params.String("input")}, nil
                },
            },
        },
        Matters: []wrapper.MatterBinding{
            {
                Context:       "",
                Name:          "my_matter",
                Read:          func() (any, error) { return state.Read(), nil },
                Write:         func(v any) error { return state.Write(v) },
                NotifyOnWrite: true,
            },
        },
        Shutdown: []func(){},
    }
}
```

The key per binding is:

- relative context path
- Brique element name

The value is a live Go function compiled into the wrapper process.

## Build

A wrapper binary is built with:

```bash
go build -o mcp_server_go ./code
```

No external dependencies beyond the standard library and `golang.org/x/net/websocket`.

## Runtime guarantees

- current Brique `response.status` and structured-error contract
- `BRIQUE_MAX_CONCURRENCY` (default `16`) and `BRIQUE_QUEUE_LIMIT` (default `256`)
- FIFO ordering per addressed capacity or matter, with parallel unrelated bindings
- responses and `wrapper_stop` outside the work queue
- strict binding validation before connection
- 4 MiB control-message ceiling and five bounded connection attempts
- matter commit before notification, relative notification origins and isolated delivery failures
- idempotent subscriptions with owner-checked unsubscription
- `running` responses renew the outbound inactivity budget
- correlation and `intention.matters` are preserved by the message model

Run validation with `go test -race ./...`.

## Placement and transit

Handlers receive `*Params`. Existing typed accessors (`String`, `Float`, `Bool`) remain; `Value(name)` additionally resolves inline values, wrapper-local `@object`, shared-filesystem `@file`, and transient cross-instance `@matter`. Semantic entries from `intention.matters` return a `MatterRef` and are never read implicitly.

`params.Call(Address{Context: ..., Cap: ..., Type: TypeUser}, values)` emits each ordinary value according to the destination and routes the call through the engine. `params.Emit(value, address)` exposes the same selection when emission and calling are separated. The runtime creates transient Matters in the destination, uploads through the HTTP lease, verifies `brique.transient`, revision and size at consumption, then guarded-deletes them. Unread references are released when the handler returns. `Outbound()` remains available for raw non-capacity intentions.
