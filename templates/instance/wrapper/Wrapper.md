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

# Wrapper Specification

## 1. Purpose

A wrapper is the unique runtime membrane between Brique and a concrete execution runtime.

It exists to:

* receive Brique messages from the engine
* resolve addressed capacities and matters to live runtime bindings
* execute code inside the local runtime
* access and mutate runtime state
* wrap results into Brique responses
* emit new intentions back into Brique when required

A wrapper is not a second engine.
A wrapper is not a context family.
A wrapper is not a router of meaning.
A wrapper is not the business code itself.

It is the bridge between:

* Brique execution semantics
* ordinary code execution

---

## 2. Position in Brique

The wrapper belongs to the execution substrate.

The engine:

* resolves intentions
* selects execution style
* routes through communication

When targeting a wrapper:

* the engine does not execute code directly
* it sends a request to the wrapper

The wrapper:

* resolves runtime bindings
* executes code
* returns a response

---

## 3. Core Model

For one wrapper name, there is exactly one runtime process per boundary context instance.

This process owns:

* one communication link with the engine
* one execution bootstrap
* one runtime registry of callable capacity bindings
* one runtime registry of readable/writable matter bindings
* one concurrency control system
* one local lifecycle (start / stop)

---

## 4. Boundary Context

A wrapper is anchored to a boundary context.

This context declares:

* the wrapper runtime in `execution`
* the communication boundary in `communication`

This is the only entry point for the engine.

All wrapper traffic goes through this context.

`execution.wrappers` resolution is strictly local to the `ExecutionLoop` of the context that declares it — each context resolves wrapper names only against its own local execution config, with no inheritance from a parent context, however architecturally close. A wrapper declared in a parent is never invocable by name from a capacity living in a child context — declaring `execution.wrappers` in `parent` does not make that wrapper reachable by a capacity declared under `parent/child`. This is a hard, non-negotiable constraint of the engine, not a configuration gap to work around: a capacity that must run inside a shared wrapper process has to be declared in the same context that owns the wrapper (see §11.1 for the actual pattern — an aggregator module in the owning context loading code contributed by other contexts, not a wrapper reference crossing context boundaries).

---

## 5. Subcontexts

Subcontexts are projections of the same runtime.

They express:

* contextualized capacities
* contextualized matters
* contextualized meaning
* contextualized traceability

They do NOT define:

* independent runtimes
* independent processes
* communication endpoints
* lifecycle control

All execution is routed through the parent wrapper runtime.

---

## 6. Code Model

The hosted code is ordinary code.

It may contain:

* functions
* classes
* methods
* callable objects
* global state
* services
* background workers
* threads or async tasks

The code should remain agnostic of Brique.

It should not need to know:

* transport protocols
* message envelopes
* routing logic
* execution families

Wrapper runtimes may be implemented in different host languages.

Current generic implementations in this repository are:

* Python: `wrapper/python`
* Go: `wrapper/go`
* C++: `wrapper/cpp`

Section 15 of this document is Python-specific and gives the concrete, mandatory implementation pattern for that language. For Go or C++, the language-specific docs (`wrapper/go/go_wrapper.md`, `wrapper/cpp/cpp_wrapper.md`) apply on top of everything in this document up to §14.

---

## 7. Brique Projection

Brique projects only:

* capacities
* matters

Descriptors define:

* Brique identity
* context
* wrapper ownership
* semantics

Descriptors do NOT define runtime symbols.

For capacities, the functional contract is also the source of truth for:

* declared params
* expected `@matter` inputs
* declared output matter effects

Wrapper code must therefore align with the capability contract, not with hidden ad hoc runtime conventions.

---

## 8. Binding Principle

Bindings live in code, not in JSON.

Canonical mapping:

(context_relative_path, brique_name) → live runtime binding

Bindings resolve to:

* functions
* methods
* objects
* getters/setters

Resolution strategy:

* dictionary lookup

The target must be a live symbol.

---

## 9. Capacity Binding

A capacity binding connects a Brique capacity to a callable.

The wrapper must:

* resolve the binding
* extract params according to the capability contract
* extract `@matter` refs according to the capability contract
* invoke the callable
* capture result
* return a Brique response

The wrapper does not perform semantic planning.

The wrapper should pass hosted code the public contract it declared.
It should not silently invent extra business inputs.

If hosted code needs additional information or data outside that contract, it should obtain it explicitly through native in-scope access or outbound intentions.

### 9.1 Long-Running Capacities

A capacity whose work takes long enough that the caller might otherwise time out waiting is not a special kind of capacity — it is an ordinary capacity that, while still running, may call `runtime.notify_running()` (Python) zero, one, or many times before it returns its final result normally.

`notify_running()` sends an intermediate response with `status: "running"` for the capacity call currently executing, correlated by the same `intention_id` as the eventual final response. It is a no-op when called outside of a capacity execution, so it is always safe to call. It does not replace the final response — the hosted function still returns its result at the end exactly as any other capacity does, and that return value becomes the final `ok`/`error` response as usual.

On the receiving side, both the engine's DSL-internal invoke mechanism and the Pulse client runtime treat `status: "running"` as "still waiting, not done" — they keep listening for the eventual final response instead of settling on the intermediate one, and they push their timeout budget back out on each `running` received, so a capacity that keeps checking in can run arbitrarily long; only silence beyond the timeout is fatal.

There is no separate mechanism to declare a capacity as "long-running" in its contract — any `interpreted`/`compiled` capacity may call `notify_running()` from inside its hosted code when its own execution time warrants it. It is purely a runtime behavior of the hosted function, not a descriptor-level property.

---

## 10. Matter Binding

A matter binding connects a Brique matter to runtime state.

Supported modes:

* read
* write
* read/write

A matter may map to:

* variable
* attribute
* property
* getter/setter
* service state

A matter binding means the wrapper can access that matter natively inside its own wrapper root.
It does not mean every capacity input matter is injected as a native object automatically.

Matter bindings are static: they are declared in the binding table and loaded once at bootstrap. No semantic Matter is created and bound at runtime. A temporary object travels as `@object` or `@file` inside an instance; across an instance boundary the runtime may create an unbound transient Matter that is consumed and deleted (§17.3). If the value must outlive the transfer, it is state and follows chart A (§17.2).

The wrapper must:

* resolve binding
* perform read/write
* ensure atomicity
* serialize result
* emit notifications to wrapper-managed subscribers when a subscribed wrapper-owned matter is mutated

For `substance_mode=wrapper`:

* `matter.subscribe` / `matter.unsubscribe` are delegated to the wrapper
* subscriber targets are stored by the wrapper runtime
* a local runtime write may emit `matter_written` from the contextualized matter address

To keep hosted code minimally intrusive, a wrapper runtime should carry this policy in the matter binding itself rather than in the hosted code symbol.

Typical pattern:

* bind `read` and `write` normally
* declare on the binding that this matter must notify on write
* let the runtime wrap the write path
* emit notifications to current subscribers
* expose observable metadata such as the number of notified subscribers

## 10.1 Reading Declared Matter Inputs

When a capacity declares `@matter` inputs, those inputs are contractual Brique refs.

The wrapper may satisfy them in two ways:

* direct native access, only if the referenced matter uses a wrapper-root-relative ref such as `./...` and has a local matter binding
* explicit engine-mediated access, typically through `matter.read`, when the matter is outside the direct native scope

So the rule is:

* same wrapper root and bound matter: direct access is allowed
* otherwise: use engine capabilities

## 10.2 Outbound Intention Discipline

When hosted code needs to call outside its direct native scope, it must do so through outbound intentions.

This includes:

* reading a matter through `matter.read`
* writing a matter through `matter.write`
* querying meaning through `meaning.query`
* invoking another capacity outside the wrapper root

In other words, wrappers use native code for native scope, and `invoke` for engine scope.

---

## 11. Identity and Context

The wrapper receives its identity from the engine at startup.

Every intention has:

* a context
* an identity

Outbound wrapper intentions MUST:

* use context paths relative to the wrapper root

They are control-plane messages and have a global serialized ceiling of 4 MiB. A wrapper must never embed voluminous or binary business content in intention params or response payloads, including as base64. Durable bulk content must be written as Matter substance through the HTTP lease data-plane and represented by a matter reference. Section 17 refines the transient case: within one Brique instance on a shared filesystem, ephemeral call data too large to inline travels through a small `@file` reference; it is not embedded in the control message. The Python, Go and C++ reference runtimes reject oversized serialized messages before WebSocket transmission and implement the same transit selection.

The wrapper does not know the absolute context.

The engine reconstructs the full context path.

### 11.1 Shared Wrapper Root: One Runtime, Several Owning Contexts

A wrapper root may be shared by capacities declared across several contexts, not only by the boundary context itself (§5, Subcontexts). This is the normal pattern for consolidating several sibling contexts onto one process instead of duplicating a runtime per context: the wrapper root lives in a parent context (the boundary context), while some of the capacities it executes are declared under a subcontext, e.g. `parent/child`, that owns no wrapper of its own.

The natural first instinct — declare the wrapper in the parent's `execution.wrappers` and leave the contributing capacities declared independently under the child, each child managing its own `execution`/`communication` config — does not work and is not what this pattern means (see §4: wrapper resolution is strictly local per context, with no parent inheritance). What actually makes sharing work is that every capacity meant to run inside the shared process is declared *within the owning context's own tree* (its `capacity/` files reference the wrapper by name, same as any single-owner wrapper), while the underlying implementation code for what conceptually belongs to `child` lives in `child`'s own directory tree and is imported directly by the owning context's `bindings.py` — see §15.3 for the concrete, mandatory file layout.

For this to work, every outbound intention issued by hosted code — `emit_intention` / `emit_and_wait` in the Python runtime — must pass two fields explicitly, matching the calling capacity exactly:

* `from_context` — the calling capacity's own context, as a path **relative to the wrapper root**, matching §11 above. For a capacity declared under subcontext `child` of a wrapper rooted in `parent`, this is `"child"` — never the empty string, even though the wrapper root's own boundary context, `parent`, is context `""` relative to itself.
* `from_cap` — the calling capacity's own exact name, never a generic placeholder.

Both fields default to `""` in the runtime API for convenience, and that default is only correct when the wrapper root is the sole owner of the capacity calling out — i.e. boundary context and capacity context coincide. Once a wrapper root serves capacities from more than one context, an empty `from_context` silently resolves to the wrapper root's own boundary context instead of the calling capacity's actual context, and any response to that outbound intention can never be routed back to the capacity that issued it — not a rejected call, but one that hangs until timeout.

`from_cap` fails the same way for a different reason: the engine resolves a response by looking up `from_cap` as a real capacity name that must actually exist (never a free-text label), regardless of whether the wrapper root is shared. A generic placeholder in `from_cap` breaks response routing even in a wrapper root owned by a single context.

Always pass both fields as the calling capacity's own exact context and name — this is not optional bookkeeping, it is what makes response routing work.

---

## 12. Wrapper-Initiated Intentions

The wrapper can emit intentions.

Hosted code uses wrapper APIs to:

* emit intentions
* optionally wait for responses

The wrapper is responsible for:

* constructing the intention
* attaching identity
* attaching relative context
* sending through communication

Wrapper-initiated intentions must preserve the public capacity contract shape:

* params as declared
* `@matter` refs as declared
* no hidden transport-specific business fields

## 12.1 Wrapper-Initiated User Traces

A wrapper may also request user trace creation.

Architecturally, this should still go through an intention.

So a wrapper-originated user trace should be modeled as:

* an outbound intention
* addressed to a trace-writing capability
* with params carrying the user trace content

The minimum useful params are:

* `trace_kind = "user"`
* optionally `intention_id`
* optionally `reason_code`
* optionally `user_text`

Conceptually:

```json
{
  "kind": "intention",
  "ts": "2026-04-12T10:15:30Z",
  "intention": {
    "to": {
      "context": "alpha_child",
      "type": "trace",
      "cap": "trace.user"
    },
    "from": {
      "context": "alpha_child",
      "type": "execution",
      "cap": "sandbox.echo"
    },
    "params": {
      "trace_kind": "user",
      "intention_id": "i_123",
      "reason_code": "debug_note",
      "user_text": "record normalized before remote write"
    }
  }
}
```

`trace.inspect` remains a reflexive read capability for persisted traces, not a write surface.

The trace-writing capability is responsible for converting this intention into the persisted trace event.

So the wrapper API does not need a dedicated trace primitive.
Hosted code should use the standard intention API toward the trace capability.

---

## 13. Awaiting Responses

Two modes:

* async: emit and continue
* sync: emit and wait

The wrapper must:

* correlate responses
* resume execution

Waiting must not block the communication loop.

---

## 14. Cross-Language Invariants

The following apply regardless of host language.

**Ordering** — guaranteed per addressed binding or per matter. Unrelated bindings may execute in parallel.

**Concurrency** — the wrapper must remain responsive, execute in parallel, and handle multiple requests.

**Backpressure** — the wrapper defines a maximum concurrency level and a queue for pending work.

**Error model** — errors are mapped to Brique responses by wrapper code. No universal mapping is enforced.

**Timeout** — defined by the wrapper implementation. Not fixed by this specification.

**Cancellation** — no standard cancellation model is defined.

**Eventing** — there is no separate event system. Active code may emit intentions at any time.

**Serialization** — must match the schema defined in matter meaning.

**Idempotency** — handled case by case.

**Observability** — relies on engine traces and wrapper logs.

**Security** — no restriction is enforced at wrapper level.

**State persistence** — wrapper state is ephemeral by default.

**Loop prevention** — no built-in loop prevention.

**Lifecycle** — the wrapper must support `wrapper_ready` and `wrapper_stop`.

**Filesystem model** — canonical layout is `code/<wrapper_name>/`, `build/<wrapper_name>/`, `tmp/wrapper_<wrapper_name>/`.

**Parameter forms** — a parameter reaches a capacity as a value, `@matter`, `@file` or `@object`. The runtime resolves it when hosted code asks for it, so hosted code never depends on the form (see §17.3).

**Wrapper-owned files** — durable state private to one wrapper is an `ext_ref` matter whose file that wrapper manages (see §17.2).

**Active components** — the wrapper may host active components that run continuously, expose capacities, expose matters, and emit intentions.

**Traceability tags** — code symbols must be traceable using `<brique:...>` tags (see §16). Tags link code to Brique elements and enable navigation. Tags are not runtime bindings.

**Execution contract** — the wrapper must support request handling, response emission, readiness, and stop behavior.

**Non-goals** — a wrapper must not duplicate engine behavior, implement routing logic, define bindings in JSON, create multiple runtimes per subcontext, or enforce business logic structure.

---

## 15. Python Implementation

This section is the mandatory, concrete pattern for the Python wrapper. It supersedes any older or looser pattern found in code that predates this document (see §15.6). If you are an LLM asked to write a new Brique capacity in Python, read this section fully before writing anything.

### 15.1 What Already Exists — Copy It, Don't Reinvent It

The generic Python wrapper runtime lives at `wrapper/python/code/wrapper/` in this repository: `runtime.py`, `registry.py`, `router.py`, `execution.py`, `matter.py`, `outbound.py`, `correlator.py`, `transit.py`, `calls.py`, `transport.py`, `constants.py`, `models.py`, `util.py`, plus `main.py` and `requirements.txt` at `wrapper/python/code/`. This is the membrane described in §1–§14 and §17, implemented and tested. The engine's own copy under `Engine/wrapper/python` and the copies already living in instance wrappers do not contain `transit.py` and `calls.py` until they are synchronized from here.

**To create a new wrapper for a context**: copy `wrapper/python/code/main.py`, `wrapper/python/code/requirements.txt`, and the entire `wrapper/python/code/wrapper/` directory verbatim into `<context_dir>/code/<wrapper_name>/`. Do not rewrite, re-derive, or "improve" this code — it is the generic runtime, and every real wrapper in this instance uses it unmodified. The only file you write yourself is `bindings.py` (§15.2) and the capacity files it imports (§15.3).

Declare the wrapper in the owning context's `context.json`:

```json
{
  "engine_config": {
    "communication": {
      "interfaces": [
        {
          "driver": "ws",
          "name": "<wrapper_name>",
          "type": "wrapper",
          "config": { "path": "/wrapper/<wrapper_name>", "egress_enabled": true, "ingress_enabled": true }
        }
      ],
      "wrapper_boundary": ["<wrapper_name>"]
    },
    "execution": {
      "wrappers": [
        {
          "wrapper_name": "<wrapper_name>",
          "mode": "interpreted",
          "ready_timeout_ms": 5000,
          "stop_timeout_ms": 1000,
          "run": {
            "cmd": ["python3", "main.py"],
            "env_name": "env1",
            "shell": false,
            "env": {
              "BRIQUE_WRAPPER_NAME": "<wrapper_name>",
              "BRIQUE_MAX_CONCURRENCY": "16",
              "BRIQUE_QUEUE_LIMIT": "256",
              "BRIQUE_TRANSIT_MAX_BYTES": "67108864",
              "BRIQUE_TRANSIT_TTL_SECONDS": "3600"
            }
          }
        }
      ]
    }
  }
}
```

For the Python, Go and C++ runtimes, `BRIQUE_MAX_CONCURRENCY` and `BRIQUE_QUEUE_LIMIT` are positive integers with defaults `16` and `256`. They bound globally executing engine-routed capacity/matter work and pending work. Responses and `wrapper_stop` bypass that queue so a saturated wrapper can still make progress. Work for one addressed binding remains ordered, while unrelated bindings may run in parallel. All three consume `BRIQUE_TRANSIT_MAX_BYTES` and `BRIQUE_TRANSIT_TTL_SECONDS`: they cap one serialized `@file` payload and the lifetime of abandoned references. Above the file limit the runtime uses a transient Matter; durable data remains an explicit semantic Matter.

### 15.2 `bindings.py` Is a Pure Import File — Never Business Logic

This is the single most important rule in this section, and it is a rule this document deliberately enforces even though it is not what every wrapper in this instance currently does (§15.6).

`bindings.py`, the file the Runtime Registry loads at bootstrap (`<context_dir>/code/<wrapper_name>/bindings.py`), must contain **only**:

* imports of one Python module per capacity (§15.3)
* the assembly of those imported callables into the two dicts `RuntimeRegistry` expects
* a `build_bindings(runtime)` function returning `{"capacities": {...}, "matters": {...}, "shutdown": [...]}`

It must never contain a class with business logic, a function that does real work, or any capacity implementation inline. If you find yourself writing `def recolte(self, params): ...` inside `bindings.py`, stop — that function belongs in its own file (§15.3).

`build_bindings` may be `async def` or plain `def` — the registry calls it and awaits the result only if it is awaitable (`inspect.isawaitable`), so either works. Prefer `async def` when any capacity module needs async setup at import/bootstrap time; plain `def` is fine otherwise.

### 15.3 One File Per Capacity, Named After the Capacity

Each capacity gets its own Python file, named after the capacity's Brique name with `.` replaced by `_`. A capacity named `import.recolte` lives in a file `import_recolte.py`. A capacity named `image.photo_au_hasard` lives in `photo_au_hasard.py` if it is the only capacity of that context, or `image_photo_au_hasard.py` if disambiguation against a sibling context's capacity is needed — but the mechanical rule is always: take the capacity's exact Brique name, replace every `.` with `_`, add `.py`.

Each capacity file exposes one callable — a plain function, an async function, or a bound method of a small local class if the capacity needs to share state with a handful of closely related helpers (private helper functions in the same file are fine; a full service class shared across many files is not — if you need that much shared state, it belongs in a matter, per §10, not smuggled into a shared object).

Wrap the callable's definition in the standard traceability tag from §16:

```python
# capacity/import_recolte.py
from __future__ import annotations
from pathlib import Path

# <brique:capacity name="import.recolte">
async def import_recolte(params: dict) -> dict:
    folder_path = str(params.get("folder_path", "")).strip()
    if not folder_path:
        raise ValueError("folder_path is required")
    # ... actual work ...
    return {"created_matter_ids": [...], "deleted_matter_ids": [...], "skipped": [...]}
# </brique:capacity>
```

Where these files live relative to `bindings.py` depends on whether the capacity belongs to the wrapper's own boundary context or to a contributing subcontext (§11.1). For a capacity owned directly by the boundary context, the file sits next to `bindings.py`:

```
<boundary_context_dir>/code/<wrapper_name>/
  bindings.py
  demo_echo.py
```

For a capacity owned by a contributing subcontext `child`, the file lives inside that subcontext's own tree — never physically inside the wrapper root's `code/<wrapper_name>/` directory, because the subcontext owns its own capacity descriptor (`child/capacity/<name>.json`) and its own code should sit next to it:

```
<boundary_context_dir>/                    (wrapper root, e.g. photographie/)
  code/<wrapper_name>/
    bindings.py                            (pure import + assembly, imports across into child/)
  child/                                    (e.g. image/)
    capacity/<cap_name>.json
    code/<wrapper_name>/
      <cap_name_with_underscores>.py
```

`bindings.py` imports each capacity file directly — flat, no intermediate aggregator file per contributor. Use `importlib` to load a module from an explicit path (this repository's contributing contexts are not on `sys.path`, so a plain `from child.code... import ...` does not resolve):

```python
# code/<wrapper_name>/bindings.py — PURE IMPORT, no business logic below this line
from __future__ import annotations
import importlib.util
from pathlib import Path
from typing import Any

THIS_DIR = Path(__file__).resolve().parent

def _load(rel_path: str, symbol: str):
    module_path = THIS_DIR / rel_path
    spec = importlib.util.spec_from_file_location(module_path.stem, module_path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"failed to load {module_path}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    fn = getattr(module, symbol, None)
    if not callable(fn):
        raise RuntimeError(f"{module_path} does not export {symbol!r}")
    return fn

# capacities owned by this boundary context
demo_echo = _load("demo_echo.py", "demo_echo")

# capacities owned by the contributing subcontext "image"
import_recolte = _load("../../image/code/import/import_recolte.py", "import_recolte")
photo_au_hasard = _load("../../image/code/import/photo_au_hasard.py", "photo_au_hasard")

def build_bindings(runtime) -> dict[str, Any]:
    return {
        "capacities": {
            ("", "demo.echo"): demo_echo,
            ("image", "import.recolte"): import_recolte,
            ("image", "image.photo_au_hasard"): photo_au_hasard,
        },
        "matters": {},
        "shutdown": [],
    }
```

The tuple key's first element is always the capacity's own context, relative to the wrapper root (`""` for the boundary context itself, `"image"` for the `image` subcontext) — this must match `brique.rel_ctx` in that capacity's own `capacity/<name>.json` descriptor exactly, and it is also the value that capacity's own code must pass as `from_context` on every outbound intention it issues (§11.1).

### 15.4 Reading the Contract Before Writing the Capacity

Before writing a capacity file, read its descriptor — `capacity/<name>.json` in the context that owns it. This descriptor is the source of truth (§7), not a convention to guess. It tells you:

* `brique.rel_ctx` — the context-relative key to use in `bindings.py`
* `brique.wrapper` — which wrapper this capacity must be bound in
* `functional.#root.inputs.params` — exactly which params to read from the `params` dict, and nothing else
* `functional.#root.inputs` `@matter` entries, if any — which matter refs the capacity expects, and whether to read them natively (§10.1) or via `matter.read`
* `functional.#root.outputs.payload` — the exact shape of the dict to return
* `functional.#root.outputs.@matter` — which matters this capacity is expected to create/write/delete as a side effect

Do not invent params, do not invent output fields, and do not add matter side effects the contract does not declare (Rule 4 of the engine bootstrap applies here just as much as it does to engine-native capacities).

### 15.5 Matter Bindings Follow the Same One-Symbol-Per-Purpose Discipline

A matter binding is not a capacity, so it does not get its own file under the one-file-per-capacity rule — but it still must not be defined inline inside `bindings.py` beyond the dict entry itself. Put the read/write accessor functions in a small dedicated module (e.g. `<matter_name>_state.py`) next to the capacity files that use them, and import them into `bindings.py` the same way:

```python
# demo_cache_state.py
_cache = "cold"

def read_cache() -> str:
    return _cache

def write_cache(value: object) -> dict:
    global _cache
    _cache = str(value)
    return {"ok": True, "value": _cache}
```

```python
# bindings.py
read_cache = _load("demo_cache_state.py", "read_cache")
write_cache = _load("demo_cache_state.py", "write_cache")

def build_bindings(runtime):
    return {
        "capacities": {...},
        "matters": {
            ("", "demo_cache"): {"read": read_cache, "write": write_cache, "notify_on_write": True},
        },
        "shutdown": [],
    }
```

### 15.6 A Note on Older Code in This Instance

Not every wrapper currently deployed in this instance follows §15.2–§15.3 yet. Some early wrappers (recognizable by a single large `bindings.py` containing full service classes with business logic inline) predate this convention. This document is the reference going forward: write new capacities following §15.2–§15.3, and if you are asked to touch an old-style wrapper, prefer extracting its capacities into the one-file-per-capacity layout over adding more inline logic to it.

### 15.7 Outbound Calls From Capacity Code

Inside a capacity file, reach the engine through the `runtime` object passed to `build_bindings` — capture it in a closure or pass it explicitly if the capacity function needs it:

```python
# capacity/import_recolte.py
async def import_recolte(runtime, params: dict) -> dict:
    response = await runtime.outbound.emit_and_wait(
        to_context="/root/photographie/image",
        to_cap="matter.create",
        to_type="matter",
        from_context="image",          # this capacity's own context, relative to the wrapper root — never ""
        from_cap="import.recolte",     # this capacity's own exact name — never a placeholder
        params={"matter_id": "photo_x", "matter": {...}},
        timeout_s=15,
    )
    ...
```

Bind it as `functools.partial(import_recolte, runtime)` in `bindings.py`, or wrap it in a one-line closure at load time — either way, `bindings.py` stays assembly-only; the closure/partial is mechanical wiring, not business logic.

`from_context` and `from_cap` are mandatory in practice (§11.1) — never rely on the `""` default once more than one context contributes to the same wrapper root.

---

## 16. Bindings as Traceability Metadata

This section defines how wrapper code binds to Brique abstractions for navigation and auditability. Bindings are **implementation metadata** — comments that establish traceability between Brique elements and native code. They do not affect runtime execution; the actual binding that the engine uses is the dictionary entry in `bindings.py` (§15), not the tag.

### 16.1 Scope

Bindings exist only for the wrapper layer. They describe:

* which Brique elements have native implementation
* which external elements are used as dependencies

They are used for:

* traceability
* auditability
* code navigation

### 16.2 Wrapper Root

The **wrapper root** defines a single native execution and linkage domain — a compiled binary, a script runtime, a loaded module. Two elements belong to the same wrapper root if they can interact directly through native code without using the Brique engine.

The wrapper root determines dependency resolution:

* `wrapper_internal` → native resolution possible
* `engine_external` → requires engine mediation

### 16.3 Bindable Elements

**Context** is not bindable. It is implicit through file location and project structure.

**Capacity** — a wrapper-implemented capacity must be bound:

```
# <brique:capacity name="...">
...
# </brique:capacity>
```

**Matter** — a wrapper-implemented matter must be bound when it has native representation:

```
# <brique:matter name="...">
...
# </brique:matter>
```

**Schema** — binding is optional; only bind if it has a native representation (for example a TypeScript type, interface, or validation function whose shape is meant to track a schema descriptor's `functional.fields`). A schema descriptor defines the expected shape of key-value pairs — field names, types, required/forbidden fields — not a matter's live content and not the physical substance behind a matter (that distinction belongs in the matter descriptor's own `functional`, e.g. `substance_type`/`substance_mode`, never in schema):

```
# <brique:schema name="...">
```

Do not reach for `<brique:matter>` to reference a schema, even informally in a comment — `matter` binds a wrapper's native representation of a matter's live content, a different element with a different nature. Use `<brique:schema>` for shape/contract references, `<brique:matter>` only for live-content bindings.

### 16.4 Section Binding

A section binding links a DSL section to a native implementation entry point:

```
<brique:section id="#...">
```

It binds only the entry point — not the full implementation, not internal helpers. Flow operators must never be bound.

### 16.5 Dependency Binding

Required when an element is used as input (invoked, read/written, used in computation, transformation, or validation) and is outside the current context — a parent context, a child context, or any other context. Comments, documentation references, and unused declarations do not count as usage.

Each dependency declares a signature: `capacity`, `matter`, `native`, or `schema` — describing what the wrapper expects, not how it is implemented.

```
<brique:import
  name="..."
  signature="..."
  description="..."
  resolution="wrapper_internal | engine_external">
```

* `wrapper_internal` — same wrapper root, resolved natively (import, include, direct call, interface, injection)
* `engine_external` — outside wrapper root, resolved via engine (intention, routing, execution family)

### 16.6 Non-Goals

Bindings do not define algorithms, enforce code structure, replicate DSL flow, define runtime routing, replace the engine registry, or guarantee correctness.

### 16.7 Binding Lifecycle and Maintenance

Bindings are created and maintained by the LLM as part of the bidirectional transformation between Brique metadata and wrapper code. The LLM is responsible for generating bindings when producing wrapper code from Brique elements, updating bindings when code evolves, updating bindings when Brique metadata evolves, and preserving semantic alignment between DSL, metadata, and implementation. Bindings are a living semantic artifact, not a static annotation.

They enable navigation in both directions: Brique → Code (identify where capacities, matter, and sections are implemented) and Code → Brique (reconstruct or update Brique elements from wrapper implementation).

Automated scripts, where used, verify structural consistency, not semantic correctness — presence of required bindings, validity of referenced names, existence of referenced Brique elements, section identifier consistency with the DSL, absence of forbidden bindings (e.g. flow bindings), consistency of dependency resolution (`wrapper_internal` vs `engine_external`). They must not attempt to validate algorithms, interpret morphing correctness, or infer missing bindings semantically.

> The LLM ensures semantic coherence. The verification layer, where present, ensures structural integrity.

---

## 17. Placement and Transit

This section is a decision aid. It tells a wrapper where state lives, in which form a parameter travels, and which path a call takes. Each chart ends in one answer.

> Status: the Python, Go and C++ reference runtimes implement address-driven parameter transit. Hosted code gives `call`/`Call` a destination address and ordinary values; it never selects `@object`, `@file` or a transient Matter. A semantic Matter still comes back as a `MatterRef` and remains explicit. Python additionally supports the optional direct in-process call path; Go and C++ currently route `Call` through the engine.

### 17.1 State Is Matter, Transit Is Not

* **State** is what must last beyond the call that produced it. It is always a matter.
* **Transit** is what one capacity hands to another so that the second can work. Its receiver consumes it. It is not durable state, although a cross-instance transfer may use an ephemeral Matter as its physical carrier.

A capacity stays stateless: a transit value that nobody consumed by the end of the call is released, never kept.

A temporary object that only lives for a call is therefore not a matter: it travels as `@object` (or `@file`), see §17.3.

### 17.2 Where Does This State Live?

Chart A, in order:

1. Must it outlive the call? No: it is transit, see §17.3.
2. Must it survive the wrapper process? No: `substance_mode=wrapper` (§10). Its content must be rebuildable from durable state.
3. Must anything other than the owning wrapper read or write it directly (another wrapper, a DSL plan, a UI)? Yes: `substance_mode=brique`, managed by the engine. No: `substance_mode=ext_ref`, a file managed by the owning wrapper.

Size, access frequency and write pattern do not change the mode. They select the file format: append-only data is a line-framed file (JSON Lines), data replaced as a whole is a JSON file, data written once is a plain file.

Wrapper-owned `ext_ref` matters:

* The locator is the absolute path of the file. The file lives in the matter directory, next to its descriptor.
* The engine does not serve the substance of an `ext_ref` matter. Readers outside the owning wrapper go through a capacity of that wrapper.
* For a JSON Lines matter, `functional.schema_ref` is the schema of **one line**.
* The owning wrapper is the only writer. Where the platform offers it, a file lock guards against a second writer during a restart.
* A line is committed only once its terminating LF is written, in a single append. At open, an unterminated last line is ignored and cut.
* A state replaced as a whole is written to a temporary file in the same directory, then renamed over the target.
* Durability (`fsync`) follows the engine's own level: it is not forced.
* Deleting the matter removes its descriptor. The engine's delete operation takes an opt-in option that also removes the file at the locator of an `ext_ref` matter (engine support pending). Without it, the owning wrapper removes the file itself.

### 17.3 In Which Form Does This Parameter Travel?

A capacity declares what it needs: a parameter name and a schema. It declares nothing about how the parameter arrives. The caller chooses the form, the runtime resolves it.

| Form | On the wire | Use |
|---|---|---|
| value | the value itself | small, at most 64 KiB serialized |
| semantic Matter | an existing reference in `intention.matters` | the data is already state; the capacity receives a `MatterRef` |
| transient `@matter` | `{"@matter":"<id>","context":"...","consume":true,...}` | large or binary transfer to another Brique instance or engine |
| `@file` | `{"@file": "<absolute path>", "format": "json"}` | too large for the payload, or the receiver is in another wrapper of the same filesystem-backed instance |
| `@object` | `{"@object": "<name>", "wrapper": "<wrapper_name>", "type": "<type tag>"}` | the receiver is in the same wrapper |

`@matter`, `@file` and `@object` are reserved keys: an exact reference shape carrying one of them is transport metadata, never business data.

Chart B, in order:

1. Is it already a semantic Matter? Keep its `MatterRef` in `intention.matters`.
2. Is it null, boolean or numeric? Value. Is it text no larger than 64 KiB in UTF-8? Value. Larger text is bulk and continues below despite being a scalar.
3. Is the receiver a capacity bound in this wrapper? `@object`.
4. Is the address internal to this filesystem-backed Brique instance? Small: value; large text, structured data or binary: `@file`; above the configured safe file-transit ceiling, transient Matter.
5. Does the address cross an instance or engine boundary (`@ext_*`)? Small: value; large text, structured data or binary: a transient Matter created in the destination context.

The 64 KiB threshold is fixed. It is not configurable for now.

**`@file`** is transit only, never state. The producer writes it in `<context tree>/.../tmp/wrapper_<wrapper_name>/transit/`. `format` defaults to `json`. The receiver accepts only this path shape inside its instance context tree, rejects symbolic links, and deletes the file after reading it. Consequently `@file` requires the wrappers to share that context-tree filesystem.

**`@object`** is posted by the caller in a wrapper-local table and removed by the receiver with a single `take`. When the receiver supplies an expected type, the stored tag must match exactly; an absent tag is then a mismatch. The check happens before the entry is removed. The table is thread-safe. Names are unique and never start with `$`, because the DSL resolves strings that start with `$`. A reference is valid only in the posting wrapper: a different `wrapper` fails with a typed error.

**Transient `@matter`** is created automatically in the destination context with `substance_mode=brique` and `brique.transient=true`, uploaded through the Matter HTTP data plane, and carries its format, committed revision and byte size. `get` first verifies that descriptor marker, then reads the complete substance, verifies revision and size, decodes it, and calls guarded `matter.delete`. A forged reference cannot authorize deletion of an ordinary Matter, and a read or decoding failure never deletes its carrier. The producer deletes the transient Matter after a definitive delivery failure and records an expiry in its descriptor for abandoned-transfer cleanup.

Runtime operations share the same semantics in every language:

* `get(name)` lazily resolves and memoizes inline values, `@object`, `@file`, and transient `@matter`; hosted code always receives the decoded value. Potentially large serialization, disk and HTTP work stays off the communication event loop.
* A semantic Matter is different: `get(name)` returns its `MatterRef` without reading it. Revision, range, append and lease semantics remain explicit.
* `emit(value, target_address)` picks the form from chart B. Hosted code never supplies `local` or a transport kind. A semantic `MatterRef` is moved to `intention.matters`; a transient Matter remains an ordinary parameter reference so `get` can consume it automatically.
* The caller is local when its origin context is under the wrapper's boundary context and its origin capacity is bound in the wrapper's registry. A DSL capacity is never local.

Lifecycle:

* A reference has a single consumer. To pass a value on, a capacity emits a new reference. To send it to several receivers, it emits several.
* If the target is not found, or answers with an error, the producer releases what it posted. If the call times out it does not: the callee may still be about to take it. Without a response to wait for (`await_response=false`) the producer cannot know and releases nothing. The receiving runtime releases the unconsumed transit references of a call when it ends, and so does the calling capacity for the references it received in the results of its own calls. At startup the wrapper deletes what is left in its transit directory.
* Transit references are never persisted in business data or journals. A transient Matter persists only its payload for the bounded lifetime of the transfer.
* The DSL relays these references as opaque values between calls and never resolves them.

### 17.4 Which Path Does This Call Take?

Chart C, in order:

1. The call comes from outside the wrapper: through the engine (imposed).
2. Internal call and tracing wanted: through the engine.
3. Otherwise, if the capacity is bound in this wrapper: direct call. If not: through the engine.

Whether to trace is a configuration and the architect's decision. The engine records intentions that pass through it according to the context trace level (`minimal`, `normal`, `debug`).

Hosted code calls with one address-driven operation: Python `await runtime.calls.call(to={...}, params=..., direct=False)`, Go `params.Call(Address{...}, values)`, C++ `runtime.call(TransitTarget{...}, values)`. The runtime derives every parameter representation from that address. Python's `direct=True` makes a plain function call when the capacity is bound in the same wrapper; Go and C++ currently keep engine routing. Reading is respectively `await params.value(name)`, `params.Value(name)`, and `params.value_raw(name)`/`string_value(name)`.

The two paths behave the same for hosted code where the runtime is concerned:

* The timeout is an inactivity budget in both. On the direct path `notify_running` renews it, as a `running` response does on the engine path. With no `timeout_s`, neither path times out. When a direct call times out, its task is cancelled and awaited through its cleanup before its unconsumed transit references are released.
* The runtime's own errors are the same: unknown capacity, unresolvable reference, exhausted budget (`asyncio.TimeoutError`).
* The caller's intention stays the current one on the direct path, so `running` acknowledgements keep acknowledging the caller's own call.
* They differ on one point only: an exception raised by the called capacity propagates natively on the direct path, and arrives as a `CapacityCallError` carrying the engine's error code on the engine path.

---

## 18. Summary

A wrapper is:

* one runtime membrane
* one process per wrapper name
* one bridge to code
* one resolver of capacities and matters

It is:

* minimal
* execution-focused
* context-aware
* bidirectional

For Python specifically: copy the generic runtime from `wrapper/python/code/` verbatim (§15.1); write `bindings.py` as pure import-and-assemble, never business logic (§15.2); write one file per capacity, named after the capacity (§15.3); read the capacity's own descriptor before writing its code (§15.4).

For placement and transit: decide where state lives with chart A (§17.2), in which form a parameter travels with chart B (§17.3), and which path a call takes with chart C (§17.4).
