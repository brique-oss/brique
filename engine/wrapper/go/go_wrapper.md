# Go Wrapper

## Scope

This directory provides the generic Go wrapper runtime for Brique.

It mirrors the Python wrapper model at full depth:

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
    matter.go
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
                Handler: func(params wrapper.Params) (any, error) {
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

No external dependencies beyond the standard library and `golang.org/x/net/websocket` or `nhooyr.io/websocket`.
