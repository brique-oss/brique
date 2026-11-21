# Brique engine — security and permissions

This is the one document about who may do what in a Brique instance, how that is
decided, how it is administered and how much of it has been proven. It describes the
code as it is. Three parts of it are checked mechanically so that they cannot drift:

* the **capacity matrix** (§5) is generated from the code and compared by a test;
* the **wrapper frame** is covered by shared protocol vectors (§8);
* the **validation status** (§12) names the tests that prove each claim.

Where this document and the code disagree, the code is right and this document is a
bug: fix the document, or write the test that makes the code say it.

Contents: 1 Scope and invariants · 2 Concepts · 3 The access source · 4 How a request is
decided · 5 Capacity matrix · 6 Signed external protocol · 7 Keys, policy generations,
activation · 8 Wrappers and local principals · 9 Running user capacities · 10 Observability
· 11 Operating the instance · 12 Validation status · Appendix A Decisions · Appendix B
Package map

---

## 1. Scope and invariants

### 1.1 What is protected

An instance is a tree of **contexts**. Each context owns **elements** (capacities, schemas,
documents), **data** (matters, structures) and a lifecycle. Messages between contexts are
`circulation.Message` values carried inside the engine as `junction.Delivery`, which also
carries the authority the message acts with. A message from outside the instance must be
signed by a known peer; a message from inside carries the local authority of the component
that produced it, or the restricted authority of a principal the policy names.

The security subsystem answers one question for every effect a message would have: *may
this principal do this to this resource?* It does so with an immutable compiled policy,
before any side effect, and it refuses by default.

### 1.2 Trust boundary

| Inside the boundary (trusted) | Outside (untrusted until proven) |
|---|---|
| the engine process and the operating-system account it runs as | any network peer, whatever it claims to be |
| the machine's protected security directory (§7, §11.1) | the instance's files, including its `security/` folder, until their signature is checked |
| the operator who edits it with `brique-sec` | wrappers when the policy gives them a `local.<name>` principal |
| wrappers the policy does not restrict (they keep the local authority of their context) | UI clients (people are not authenticated in this version, §1.4) |

An attacker who can replace the engine binary, or write the protected directory, is outside
the threat model: they already are the operator.

### 1.3 Invariants

1. **Fail closed.** If security is not configured, the external boundary is closed; if it
   is configured and cannot be loaded, the engine does not start. Unknown, malformed or
   ambiguous input is refused, never guessed.
2. **A key is not a permission.** Authentication proves who signed; the policy alone says
   what that principal may do. Principals are stable, keys are replaceable.
3. **Authentication precedes authority, authority precedes effects.** Signature, audience,
   freshness, replay reservation, capability, then resources, then the handler.
4. **Default deny, and a refusal wins.** A matching exception beats any grant.
5. **Authority only narrows.** A derived call carries the admission of the call that
   caused it, never more; an actor can narrow a principal, never widen it.
6. **Old authority does not outlive the policy.** Every use is decided against the admitting
   snapshot **and** the current one; revocation takes effect at the next action, including
   for leases and subscriptions that outlive their request.
7. **A refusal does not reveal.** To an external caller, a resource it may not touch looks
   exactly like one that does not exist; the precise rule goes to the denial journal.
8. **Durable before dispatch.** A signed request is reserved in the replay ledger, durably,
   before anything runs; a duplicate never runs twice; a crash never reopens the window.
9. **Bounded before expensive.** Sizes, counts and queues are bounded before cryptographic or
   allocating work; saturation refuses, it does not drop silently or grow without bound.
10. **No way in through the world.** No intention can read or change the policy, the keys,
    the replay ledger or the denial journal; the operator does it offline with `brique-sec`.

### 1.4 Accepted limits

These are decisions, not defects, and an operator must know them.

* **Local ingress.** A message that arrives through an ordinary `ws`, `http` or `https`
  interface, or from a wrapper the policy does not restrict, runs with the local authority of
  its context. The controls are the interface's own settings (`ingress_enabled`,
  `allowed_origins`, `allow_any_origin`) and the network around it. The engine prints a
  warning at startup for an interface that accepts any origin. Signed external traffic enters
  only through the root `outerCtx` HTTPS interface.
* **No authentication of people.** A UI is not a principal in this version.
* **Native wrapper code is not mediated.** A wrapper that reads files directly is outside what
  the policy can restrict; confine it with the operating system.
* **Cross-window safety belongs to the capacity.** The replay guarantee holds while the ledger
  remembers (§6.4). A newly signed request with a new `intention_id` is a new operation; use
  an idempotency key or `expected_revision` where a repeated effect matters.
* **`define` and `control` granted to an external principal are code execution with the engine's
  rights.** Native wrapper code is not mediated (above), so a principal that may `define` a
  capacity or `control` a context can have the engine run code it supplied, with the engine's own
  authority and none of the policy's restrictions. The engine has no "never external" category
  (D03, D23): grant these two verbs only to a principal you would trust with a shell on the machine,
  never to a peer you merely exchange data with, and never to `public.web` or `local.ui`. The
  compiler warns for each such grant (§3.1); read the warning as this sentence.
* **A façade hands out what its owner can read** if its author puts it in the answer (§9.2).
* **The audit journal is an operational record, not a tamper-proof ledger.**
* **A wrapper that is given a secret can disclose it** (§8.6). The policy decides which wrappers
  receive a secret; it does not decide what a wrapper does with it. The local machine and its
  operating-system account are trusted (§1.2): what other processes of that account can read is out
  of scope.
  **The right is bound to the wrapper's name, not to the command it runs.** The command (`run.cmd`)
  comes from the `context.json` of the instance, which is not signed, and a wrapper runs under the
  account of the engine. Whoever can write the files of an instance can therefore run a program of
  their choice under that account, and that account can also read the value of the secret
  (`~/.brique/<name>/secrets/<secret>`, mode 0600, same owner). Changing `run.cmd` while keeping the
  name gives that program the secret as well; binding the right to a hash of the command would not
  prevent it, since the program can read the file itself. Treat the files of an instance you did not
  write as code, not as data, before you start it. The grant would only gain its full meaning if
  wrappers were confined by the operating system (another account, a sandbox) so that the file is
  not readable by them.

---

## 2. Concepts

**Instance.** One running engine with one stable `instance_id`; it is the *audience* of every
signed message it accepts.

**Principal.** The subject of authorization. Kinds: `instance` (another Brique instance),
`service`, `user` — all identified by a signing key — and `local`, a restricted internal
component named `local.<name>` with no key (§8). A principal has one or more keys and a status
(`active`, `disabled`, `revoked`); `revoked` is a tombstone that can never be reactivated.

**Key.** An Ed25519 key whose id is derived from the public key (`bk1_…`). Keys have a status
(`active`, `retired`, `revoked`) and an optional validity window. A retired key still verifies
(for rotation) but is not used to sign; a revoked key never verifies.

**Actor.** An optional label the sender signs into an intention (`actor_id`). A principal may
declare actors, each with grants of its own; the principal then permits only what **both** its
own grants and the actor's permit. The actor never widens (§3.4).

**Verb.** `read`, `write`, `create`, `call`, `define`, `control` — a closed set, independent
of one another (none implies another):

| Verb | Targets | Effect |
|---|---|---|
| `read` | any | read the covered genres; `read_recursive` (whole subtree) only from a subtree target `/ctx/**` |
| `write` | matter, structure | modify, append, truncate, delete data |
| `create` | matter, structure | create data (also by clone and derive); creating gives no right on the result |
| `call` | context, subtree, `#capacity:name`, `#capacity:*` | execute **user** capacities (resource `capacity/execute`) |
| `define` | capacity, schema, document, context | create, change, delete definitions |
| `control` | whole contexts | lifecycle of a context and maintenance of its projection |

**Target.** `/ctx` (that context alone), `/ctx/**` (its subtree), `/ctx#genre:name` (one named
element), `/ctx#genre:*` (every element of a genre). Genres: `capacity`, `schema`, `document`,
`matter`, `structure`. Elements created later inside a granted subtree are covered.

**Grant and exception.** A grant gives one verb over targets and may carry `except` targets. An
exception removes the access whatever other grant would give it.

**Engine capacity and user capacity.** An *engine capacity* (`matter.read`, `edit.create`,
`context.stop`, …) is never named by a policy: it declares a **class**, the class says which
verb opens its door, and the resources it touches are checked next (§4, §5). A *user capacity*
(a DSL, a wrapper-backed function written by the architect) is an element like any other:
`call` on it is the `execute` action of the `capacity` genre.

**Resource.** `kind/action` on a context, optionally with an id: `matter/read`, `capacity/execute`,
`context/read_recursive`, `trace/payloads`, … The set of kinds and actions is closed and lives in
`trust.EngineResourceActions()`.

**Policy generation.** A signed policy plus the bootstrap it runs with, installed as a directory
`generations/g<revision>/`. A policy has an exact-successor revision chain and a previous-policy
hash; the engine adopts one successor per reload.

**Policy format.** `1` — capacities opened by name (kept for existing policies; its names are no
longer checked against the instance). `2` — engine capacities by verb and class, user capacities
by `capacity/execute`, local principals, actors. `3` — format 2 plus `as_owner`. `4` — format 3 plus
the reserved keyless `public.web` subject. The compiler emits the lowest sufficient format: 2 by
default, 3 for `as_owner`, and 4 only when public web rights are configured. An engine that does not
understand anonymous admission therefore refuses the policy instead of interpreting it loosely.

---

## 3. The access source

Administrators do not write the signed policy; they write a short **access source** that
`security/policysrc` compiles into it. The compiler has no catalog of capacities: it needs only the
resource actions of each kind.

```json
{
  "format": "brique-access-source", "version": "1",
  "instance_id": "prod-a", "policy_id": "main",
  "principals": {
    "partner-a": {
      "kind": "instance",
      "keys": [{"public_key": "<base64url Ed25519>"}],
      "grants": [
        {"read":   "/root/catalog/**", "except": ["/root/catalog/secrets/**"]},
        {"write":  "/root/catalog/inbox#matter:orders"},
        {"create": "/root/catalog/inbox#matter:*"},
        {"call":   "/root/catalog#capacity:price.quote"}
      ]
    }
  }
}
```

### 3.1 What compiles to what

Each grant compiles to a **verb layer** (the door of the capacities of the class) and a **resource
layer** (the exact resources). `read` on a subtree adds `read_recursive`, which a plain context
target never gives; an exception on `read` removes it from any target that covers the context.
`define` and `control` always produce a warning, and so does a `call` that covers a whole context
(code installed later is covered). A compiled policy is capped at 1 MiB.

### 3.2 `trace_payloads`

A principal with `"trace_payloads": true` also holds the message-content right
(`trace/events/payloads`) on every context whose trace its `read` grants cover as a whole. An element
target never gives it; a read exception removes it.

### 3.3 Local principals

`kind: "local"`, named `local.<name>`, no keys, no actors, always format 2 or above. It is the
policy identity of a wrapper (§8) or of a façade owner (§3.5). The `local.` prefix and the `local`
kind go together; any other principal may not use the prefix.

`local.ui` is the exact local principal reserved to a browser controlling its own instance through
an authenticated bridge. It is absent by default and receives only its explicit grants and limits.
The engine manufactures `@local_ui:/<channel>/<ui>` and clears page identity; loopback alone is no
proof. A wrapper cannot borrow this id, including while the principal is disabled. The runtime
advertises the behavior as `brique.local-ui/1`.

The reference browser rendez-vous is native messaging, not a loopback service. The user chooses one
registered gateway locally through the launcher; `brique.browser-gateway/1` is an owner-only,
atomically replaced alias and revision. Browser content never selects this alias, and an unavailable
choice never falls through to another instance. This rendez-vous carries no authority by itself:
only the engine may create the `local.ui` admission. Each host captures one alias/revision for its
whole life; a later choice applies only to a new host. Root object/path, control PID/generation and
IPC hello generation are independent gates, so a reachable stale or foreign endpoint is rejected
before it can observe a Sink binding.

For a descriptor-free local open, the keyless native host additionally requires the origin attested
by the extension to equal the exact `brique-web` URL currently reported ready by the launcher for the
selected gateway. Merely owning another loopback port is insufficient. The engine re-evaluates
`local.ui` on every message, carries it only in a non-serializable `Delivery`, and returns responses
only to the engine-created channel/UI coordinates.

The in-memory native channel speaks `brique.browser.bridge/1`: the host creates session and channel
ids, client requests have an exact increasing sequence, and `open` binds one canonical origin,
document, frame and UI after user activation. Payloads are opaque and bounded. EOF, disconnect or
close destroys the corresponding authority; an id can never restore it. The packaged native host and
engine adapter now consume this contract, so the runtime advertises it. Chrome registration still
requires a local manifest with the exact allowed extension origin.

The extension-to-service-worker port is not itself a native rendez-vous. `connectNative()` is delayed
until the isolated content relay has observed trusted active user input and releases a valid `open`.
Ordinary pages and synthetic clicks therefore create no native process. Navigation, native-host loss
or service-worker loss closes the channel; the same document cannot recreate it, while a new document
must repeat trusted activation and receives fresh session/channel coordinates.

### 3.3.1 Public web subject

`kind: "public"` is reserved to the exact id `public.web`. It has no key, peer, encryption mode or
actor and raises the signed policy to format 4. It is absent by default. Its grants are ordinary
explicit grants; merely serving or opening a web page creates no right.

Only the dedicated public ingress may ask the trust runtime to admit this subject. For every
accepted browser connection the ingress creates an ephemeral channel id and manufactures the whole
source as `@public_web:/<channel>/<ui>`. Page bytes cannot choose that source, identity, actor or
owner. `public.web` is the common authorization subject; the channel is the operational owner used
for responses, subscriptions, leases and quotas. It is not an account or a durable
identity.

The public listener is distinct from the privileged local listener. It requires the
`brique.public-web.v1` WebSocket subprotocol, exact Host/Origin policy, server-generated channels and
64 KiB frames. Connection, address, rate and outgoing-queue budgets are applied before effects. The
engine publishes only a loopback backend in `brique.engine-runtime/2`; `brique-web` validates its own
origin and proxies only UI names present in the immutable publication. Authority remains in Comm.
When an extension bridge becomes identified, Spark emits a disconnect, closes the public channel and
only then activates the identified transport; messages from the old transport are ignored.

### 3.4 Actors

```json
"actors": { "alice": {"grants": [ {"read": "/root/catalog/public/**"} ]},
            "default": {"grants": [ ... ]} }
```

* a request naming a declared actor gets the intersection of the principal's and the actor's grants
  (each set with its own exceptions);
* a request with **no** actor uses the `default` entry, and is refused if there is none;
* a request naming an **undeclared** actor is refused (`actor_denied` in the journal);
* a principal that declares no actor ignores the label.

One label is deliberately different: `brique.ui` is reserved to the browser bridge. It is refused
unless the receiving policy contains an exact actor binding of that name, even when the principal
declares no other actor. A `default` binding does not cover it; an empty binding grants nothing; a
non-empty binding remains the intersection with the principal. Existing direct calls using any other
label keep the historical rule above. The runtime advertises this semantic as `brique.ui-actor/1` so
a bridge can reject an engine that would ignore the reserved label.

The actor is a claim of the sender, signed with the rest of the header. It is not a trust boundary:
a different level of trust is a different principal. Each actor compiles to its own scope, so the
engine walks it only for principals that declare actors.

### 3.5 Façades: `as_owner`

```json
{"call": "/root/reports#capacity:report.generate", "as_owner": "local.reports"}
```

The capacity's own sub-calls run with the rights of the local principal `local.reports`, bounded to
the subtree of the capacity's context, instead of the caller's. The caller needs no direct access to
what the capacity reads.

* `as_owner` is on a `call` grant of the principal itself (never an actor's) and names a
  `kind: "local"` principal of the source by its full id.
* An owner cannot itself use `as_owner` (no chain of façades).
* When several grants open the same capacity, the most specific names the owner: the deepest
  context, then a named capacity over `#capacity:*`, then source order.
* The owner is re-evaluated against the current policy at every use: disabling or removing it cuts
  the façade at its next action. The caller's right to call the façade is checked at the door, not
  again while the call runs.
* A façade cannot open a subscription (it would outlive the call).

### 3.6 Warnings and checks the compiler gives

An invalid source is refused whole; nothing is written. Beyond syntax, it checks that `as_owner`
names a local principal of the source, that an owner does not use `as_owner`, that targets suit the
verb (`control` names whole contexts only), and that no key is shared between principals.

---

### 3.7 `encryption`

`"encryption"` says how the messages of a principal that speaks over the network travel (§6.9):

| Value | Meaning |
|---|---|
| absent | an `instance` principal is sealed; a `service` or `user` principal is in the clear |
| `"required"` | sealed, whatever the kind (not for a local principal) |
| `"none"` | in the clear, with a peer that cannot or must not seal |

It compiles to the principal's `content_mode` in the signed policy, so it is part of what `apply`
signs and `diff` shows. `brique-sec principal encryption <id> required|none|default` edits it.

### 3.8 `limits`

A principal that babbles — a peer in a loop, an agent that calls itself — must not take the instance
from the others. Every principal has a request budget: a sustained rate and a burst above it.

```json
"limits": { "rate_per_second": "20", "burst": "40" }
```

| Setting | Meaning |
|---|---|
| absent | the default: 20 requests per second with a burst of 40 for a principal that calls over the network, 100 and 200 for a restricted local principal (a wrapper); 256 derived calls and a depth of 16 for each request it makes |
| `rate_per_second`, `burst` | strings, like every number of a source; the burst is at least the rate |
| `chain_calls`, `chain_depth` | strings; what one admitted request may set off inside this instance (below) |
| `"unlimited": true` | no limit; it has to be written, an absent setting never lifts it |

`brique-sec principal limits <id> default|unlimited|<rate> [<burst>]` edits it (the burst defaults to
twice the rate; `principal limits <id> chain <calls> [--depth <n>]` sets the chain budget and leaves the
rate alone; `default` removes all of it). It is part of the signed policy, so `apply` signs it and
`diff` shows it.

**The chain budget.** A request that was admitted can make a wrapper or a DSL step call others, which
call others. A wrapper's delegation already allows 128 derived calls and lives five minutes, but each
call that is delegated in turn gets a delegation of its own, so nothing bounded the total. Each
admitted request now carries one counter, shared by every call derived from it: the calls in all
(`chain_calls`) and how deep they nest (`chain_depth`). The call that goes over is refused where it is
created, journaled against the principal (`chain_budget`), and answered to a waiting wrapper as
`unavailable` ("call chain budget exceeded"); a DSL step gets the canonical refusal. The budget
belongs to the request: another request of the same principal starts again, and a refused call is
not counted. `unlimited` lifts it with the rate.

### 3.9 `secrets`

Which wrappers may be given which secret (§8.6), by the name of the secret:

```json
"secrets": {
  "anthropic": { "wrappers": ["llm_llm"] }
}
```

A name is `[a-z][a-z0-9._-]{0,63}`, like every identifier of the source. `wrappers` lists `wrapper_name`s
(unique in an instance), at least one and at most 64, without repeats; a source may name at most 256
secrets. The section is decoded strictly like the rest: an unknown field, a number or a list where an
object belongs is refused, so the value of a secret cannot be written here by mistake.

It is part of the signed policy (`wrapper_secrets`, absent when there is nothing to grant: such a policy is
byte for byte what it was before, and no format version changes), so `apply` signs it and `diff` shows it
as `+ secret <name>: <wrapper>` / `- secret <name>: <wrapper>`. It does not make a wrapper a restricted one
(§8.2): the two are independent. There is no command that edits it yet; write it by hand, then `apply
--reload`.

## 4. How a request is decided

**Matter budgets set on the instance.** Not part of the policy and not per principal: they are settings of
the Matter configuration, and each one is a net against a loop or a bug, not a defence against an adversary.

| Key | Default | Effect |
|---|---|---|
| `min_free_disk_mib` | 256 (`0` disables) | under this free space every Matter mutation is refused with `disk_space_low`, whoever asks; an unmeasurable disk refuses too |
| `owner_bytes_burst_mib`, `owner_bytes_per_minute_mib` | 2048, 1024 (burst `0` disables) | bytes an external principal may write: inline data, the source of a `clone`, the body of a lease upload (charged at commit); refused with `byte_budget_exceeded`; local callers are not metered |
| `heavy_burst`, `heavy_per_second` | 40, 20 (burst `0` disables) | `create`, `write`, `append`, `clone`, `derive` (so the opening of a lease) per external principal; refused with `rate_limited`; `define` and `control` are not Matter capabilities and are not counted here |
| `outbox_ttl_hours` | 168 (`0` keeps forever) | an unacknowledged durable event older than this is dropped and traced `outbox_expired`; an outbox also holds at most 1024 events per subscription and 16384 in all, the overflow event being dropped and traced `outbox_full` |

### 4.1 The doors

A signed request meets, in order:

1. **Authentication** (§6): format, audience, key, signature, freshness. All failures look the same to
   the sender; the journal keeps the class.
2. **Capability door** (Comm, then again at the family): the destination capacity must be granted.
   For an engine capacity, one verb of its class must be held on the addressed context. For a user
   capacity, `capacity/execute` on that capacity.
3. **Replay reservation** (§6.4), durable, before anything is dispatched.
4. **Family door**: the family's extractor returns **every** resource the request would touch
   (purely: no I/O, stable order, de-duplicated, at most 256); each is authorized against both
   snapshots; **one refusal refuses the whole request** (or, for the per-element operations below,
   that element).
5. **The handler**, which runs only with a complete positive decision.

### 4.2 Two snapshots

An admission pins the snapshot that admitted it. Every decision is the **intersection** of the
admitting snapshot and the currently active one:

* policy **expansion** cannot enlarge an old chain;
* **restriction, disabling, retirement and revocation** take effect at the next action;
* a keyless local or public admission must still exist with its exact kind in the current snapshot.

### 4.3 Derived authority

A call caused by an admitted call (a DSL step, a delegated wrapper call, a scatter branch) is
derived under the admitting call's admission: same principal, same root, the capacity re-authorized
against both snapshots, no identity of its own. An externally admitted message cannot be rewritten by
routing, and cannot be serialized, forged, widened or transferred: the types hide their fields and
refuse JSON in both directions.

### 4.4 Per-element decisions and uniform refusals

* `matter.read_batch`, `read.meaning`, `edit.*`, and the children of a Control request are decided
  **element by element**: a refused element is answered exactly as an absent one, and is never acted
  on.
* To an external caller a refused resource answers `not_found` / `"not found"` /
  `{"reason":"not_found"}`; a capacity that is not granted answers `unauthorized` /
  `"not authorized"` / `{"reason":"capability_denied"}`. The same answer covers a capacity that does
  not exist. `matter.exists` answers `{"exist": false}` for a matter the caller may not stat.
* The decision precedes any storage access; the differential tests compare the content of the
  answers, not their timing.

### 4.5 Filtered discovery

Operations that look across the tree return only what the caller may read:

* `meaning.query` is filtered by the caller's readable intervals (an index range per subtree, 0.44 ms
  measured) and flags `truncated` past 100 000 examined rows;
* `read.structure` returns a tree from which contexts and elements outside the policy are removed
  (a readable descendant promotes its ancestors as structure nodes only);
* `trace.inspect` is **all or nothing** on content: it needs `read` on the addressed context and
  returns the whole trace of that context; message **content** needs the separate `trace/payloads`
  right and is stripped without it;
* `vocabulary.*` is global and unfiltered: reading needs `read_recursive` on `/root`, changing it
  needs `write` on `/root`.

### 4.6 Leases and subscriptions outlive the request

A read, write or append lease opened for an external caller keeps that caller's admission and the
right it was opened for. Every HTTP request on the lease, and the commit of an upload, evaluates the
right again against both snapshots; a right withdrawn after the opening ends the transfer with `403`,
closes the lease and commits nothing.

A subscription records its owner's principal id (and actor), its matter, and a delivery target frozen
to the address the sender signed as its own (a request cannot choose a target). Each emission, live
or from the durable outbox, re-evaluates the owner's `matter/read` against the **current** policy by
principal id; a withdrawn right, a disabled or removed principal ends the subscription silently and
drops its undelivered events. Only the owner can unsubscribe or acknowledge; to anyone else the
subscription answers as a missing one. The engine refuses an external subscription that could never be
delivered (no configured peer, an origin that is not a canonical context under the peer's root), a
caller-chosen target, a wrapper-mode matter, a façade caller, and more than 64 per principal. Events
for an external subscriber carry no `source_intention_id`.

---

## 5. Capacity matrix

Reading guide. Classes map to verbs: `read`, `filtered_read` and `all_or_nothing` are opened by
`read`; `write` by `write`; `create` by `create`; `read_or_create` by either; `define` by `define`;
`control` by `control`. After the door, the resources of the second table are all required.

Exposure beyond the table:

* **Matter, Reflexive**: open to external principals through the doors above.
* **User capacities**: open to external principals with `call`; they run with the caller's rights or
  an owner's (§9). An unknown user capacity is refused exactly like an ungranted one.
* **Control (`context.*`, `start`, `stop`, `restart`) and `wrapper.start|stop|restart`**: open to
  **restricted local principals only**. Control accepts a capacity when the control verb is held on
  the parent or on a named child, and decides each addressed child on its own with
  `context/control`; a refused child is never acted on and is answered "child not found". The
  wrapper controls need the control class plus `context/control` on the context that owns the wrapper.
  A signed external principal is always refused here.
* **Trace family writes** and **scatter**: refused to externally admitted traffic.
* `matter.unsubscribe`, `matter.subscription.ack` and `edit.get_element_template` touch no world
  resource (the first two act on the caller's **own** subscription, decided by ownership).

<!-- BEGIN GENERATED capacity-matrix -->
Classes: which verb opens the door of each engine capacity.

| Family | Capacity | Class | Opened by verb |
|---|---|---|---|
| matter | `matter.append` | write | write |
| matter | `matter.clone` | read_or_create | read or create |
| matter | `matter.create` | create | create |
| matter | `matter.delete` | write | write |
| matter | `matter.derive` | read_or_create | read or create |
| matter | `matter.exists` | read | read |
| matter | `matter.read` | read | read |
| matter | `matter.read_batch` | read | read |
| matter | `matter.stat` | read | read |
| matter | `matter.subscribe` | read | read |
| matter | `matter.subscription.ack` | read | read |
| matter | `matter.truncate` | write | write |
| matter | `matter.unsubscribe` | read | read |
| matter | `matter.write` | write | write |
| matter | `structure.clone` | read_or_create | read or create |
| matter | `structure.create` | create | create |
| matter | `structure.delete` | write | write |
| matter | `structure.derive` | read_or_create | read or create |
| matter | `structure.patch` | write | write |
| matter | `structure.read` | read | read |
| reflexive | `edit.create` | define | define |
| reflexive | `edit.delete` | define | define |
| reflexive | `edit.duplicate` | define | define |
| reflexive | `edit.get_element_template` | read | read |
| reflexive | `edit.patch_meaning` | define | define |
| reflexive | `meaning.query` | filtered_read | read |
| reflexive | `meaning.rebuild` | control | control |
| reflexive | `meaning.update` | control | control |
| reflexive | `read.capacity` | read | read |
| reflexive | `read.document` | read | read |
| reflexive | `read.meaning` | read | read |
| reflexive | `read.state` | read | read |
| reflexive | `read.structure` | filtered_read | read |
| reflexive | `trace.inspect` | all_or_nothing | read |
| reflexive | `ui.list` | filtered_read | read |
| reflexive | `ui.open` | read | read |
| reflexive | `vocabulary.delete` | define | define |
| reflexive | `vocabulary.get` | all_or_nothing | read |
| reflexive | `vocabulary.patch` | define | define |
| reflexive | `vocabulary.query` | all_or_nothing | read |
| execution | `wrapper.restart` | control | control |
| execution | `wrapper.start` | control | control |
| execution | `wrapper.stop` | control | control |
| control | `context.restart` | control | control |
| control | `context.start` | control | control |
| control | `context.stop` | control | control |
| control | `restart` | control | control |
| control | `start` | control | control |
| control | `stop` | control | control |

Resources: what each capacity of a family with extractors needs, as returned by its real extractor for a probe request (`target` is the addressed context, `{x}` the request parameter the identifier comes from).

| Family | Capacity | Resources required (all of them) |
|---|---|---|
| matter | `matter.append` | `matter/append @ target #{matter_id}` |
| matter | `matter.clone` | `matter/create @ target #{target_matter_id}`<br>`matter/read @ target #{source_matter_id}` |
| matter | `matter.create` | `matter/create @ target #{matter_id}` |
| matter | `matter.delete` | `matter/delete @ target #{matter_id}` |
| matter | `matter.derive` | `matter/create @ target #{target_matter_id}`<br>`matter/read @ target #{source_guards[].matter_id}` |
| matter | `matter.exists` | `matter/stat @ target #{matter_id}` |
| matter | `matter.read` | `matter/read @ target #{matter_id}` |
| matter | `matter.read_batch` | `matter/read @ target #{matter_ids[]}` |
| matter | `matter.stat` | `matter/stat @ target #{matter_id}` |
| matter | `matter.subscribe` | `matter/read @ target #{matter_id}` |
| matter | `matter.subscription.ack` | none |
| matter | `matter.truncate` | `matter/truncate @ target #{matter_id}` |
| matter | `matter.unsubscribe` | none |
| matter | `matter.write` | `matter/write @ target #{matter_id}` |
| matter | `structure.clone` | `structure/create @ target #{target_structure_id}`<br>`structure/read @ target #{source_structure_id}` |
| matter | `structure.create` | `structure/create @ target #{structure_id}` |
| matter | `structure.delete` | `structure/delete @ target #{structure_id}` |
| matter | `structure.derive` | `structure/create @ target #{target_structure_id}` |
| matter | `structure.patch` | `structure/write @ target #{structure_id}` |
| matter | `structure.read` | `structure/read @ target #{structure_id}` |
| reflexive | `edit.create (capacity element)` | `capacity/create @ target #{name}` |
| reflexive | `edit.create (child context)` | `context/create @ target/{name}`<br>`context/write @ target` |
| reflexive | `edit.delete (capacity element)` | `capacity/delete @ target #{name}` |
| reflexive | `edit.delete (child context)` | `context/delete @ target/{name}`<br>`context/write @ target` |
| reflexive | `edit.duplicate (capacity element)` | `capacity/create @ target #{target_name}`<br>`capacity/read @ target #{source_name}` |
| reflexive | `edit.duplicate (child context)` | `context/create @ target/{target_name}`<br>`context/read @ target/{source_name}`<br>`context/write @ target` |
| reflexive | `edit.get_element_template` | none |
| reflexive | `edit.patch_meaning (capacity element)` | `capacity/write @ target #{name}` |
| reflexive | `edit.patch_meaning (the context itself)` | `context/write @ target` |
| reflexive | `meaning.query` | `context/read_recursive @ target` |
| reflexive | `meaning.rebuild` | `context/control @ target` |
| reflexive | `meaning.update` | `context/control @ target` |
| reflexive | `read.capacity (capacity)` | `capacity/read @ target #{cap_name}` |
| reflexive | `read.document (document)` | `document/read @ target #{name}` |
| reflexive | `read.meaning (capacity element)` | `capacity/read @ target #{name}` |
| reflexive | `read.state` | `context/stat @ target` |
| reflexive | `read.structure` | `context/read_recursive @ target` |
| reflexive | `trace.inspect (events)` | `trace/read @ target #events` |
| reflexive | `trace.inspect (with message content)` | `trace/payloads @ target #events`<br>`trace/read @ target #events` |
| reflexive | `ui.list (filtered catalog)` | none |
| reflexive | `ui.open (target UI)` | `context/read @ target` |
| reflexive | `vocabulary.delete` | `context/write @ /root` |
| reflexive | `vocabulary.get` | `context/read_recursive @ /root` |
| reflexive | `vocabulary.patch` | `context/write @ /root` |
| reflexive | `vocabulary.query` | `context/read_recursive @ /root` |
<!-- END GENERATED capacity-matrix -->

---

## 6. Signed external protocol

### 6.1 Envelope

An external message is a strict JSON object with exactly three members:

```json
{ "protected": { ... }, "payload": "<base64url, no padding>", "signature": "<base64url Ed25519>" }
```

`protected` holds only strings:

| Field | Meaning |
|---|---|
| `format`, `version` | exactly `brique-external-message`, `1`; no negotiation on an untrusted message |
| `purpose` | `intention` or `response` |
| `audience_instance_id` | must equal the receiving instance's id |
| `key_id` | derived id of an active trusted key; the **algorithm is not chosen by the message** (Ed25519) |
| `issued_at_ms`, `expires_at_ms` | decimal strings |
| `intention_id` | canonical lowercase UUIDv4 |
| `content_mode` | `clear`, or `hpke` when the payload is sealed to the recipient (§6.9) |
| `recipient_key_id` | with `hpke` only: the key the payload is sealed to; signed, so a message cannot be redirected |
| `payload_encoding`, `payload_size`, `payload_hash` | `base64url`; exact decoded size; hash of the exact payload bytes (the ciphertext, when sealed) |
| `actor_id` | optional, intentions only: `[a-z][a-z0-9._-]{0,63}`, signed with the rest; a receiver that predates it rejects a message that carries one |
| `request_hash`, `response_sequence`, `response_phase` | response only (below) |

The payload is exact bytes (the inner `circulation.Message` JSON) so that nothing re-serializes what
was signed. Sources are projected as `@ext_<principal>:/path` from the **authenticated** principal;
the message cannot choose it, and a response cannot choose its own return target.

### 6.2 Hashes and signature

```text
payload_hash = bmh1_<b64url(SHA-256("BRIQUE-MESSAGE-PAYLOAD-V1\0" || payload_bytes))>
signature    = Ed25519(key, "BRIQUE-EXTERNAL-MESSAGE-SIGNATURE-V1\0" || JCS(protected))
request_hash = bmr1_<b64url(SHA-256("BRIQUE-SIGNED-MESSAGE-HASH-V1\0" || JCS(protected) || "\0" || signature))>
```

Because the protected header carries the payload hash and size, the signature covers the exact
payload without a signer (or HSM) having to accept megabytes. The `request_hash` of an intention is
what its responses carry.

### 6.3 Freshness

Maximum lifetime 5 minutes; accepted future skew 30 s; accepted expiry skew 30 s. An envelope is
fresh only if `expires > issued`, `expires − issued ≤ 5 min`, `issued ≤ now + 30 s` and
`now ≤ expires + 30 s`. The engine persists a **security time floor** at every activation and at
close; a restart, or a reload, with the clock behind it is refused until the clock is repaired.

### 6.4 Replay ledger

One key is reserved atomically after the key resolves to a principal:
`(audience_instance_id, principal_id, intention_id) → request_hash`. The ledger is an append-only,
checksummed journal with an in-memory index; reservations are durable (group-committed) before
dispatch.

```text
reserved → dispatched → running → completed
                  └→ rejected
                  └→ outcome_unknown
```

* a **different hash** for a known key is a conflict and is refused; the **same hash** is a transport
  retry and is never dispatched again;
* a cached latest running response or final response is retransmitted byte for byte;
* after a crash, `reserved`/`dispatched`/`running` entries become `outcome_unknown`: reported as that
  stable error, **never** converted into a second execution or an automatic retry;
* a request nobody waits for (`await_response` false) is finished as soon as it is dispatched, so it
  does not count against the ledger for good, and a duplicate is still suppressed;
* torn tails are truncated on recovery, corruption is refused.

**What is synchronized.** Only the reservation (`reserved`) is synchronized before it is
acknowledged, in groups. The transitions that follow (`dispatched`, `running`, `completed`,
`rejected`, `outcome_unknown`) are **written** to the journal but not synchronized at once; the next
group commit persists them, a flush every 250 ms does when none comes, and so do `Close` and the
compaction. So a call costs one shared synchronization, not three in series (measured: 60 to 140 calls
a second in sequence and about 90 to 1 100 a second at 32 in flight between two instances, on APFS).
A crash of the **process** loses nothing that was written. A failure of the **machine** can lose the
unsynchronized transitions: the entry then comes back in an earlier unfinished state, which recovery
turns into `outcome_unknown`; it never comes back before its reservation, so a request is never run a
second time. The window for a record to be lost is at most 250 ms (before, it was the few
milliseconds between the write and the synchronization of a batch). A machine failure that leaves a
hole **inside** that window, with valid records after it, is a corrupted journal and recovery fails
closed, as it does for any corrupted record: only a record cut at the very end is truncated. This
was not tested on a real power cut. A write error on a transition poisons the ledger.

**Maintenance.** At startup and every minute the service compacts the journal without the entries
that are finished and past their retention (default an hour after expiry; an unfinished entry a day
later). One rewrite and one sync, about 0.2 s at 100 000 entries, only when it is worth it (many
droppable entries, or the entry or size limit near 80 %). Without this an instance fills its limits
and refuses every signed request.

**Limits.** 100 000 entries, 512 MiB of journal, 128 MiB of cached responses, 4 MiB per envelope;
**one principal may hold at most a quarter of the entries**. A principal at its quota is refused
(journaled as `quota_exceeded`), the others are served, and a duplicate of what it already holds is
still suppressed. The quota bounds a share of the ledger, not a rate.

### 6.5 Responses

A response is accepted only for an awaited intention registered **before** egress with its exact
`request_hash`, the expected principal and the local origin. Responses are correlated and sequenced in
two phases (begin, then commit once the signed envelope is durably cached): `running` responses carry
increasing sequences and refresh the silence deadline, one `final` response consumes the wait.
Wrong principal, wrong hash, duplicate, regressing, unsolicited and post-final responses are rejected.
Shutdown or restart of the waiting instance reports `response_state_lost` and never an automatic
replacement intention.

**A request that fails inside the instance after it was admitted.** The caller is on another instance, so
it is answered through its own request, with `unavailable` ("external destination is unavailable"), and
never with the reason: this holds when the wrapper it was meant for has no connection, is too slow to take
it, or loses its connection before it answers. An ordinary failure acknowledgement is addressed to the
caller's `@ext_` name, and the root drops every response to such a name that did not go through the signed
boundary, so before this the caller heard nothing and waited for its silence timeout (measured: 120 calls
to a context whose wrapper was stopped, none answered in 25 s; now all 120 answered in a few seconds).
The same holds when the context it is for is unknown or cannot take it (the hop between contexts); a local
message that fails there keeps the ordinary acknowledgement, and nothing is signed to a peer for it. When the
instance is stopping, the ordinary acknowledgement is used and the caller's own silence timeout ends the wait.

**How long a call waits.** A call to a peer that awaits an answer ends with an error after
`external_call_silence_ms` (default 2 minutes, at most 30) with nothing heard; any `running` response
renews the wait, and the absolute limit stays 24 hours. It was 30 minutes, which kept a caller waiting that
long for an answer lost on the way. A capacity that a peer calls and that works longer than the silence
without answering must call `notify_running`. A signed message the peer refuses as overloaded (429, 503) is
sent again three times; the peer refuses before it queues anything and a second answer is rejected by the
registry above, so a retry never runs a request twice.

### 6.6 Admission sequence

1. strict size and shape checks on the raw bytes (before any crypto), one signature, no trailing data;
2. key lookup in the active snapshot, audience, purpose, freshness, signature;
3. the immutable admission is built and bound to those exact verified bytes;
4. capability door; replay reservation; dispatch directly to the addressed context (no parent hop);
5. root responses leave through the signed egress adapter only.

Before authentication the sender sees one generic failure. After it, an authorization denial may
produce a signed `unauthorized` response when a response is awaited; a conflicting replay gets a
generic signed conflict; fire-and-forget calls gain no application response; no signed response is
ever generated for an unauthenticated request (no signing oracle).

### 6.7 Transport

Signed traffic uses HTTPS only: the peer's URL comes from the protected bootstrap, must be `https`,
certificate verification cannot be disabled and a server-name override is refused for signed traffic
(`tls_ca_file` may name a private CA). HTTP `202` means only that the local ingress accepted the bytes.
Signed egress uses a physically separate verified-TLS queue; an ambiguous network outcome retains the
send in memory and is reported as `delivery_outcome_unknown`, never retried automatically.

### 6.8 Bounds

Envelope ≤ 4 MiB (base64 included), so the largest payload that fits is about 3 MiB minus 2 KiB (the
payload bound is 3 MiB); larger content goes through Matter. Top-level and protected object sizes,
field lengths and decoded lengths are checked before allocation. The pending-response registry holds
at most 4096 waits, each silent at most 30 minutes and at most 24 hours in all.

### 6.8.1 Request rate

Two budgets bound what a caller makes the instance do, each in memory and bounded in size:

- **By address, before the queue.** The signed listener gives each remote address (an IPv6 address
  counts by its /64) 2 000 requests per second with a burst of 4 000, and answers `429` above it. A
  stranger with no key therefore cannot fill the queue every peer shares. The budget is generous
  because it is not only a flood guard: several instances can sit behind one gateway, and an
  authenticated peer sends here the **answers** to the calls this instance made, at the rate it
  made them. A budget below that rate refuses an answer, nothing retries it, and the caller waits
  for ever (this was 200 and 400, and lost one answer in 600 at about 600 calls a second between
  two instances). The interface key `address_rate` sets it; the burst is twice the rate.
- **By principal, after the signature.** A request over the budget of its authenticated principal
  (§3.8) is not run. A caller that waits for an answer gets the one a saturated instance gives
  (`unavailable`, "request rate exceeded"), through the replay ledger like any admitted request; a
  request nobody waits for is dropped. The refusal is journaled against the principal
  (`rate_limited`). A responses stream is not limited: the pending-response registry bounds it.

A restricted local principal is limited the same way, by the budget of its policy entry; its
over-budget request is dropped and journaled (`rate_limited`). A wrapper with no entry keeps the
trust of its context, and no budget.

### 6.9 Confidentiality

The content of a message is sealed to the key of its recipient and opened with the recipient's
private key, so that the HTTPS connection, a proxy that terminates it, or anything between two
instances sees the envelope's header and nothing of what the message asks or answers.

**One key.** There is no second key to exchange: the Ed25519 key that identifies an instance is also
its encryption key, through the standard conversion of an Ed25519 key to the X25519 key of the same
pair (`security/seal`). Content is encrypted with HPKE (RFC 9180: DHKEM X25519, HKDF-SHA256,
ChaCha20-Poly1305), in a context separated from signing by its own domain string. The signer
provider opens: the private key stays where it is and only the plaintext leaves it.

**Sign, then seal, bind.** The sender seals the payload to the recipient and signs the header, which
names the recipient key (`recipient_key_id`) and carries the hash and size of the ciphertext. The HPKE
context is bound to that header (without the two ciphertext fields), so a ciphertext cannot be moved
into another message, even one the same sender signs. The receiver verifies the signature first and
decrypts only for an authenticated, active principal; a payload that is still sealed reads as empty,
so a caller that forgets to open never treats ciphertext as content.

**What the policy requires.** A principal carries a minimum `content_mode`:

- `hpke`: its messages are accepted only sealed. A clear message from it is refused although its
  signature is good, and the refusal is journaled against the principal (`encryption_required`).
  Calls to it are sealed to the most recent key of the machine they go to (§7.4.1), and its answer
  must come back sealed, else the pending call does not accept it.
- absent or `clear`: either is accepted; a sealed message is opened, and answered sealed.

A response is sealed to the key that signed its request. A message that cannot be opened is journaled
(`decrypt_failed`) and answered with nothing. In the access source an instance principal is `hpke` by
default; `"encryption": "none"` opts it out, and a service or user principal is sealed with
`"encryption": "required"` (§3.7). Local principals do not use the network and have no mode.

**What stays visible.** The header: who signs (key id), who it is for, the times, the intention id,
the sizes and the purpose; the traffic's size and rhythm; and, on the receiving instance, the content
once opened (traces follow the rights of §3.2). Sealing hides what is asked and answered, not that
two instances talk.

**Forward secrecy.** Each message uses a fresh ephemeral key, so recording traffic does not help
decrypt a later one; but a recipient's key that is later compromised opens what was recorded for it.
Rotating a key (§7.6) limits that to the messages sealed to it.

---

## 7. Keys, policy generations, activation

### 7.1 Key roles never overlap

The **instance message key** signs outbound intentions and responses. The **policy authority** key
signs policies; the engine holds only its public key. A peer's key is only ever *public* data in the
policy. Private keys are never configuration data: they live in protected files (or, later, a
keystore/HSM) read by a signer provider, never in `context.json`, the policy or the source.

**Exchanging keys.** Each instance creates its own key pair with `init`; the private key stays in
`keys/message-signer.pem` (`0400`) and is read only by the engine's signer. To let instance B recognize
instance A, the operator of A runs `brique-sec key show` and gives B the *public* key (or the whole
`principal add` line it prints); B registers it with `principal add a --kind instance --key <public key>`.
The private key is never transmitted, and the engine uses it for one thing: signing what the instance
sends (§6). The policy authority key (`authority/policy-authority.pem` in solo mode) is separate and only
signs policies.

### 7.2 Signer contract

The engine signs through an interface with one immutable set of signers, selected by the bootstrap
(`active_message_signer`, with retired ones kept to verify rotation). A signer reports its key id and
algorithm, signs a purpose-tagged input under a deadline, and fails closed: a signer error is a
normalized, non-leaking error and never a fallback to another key.

### 7.3 Development file provider

A strict PKCS #8 Ed25519 key in a protected file (one PEM block or DER, ≤ 16 KiB), read-only
(`0400` on unix, the read-only attribute on windows), owned by the engine's user, no hard link, no
symbolic link. The key id is recomputed and must equal `expected_key_id`; a self-test signs before
the signer is accepted. Other providers (OS keystore, PKCS #11/HSM, remote KMS) are described in the
roadmap and not shipped.

### 7.4 Bootstrap and trust anchor

The bootstrap belongs to the **machine**, not to the instance: strict, bounded JSON, public data only —
`instance_id`, the signers (where each private key file is), `policy_authorities` (the **trust
anchor**: the public key that signs policies) and `policy_signature_threshold`. The engine never
rewrites it.

Who the authority is depends on the arrangement (§11.1). By default it is **the machine itself**:
`init` makes an authority key on the machine, and `apply` compiles the access source of the instance and
signs it there. Nothing has to be remembered from another machine, and the instance carries no
signature. In the **anchored** arrangement the authority is external (its private key is offline), the
signed policies travel with the instance, and the machine is told which public key to trust; keeping the
anchor out of the instance is what makes the carried signatures meaningful.

The addresses of the peers are **not** in the bootstrap: they are part of the access source and of the
signed policy (`external_peers`: `principal_id`, `audience_instance_id`, `interface`, `target` — HTTPS
only, no credentials, one principal per audience, no duplicate, each a principal of the policy). A
bootstrap may still list peers of its own; the engine refuses a principal named twice.

#### 7.4.1 A principal on several machines

An instance that runs on two machines has two keys, one per machine, under one name. Its peer lists
**endpoints** instead of one `target`: each is a machine, with a name, the base URL of its listener and
the identifiers of the principal's keys that live on it.

```json
"peers": [{ "principal_id": "alice", "audience_instance_id": "alice", "interface": "outerCtx",
  "endpoints": [
    { "name": "portable", "target": "https://portable:8443/brique", "key_ids": ["bk1_…"] },
    { "name": "bureau",   "target": "https://bureau:8443/brique",   "key_ids": ["bk1_…", "bk1_…"] }
] }]
```

- **A key lives on one machine.** Several keys can live on the same machine (a rotation). Every key an
  endpoint names must be a key of the principal, at most 8 endpoints per peer, and a peer has either a
  `target` or endpoints, never both. A peer with a single `target` is unchanged: every key of the
  principal is on that machine.
- **A message goes to one machine.** `@ext_alice:/path` goes to the machine of the principal's most
  recent key, and `@ext_alice~bureau:/path` to the machine of that name. The most recent key is the one
  whose `not_before` (in the access source, on each key) is the latest among the keys valid now, and then
  the smaller key identifier; a key that starts later is not used yet. An unknown machine is refused at
  once.
- **It is sealed to that machine's key.** The key chosen is the most recent valid key of the machine, the
  only holder of its private half.
- **The answer goes back where the request came from.** A request signed by a key is answered at the
  address of the machine that key lives on, sealed to that key.
- **An answer is accepted only from the machine asked.** The pending call remembers the keys of the machine
  it was sent to: a response signed by another key of the same principal, from another machine, is
  refused.

### 7.5 Policy generations and activation

The engine finds its two directories (§11.1) from the instance it is given, and recovers, or
activates for the first time, one generation:

* the **first** activation of a machine that has never run the instance uses the newest generation it
  holds (or the one named by `-security_generation`) and **adopts** it, whatever its revision — verified
  against the trust anchor first — instead of replaying every revision from 1; from then on only the
  exact successor is accepted. A restart recovers the durable generation recorded in
  `activation-state.json` (an explicit generation that conflicts with it is refused);
* a **reload** takes the exact successor (revision +1, previous hash equal), verifies the proof,
  checks lifecycle (tombstones of revoked principals and terminal key states cannot be removed or
  reactivated), compiles, persists the activation record (with the security time floor), then
  publishes one complete runtime atomically — signers, trust runtime, replay service, peers — so a
  request sees a complete old or a complete new snapshot, never a mixture;
* the engine adopts **one** successor per reload and retains each reloaded runtime for the requests
  still using it, at most **32 per process**: the 33rd reload is refused with an instruction to
  restart (the restart recovers the durable generation);
* a reload request is coalesced if one is outstanding; it comes from `SIGHUP` on unix and from a
  change of `reload.request` on windows (§11.5).

Rollback is refused by the durable activation state; the replay journal is opened (and recovered)
**before** a first activation is made durable, so a failed recovery never selects a generation the
process cannot serve.

### 7.6 Rotation, compromise, recovery

* **Rotate the message key:** add the new key as a signer, make it active, give peers the new public
  key (their policy), keep the old one retired until in-flight messages have expired (5 minutes plus
  skew), then remove it.
* **Rotate a peer's key:** add the new key to the principal as `active`, retire the old, apply; revoke
  only after the peer has switched. A revoked key never verifies; a revoked principal cannot be
  reactivated.
* **Compromise of the policy authority:** in strict mode (§11.2) the private key is offline; replace
  the authority in the bootstrap of a new installation. There is no online path to change it.
* **Compromise of the message key:** revoke it at every peer, generate a new one, re-issue.
* **Backup:** back up the security directory without the replay journal only if you accept that
  restoring it reopens the freshness window (§6.4); prefer to keep the journal with its host.

### 7.7 The HTTPS certificate is not the identity

Two key pairs, with different jobs. The **message key** (§7.1) signs every message and says *which
instance sent it*; its public half is what a peer registers with `principal add`. The **HTTPS
certificate** and its key (`tls_cert_file`, `tls_key_file`) encrypt the connection and say *which
server this is*; they are ordinary X.509, from a public authority or made by `brique-sec tls init`
(self-signed, ECDSA P-256), and they say nothing about Brique principals. The engine uses no client
certificate: a caller authenticates by signing. The content of a message is also sealed to the
recipient's message key (§6.9), so it stays unreadable to a party that terminates the connection.

When the interface names no file, the engine uses `<machine>/tls/cert.pem` and `key.pem`, and adds
`<machine>/tls/trusted.pem` to the certificates it accepts from the peers it calls (the system's are kept).
Both are looked at again every couple of seconds: a regenerated or renewed certificate is served and
trusted without a restart, and a pair that does not load (half written, mismatched) is ignored while the
last good one keeps serving. The minimum TLS version is 1.2 and verification is on unless
`tls_insecure_skip_verify` is set.

---

## 8. Wrappers and local principals

### 8.1 The launch secret

Each time the engine launches a wrapper it issues a fresh 256-bit secret and puts it in the wrapper's
environment as `BRIQUE_WS_URL=ws://addr/k/<secret>`; the wrapper libraries are unchanged. The shared
WebSocket listener verifies it in constant time; a wrong or missing secret is a `404` and a journaled
`wrapper_secret_invalid`. The secret is replaced at every launch and revoked on stop or exit; it is
**not** invalidated on disconnect, so a wrapper can reconnect.

The listener setting `wrapper_auth` is `required` (the engine default: a plain URL gets `404`) or
`optional` (for explicit local development: a plain URL is accepted except for a wrapper the policy
restricts). A restricted wrapper always requires the secret.

### 8.2 Restricted wrappers

A wrapper whose policy has a principal `local.<wrapper-name>` is admitted at Comm ingress as a
distinct *local* admission. It passes the same doors as an external principal — capability, resources,
actors do not apply — and is answered with **ordinary local responses**, never a signed permit. Its
lifecycle messages (`wrapper_ready`, `wrapper_failed`) stay trusted. A wrapper with no such principal
(or whose name is outside the identifier grammar) keeps the local authority of its context.

### 8.3 Delegation

A signed external call dispatched to a wrapper cannot hand its admission to the wrapper (the types
refuse serialization). Junction owns one bounded **delegation registry** per instance: a random
256-bit handle (unpadded base64url, 43 characters), bound to the exact wrapper connection and the
retained parent delivery, with a short expiry and use counters. The wrapper receives the handle in a
frame and puts it on its own calls; the engine resolves it through the same derivation as any child
(both snapshots, correlation checked), binds the wrapper's response to the retained parent, and
revokes the chain on a terminal response, a reconnect or an expiry. A frame **with** a handle carries
the caller; a frame **without** one carries the wrapper's own authority (local if trusted, its
`local.<name>` principal if restricted). The engine never infers that a raw frame belongs to an
external call. The Python, Go and C++ runtimes attach the handle automatically.

### 8.4 The frame

```json
{"brique_wrapper_frame":"brique.wrapper/1","message":{...},"delegation":"<43 chars>"}
```

or a raw message with no handle. A frame must have exactly these three members, a message of kind
`intention` or `response`, and a handle of exactly 43 base64url characters (the engine also refuses
the all-zero handle). The key's **presence** claims the protocol: a frame whose format value is empty
is refused, never read as a raw message.
`templates/instance/wrapper/vectors/wrapper_frames.json` is the shared corpus of 25 cases run by the
engine, Go, Python and C++ suites.

### 8.5 Egress

Messages that leave the instance go through the immutable peer directory (`@ext_<principal>:/path` is
resolved to the HTTPS target configured for that principal, or `@ext_<principal>~<machine>:/path` to one
of its machines, §7.4.1; an unknown principal or machine is refused with no send), and an externally admitted message never leaves except as the signed answer to its own request.
A call may be emitted by any context of the instance and by an element of its user interface
(`@ui_main:/panel`): the other instance is told the context that owns the interface as the origin, and the
answer — or the error, at once — is delivered back to the element.

A restricted local principal, and any chain admitted from another instance, **cannot send to another
instance**: the call is answered at once with the canonical refusal and journaled as `egress_denied` under
the principal's name. A policy that would grant such a route (decision D07) is not implemented (open item
E1); an unrestricted local component is not subject to it.

### 8.6 Secrets given to a wrapper

A provider key or any other secret reaches a wrapper's environment if and only if three things hold:

| | Where | Says |
|---|---|---|
| **a need** | `run.secrets` of the wrapper in its `context.json`: `{"name": "anthropic", "env": "ANTHROPIC_API_KEY"}` | this wrapper wants this secret, in this variable |
| **a right** | `secrets` of the access source (§3.9), signed | the policy lets this wrapper have it |
| **a value** | `~/.brique/<name>/secrets/<secret>` (0600), written by `brique-sec secret add` | the machine holds it |

Nothing less is enough: a value with no right gives nothing, a right with no need gives nothing, and a
declaration the policy does not grant stops the wrapper from starting. A wrapper that declares no secret
is launched as before, with the environment it inherits; nothing is removed from it.

**At launch**, in `startWrapperProcess` and never at build time, the engine checks the declaration (a
secret name, a variable `[A-Z_][A-Z0-9_]*` that is not `BRIQUE_*`, is not set in `run.env` and is not used
twice), asks the **current** trust runtime whether the policy grants the secret to the wrapper (a reload
publishes a new runtime, so it is asked at each launch and never kept), reads the value with the protected-file
reader that guards the keys (owner only, no other link, no group or other access) and puts it in the
environment, over any inherited variable of the same name. Any failure stops the launch before a process
exists. The answer is `refused` / `wrapper_run_failed` and names the secret, the wrapper and what to do; it
never carries the value. With no active policy, no secret is given.

**A refusal because of a secret is not remembered.** A launch that fails otherwise leaves the wrapper marked
as failed until its context restarts. A refusal for a secret does not: the next call looks again, so storing
the value or applying the right is enough, with no restart.

**What it does not do.** A wrapper that receives a secret can use it and disclose it (§1.4). The right follows the
wrapper's name, not its command: a modified `run.cmd` under the same name is given the secret, and in any case
runs under the account that can read the secret file (§1.4). Withdrawing a right
does not reach a wrapper already running: it keeps the environment it was given, and the next launch is
refused. A secret that is still in the environment of the engine's own process, or in a file such as
`.env`, is still inherited by every wrapper: the mechanism protects only once the value is kept in one place.

---

## 9. Running user capacities

### 9.1 The caller's rights

A user capacity runs with the **rights of the principal that called it**. A DSL capacity derives each
step under the caller's admission, so every step is authorized at its own door and a step the caller
may not make is refused with the canonical capability refusal, emitting nothing. A step's reply is
accepted only when it is bound to that same admission. Wrappers do the same through the delegation
handle. The caller therefore needs `call` on the capacity **and** the rights on what the
implementation uses through the engine.

### 9.2 `as_owner`

When the rule that opens the capacity names an owner (§3.5), the sub-calls use the owner's rights,
bounded to the capacity's context (`Admission.ceiling`, applied to decisions, to readable intervals and
to replay), re-evaluated at every use. The owner is derived from the **current** snapshot; the signed
response of the root request stays the caller's (a shared response permit: the original request cannot
answer a second time). If the policy names an owner that can no longer be admitted, the call is refused.
A façade that puts a lease or data in its answer gives the caller what the owner can read — the author's
choice.

### 9.3 Retrying a step

A DSL `invoke` with `retry` repeats only after a **definitive** failure. After a timeout or a stopped
context the outcome is unknown and the step may have completed, with the caller's rights, so it is not
repeated unless the author writes `retry.on_timeout: true`.

### 9.4 Provenance

When the engine admits a root intention (signed ingress, or a restricted wrapper) it records the
principal and actor in a bounded, expiring book (8 192 roots, one hour). `TraceEmit` stamps every trace
event whose root is in it with `principal` and `actor`, and replaces any value a producer supplied. The
derived calls of a DSL step or a delegated wrapper call carry the same root, hence the same provenance.
Under `as_owner` the provenance stays the caller's. `trace.inspect` can filter on `principal`.

---

## 10. Observability

### 10.1 Denial journal

`<security_dir>/audit/denials-YYYYMMDD.jsonl` (directory `0700`, files `0600`, opened with
`O_NOFOLLOW` or refused if a link on windows). It records **why** an external access was refused, never
what the message contained. One JSON object per line, ≤ 2 KiB; fields supplied by an unauthenticated
caller are marked `"untrusted": true` and carry no principal. Closed reason set: `unknown_key`,
`key_inactive`, `principal_inactive`, `expired`, `replay`, `audience_mismatch`, `bad_signature`,
`capability_denied`, `resource_denied`, `actor_denied`, `egress_denied`, `wrapper_secret_invalid`,
`payload_limit`, `unsupported`, `quota_exceeded`, `internal`.

It never blocks the hot path (one non-blocking send; a full 4096 queue is counted and written as a
`suppressed` line; so is a file at its 64 MiB cap). Identical refusals (same principal, reason, resource)
within 10 s become one line with a `count`; the first line of a burst can therefore take up to about 10 s to appear on disk (closing the engine flushes it). Daily rotation, 14 days kept (`-audit_max_file_mib`,
`-audit_retention_days`). `permission_id` names the explicit deny rule and is empty when the refusal is
only the absence of an allow; it is computed on the refusal path only (the policy is decoded lazily on the
first refusal of a snapshot). Read it with `brique-sec denials [--since] [--principal] [--reason] [--limit]`.

### 10.2 Trace

Engine trace events carry `principal` and `actor` when the chain's root was admitted (§9.4). Message
content in the trace is the separate right of §3.2. Trace configuration warnings and the operational
messages go to standard error; nothing secret is logged (the engine does not log its environment).

### 10.3 Startup warnings

The engine warns about an interface that accepts any origin and runs messages with the local authority
of its context, and about `tls_insecure_skip_verify` (which signed traffic refuses anyway).

---

## 11. Operating the instance

### 11.1 Where the security lives

An instance is a private tree, versioned and run on a machine by its owner. Its security has two
parts, and the line between them is **what is secret or belongs to one machine**.

```text
<instance>/security/access.json         travels with the instance (git), ordinary permissions:
                                        principals, their public keys, grants, peers
<instance>/root/                        the root context (what -root_path names)
```

```text
~/.brique/<instance name>/              this machine only (%APPDATA%\Brique\<name> on windows), never versioned
  keys/message-signer.pem               the instance's private message key (0400)
  tls/cert.pem, tls/key.pem             the HTTPS certificate and its key (0600)
  tls/trusted.pem                       certificates of peers that use their own (0600)
  authority/policy-authority.pem        this machine's authority key (0400), solo arrangement
  source/bootstrap.template.json        signers and the trust anchor
  generations/g000001/                  bootstrap.json + trust-policy.json: the access source, compiled and signed here
  activation-state.json                 written by the engine: durable generation, time floor
  replay.journal                        written by the engine (§6.4)
  audit/                                written by the engine (§10.1)
  secrets/<name>                        the value of a secret a wrapper may be given (0600, §8.6)
  engine.pid, reload.request            engine / brique-sec signalling
  .admin.lock                           held by every mutating brique-sec command
```

**Where `security/` is.** `<instance>` is the directory that holds the root context directory next to
its wrappers (`<instance>/root`, `<instance>/wrapper`, …). The security folder is a **sibling of
`root/`, not inside it**: the engine confines every context to its own directory, so nothing in the tree
of contexts — a capacity, a document, a name with `..` — can reach it.

**Finding them.** The engine is given the root context (`-root_path <instance>/root`) and nothing else:
if `<instance>/security/access.json` exists it takes the instance name from it and uses
`~/.brique/<name>`. `-security_dir`, then `BRIQUE_SECURITY_DIR`, name another directory for the
machine's part. An instance without a `security/` folder has no signed communication unless a
directory is given explicitly; the environment variable alone never switches it on. A launcher such as
the editor extension needs no configuration. The first start uses the newest generation the machine
holds.

**The template.** `templates/instance/security/access.json` is an empty, valid access source under the
reserved name `my-brique-instance`. An instance made from the template starts locally, with no signed
communication, until `brique-sec --root <instance>/root init --instance <name>` gives it a name of its own
(`init` refuses the reserved name and renames only a source that is still empty); two instances made from
the template can therefore never share a directory on a machine.

**The policy changes when the owner says so.** The engine acts on the generation signed on the machine.
Editing `access.json`, or pulling a change to it, changes nothing until `brique-sec apply` is run on the
machine; `brique-sec diff` shows what is pending. So a change that arrives through the instance cannot
widen anyone's access by itself, and the history of who may do what is the history of one readable file.

**A lost machine** is replaced by checking the instance out and running `brique-sec --root <instance>/root
init`: it makes new keys, compiles and signs the source it finds, and keeps every principal, grant and
peer. Nothing has to be remembered. What changes is the instance's own message key, so peers must register
the new public key (`brique-sec key show`). Back up `~/.brique/<name>` if you want to keep the same key.

**No capacity reaches it.** Elements are files named by a single name (a capacity, schema, document,
matter or structure name or identifier is one file name: no separator, no `..`), so a caller who may
define or write in a context cannot name a path into `security/` or into another context. The
structure of a context lists its declared children and its own elements only.

Machine directories are `0700`, files `0600` or `0400`; on windows the owner is the current user, the
administrators or the system and no other principal has write (for a file, read) access. The files of
`<instance>/security/` have the ordinary permissions of a versioned file and are never followed through
a symbolic link. A directory given explicitly with no `security/` folder in the instance keeps
**everything** in that one protected directory, as before. `brique-sec doctor` reports anything the
engine would refuse.

**Anchored arrangement (very sensitive instances).** `brique-sec --root <instance>/root init --strict
--authority-public <key>` makes the authority external: its private key never touches the machine,
policies are signed offline (`compile --out`, `sign`, `apply --signed`) and the signed generations
(`<instance>/security/generations/g000001/trust-policy.json`, …) travel with the instance next to the
source, with a single `bootstrap.json` on the machine that names the trust anchor. A machine that checks
such an instance out must be told the authority's public key (`init --strict --authority-public <key>`,
printed by `brique-sec key show` where the authority lives) and adopts the newest generation after
verifying its signature. A policy file changed in the instance — a grant widened, a signature replaced,
an emptied file — is refused, because only the anchor on the machine verifies. The price is that the
authority's public key must be kept somewhere outside the instance.

### 11.2 Three commands for a catalog reader

```sh
brique-sec --root ./my-instance/root init --instance prod-a
brique-sec --root ./my-instance/root principal add partner-a --kind instance --key <base64url Ed25519 public key>
brique-sec --root ./my-instance/root grant add partner-a read '/root/catalog/**' --except '/root/catalog/secrets/**'
brique-sec --root ./my-instance/root apply --reload
```

Quote targets that contain `**`. Start the engine with `-root_path ./my-instance/root`. `--root` is that same root
context directory; `--dir` (or `BRIQUE_SECURITY_DIR`) overrides the machine's directory. `--dir` alone, with no
`--root`, keeps everything in one directory.

| Command | Role |
|---|---|
| `init [--strict --authority-public k]` | directory, instance key, authority, bootstrap, generation `g000001` |
| `key show` | the public keys of this instance (message key, policy authority), and the `principal add` line to run on a peer |
| `tls init\|trust\|show` | the HTTPS certificate of this machine and the interface that uses it; trust a peer's certificate; show what a peer needs |
| `principal add\|encryption\|limits\|disable\|enable\|remove\|list` | edit the source; `--kind local` creates a keyless local principal (`local.<wrapper>` or the reserved `local.ui`); `principal add public.web --kind public` creates the keyless public subject with no grant; `--encryption required\|none` on `add`, or `principal encryption <id> required\|none\|default`, chooses how peer messages travel (§3.7); `principal limits <id> default\|unlimited\|<rate> [<burst>]` sets its request budget (§3.8) |
| `grant add\|remove\|list [--actor a] [--except t] [--as-owner local.x]` | edit grants; the rank is the position in `grant list` |
| `peer add\|remove\|list` | where a principal is reached: `--audience`, `--interface`, `--target https://…`; part of the signed policy. With `--name <machine> --key <public key>…` (repeatable), `peer add` reaches the principal on one more machine, and `peer remove --name <machine>` stops (§7.4.1) |
| `principal key add\|remove` | give a principal another public key, with `--not-before <RFC 3339 UTC date>`, or take one away (a key a machine still names stays) |
| `compile [--out f]` | source to unsigned policy; prints warnings |
| `simulate <principal> <verb> <target> [--actor a]` | `PERMIS`/`REFUSÉ`, the deciding rule and its rank (the reference evaluator) |
| `diff` | source compiled against the active policy |
| `apply [--reload] [--force] [--signed f]` | check, sign, install `g<revision>` atomically, ask the engine to reload |
| `sign --key k --in policy --out envelope` | offline signature for strict mode |
| `doctor [--root dir]` | permissions, keys, generation chain, drift, missing contexts and capacities |
| `denials` | read the denial journal |

**Solo** (the authority key is a `0400` file next to the directory; `apply` signs) and **strict** (the
private key never touches the machine: `compile --out`, `sign` offline, `apply --signed`; the signed
policy must match the source) run the same engine. A restricted wrapper needs `wrapper_auth` required on
the listener.

Guarantees: an invalid change is never written; `apply` replays the engine's acceptance rules on a
throwaway runtime and refuses before installing; a generation appears with one rename; every mutating
command holds the admin lock for its whole read-modify-write; a stale or reused PID is never signalled.

### 11.3 Recipes

Every command takes `--root <instance>/root`, the same path as the engine's `-root_path`; the examples
write it `$ROOT`. A change is not active until `brique-sec --root $ROOT apply --reload`.

**Set up an instance (once).**

```sh
brique-sec --root $ROOT init --instance monsite     # names it; makes the keys on this machine
./brique-engine -root_path $ROOT                    # signed communication is on
```

`init` creates `<instance>/security/access.json` (versioned with the instance) and
`~/.brique/monsite/` (keys, signed policy, state; never versioned). Until `init`, an instance made from
the template runs locally only.

**Let another instance talk to this one.** Both sides do the same, exchanging three things: the
**instance name**, the **public key** (`brique-sec --root $ROOT key show`) and the **HTTPS address**.

1. Give the machine an HTTPS certificate and the instance an HTTPS interface:

   ```sh
   brique-sec --root $ROOT tls init --host monsite.example --host 203.0.113.7 --addr 0.0.0.0:8443
   ```

   This makes a self-signed certificate and its private key in `~/.brique/<name>/tls/` (the key never
   leaves the machine), valid for the names and addresses given (365 days, `--days` to change), and adds an
   `outerCtx` HTTPS interface to the root `context.json` if it has none. The `context.json` names no
   certificate and no path of the machine: the engine looks in the machine's directory, so the file stays
   valid on any machine. Restart the engine once for it to open the interface. Give the other side the
   certificate it prints (`tls show`), and trust the certificate it gives you:

   ```sh
   brique-sec --root $ROOT tls trust alice-cert.pem
   ```

   The certificates you trust are added to the system's, never substituted for them. A certificate from a
   public authority (Let's Encrypt, …) needs none of this: name its files in the interface
   (`tls_cert_file`, `tls_key_file`) and peers verify it as they verify any website.
2. Register the other instance, decide what it may do, and say where to reach it:

   ```sh
   brique-sec --root $ROOT principal add alice --kind instance --key <alice's public key>
   brique-sec --root $ROOT grant add alice call '/root/tools#capacity:*'
   brique-sec --root $ROOT grant add alice read '/root/docs/**' --except '/root/docs/private/**'
   brique-sec --root $ROOT peer add alice --audience <alice's instance name> --interface outerCtx --target https://alice.example:8443/brique
   brique-sec --root $ROOT apply --reload
   ```

   `--interface` is the name of the local HTTPS interface of step 1. The peer is needed in both
   directions: it is where answers to Alice are sent and where calls to her go. Check a right before
   applying with `brique-sec --root $ROOT simulate alice call '/root/tools#capacity:echo'`.
3. Alice does the same with your name, key and address. An external address is
   `@ext_<principal name>:/path`, with the name each side chose for the other: you call her as
   `@ext_alice:/root/tools`, she calls you under the name she registered for you.

What she cannot do is refused identically to what does not exist; every refusal is in the journal
(`brique-sec --root $ROOT denials --principal alice`).

**Give a peer its own outbound interface (when a peer may stop answering).** All the peers named
`--interface outerCtx` share that one interface, which sends one message after the other with a 5 s
timeout: a peer that accepts the connection and never answers holds back the sends to every other
peer. (Measured, one healthy peer and one stopped: the calls to the healthy one waited up to 20 s, and
with an answer silence of 6 s 3 of 8 failed.) Give each peer that matters an outbound-only HTTPS
interface in the root `context.json`, and name it in `peer add`:

```json
{ "name": "to_alice", "type": "outerCtx", "driver": "https",
  "config": { "ingress_enabled": false, "egress_enabled": true, "path": "/brique" } }
```

```bash
brique-sec --root $ROOT peer add alice --audience <alice's instance name> --interface to_alice --target https://alice.example:8443/brique
```

Each peer then has its own queue, its own sending goroutine and its own timeout (measured: the healthy
peer at 9 ms with the other one stopped). **Inbound there is still one door:** every peer's signed
requests and answers arrive through the one `outerCtx` listener, share its queue of 64 places and its
verification workers (at most 16), and are told apart only by the budget of the remote address (§6.8.1)
and, after the signature, the budget of the principal. A per-peer inbound interface does not exist.

**Renew or regenerate the certificate.** `brique-sec --root $ROOT tls init --host … --force` makes a new
pair; the engine serves it within a couple of seconds, with no restart. Peers who trust certificates
with `tls trust` add the new one the same way, and their engine picks it up as it changes. (A
certificate has its own life, apart from the instance's identity key: regenerating it does not change who
the instance is.)

**Edit by hand.** `security/access.json` is the source the commands write; you may edit it yourself and
then run `apply`. It is read strictly (an unknown field is an error and nothing is installed), validated
as the commands do, and `brique-sec --root $ROOT diff` shows what the change would do. Changing a
principal's keys (several keys, `active` / `retired` / `revoked`) is done only this way.

**Replace a lost machine.** Check the instance out and run `brique-sec --root $ROOT init`. It makes new
keys on this machine, compiles and signs the source it finds and keeps every principal, grant and peer;
nothing has to be remembered from the lost machine. The instance's own message key is new, so peers must
register the new public key (`key show`). To keep the same key, restore `~/.brique/<name>` from a backup.
Instances that carry signed policies (anchored arrangement, §11.1) additionally need the authority's
public key.

**Restrict a wrapper.** `principal add local.worker --kind local`, then `grant add local.worker …`.

**A façade.** `principal add local.reports --kind local`; give it its `read`/`write`; then
`grant add partner-a call '/root/reports#capacity:report.generate' --as-owner local.reports`.

**Give a wrapper a secret** (a provider key, say). The value is kept on this machine, the right in the
access source, the need in the wrapper's `context.json` (§8.6):

```sh
pbpaste | brique-sec --root $ROOT secret add anthropic   # or any command that prints it; never an argument
```

Then, in `<instance>/security/access.json`, by hand:

```json
"secrets": { "anthropic": { "wrappers": ["llm_llm"] } }
```

and in the wrapper's entry of its context's `context.json`:

```json
"run": { "secrets": [ { "name": "anthropic", "env": "ANTHROPIC_API_KEY" } ] }
```

then `brique-sec --root $ROOT diff` (it shows `+ secret anthropic: llm_llm`) and `apply --reload`. The wrapper
is given the key at its next launch; no restart of the context is needed after a refusal. To rotate, run
`secret add` again with the new value and restart the wrapper. `secret list` shows names, sizes and dates, never
values; `secret remove <name>` deletes a value and leaves the right. The last step is to take the key out of
the engine's own environment and of any file that holds it, or every wrapper keeps inheriting it.

### 11.4 Runbook

| Situation | What happens | What to do |
|---|---|---|
| Reload refused with "restart the engine" | the engine retains 32 reloaded runtimes | restart; it recovers its durable generation; reload |
| "several generations waiting" | the engine adopts one successor per reload | reload as many times as `apply`/`doctor` say |
| Engine refuses to start: "security clock mismatch" | the wall clock is behind the persisted floor | repair the clock; there is no override |
| A peer is refused and the journal says `quota_exceeded` | it holds a quarter of the replay ledger | see which requests it leaves unanswered; raise `MaxEntriesPerPrincipal` only if justified |
| Everyone is refused and the replay ledger is full | the entry or size limit is reached | maintenance frees finished entries each minute; if it cannot, the entries are unfinished — look at `outcome_unknown` and stuck handlers |
| A caller got `outcome_unknown` | the request may or may not have run | do not retry blindly: check the effect, or use an idempotency key |
| A caller got `response_state_lost` | the waiting instance restarted | same as above: the remote operation may have completed |
| Startup is slow with a big journal | recovery rebuilds the index (about 1.4 s and 1 GiB of allocations at 100 000 entries) | expected; keep the retention short |
| A key is compromised | see §7.6 | revoke, re-issue |

### 11.5 Windows

Windows has no mode bits, no `openat`, no `SIGHUP`. The engine and `brique-sec` use the access list as
the permission and refuse reparse points (symbolic links and junctions) at every path segment; facts are
read from the open handle, not from the path; hard-linked files are refused; locks are `LockFileEx` on a
range far beyond the content (so `engine.pid` stays readable). `init` creates the security directory with
an explicit non-inherited list for the current user, the administrators and the system. `apply --reload`
checks the engine holds the lock on `engine.pid`, then rewrites `reload.request`, which the engine polls
once a second. Residual: each path segment is opened by path rather than relative to its parent's handle;
the parents are verified as writable by trusted principals only, which is what an attacker would need to
exploit the gap. **This code is cross-compiled and vetted but has not yet been run on windows.**

---

## 12. Validation status

State on 2026-11-01, darwin/arm64, Go 1.26. "Proven" means a named test fails when the property is
broken (most were checked by deliberately breaking it).

### 12.1 What is proven

| Claim | Evidence |
|---|---|
| The compiled policy decides exactly as an independent evaluator, for the verbs, `read_recursive`, the doors of every classified capacity and user capacities | differential property test, about 5.6 million answers; `TestPropertyOwnerMatchesOracle` for owners |
| For any configuration (verbs, subtrees, sub-contexts, named elements, genres, exceptions inside granted subtrees), the decision on a REAL request — the family's door plus the resources its real extractor returns — equals an independent evaluator; the extractors themselves are compared with a hand-written model of what each capacity touches | `TestDoorDecisionsMatchAnIndependentEvaluatorForAnyConfiguration` (all 39 Matter and Reflexive capacities × 6 contexts × random sources; 1.2 million decisions at 3000 sources via `BRIQUE_PROPERTY_ITERATIONS`); mutations of five extractors are caught |
| Under a restrictive policy, over real signed HTTPS between two instances: a grant is honored and everything else is refused alike; a reload or a key rotation during a call cuts only the new calls; a restart of the callee during a call is never answered as a success and the same intention is not served twice; a burst of 150 calls gets exactly one terminal answer each (about half served, half refused by back-pressure); a full key rotation (callee then caller) serves the new key and keeps the old one refused; a restart of the caller during a call ends its wait with `response_state_lost`, delivers nothing to the new instance and does not replay; the whole N7 catalogue (41 flows) passes under a verb policy confined to the peer's subtree, and the flows that rebuild the remote projection are refused without `control`; a format-2 (verbs) policy is honored, and an exception added by a reload makes the context answer as absent; a DSL flow follows what the peer grants | `TestEngine_N5_POLICY_01` to `_11`, `TestEngine_N7_ENG_POLICY_01`/`_02`, `TestEngine_N7_WholeCatalogueUnderARestrictivePolicy` |
| An absent, zero or invalid admission, or a missing runtime, authorizes nothing, reads nothing and journals nothing as a caller; a restricted local admission is built only for its own wrapper | `TestAnAbsentOrInvalidAdmissionOrRuntimeRefusesEverything`, `TestRestrictedLocalAdmissionRefusesAnythingButItsOwnWrapper` (mutations caught) |
| A Go wrapper passes the default same-origin policy of the listener without widening `allowed_origins`: it presents the engine's own host as `Origin` | `TestDialOriginIsTheEnginesOwnHost`, `TestConnectPassesTheEnginesSameOriginPolicy` (go wrapper library); verified with a compiled Go wrapper started by the engine |
| With two real engine processes configured only with `brique-sec`, real HTTPS between them and real Python wrappers: a wrapper calls a wrapper of the other instance in both directions; an ungranted capacity is refused exactly like a missing one; a chain through a second wrapper runs with the original caller's rights, and a derived call it does not hold is answered at once with the canonical refusal; a wrapper connection without the launch secret gets 404; restricted local principals act as themselves toward each other and their refusals are read with `brique-sec denials` under their name; a restricted wrapper cannot reach another instance (answered at once, `egress_denied` journaled); a grant added or removed with `apply --reload` applies to the next call of a connected wrapper; the peer is registered with the key printed by `brique-sec key show` | `TestRealProcesses_TwoInstancesWrappersAndLocalPrincipals` (builds the binaries; skipped with `-short`) |
| By default the instance carries only the access source and the machine signs it itself: a new machine rebuilds every principal, grant and peer from the source alone, with keys of its own and no authority key to remember; a change to the source in the instance changes nothing until `apply`; the engine finds both directories from `-root_path` alone | `TestTheInstanceCarriesOnlyWhoMayDoWhat`, `TestANewMachineRebuildsThePolicyFromTheSourceAlone`, `TestChangingTheSourceInTheInstanceChangesNothingUntilApply`, `TestAnInstanceThatCarriesItsPolicyFindsItsMachineDirectoryByName`, `TestTheInstanceTemplateShipsAnInitializedEmptyAccessSource`, the real-process test |
| In the anchored arrangement the signed policies travel with the instance: the engine runs from the instance's policy and the machine's keys, a machine that never ran the instance adopts its newest generation, a policy file changed in the instance (a grant widened, a signature replaced, an emptied file) is refused, a peer address cannot change without a new signature, and the instance holds no key, bootstrap, journal or activation state | `TestTheInstanceCarriesThePolicyAndNeverASecret`, `TestTheEngineRunsFromTheInstancePolicyAndTheMachineKeys`, `TestAPolicyChangedInTheInstanceWithoutTheAuthorityIsRefused`, `TestANewMachineAdoptsAnInstanceThatAlreadyCarriesAPolicy`, `TestAdoptionStartsFromTheNewestGenerationAndOnlyOnAFreshRuntime`, `TestExternalPeersAreSignedWithThePolicy` |
| A capacity, schema, document, matter or structure name that is a path (`../security/x`, `a/b`, `..`) is refused everywhere it would become a file name, so no caller reaches `<instance>/security/` or another context's definitions | `TestValidElementName`, `TestEngine_N6_ReflexiveNamesCannotReachOtherFolders`, `TestEngine_N6_MatterIdentifiersCannotReachOtherFolders` |
| The HTTPS side comes from commands, with nothing of the machine in the versioned `context.json`: `tls init` makes a certificate the engine loads and peers accept for every name and address given, refuses to overwrite it without `--force`, and adds the interface to `context.json` changing only the lines it adds; `tls trust` adds a peer's certificate once; the engine finds its files in the machine's directory, serves a regenerated certificate and trusts a newly trusted one with no restart, and keeps the last good pair when a new one does not load | `TestTLSInitMakesACertificateTheEngineAndPeersAccept`, `TestTLSTrustAddsAPeersCertificateOnce`, `TestEnsureOuterInterfaceOnlyAddsTheInterface`, `TestEnsureOuterInterfaceOnTheRealTemplate`, `TestTheHTTPSInterfaceFindsItsFilesInTheMachineDirectory`, `TestTheCertificateIsReloadedWhenItsFilesChange`, `TestTheClientTrustsANewCertificateWithoutARestart`, `TestTLSCommandsMakeTheHTTPSInterfaceWork`, the real-process test (certificate regenerated while the engines run) |
| The user interface can send an intention to another instance and receives the answer, in both directions; an unknown peer is answered to the interface at once, and what the other instance did not grant is refused to it like to anyone | the real-process test (user interface sends an intention to another instance) |
| With the user interface made of the real parts — Spark starts each engine on its instance, the interface is a Pulse runtime on Spark's relay (`spark/test/e2e/uiDriver.ts`) — two instances with an identity and an HTTPS certificate of their own, made and exchanged only with `brique-sec`: a button sends an intention to the other instance in both directions and the answer is delivered to the button's place in the interface with Pulse's flow in the `success` state; a button asks a wrapper to call the other instance; what is not granted or not known is refused to the interface at once and identically; the refusals are in the other instance's journal under the caller's name; a certificate regenerated while everything runs is trusted with no restart | `TestRealProcesses_SparkAndPulseDriveTwoInstancesWithTheirOwnIdentities` (needs `node`, `python3` and `npm install`; skipped with `-short`) |
| The content of a message is sealed to the recipient's key and opened with its own, with the one Ed25519 key: the conversion of the public and private halves agree for any key, only the recipient opens, a ciphertext altered at any byte, truncated, opened under another header or by another key is refused, and random input never opens | `TestTheTwoConversionsAgree`, `TestSealThenOpen`, `TestOnlyTheRecipientOpensWhatIsBoundToTheRightHeader`, `FuzzOpen` |
| A sealed message shows no plaintext, a payload not yet opened reads as empty, a ciphertext lifted into another signed message does not open, and the header rules (recipient present exactly when sealed, matching its key) hold | `TestSealedIntentionRoundTrip`, `TestSealedResponseRoundTrip`, `TestSealedContentCannotBeMovedToAnotherMessage`, `TestSealedHeaderRules` |
| Between two engines over signed HTTPS: sealed both ways is served and answered; a clear message is refused where sealing is required; a sealed message to a peer that also accepts clear is served; a change of the encryption alone is a change to `apply` | `TestEngine_N5_SEAL_01` to `_03`, `TestChangingOnlyTheEncryptionIsAChangeToApply`, `TestAnInstancePrincipalIsSealedByDefaultAndTheOperatorCanChangeIt` |
| With two real engine processes whose only link is an intercepting HTTPS relay that both trust: the relay captures the exchange and finds neither the question, the answer nor the capacity called; once the operator opts the peers out, the same relay reads the clear exchange | `TestRealProcesses_AnInterceptingRelayReadsNothingOfASealedExchange` |
| A principal that babbles is slowed to its budget and told so (the excess is answered `unavailable`, the burst is honored, a principal with no setting is limited, `unlimited` lifts it); a neighbour keeps its own budget; a lowered rate applies at once; memory stays bounded under a flood of keys; the policy and command validate the numbers | `TestABurstThenTheSustainedRate`, `TestOneCallerBabblingDoesNotTakeTheOthersTokens`, `TestALoweredRateAppliesAtOnce`, `TestInvalidRatesAllowNothingAndMemoryIsBounded`, `TestRateLimitDefaultsExplicitAndUnlimited`, `TestTheOperatorSetsLiftsAndResetsAPrincipalsRate`, `TestEngine_N5_RATE_01`/`_02` (over signed HTTPS between two engines) |
| One admitted request cannot set off more derived calls than its budget or nest them deeper; the call over is cut with `ErrChainBudget`, the budget belongs to the request, and the operator sets it with `principal limits <id> chain` without touching the rate | `TestOneRequestCannotSetOffMoreCallsThanItsBudget`, `TestOneRequestCannotNestCallsDeeperThanItsBudget`, `TestTheBudgetBelongsToTheRequest`, `TestTheOperatorBoundsWhatOneRequestSetsOff` |
| A principal on two machines is reached by key and by name: the default machine is the one of the most recent key, a named machine is reached with `~name`, an unknown one is refused, a key that starts later is not chosen, and the answer to a request goes to the machine of the key that signed it; a message is sealed to the key of the machine it goes to; an answer signed by another machine's key is refused; the signed policy refuses a key that is not the principal's, a key on two machines and a target together with endpoints | `TestAPeerOnTwoMachinesIsResolvedByKeyAndByName`, `TestRotationOnOneMachineSealsToTheMostRecentKey`, `TestAPeerWithOneTargetKeepsWorkingForEveryKey`, `TestEndpointsAndKeysAreCheckedBeforeAnythingIsWritten`, `TestKeysOfAPrincipalAreAddedAndRemovedWithTheirDate`, `TestPeerEndpointsAreSignedAndChecked`, `TestActiveKeysAreTheValidOnesMostRecentFirst`, `TestBeginResponseChecksEveryBinding`, `TestEngine_N5_MACH_01` to `_03`, `TestRealProcesses_APeerOnSeveralMachinesIsConfiguredWithTheCommandLine` (the command line alone configures the machines of a peer) |
| `brique-sec key show` prints the public key a peer registers, derived from the key the engine signs with, and no private material | `TestIdentityGivesThePeerTheKeyTheInstanceSignsWith`, `TestKeyShowGivesTheOtherInstanceWhatItNeeds` |
| Every engine capacity has exactly one class; a new capacity is unreachable until classified | `TestEveryClassifiedCapacityHasExactlyOneKnownClass`, `access.NewFamilyRegistry` |
| No family has a bare-message inbox; the only route in is the delivery channel | `TestNoFamilyLoopOwnsABareMessageInbox` |
| This document's capacity matrix is current | `TestSecurityDocCapacityMatrixIsCurrent` |
| A restricted principal's, a signed caller's and an owner's rights are decided at every door, end to end, on the real engine and the real Python wrapper | `engine_n6_execution_caller_rights_test.go` |
| A real wrapper process whose name matches a `local.<name>` principal, with `wrapper_auth` required and nothing injected, connects with its launch secret and acts as that principal: its granted call is served, its ungranted call is refused and journaled under `local.sandbox_py`; the same wrapper without a principal keeps the trust of its context | `TestEngine_N6_EXEC_ARestrictedRealWrapperActsAsItsPrincipal` (real Python and C++ wrappers started by the engine) |
| Under a restrictive policy an ungranted engine capacity, an ungranted user capacity and a missing one are refused identically over signed HTTPS; a reload mid-call does not cut the admitted call and refuses the next; a disabled principal is answered nothing; a restarted peer does not run an intention twice | `engine_n5_policy_test.go` |
| Two instances over verified HTTPS: routing, boundaries, concurrency, failure, error crossing, composition | `engine_n5`, `engine_n7`, `engine_n8` (about sixty scenarios) |
| Activation state, replay journal, compaction and generation install survive process death at random instants | four kill-and-reopen campaigns (`TestCrashCampaign…`); replacing the atomic writer with a plain write makes the first one fail |
| A clock behind the durable floor is refused at start and at reload | `TestClockBehindTheDurableFloorIsRefused` |
| The replay ledger frees itself; a principal cannot take more than its share; a fire-and-forget request is finished | `TestMaintenance…`, `TestTheEngineMaintainsItsReplayJournal`, `TestOnePrincipalCannotFillTheLedger`, `TestAFireAndForgetRequest…` |
| The wrapper frame is decoded identically by the engine, Go, Python and C++ | `wrapper_frames.json`, run by four suites |
| No data race in any package | `go test -race ./...` clean |
| Nine fuzz targets: strict JSON and canonicalization, envelope verification, bootstrap, activation state, replay journal recovery, identifiers, protected paths, targets, the signed ingress handler | eight seconds each without failure; run them long in CI |
| A wrapper is given a secret only if it declares it, the signed policy grants it and the machine holds a value, and not otherwise; the right is signed (altering it breaks the proof), absent from a policy that grants none, validated like the rest of the source, and installed by `apply` even when it is the only change | `trust/wrapper_secrets_test.go`, `policysrc/secrets_test.go`, `TestSecretRightsGoFromTheSourceToTheEngine`, `TestSecretRightsThroughTheCommandLine` |
| At launch, a secret replaces an inherited variable; a right with no declaration gives nothing; no policy, a right for another wrapper, no value or a value readable by others starts no process and the refusal never carries the value; a refusal for a secret is retried, any other launch failure is still remembered (checked by removing the guard) | `execution/execution_wrapper_secrets_test.go` |
| With a real engine, the real `brique-sec` and a real Python wrapper: refused with nothing granted, refused with a value but no right, given after `apply`, not given to a wrapper that declared nothing, kept by a running wrapper when the right is withdrawn and refused at its next launch, given again once granted again with no restart; the value is in no file of the instance, of the machine or of the logs except its own (checked by cutting the delivery) | `TestRealProcesses_AWrapperIsGivenOnlyTheSecretsItIsGranted` |
| The values are stored owner-only, read back by the engine's own reader, replaced by a rotation, refused when empty, with a NUL or too long, and never printed by `brique-sec`; `doctor` sees a value that others can read | `secadmin/secrets_test.go`, `secrets_doctor_test.go`, `cmd/brique-sec/secret_test.go` |

### 12.2 Measurements

Apple M4, APFS, single runs.

| Operation | Result |
|---|---|
| Capability decision, short path / 8 levels | 150–165 ns / about 830 ns, 3 allocations |
| Resource decision | about 200 ns |
| `Delivery` construction | 36 ns, no allocation |
| Envelope verification (parse, key, SHA-256, Ed25519), 512 B / 16 KiB / 256 KiB / ~3 MiB | 0.07–0.12 ms / 0.25–0.4 ms / 2.9 ms / 33 ms |
| Envelope signing, 512 B / 256 KiB | 0.08 ms / 0.34 ms |
| Replay reservation, one sync each / group commit (10 per sync) / duplicate lookup | 3.76 ms / 0.39 ms / 1.8 µs |
| Replay recovery, 1 000 / 10 000 / 100 000 entries | 18 ms / 136 ms / 1.4 s and 1.0 GiB allocated |
| Compaction, same sizes | 5 ms / 23 ms / 220 ms |
| Policy reload (80 principals, 160 scopes): verify and decode / prepare and commit | 39 ms, 30 MB / 5 ms, 3.5 MB |
| Pending-response registry, one running response / register and finish | 70 ns / 600 ns |
| First denial explanation of a snapshot / next | 6.6 ms / 1.3 µs |
| Filtered `meaning.query` | 0.44 ms |

### 12.3 Open items

| Id | Severity | Item |
|---|---|---|
| W1 | High for portability | Run the whole suite on windows; the port is written, not run |
| V2 | Medium | Power loss is not modelled (the campaigns kill the process; they do not drop unsynced writes); the egress commit of a cached answer is not killed mid-way |
| V3 | Medium | Missing measurements: latency percentiles and an end-to-end admission benchmark, a maximum-size policy, saturation behaviour; the constants in code do not yet cite the numbers above. Measured burst, with the request budget lifted: 150 simultaneous calls from one principal give about 80 served and 70 refused at once with `unavailable` (B's signed ingress queue holds 64 and answers 503 when full, by design: it sits before signature verification; A's egress queue holds 16). A sender now retries a signed message the peer refused with 429 or 503 three times (25, 100 and 250 ms) before it reports it; a client that bursts beyond that must still retry `unavailable` itself |
| V4 | Low | Replay recovery allocates about 280 times per entry (1 GiB at the maximum) |
| V5 | Low | Fuzz follow-ups: UUID and `@` address parsing, extractor normalization, WebSocket framing; continuous fuzzing in CI |
| V6 | Low | A published corpus of signed-envelope vectors (only the wrapper frame has one) |
| V7 | Low | Provenance is best effort (a bounded book): an evicted root leaves its events without a principal |
| V8 | Info | The operator runbook (§11.4) has not been exercised by someone other than its author |
| E1 | Medium | Egress control (D07): no grant lets a restricted local principal (or an admitted chain) send to an external peer; they are refused (`egress_denied`) |
| R1 | — | An **independent review** that traces every externally reachable side effect from envelope to durable replay has not been done; §4.1 and the denial journal are written to make it possible |

---

## Appendix A. Decisions

The model was settled decision by decision; this is the surviving list.

| # | Decision |
|---|---|
| D01 | Granularity: a context alone or with its subtree, exclusions of contexts or elements, named elements, capacities |
| D02 | Identity has two levels: the emitting **instance** (principal) and the **actor** inside it |
| D03 | An external principal receives the verbs the policy gives it, `define` and `control` included; the policy decides, the engine has no "never external" category |
| D04 | The right to data is independent of the channel (direct call, subscription, HTTP access to the substance) |
| D05 | Cross-tree discovery is filtered by the caller's rights |
| D06 | A refused element is indistinguishable from a missing one |
| D07 | Egress to an external instance is controlled: refused by default for restricted principals and admitted chains; a grant that opens a route is **not implemented** (open item E1) |
| D08 | No time windows on rights. Rates are not limited; a **share of the replay ledger** per principal is (added later) |
| D09 | Creating gives no right on the result |
| D10 | Elements created later in a granted subtree are covered; the compiler needs no capacity catalog |
| D11 | Revocation applies to every new delivery, long sessions included |
| D12 | Provenance (principal, actor, origin of the chain) is kept in the traces |
| D13 | A capacity runs with the rights of its caller; its mediated sub-calls are authorized against the caller's admission, unless `as_owner` |
| D14 | The actor is not a trust boundary: a label that can only restrict, mandatory if the principal declares actors |
| D15 | Refusals are journaled |
| D16 | Wrappers launched by the engine get a per-launch secret, with no change to the libraries |
| D17 | Simplicity first: degrade a feature before adding a concept |
| D18 | `trace.inspect` is all or nothing on content |
| D19 | `vocabulary.*` returns the complete vocabulary |
| D20 | `read.structure` is a tree from which what the policy does not cover is removed |
| D21 | The LLM pilot (MCP) is a restricted local principal; a wrapper with no entry stays trusted |
| D22 | Message content in the trace is a yes/no right, off by default (`trace_payloads`) |
| D23 | `define` and `control` are verbs like the others, grantable to any principal |
| D24 | `as_owner` is included: a façade runs as a named local owner, bounded to the capacity's subtree |
| D25 | `create` is a separate verb from `write` for data |
| D26 | Engine capacities are authorized by verb and class, with no capacity names anywhere in a policy |
| D27 | An instance carries its access source and its peers (versioned with it); private keys, the signed policy, the trust anchor and durable state stay on the machine. Carrying the signed policy too is the anchored arrangement, for instances that need an authority kept outside |
| D28 | Peer addresses are part of the access source and of the signed policy |
| D29 | An element name or identifier is one file name, never a path |
| D30 | The message key is also the encryption key (Ed25519 converted to X25519); the content is sealed with HPKE to the recipient, bound to the signed header; an instance principal is sealed by default and the operator opts it out explicitly |
| D31 | Every principal has a request budget by default; lifting it is an explicit setting. An over-budget request is answered `unavailable` when its caller waits, dropped when nobody does |
| D32 | The calls one admitted request sets off inside the instance are counted in all and by depth, in a counter every derived delivery shares; the call over the budget is refused where it is created |
| D33 | A key is bound to a machine: a peer lists endpoints, each with its address and the keys that live on it; messages are sealed to the most recent key of the machine they go to, answers go back to the machine of the key that signed the request, and an answer is accepted only from the machine asked |
| D34 | Anonymous browser authority is the explicit keyless `public.web` policy subject; the engine manufactures a separate ephemeral channel for operational ownership, and no connection grants rights by itself |
| D35 | Signed browser traffic uses the reserved actor `brique.ui`; it always needs an exact binding, `default` never substitutes for it, and principal-only evaluation is exposed only as a refusal diagnosis, never as an Admission |
| D36 | A browser controlling its own instance runs as the explicit keyless principal `local.ui`, never as ambient Context authority; engine-created channel/source own the exchange and wrapper channels cannot borrow the id |
| D37 | The browser rendez-vous uses native messaging and one explicit owner-protected launcher gateway choice; no page-selected alias, loopback trust, silent fallback or bridge-held Brique key |
| D38 | A browser bridge session is memory-only and host-owned: exact request sequence, immutable origin/document/frame/UI binding, host-created channel, bounded opaque payloads and irreversible close/EOF |
| D39 | A public instance descriptor is short-lived and self-signed through the signer boundary; identified use additionally requires one atomic check against the active Brique Sec key, principal, audience, browser origin and configured key-to-machine binding. Five-minute validity plus a fresh hello bound to its `bid1_` hash replaces a persistent generation counter |
| D40 | Identified browser WSS uses a three-message, mutually signed handshake on one connection: initiator challenge and current `bid1_`, responder challenge and complete echo, then initiator finish. Both sides recheck current Brique Sec state; the resulting `bwg1_` generation is transcript-bound, memory-only and never resumes another socket |
| D41 | A signed browser endpoint is only a proposal: its key selects one configured Brique Sec machine, its WSS URL must remain under that machine's signed HTTPS target, and every resolved IP must pass an explicit local policy. Dialing is pinned to the checked IP set, so DNS is not consulted again |
| D42 | The browser WSS socket is engine-to-engine, never browser-to-engine: it requires the dedicated subprotocol, rejects Origin, bounds concurrent sessions and handshake time, and completes the three signed messages before handing the socket to any adapter. A proxy is not discovered implicitly because it would be an unchecked destination |
| D43 | After the hello, signed envelopes remain byte-identical and use a tiny socket-generation-local binary frame. One ordered reader and one writer preserve WebSocket safety; bounded pending sends receive ack only after remote ingress accepts the bytes, reject on explicit refusal, and an unacknowledged close remains outcome-unknown. The transport never retries an Intention |
| D44 | Authenticated duplex routes are ephemeral and transport-neutral: Comm verifies that the signed key, configured peer instance and reserved `brique.ui` actor match the route, UI requests may select the active peer/key route, and every response or replay returns on the exact admitted `bwg1_` without silent HTTPS fallback |
| D45 | The identified WSS server is an explicit root `outerCtx` HTTPS feature: its advertised external URL and UI origins are configuration, never inferred from a listener or request; descriptor and hello use the current Brique Sec generation, and every refresh signs under a bounded local deadline even when the HTTP client supplies none. TLS 1.3 is mandatory, browser Origin is refused and interface shutdown cancels every route |
| D46 | The identified WSS initiator accepts only bridge-attested descriptor bytes and origin, then composes the current Brique Sec gate, endpoint-under-target check, bounded DNS/IP policy, pinned dial and signed hello. It rechecks the atomically published complete runtime after the handshake, so a concurrent reload closes the socket before route registration. It registers the valid route before publishing the channel, and context cancellation closes it without retry |
| D47 | The remote branch of the browser Sink owns the bridge channel-to-route binding: with a descriptor it derives the exact peer and machine from the authenticated WSS connection, clears page source and identity, keeps only semantic user type, and forces actor `brique.ui`. Root Comm ingress manufactures the registered `@ui_<channel>` source exactly as child ingress does; signed responses return only through that ephemeral UI route. Closing either side removes route and ownership exactly once |
| D48 | Native host to engine uses a distinct owner-only `brique.browser-engine/1` IPC endpoint, never the administrative control socket or a loopback port. The current engine generation is the first gate; one connection carries one host session, bounded channels and payloads, exact request/event sequences, and EOF closes every Sink binding exactly once. This transport holds no Brique identity or rights |
| D49 | Engine IPC is mounted only by an explicit root `browser_bridge` block and is published before readiness. Malformed or unknown security keys fail startup. Public Internet, cleartext loopback and each private CIDR are independent local choices; remote signatures cannot enable them. Client TLS is 1.3 with system roots plus the current protected Brique Sec trust bundle. Shutdown closes IPC bindings before Comm |
| D50 | The native host resolves exactly the owner-selected launcher alias and the matching ready engine descriptor; root object/path, generation, PID and control status must agree. For local mode it also requires the exact launcher-supervised web origin, normalizing only the optional trailing slash of the root URL, never arbitrary loopback or another path. It never starts an instance, falls back to another alias, reads a Brique key or trusts Chrome's process arguments for business authority. One process owns one K2 Session and exits on native EOF or disconnect |
| D51 | The local branch of the browser Sink accepts descriptor-free traffic only after D50, re-admits the explicit `local.ui` principal on every message, constructs a non-serializable Delivery that replaces page source/identity, and routes replies only to the exact engine-created channel/UI. Revocation, close and EOF fail closed and remove the ephemeral route |
| D52 | Extension injection and a page port create no native process. Only an `open` released by trusted active user input starts native messaging. Navigation, native-host death and service-worker termination irreversibly close that document's authority; a fresh document repeats activation and receives new process/session/channel coordinates |
| D53 | A secret reaches a wrapper only if the wrapper declares it, the signed policy grants it and the machine holds the value; the right is indexed by wrapper name, not by `local.<name>` and not by the wrapper's command (an unsigned `run.cmd` under the same name is given the secret, and runs under the account that can read the secret file), so granting a secret does not restrict the wrapper; a refusal for a secret is not remembered; the policy is asked at each launch; the local machine is trusted and a wrapper that receives a secret can disclose it |

The composed remote qualification for D38/D44/D47/D48 crosses a K2 Session, the real owner-only IPC
listener and client, BrowserSink, Comm, mutually configured Brique Sec runtimes and pinned TLS 1.3.
It verifies that a page-forged source and identity are removed before signing and that the signed
response returns through the same IPC generation and exact Session channel. A Chrome for Testing 157
campaign on macOS additionally covers real injection, trusted activation, native-host process launch,
the principal/`brique.ui` rights intersection at B, `read.state`, and the final response in the page.
Other browsers, Windows-native ACLs and non-loopback network deployments remain separate qualifications.

## Appendix B. Package map

| Package | Role |
|---|---|
| `security/wire` | signed envelope: build, strict verify, classified failures |
| `security/inner` | strict decode of the authenticated inner message, source projection |
| `security/trust` | policy model, validation, compiled immutable snapshot, decisions, activation runtime |
| `security/policysrc` | access source, compiler, reference evaluator |
| `security/access` | resource extractor registry, classes of engine capacities |
| `security/admission` | immutable external/restricted admission, read scope, denial recording |
| `security/replay` | durable replay ledger, group commit, maintenance, quota |
| `security/pending` | outbound response registry (two-phase, ordered) |
| `security/runtime` | one atomically published complete runtime (signers, trust, replay, peers) |
| `security/instancedescriptor` | strict short-lived public descriptor, signature/freshness/origin checks and `bid1_` binding hash |
| `security/browserhello` | bounded mutual WSS hello state machines, fresh challenges and transcript-derived `bwg1_` connection generation |
| `networkpolicy` | bounded DNS resolution, public/LAN address policy and IP-pinned dial function for browser WSS |
| `browsertransport` | real WebSocket hello, bounded ordered `security/wire` framing and transcript-derived registration into `signedtransport`; no Comm policy |
| `browserclient` | initiating composition of current Brique Sec, network policy, pinned WSS dial and pre-published hub route; no browser message authority |
| `signedtransport` | bounded in-memory rendez-vous from authenticated pair/key/`bwg1_` routes to Comm; no WebSocket, Intention or policy semantics |
| `security/signer`, `signer/file` | signer boundary and the file provider |
| `security/bootstrap` | protected bootstrap loader |
| `security/hostdir` | where the instance's security folder and the machine's directory are, found from the instance alone |
| `security/activation` | durable activation state |
| `security/manager` | startup, reload, shutdown of the runtime |
| `security/catalog` | resource kinds and actions the policy is compiled against |
| `security/audit` | denial journal |
| `security/refusal` | canonical refusals |
| `security/secadmin`, `cmd/brique-sec` | offline administration, including the values of the secrets of the machine |
| `junction` | `Delivery`, derivation, response permits, wrapper secrets and delegation, provenance |
| `communication` | signed ingress and egress, routing, restricted wrappers, and the browser Sink whose distant and local branches bind one bridge channel to authenticated or explicitly admitted authority |
| `browserbridge` | bounded native-messaging/session state plus owner-only generation-bound engine IPC client/listener; no identity or admission |
| `browserhost` | keyless native-host orchestration from protected launcher choice and engine discovery to one K2 Session |
| `launcher` | protected instance registry, supervision and browser gateway choice |
| `execution`, `matter`, `reflexive`, `context`, `trace` | the families and their doors |
| `shared` | protected files (unix and windows), instance files, element names, atomic replace |
