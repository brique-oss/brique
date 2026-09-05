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

# Intent — `mcp_brique` Context

# Vision

**Brique is a cognitive framework for representing, structuring, formalizing, implementing, and executing intentions.**

It is not a programming framework, nor simply an execution engine. It provides an explicit representation of the cognitive process that transforms a human intention into an operational system.

The purpose of the MCP is to make this cognitive framework accessible to a Large Language Model.

Its primary objective is not to expose an API, but to install the cognitive frame that enables the LLM to reason, navigate, and operate naturally within Brique.

---

# Objective

Create an MCP wrapper allowing coding agents (Codex, Claude Code, or equivalent) to interact seamlessly with a Brique instance.

The wrapper acts as the bridge between an external LLM and Brique.

It is intentionally simple. Its responsibility is to transport requests and responses between the MCP protocol and the Brique engine.

The intelligence of the system does not reside inside the wrapper.

It resides in the cognitive framework transmitted to the LLM and in the Brique engine itself.

---

# Purpose of the Cognitive Framework

The purpose of the cognitive framework is to maximize the LLM's ability to transform an initially vague user intention into an operational implementation using the Brique model.

The LLM does not explain Brique to the user.

Instead, it internally adopts the Brique cognitive framework as its own reasoning model while interacting naturally with the user.

The user may know nothing about Brique.

The user may know Brique perfectly.

The reasoning process remains identical.

The LLM is responsible for progressively transforming intentions into explicit artifacts.

---

# Intention Transformation

Within Brique, an intention naturally evolves through successive representations.

```
Intention
    ↓
Clarification
    ↓
Documents
    ↓
Formalization
    ↓
Brique Elements
    ↓
Implementation
    ↓
Executable Code
    ↓
Execution
    ↓
Trace
```

Each stage produces a well-defined output.

Inputs, however, may originate from anywhere within the Brique ecosystem:

* documents
* source code
* traces
* matters
* capacities
* structures
* schemas
* semantic descriptors
* runtime state

The transformation is constrained by the expected output of each stage, not by the origin of its inputs.

---

# Cognitive Context

When an LLM connects through the MCP, it receives a cognitive context rather than a traditional API description.

This context includes:

* the Brique vision;
* the Brique ontology;
* the Brique DSL;
* reasoning rules;
* navigation rules;
* the engine decision tree;
* engine capability families.

This context establishes the cognitive environment in which the LLM will reason throughout the entire interaction.

The initial goal is not token optimization.

The goal is to maximize interoperability between:

* the user;
* the LLM;
* the Brique engine.

Optimization of context size comes later, once the desired reasoning behavior has been validated.

---

# Ontology

The ontology defines the conceptual universe of Brique.

It introduces the relationships between:

* Intention
* Context
* Matter
* Capacity
* Structure
* Schema
* Trace
* Meaning
* Semantic Field
* Brique DSL

The ontology is not documentation.

It defines the conceptual model used by the LLM to reason.

---

# Brique DSL

The Brique DSL provides the formal language used to describe capacities.

It defines:

* contracts;
* semantic sections;
* transformation flows;
* references;
* morphings;
* execution semantics.

The DSL is part of the ontology itself.

Understanding capacities requires understanding the DSL.

---

# Reasoning Rules

The reasoning rules define how the LLM should think while operating inside Brique.

They include principles such as:

* Every action starts from an intention.
* Never assume an intention is immediately implementable.
* Clarify before formalizing whenever necessary.
* Formalize before implementing.
* Treat artifacts as persistent memory.
* Use conversations only as temporary reasoning space.
* Load only the knowledge required to progress.
* Preserve the semantic consistency of the instance.

These rules constrain the reasoning process rather than the user interaction.

---

# Navigation

The LLM must be capable of navigating the Brique instance autonomously.

Navigation occurs along two complementary dimensions.

## Structural Navigation

Locate information through the organization of contexts, sub-contexts, structures, and addresses.

## Semantic Navigation

Locate information through meaning, semantic descriptors, vocabulary, and the semantic field.

Together they allow the LLM to progressively identify the artifact or capability required to accomplish its objective.

---

# Engine Capabilities

Engine capabilities constitute the substrate through which an LLM interacts with a Brique instance.

They allow the LLM to:

* inspect the instance;
* navigate its structure;
* query the semantic field;
* create, read, update, or delete elements;
* manage wrappers;
* observe execution traces;
* retrieve engine capability descriptors;
* invoke engine operations.

Engine capacities share the same descriptive model as user capacities.

Only their scope differs.

---

# Invocation

Every interaction with Brique is intention-driven.

Once the LLM knows the contract of a capability, it constructs an intention conforming to that contract and submits it through the MCP.

The MCP itself performs no reasoning.

It only transports communication between the LLM and the Brique engine.

---

# Design Philosophy

The first implementation prioritizes cognitive effectiveness over optimization.

The objective is to discover the cognitive framing that enables an LLM to collaborate most effectively with a human and with Brique.

Token optimization, compression, and context minimization are secondary concerns.

They will emerge from observing real interactions and identifying which knowledge is truly essential.

---

# Final Goal

The MCP is not primarily a communication protocol.

It is the entry point into the Brique cognitive framework.

Its purpose is to enable a Large Language Model to naturally transform human intentions into structured knowledge, formal Brique artifacts, and executable implementations while reasoning entirely within the conceptual universe of Brique.
