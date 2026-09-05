# Python Wrapper Specification

## 1. Purpose

This specification defines the Python implementation model of a Brique wrapper.

The Python wrapper is a runtime membrane placed between:

* the Brique engine on one side
* an existing Python codebase on the other side

Its role is not to redesign the hosted code.
Its role is not to turn ordinary Python code into a Brique-native system.
Its role is not to duplicate engine behavior.

Its role is to:

* connect to the engine
* receive Brique messages
* resolve capacities and matters to Python bindings
* execute Python callables or access Python state
* return normalized Brique responses
* emit outbound intentions when requested by hosted code

The wrapper must be able to sit on top of an existing codebase with minimal intrusion.
It is a membrane, not a rewrite.

---

## 2. Design Principles

The Python wrapper implementation must obey the following principles.

### 2.1 Minimal intrusion

The wrapper must be addable on top of an existing Python project without requiring architectural redesign of that project.

Hosted code should remain ordinary Python code.

### 2.2 Live binding

Bindings must resolve to live Python symbols:

* functions
* bound methods
* objects
* getter/setter accessors
* service instances

The wrapper must execute real Python objects, not symbolic names stored as canonical runtime truth.

### 2.3 Clear separation of responsibilities

The engine owns:

* routing
* global context reconstruction
* execution semantics at engine level
* trace orchestration

The wrapper owns:

* local transport connection
* runtime bootstrap
* binding lookup
* Python invocation
* matter read/write access
* local concurrency and queueing
* response normalization

Hosted code owns:

* business logic
* runtime state
* internal algorithms
* domain-specific behavior

### 2.4 Existing-code-first

The wrapper must adapt to existing code instead of forcing existing code to adapt to the wrapper.

### 2.5 Language independence at architectural level

This specification is Python-specific in implementation, but all concepts must remain compatible with the language-independent wrapper model.

---

## 3. Runtime Position

The Python wrapper is a single process attached to one wrapper name for one boundary context instance.

It is the only Python runtime process that represents that wrapper instance toward the engine.

Subcontexts do not create additional Python wrapper processes.
They only contribute contextual projections resolved through the same runtime.

---

## 4. High-Level Architecture

The Python wrapper is composed of the following runtime components:

1. Transport Client
2. Message Router
3. Runtime Registry
4. Capacity Executor
5. Matter Manager
6. Outbound Intention API
7. Await Correlator
8. Scheduler and Queue
9. Lifecycle Manager
10. Hosted Code Adapter Layer

These components may be implemented as Python classes, modules, or cooperating runtime services inside one process.

They do not need to be isolated OS processes.
They are logical runtime components within the same wrapper process.

---

## 5. Component Model

### 5.1 Transport Client

The Transport Client is responsible for the communication link with the engine.

Its responsibilities are:

* establish the connection
* receive incoming messages
* send outgoing messages
* maintain connection state
* surface transport failures to the wrapper runtime

It must not contain business logic.
It must not perform binding resolution.
It must not interpret meaning.

#### Interface

It should expose operations equivalent to:

* `connect()`
* `send(message)`
* `receive_loop(handler)`
* `close()`

---

### 5.2 Message Router

The Message Router receives parsed transport messages and routes them to the correct internal component.

It is responsible for dispatching between:

* control messages
* capacity invocations
* matter reads
* matter writes
* responses to outbound intentions

It must remain thin.
It must not itself execute business code.

#### Interface

It should expose operations equivalent to:

* `route_incoming(message)`
* `route_response(message)`
* `route_control(message)`

---

### 5.3 Runtime Registry

The Runtime Registry is the canonical in-process registry of live Python bindings.

It stores mappings from:

* relative context path
* Brique element name

to:

* live callable binding
* live matter accessor binding

The resolution strategy is dictionary lookup.

The registry is **not dynamic at runtime**.
It is defined by a Python configuration module associated with the wrapper.

This configuration module contains the binding declarations coded explicitly by the wrapper implementer.
The wrapper does not expose runtime registration APIs such as dynamic `register_capacity(...)` or `register_matter(...)` for normal operation.

Instead, the wrapper loads a static binding table during bootstrap.

#### Responsibilities

* load the binding configuration module
* hold the in-memory mapping tables
* resolve addressed bindings
* expose stable lookup behavior during execution

#### Interface

It should expose operations equivalent to:

* `load_bindings(config_module)`
* `resolve_capacity(relative_context, name)`
* `resolve_matter(relative_context, name)`

---

### 5.4 Capacity Executor

The Capacity Executor invokes Python callables.

It is responsible for:

* receiving an addressed capacity request
* resolving the binding through the Runtime Registry
* adapting params and `@matter` refs according to the capability contract
* invoking sync or async callables
* capturing result or exception
* normalizing result into a wrapper response

The executor must support ordinary Python functions and coroutine functions.

It may also support callable objects and bound methods.

The Capacity Executor must preserve the public contract declared by the capacity descriptor.

So it should:

* pass params as declared
* pass `@matter` refs as declared
* avoid inventing hidden business inputs
* keep host-language adaptation minimal and explicit

#### Interface

It should expose operations equivalent to:

* `execute_capacity(invocation)`
* `normalize_result(...)`
* `normalize_error(...)`

---

### 5.5 Matter Manager

The Matter Manager handles runtime state access.

It is responsible for:

* resolving matter bindings
* executing reads
* executing writes
* enforcing local ordering guarantees
* enforcing per-matter concurrency protection
* serializing matter values

A matter binding may be represented by:

* direct object reference
* getter/setter functions
* accessor object

The existence of a matter binding means the wrapper can access that matter natively inside its own wrapper root.
It does not imply that every capacity input matter is materialized automatically as a native Python object.

For wrapper-owned mutable matter, notification policy should live in the matter binding, not in the hosted code symbol.

Recommended pattern:

* declare `read` and `write` in the static matter binding
* mark on that binding that write notifications are enabled
* let the runtime wrap the write path and emit `matter_written` after a successful mutation

This keeps hosted code light while preserving:

* centralized notification emission
* testable metadata such as the number of subscribers notified

#### Interface

It should expose operations equivalent to:

* `read_matter(request)`
* `write_matter(request)`

#### Contract With Capacity Inputs

When a capacity receives `@matter` inputs, those inputs are contractual Brique refs.

The Python wrapper may satisfy them in two ways:

* direct native access, when the referenced matter uses a wrapper-root-relative ref such as `./...` and a local matter binding exists
* engine-mediated access, typically through outbound `matter.read`, when the matter is outside direct native scope

So the practical rule is:

* wrapper-local bound matter => direct access allowed
* otherwise => use an outbound intention

---

### 5.6 Outbound Intention API

The Outbound Intention API is the internal API exposed to hosted code so that it can initiate Brique intentions.

Hosted code must not construct transport envelopes directly.

The wrapper must provide a Python-facing API equivalent to:

* `emit_intention(...)`
* `emit_and_wait(...)`

This API is responsible for:

* accepting a relative source context
* attaching wrapper identity
* constructing the outbound request
* sending it through the Transport Client
* optionally registering a wait handle for the response

Outbound intentions must only carry context paths relative to the wrapper root.

The wrapper root itself is not known as an absolute path by hosted code.
The engine reconstructs the absolute context path.

This is also the correct path when hosted code needs:

* `matter.read`
* `matter.write`
* `meaning.query`
* any capacity outside its direct native scope

So hosted code should use direct Python access only for wrapper-internal scope, and `emit_intention` / `emit_and_wait` for engine scope.

### 5.6.1 User Trace Emission

User trace emission should still use the outbound intention model.

A wrapper-originated user trace should therefore be emitted as an intention toward a trace-writing capability.

The intended payload is:

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

This must not target `trace.inspect`.
`trace.inspect` is a read capability exposed through Reflexive.

Current repository state:

* the public Python wrapper outbound API exposes `emit_intention(...)` and `emit_and_wait(...)`
* this is sufficient for user trace creation, since user traces should be modeled as outbound intentions toward the trace capability

---

### 5.7 Await Correlator

The Await Correlator manages pending outbound requests waiting for responses.

It is responsible for:

* creating correlation entries
* storing wait handles
* receiving responses from the Message Router
* waking waiting executions
* cleaning pending entries

In Python, this can be implemented with primitives such as:

* `asyncio.Future`
* `asyncio.Event`
* thread-safe condition variables
* concurrent futures

The chosen primitive must be compatible with the global wrapper concurrency model.

#### Interface

It should expose operations equivalent to:

* `register_pending(request_id)`
* `resolve_pending(response)`
* `fail_pending(request_id, reason)`

---

### 5.8 Scheduler and Queue

The Scheduler and Queue enforce concurrency policy.

The wrapper must support:

* a maximum parallel execution limit
* a queue for excess work

It must preserve the required ordering rule:

* ordering is guaranteed per addressed binding or per matter
* unrelated bindings may execute in parallel

This means the scheduler must be capable of:

* parallel execution across unrelated work
* serialized execution for the same addressed binding or matter when required by the ordering rule

Possible implementation approaches include:

* a shared worker pool plus keyed locks
* an asyncio task scheduler plus keyed semaphores
* a central queue plus per-key execution lanes

#### Interface

It should expose operations equivalent to:

* `submit_work(key, work_item)`
* `run()`
* `shutdown()`

---

### 5.9 Lifecycle Manager

The Lifecycle Manager governs wrapper startup and shutdown.

It is responsible for:

* bootstrap sequencing
* loading hosted code adapters
* creating runtime services
* registering bindings
* signaling `wrapper_ready`
* coordinating `wrapper_stop`
* stopping background components cleanly when possible

#### Interface

It should expose operations equivalent to:

* `bootstrap()`
* `start()`
* `announce_ready()`
* `stop()`

---

### 5.10 Hosted Code Adapter Layer

The Hosted Code Adapter Layer is the integration surface between the wrapper and existing Python code.

Its role is to connect the wrapper to real code without forcing the existing codebase to adopt Brique structures internally.

It may take forms such as:

* a Python binding configuration module
* adapter objects wrapping existing services
* explicit bootstrap code that binds existing symbols to wrapper names

This layer is where the wrapper is tailored to the actual codebase.

It is not part of engine protocol.
It is local integration code.

The canonical approach is a wrapper-local Python file that declares the binding tables in code.
These tables are then loaded by the Runtime Registry during bootstrap.

---

## 6. Main Runtime Flows

### 6.1 Startup Flow

1. The wrapper process starts.
2. The Lifecycle Manager initializes core components.
3. The Hosted Code Adapter Layer imports or constructs the hosted runtime elements.
4. The Runtime Registry loads the wrapper binding configuration module.
5. The Runtime Registry is populated with the declared live bindings.
6. The Transport Client connects to the engine.
7. The receive loop is started.
8. The wrapper sends `wrapper_ready`.

### 6.2 Capacity Invocation Flow

1. The Transport Client receives a message.
2. The Message Router identifies a capacity invocation.
3. The Scheduler receives a work item keyed by addressed binding.
4. The Capacity Executor resolves the live binding from the Runtime Registry.
5. The callable is invoked.
6. The result or error is normalized.
7. The Transport Client sends the response.

### 6.3 Matter Read Flow

1. The Transport Client receives a matter read request.
2. The Message Router dispatches it to the Matter Manager.
3. The Scheduler serializes by matter key when required.
4. The Matter Manager resolves the binding.
5. The matter value is read and serialized.
6. The response is sent.

### 6.4 Matter Write Flow

1. The Transport Client receives a matter write request.
2. The Message Router dispatches it to the Matter Manager.
3. The Scheduler serializes by matter key.
4. The Matter Manager resolves the binding.
5. The write is performed atomically at matter granularity.
6. The resulting visible state is serialized if needed.
7. The response is sent.

### 6.5 Outbound Intention Flow

1. Hosted code calls the Outbound Intention API.
2. The wrapper constructs an outbound intention using:

   * wrapper identity
   * relative source context
   * request payload
3. The Transport Client sends the message.
4. If no waiting is requested, control returns immediately.
5. If waiting is requested, the Await Correlator registers a pending handle.
6. On response reception, the Message Router routes the response to the Await Correlator.
7. The pending execution resumes.

### 6.6 Stop Flow

1. A stop signal is received from the engine or local runtime.
2. The Lifecycle Manager marks the wrapper as stopping.
3. New work is no longer accepted.
4. Queued and in-flight work is handled according to local policy.
5. Background components are stopped when possible.
6. The transport is closed.

---

## 7. Process Model

The Python wrapper is a single OS process.

Inside that process, several execution modes are possible:

* pure `asyncio`
* threaded dispatch around a main I/O loop
* hybrid model with async transport and threaded execution for blocking code

The specification does not force one Python concurrency implementation.
However, the following must hold:

* the communication loop must stay responsive
* waiting on outbound responses must not block the main communication loop
* parallel work must respect concurrency limits
* ordering must be preserved per addressed binding or per matter

For existing blocking codebases, a hybrid model is often the safest membrane design:

* async I/O or non-blocking transport toward the engine
* worker-thread execution for blocking Python callables
* synchronized access for sensitive matter bindings

---

## 8. Interface Model

### 8.1 Engine-Facing Interface

The engine-facing interface is implemented through the Transport Client and message normalization logic.

The Python wrapper must be able to:

* send `wrapper_ready`
* receive invocation requests
* receive matter access requests
* receive stop requests
* send responses
* receive responses to wrapper-initiated intentions

### 8.2 Hosted-Code-Facing Interface

The hosted-code-facing interface is a local Python API.

At minimum, it should provide primitives equivalent to:

* `emit_intention(...)`
* `emit_and_wait(...)`
* `read_local_matter(...)` if useful internally
* `write_local_matter(...)` if useful internally

The wrapper does not rely on dynamic runtime registration functions such as `register_capacity(...)` or `register_matter(...)`.

Bindings are declared explicitly in the wrapper binding configuration file and then loaded at bootstrap.

This interface should be minimal and unsurprising to Python developers.

---

## 9. Data Model

### 9.1 Capacity Bindings

A capacity binding should minimally include:

* relative context path
* Brique name
* Python callable
* optional metadata for parameter adaptation

Capacity bindings are declared statically in the wrapper binding configuration module.

### 9.2 Matter Bindings

A matter binding should minimally include:

* relative context path
* Brique name
* read accessor
* optional write accessor
* ordering/concurrency key

Matter bindings are declared statically in the wrapper binding configuration module.

### 9.3 Pending Outbound Requests

A pending outbound request should minimally include:

* request identifier
* wait primitive
* creation state
* timeout policy if any

---

## 10. Invariants

The Python wrapper implementation must preserve the following invariants.

### 10.1 Membrane invariant

The wrapper is a membrane over existing code, not a rewrite of that code.

### 10.2 Single-runtime invariant

There is one wrapper runtime process per wrapper name and boundary context instance.

### 10.3 Live-binding invariant

Every callable or matter access executed by the wrapper resolves to a live Python binding.

### 10.4 Relative-context invariant

Outbound wrapper intentions must carry only context paths relative to the wrapper root.

### 10.5 Identity invariant

The wrapper identity is provided by the engine at startup.
The wrapper does not invent its identity dynamically.

### 10.6 Ordering invariant

Ordering is guaranteed per addressed binding or per matter.
Unrelated bindings may execute in parallel.

### 10.7 Matter-visibility invariant

Matter writes must not expose intermediate invalid state.
A write is visible only as a completed matter update.

### 10.8 Responsiveness invariant

The wrapper communication loop must remain responsive while hosted code executes.

### 10.9 Queueing invariant

If incoming work exceeds the configured concurrency limit, excess work must enter a queue rather than breaking the runtime model.

### 10.10 Boundary invariant

The wrapper does not own global routing, global context reconstruction, or meaning interpretation.

---

## 11. Error Handling Model

The Python wrapper must always convert execution outcomes into valid wrapper responses.

However, the mapping from Python exceptions or local failures to response semantics is implementation-defined.

This means:

* one wrapper may map a given exception to failure
* another may map it to refusal
* another may encode it differently

The specification requires normalized response emission, not a universal exception taxonomy.

---

## 12. Timeout Model

Timeout behavior is not fixed by the generic Python wrapper specification.

An implementation may choose:

* no timeout
* per-call timeout
* per-outbound-wait timeout
* externally configured timeout

---

## 13. State Model

Wrapper runtime state is ephemeral by default.

If the Python wrapper process stops and restarts:

* in-memory registry state is rebuilt
* pending waits are lost
* in-memory matter state hosted only inside the process is lost unless the hosted code persists it elsewhere

Persistence is not part of the minimal wrapper contract.

---

## 14. Security Model

The Python wrapper does not enforce a built-in capability restriction layer.

Hosted code may emit intentions toward any target.

System-level discipline is the responsibility of architecture and implementation choices, not of the minimal wrapper runtime.

---

## 15. Observability Model

The wrapper relies on two main observability surfaces:

* engine-side traces
* wrapper-side Python logs

The wrapper implementation should therefore make it easy to log:

* startup state
* registered bindings
* invocation begin/end
* matter access begin/end
* outbound intention emission
* pending wait resolution
* stop behavior

---

## 16. Recommended Python Implementation Style

A recommended implementation style is:

* `asyncio`-based transport and message reception
* a registry implemented with dictionaries
* a bounded scheduler for invocation work
* per-binding or per-matter keyed locking
* explicit adapter/bootstrap code for existing services
* a small Python API object injected into hosted code for outbound intentions

This style is not mandatory, but it fits the required invariants well.

---

## 17. Minimal Internal Class Set

A practical Python implementation will often contain classes equivalent to:

* `WrapperRuntime`
* `TransportClient`
* `RuntimeRegistry`
* `MessageRouter`
* `CapacityExecutor`
* `MatterManager`
* `OutboundAPI`
* `AwaitCorrelator`
* `Scheduler`
* `LifecycleManager`

In addition, it should include a wrapper-local Python binding configuration module.

These may be collapsed or separated depending on implementation size.

---

## 18. Non-Goals

The Python wrapper must not:

* duplicate engine routing
* define canonical bindings in JSON
* require the hosted codebase to become Brique-native
* create one runtime per subcontext
* define a global security model
* enforce persistence by default
* solve loop prevention automatically

---

## 19. Summary

The Python wrapper is a thin but strict runtime membrane.

It connects:

* the engine
* local Python code

It must remain:

* minimal in intrusion
* explicit in runtime bindings
* responsive under load
* ordered where required
* parallel where allowed
* compatible with existing Python codebases

Its architecture is not centered on framework ideology.
It is centered on one job only:

making existing Python runtime elements participate correctly in Brique.
