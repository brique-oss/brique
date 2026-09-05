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



