# Brique

**Brique is an executable architecture framework for making a system’s responsibilities, transformations, state, and intentions explicit.**

It provides a small set of composable primitives to describe what exists in a system, where responsibilities are located, what transformations are possible, how those transformations are addressed, and what actually happens when they execute.

Brique is designed so that architecture remains connected to implementation and runtime behavior while staying modular, decentralized, interoperable, traceable, and evolvable.

Because this architecture is explicit and persistent, it can also become a shared working space between people, software, and language models.

## Mental model

Brique represents a system through a small set of explicit elements.

A **Context** situates a responsibility.

A **Capacity** describes a transformation that can be performed within that responsibility.

**Matter** represents what exists, is read, produced, referenced, or transformed.

An **Intention** addresses a Capacity and asks for a transformation to occur.

```text
World
 └─ Context
     ├─ Matter
     └─ Capacity

Intention
    ↓
Context / Capacity
    ↓
transforms Matter
```

These four elements form the core model.

Other elements extend it:

* **Document** preserves clarified understanding, decisions, and architectural intention.
* **Schema** describes an expected representation.
* **Wrapper** connects a Capacity or Matter to conventional code and runtime state.
* **Trace** records what actually happened during execution.

Together, they allow a system to be described not only by its implementation, but by an explicit world of situated responsibilities, possible transformations, state, and execution.

## Decompose downward, compose upward

Brique organizes architecture through two complementary movements.

When a responsibility is too broad, it can be decomposed into more precise responsibilities.

Each responsibility is situated in its own Context and can expose more specialized Capacities.

```text
responsibility
    ↓
sub-responsibilities
    ↓
specialized Capacities
```

This decomposition can continue recursively: a Context can contain other Contexts, each with the same architectural model.

The opposite movement is composition.

Specialized Capacities can be connected through their contracts and combined into larger transformations.

```text
specialized Capacities
    ↓
composition
    ↓
higher-level transformation
```

The architecture becomes coherent when both directions agree: decomposition makes responsibilities precise, while composition reconstructs the larger behavior expected from the system.

This recursive organization is what allows Brique to remain modular as a system grows.

## Contract before implementation

A Capacity defines a transformation through an explicit contract.

That contract can describe:

* its role;
* its inputs;
* its outputs;
* its effects;
* its invariants;
* its success conditions;
* its failure conditions.

The contract states **what the transformation means and what it must preserve**.

Its **resolution** states **how that transformation is performed**.

This separation allows a responsibility to exist before its implementation is fixed.

A Capacity can first be formalized as a contract, then later be resolved through Brique itself, interpreted code, compiled code, or another implementation behind a Wrapper.

```text
Capacity
 ├─ contract
 │   └─ what the transformation means
 └─ resolution
     └─ how the transformation is performed
```

The implementation can therefore evolve without silently changing the responsibility it realizes.

This makes contracts a stable surface for composition, delegation, replacement, and progressive refinement.

## From intention to execution

A Brique architecture is executable.

Execution begins with an **Intention** addressed to a specific Capacity in a specific Context.

The Capacity performs the transformation defined by its contract and resolution, reading or transforming Matter when required.

```text
Intention
    ↓
Context / Capacity
    ↓
Matter
    ↓
Transformation
    ↓
Response
```

A Capacity can also emit Intentions toward other Capacities.

Larger behaviors can therefore be built by composing smaller, situated transformations.

Brique keeps three levels distinct:

* **Architecture** describes what responsibilities and transformations exist.
* **Execution** describes what was actually requested.
* **Observation** describes what actually happened.

Execution leaves observable events in **Trace**, making it possible to follow the path from an initial Intention through the transformations and responses it produced.

```text
Architecture
    ↓
Intention
    ↓
Execution
    ↓
Trace
    ↓
Observation
```

This keeps runtime behavior connected to the architecture that gave it meaning.

## Persistent architecture

Brique is designed so that architecture does not disappear into implementation or conversation.

Understanding can progressively become explicit and persistent:

```text
clarify
→ formalize
→ implement
→ execute
→ observe
→ evolve
↺
```

These are not rigid phases.

A system can move back from implementation to clarification, from observation to architecture, or from formalization to decomposition whenever needed.

Different Brique elements preserve different parts of this evolution:

* **Documents** preserve clarified understanding, decisions, assumptions, and architectural intention.
* **Contexts and Capacities** preserve the formalized architecture.
* **Matter** preserves the state and resources being transformed.
* **Code and Wrappers** preserve implementation.
* **Trace** preserves execution observations.

This creates continuity between:

```text
why something exists
        ↓
what it is responsible for
        ↓
how it is implemented
        ↓
what actually happened
```

The architecture can therefore evolve from observation without requiring its meaning to be reconstructed from scratch.

## A shared architecture

Because Brique makes responsibilities, transformations, state, and execution explicit, the architecture can become a shared working space.

People can work on the same persistent representation of the system:

* clarify a responsibility;
* refine a contract;
* inspect Matter;
* change an implementation;
* observe execution;
* revisit a decision from what actually happened.

The same explicit architecture can support collaboration between people, and collaboration between people and language models.

A language model can inspect the same Contexts, Capacities, Documents, Matter, and Trace as a human.

It can help clarify an intention, propose a formalization, implement a validated transformation, execute it, and inspect the resulting observations.

But the architecture does not belong to the model.

The model operates within an explicit world whose responsibilities, contracts, and state remain persistent outside the conversation.

```text
human
   ↘
    shared Brique world
   ↗
language model
```

This makes collaboration less dependent on conversational memory and allows different people, models, and tools to resume work from the same explicit architectural state.

Brique therefore supports delegation without requiring the architecture itself to become implicit inside the actor performing the work.

A Brique architecture can describe a software system, a workflow, a data transformation, an integration boundary, or a larger system composed from all of these.

## Try Brique

The model becomes concrete through the Brique runtime and VSCode extension.

Brique is used through its VSCode extension.

From the repository root:

```bash
npm install
npm run vscode:deploy
```

Then open VSCode and run:

```text
Brique: New Instance
```

Choose a folder for the new instance.

Brique creates a root Context, starts the associated engine, and opens the Brique editor.

From there, you can explore the Context tree, inspect Capacities and Matter, and see how the architecture is represented and executed.

You can also open an existing instance with:

```text
Brique: Open Context
```

or:

```text
Brique: Open Workspace Context
```

### Run an instance from the command line

An instance is a directory that holds the root Context next to its wrappers:

```text
my-instance/
  root/        the root Context and the Contexts below it   ← what the engine is given
  wrapper/     the runtime libraries of the wrappers
  security/    who may do what (created when the instance is given an identity, see below)
```

Without the extension, build the engine and start it on the root Context:

```bash
npm run build:brique          # writes bin/brique (the engine) and bin/brique-sec (see below)
./bin/brique -root_path my-instance/root
```

An instance runs locally with no further setup.

### Share a Context as a Git submodule

A Context may live in its own Git repository and be mounted at its normal path in an instance as a
submodule. Brique does not need a special command or runtime concept for this: after Git has populated
the submodule, the engine still sees an ordinary directory containing `context.json` and the complete
Context tree below it.

This separates two useful notions:

* the instance is each person's executable assembly of Contexts;
* a shared Context is an independently versioned repository;
* the instance repository records the exact commit of that Context that it executes.

Before publishing a Context, review its `.gitignore`. Commit its architectural definition — including
`context.json`, element descriptors, code, wrapper bindings, UI sources, and shareable child Contexts —
but exclude generated or machine-local state such as `projection/`, `trace/*.jsonl`, `.spark/`, build
outputs, caches, local environments, credentials, private keys, and non-shareable Matter substance.
Stop the engine during the initial conversion so that it cannot generate files while the first commit
is being assembled.

Assume the instance itself is already a Git repository, the Context is at
`root/shared-context`, and an empty remote repository has been created. Convert the existing directory
in place:

```bash
cd my-instance/root/shared-context
git init -b main
git add .
git commit -m "Publish shared Context"
git remote add origin git@github.com:example/shared-context.git
git push -u origin main

cd ../..
git rm -r --cached root/shared-context
git submodule add --force git@github.com:example/shared-context.git root/shared-context
git submodule absorbgitdirs root/shared-context
git add .gitmodules root/shared-context
git commit -m "Mount shared Context as a submodule"
```

`git rm --cached` removes the old files only from the parent repository's index; it does not delete the
working directory. `git submodule add --force` then registers the repository already present at that
same path. Check `git status` and the submodule's remote before committing the conversion. If the
Context was never tracked by the instance repository, omit the `git rm --cached` step.

Clone an instance and all its shared Contexts with:

```bash
git clone --recurse-submodules git@github.com:example/my-instance.git
```

For an instance that is already cloned:

```bash
git submodule update --init --recursive
```

Work on the shared Context inside its repository, on an explicit branch, then record the selected
commit in the instance:

```bash
git -C root/shared-context switch main
git -C root/shared-context pull --ff-only
# edit, commit, and push inside root/shared-context when contributing to it

git add root/shared-context
git commit -m "Update shared Context"
```

The last commit changes only the instance's pointer to the shared Context. Other instances remain on
their previously selected commit until their owners deliberately update it. A Context repository may
itself contain submodule Contexts; use the recursive clone/update forms above for that fractal case.

### Connect through MCP

Once an instance is open, use the `MCP` button in the Brique editor to start its MCP server.

A compatible MCP client can then connect to the instance and work on the same explicit Brique world.

Through MCP, a connected client can:

* inspect Contexts and their elements;
* retrieve Capacity contracts;
* read Matter and Documents;
* send Intentions;
* observe Responses and Trace;
* contribute to the architecture from the persistent state of the instance rather than from conversational memory alone.

## Give an instance an identity and connect it to others

Instances talk to each other over HTTPS, and every message is signed. For that an instance needs a
**name**, a **key pair** that identifies it, and an **HTTPS certificate**. `brique-sec` creates all
three and manages who may do what. It is built with the engine (`bin/brique-sec`).

```bash
# 1. Once per instance: a name, and the keys of this machine
./bin/brique-sec --root my-instance/root init --instance monsite

# 2. Once per machine: the HTTPS certificate, and the HTTPS interface in the instance
./bin/brique-sec --root my-instance/root tls init --host mon-hote.exemple --addr 0.0.0.0:8443

# 3. What to give the other instance, and what to ask of it
./bin/brique-sec --root my-instance/root key show      # your name and public key
./bin/brique-sec --root my-instance/root tls show      # your certificate (the file to hand over)

# 4. For each instance you want to talk to (alice here): who she is, what she may do, where she is
./bin/brique-sec --root my-instance/root tls trust alice-cert.pem
./bin/brique-sec --root my-instance/root principal add alice --kind instance --key <alice's public key>
./bin/brique-sec --root my-instance/root grant add alice call '/root/tools#capacity:*'
./bin/brique-sec --root my-instance/root grant add alice read '/root/docs/**' --except '/root/docs/private/**'
./bin/brique-sec --root my-instance/root peer add alice --audience <alice's instance name> --interface outerCtx --target https://alice.example:8443/brique

# 5. Make it active, then (re)start the engine the first time
./bin/brique-sec --root my-instance/root apply --reload
```

Alice does the same with your name, public key and certificate. Nothing is active until `apply`, and
whatever she is not granted is refused exactly like something that does not exist.

**What is where.**

| Where | What | Versioned with the instance? |
|---|---|---|
| `my-instance/security/access.json` | who may do what: instances, their public keys, their rights, their addresses | yes |
| `~/.brique/<name>/` | this machine's private key, HTTPS certificate and key, the signed policy, the journal of refusals | **no, never** |

You can edit `security/access.json` by hand and run `apply`, or use the commands: both end in the same
file, and `brique-sec diff` shows what a change would do before you apply it. Changing the file, or
pulling a change to it, changes nothing until `apply` is run on the machine. The engine finds both
places from `-root_path` alone, so the extension needs no configuration.

**Keys.** The key that identifies the instance never leaves the machine; only its public half is given
away. The HTTPS certificate is separate: it encrypts the connection, and a regenerated one
(`tls init --force`, then `tls trust` on the other side) takes effect without restarting either engine.

**Messages are encrypted end to end.** What an instance asks another, and what comes back, is signed
with its key and sealed to the key of the instance it is for, so a proxy or anything else between
the two sees who talks to whom and nothing of the content. There is no second key: the same key that
identifies the instance is used. An instance you register with `principal add --kind instance` is
sealed by default; for a peer that cannot, `principal encryption <id> none`, then `apply`.

**If you lose the machine**, check the instance out on a new one and run `init` again: the new machine
makes its own keys and rebuilds the policy from `security/access.json`, so nothing has to be remembered.
Your peers register your new public key (`key show`). To keep the same identity, back up `~/.brique/<name>/`.

The full model — rights, restricted wrappers, the signed protocol, what has been proven — is in
[engine/SECURITY.md](engine/SECURITY.md); the commands and recipes are in its section 11.
