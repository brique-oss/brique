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
* **A façade hands out what its owner can read** if its author puts it in the answer (§9.2).
* **The audit journal is an operational record, not a tamper-proof ledger.**

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
by `capacity/execute`, local principals, actors. `3` — format 2 plus `as_owner`. The compiler emits
format 2, and format 3 only when a source uses `as_owner`, so an older engine keeps reading a policy
that does not need the newer feature.

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

## 4. How a request is decided

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
* a local admission has no key, so the current snapshot must still hold an active principal of the
  same id.

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
| `content_mode` | `clear` (the only mode shipped, §6.9) |
| `payload_encoding`, `payload_size`, `payload_hash` | `base64url`; exact decoded size; hash of the exact payload bytes |
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

### 6.9 Confidentiality

Only `content_mode=clear` ships; the engine refuses anything else. An HPKE mode (end-to-end
confidentiality of the payload, key rotation of recipients, no downgrade) is **designed but not
implemented**: the requirement is that a message can never downgrade the mode a policy requires.

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
certificate: a caller authenticates by signing. Messages are not encrypted at the message level (§6.9),
so the connection's TLS is what protects their content in transit.

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

The listener setting `wrapper_auth` is `required` (a plain URL gets `404`; set in the template for new
instances) or `optional` (the engine default, so existing instances keep working; a plain URL is
accepted except for a wrapper the policy restricts). A restricted wrapper always requires the secret.

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
resolved to the HTTPS target configured for that principal; an unknown principal is refused with no
send), and an externally admitted message never leaves except as the signed answer to its own request.
A call may be emitted by any context of the instance and by an element of its user interface
(`@ui_main:/panel`): the other instance is told the context that owns the interface as the origin, and the
answer — or the error, at once — is delivered back to the element.

A restricted local principal, and any chain admitted from another instance, **cannot send to another
instance**: the call is answered at once with the canonical refusal and journaled as `egress_denied` under
the principal's name. A policy that would grant such a route (decision D07) is not implemented (open item
E1); an unrestricted local component is not subject to it.

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
| `principal add\|disable\|enable\|remove\|list` | edit the source; `--kind local` creates `local.<wrapper>` without a key |
| `grant add\|remove\|list [--actor a] [--except t] [--as-owner local.x]` | edit grants; the rank is the position in `grant list` |
| `peer add\|remove\|list` | where a principal is reached: `--audience`, `--interface`, `--target https://…`; part of the signed policy |
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
| V3 | Medium | Missing measurements: latency percentiles and an end-to-end admission benchmark, a maximum-size policy, saturation behaviour; the constants in code do not yet cite the numbers above. Measured burst: 150 simultaneous calls from one principal give about 80 served and 70 refused at once with `unavailable` (B's signed ingress queue holds 64 and answers 503 when full, by design: it sits before signature verification; A's egress queue holds 16). A does not retry a 503 automatically; a client that bursts must retry `unavailable` itself |
| V4 | Low | Replay recovery allocates about 280 times per entry (1 GiB at the maximum) |
| V5 | Low | Fuzz follow-ups: UUID and `@` address parsing, extractor normalization, WebSocket framing; continuous fuzzing in CI |
| V6 | Low | A published corpus of signed-envelope vectors (only the wrapper frame has one) |
| V7 | Low | Provenance is best effort (a bounded book): an evicted root leaves its events without a principal |
| V8 | Info | HPKE confidentiality is not implemented (§6.9); the operator runbook (§11.4) has not been exercised by someone other than its author |
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
| `security/signer`, `signer/file` | signer boundary and the file provider |
| `security/bootstrap` | protected bootstrap loader |
| `security/hostdir` | where the instance's security folder and the machine's directory are, found from the instance alone |
| `security/activation` | durable activation state |
| `security/manager` | startup, reload, shutdown of the runtime |
| `security/catalog` | resource kinds and actions the policy is compiled against |
| `security/audit` | denial journal |
| `security/refusal` | canonical refusals |
| `security/secadmin`, `cmd/brique-sec` | offline administration |
| `junction` | `Delivery`, derivation, response permits, wrapper secrets and delegation, provenance |
| `communication` | signed ingress and egress, routing, restricted wrappers |
| `execution`, `matter`, `reflexive`, `context`, `trace` | the families and their doors |
| `shared` | protected files (unix and windows), instance files, element names, atomic replace |
