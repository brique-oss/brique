# Sandbox N6 guide

This document describes the workspace model used by the N6 reflexive engine tests.

## Model

The workspace contains contexts, capacities, schemas, documents, matters, and structures.

## Entities

Each local document has a JSON meaning descriptor and a content file referenced by
`brique.file`.

## Projection

Meaning projection indexes the descriptor while `read.document` resolves and reads
this Markdown content.
