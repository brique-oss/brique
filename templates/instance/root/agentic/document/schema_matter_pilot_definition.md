# Matter schema — `pilot_definition`

## Intention

Represent the stable definition of a specialized cognition.

The `pilot_definition` Matter carries no execution state. It describes what characterizes a pilot when a continuity uses the generic `pilot` Capacity.

It defines in particular:

- the expected way of reasoning,
- the model used,
- the inference parameters,
- the default projection of the continuity,
- the Capacities explicitly known,
- the cognitive rules specific to this pilot.

## Element and data

This Matter is described by two schemas, which must not be confused:

| Schema | Describes | Content |
|---|---|---|
| `pilot_definition` (element) | the Matter as a Brique element | `brique.kind: matter`, `substance_mode: brique`, `substance_type: text`; `objective.type: pilot_definition`; `functional`: `format: json` and `schema_ref` |
| `pilot_definition_data` (data) | the **substance** of the Matter | the JSON document described by the tables below |

The content is the substance of the Matter (`<matter_id>.json`), never its descriptor. The `functional.schema_ref` field of the descriptor designates `/root/agentic/schema/pilot_definition_data`.

## Schema (`pilot_definition_data`)

### `pilot_definition`

| Field | Description |
|---|---|
| `id` | Unique identifier of the pilot definition. |
| `prompt` | Main prompt defining the cognitive identity of the pilot. It can describe in particular its role, its way of reasoning, its domain, its responsibilities, its cognitive constraints, its decision style. |
| `model` | LLM model used by default. |
| `parameters` | Inference parameters associated with the model. Possible examples: `temperature`, `max_tokens`, reasoning effort, parameters specific to the provider or the model. The exact content remains dependent on the inference capacity used. |
| `default_projection` | Definition of the continuity projection used by default to build the context of the pilot. |
| `known_capacities[]` | Set of the Capacities explicitly known by this pilot from the start of a continuity. |
| `rules[]` | Set of additional cognitive rules or preferences specific to the pilot. |
| `limits` | Optional operational ceilings. `limits.max_inferences_per_message` lowers, without ever raising it, the default ceiling (50) of cognitive steps allowed for a single message. |

## `prompt`

`prompt` represents the stable cognitive core of the pilot. It is always present in the context sent to the LLM.

It must not contain the particular state of a continuity. It therefore does not contain:

- `initial_intention`
- `current_intention`
- current message
- wait group results
- continuity history
- dynamically discovered capacities

## `model`

`model` designates the inference model to use by default.

It does not directly represent the execution provider of the model if this distinction is already handled by the context or by the inference Capacity.

The exact definition of this reference will depend on the contract of the LLM subsystem.

## `parameters`

`parameters` is an explicit structure containing the parameters needed for the inference.

It must not contain agentic logic. It only configures the execution of the model.

## `default_projection`

`default_projection` describes the deterministic view of the continuity presented by default to the pilot.

This projection can define in particular:

- the message `kind`s to include,
- the `kind`s to exclude,
- filters on `from` or `to`,
- correlation filters,
- temporal or sequence restrictions,
- quantitative limits,
- structural aggregation rules.

The projection remains strictly deterministic. It performs no cognitive synthesis.

A dynamic projection can complement or temporarily replace this projection during the execution of a continuity.

## `known_capacities`

`known_capacities[]` contains the Capacities that the pilot knows explicitly from the start.

### `known_capacity`

| Field | Description |
|---|---|
| `ref` | Reference to the Brique Capacity. |
| `description` | Short description that lets the LLM understand why and in which context this Capacity can be useful. |

The complete contract of the Capacity is not duplicated here. It stays carried by the Capacity itself. The pilot can read this contract when it wants to use the Capacity.

## Discovered Capacities

Each known Capacity declares its **family** (`target_type`: `user` by default, `matter`, `reflexive`, `execution`, `control`). The architect knows it, the model guesses it poorly: faced with a data lookup, it writes `matter` for a user Capacity, and the intention is never routed. The list given to the model therefore shows the family of each Capacity, and `pilot.activate` routes an intention aimed at a known Capacity with the family that the definition declares, whatever the model wrote: it is code, deterministic, not a request.

Capacities discovered dynamically during a continuity are not automatically added to `known_capacities[]`.

They belong to the cognitive context of the continuity. They can be reused in that continuity if they remain relevant.

The stable definition of the pilot changes only through explicit modification of the `pilot_definition` Matter.

## `rules`

`rules[]` contains cognitive rules specific to the pilot.

These rules must not duplicate the generic agentic protocol.

Examples:

- prefer certain Capacities in a given domain,
- respect certain business constraints,
- adopt certain reasoning priorities,
- not perform certain types of actions without sufficient information.

The rules of the generic protocol such as:

- mailbox management,
- wait groups,
- validation of `pilot_output`,
- safe points,
- resumption after an error,

must not be repeated here. They belong to the generic `pilot` Capacity.

## Context construction

`pilot_definition` contributes to the LLM context but is not the whole context.

The composite context contains in particular:

- `pilot_definition`
- `initial_intention`
- `current_intention`
- `available_capacities`
- the conversation (`continuity_projection`, converted into messages with their role)
- `current_message`
- a possible `active_wait_result`
- a possible `execution_result` or error

The cognitive core of `pilot_definition` is always present. Its bulkier or documentary parts can be projected if necessary.

## Relation with `continuity`

A `continuity` contains a `pilot_ref`. `pilot_ref` references a `pilot_definition` Matter.

The same `pilot_definition` can be used by several continuities. Each continuity nevertheless keeps:

- its own intention,
- its own history,
- its own discovered Capacities,
- its own runtime.

## Relation with the `pilot` Capacity

`pilot_definition` does not execute itself. The generic `pilot` Capacity uses the definition to perform the cognition.

The separation is therefore:

- `pilot_definition` = configured cognition
- `pilot` = execution protocol of this cognition

## Invariants

- `prompt` is mandatory.
- `model` is mandatory.
- `default_projection` is defined explicitly or has a default value known to the system.
- `known_capacities[]` can be empty.
- `rules[]` can be empty.
- `pilot_definition` contains no continuity state.
- `pilot_definition` contains no runtime state.
- `pilot_definition` contains no history.
- `pilot_definition` contains no wait group.
- `pilot_definition` contains no mailbox.
- `pilot_definition` contains no particular intention of a continuity.
- Dynamically discovered Capacities never implicitly modify `pilot_definition`.

## Versioning

No versioning mechanism specific to Brique is defined here.

The history and possible versioning of `pilot_definition` are for the moment managed by Git.

## What does not belong to this schema

- No `initial_intention`.
- No `current_intention`.
- No messages.
- No runtime.
- No mailbox.
- No wait groups.
- No dynamically discovered capacities.
- No parent/child relation.
- No `structure_path`.
- No generic execution metadata.

`pilot_definition` describes only the stable cognition to apply when a continuity uses this pilot.


## Added limits and policy

- `limits.wait_timeout_s`: delay (3600 s by default) after which a wait without response ends, each missing response becoming a `timeout` error response.
- `allowed_contexts` and `forbidden_contexts`: context paths (a capacity addressed to this context or to a context it contains is covered). Same rules as for capacities: the forbidden wins, the allowed is satisfied by a listed capacity or context.
- `allowed_capacities` and `forbidden_capacities`: capacity reference patterns that the pilot may or may not invoke; `forbidden_capacities` wins, and the contract readers (`/root/read.capacity`, `*/read.meaning`) remain allowed unless explicitly forbidden. Enforced by code before any effect (see `capacity_pilot_activate`).


## Where a `pilot_definition` lives

A `pilot_definition` is a Matter like any other: the `pilot_ref` of the continuity designates it by `{ context, matter_id }` and it can live in **any context**. The `/root/agentic/pilot` context is made for real pilots. The pilots that exist only for tests (`it_pilot`, `real_pilot`, `real_mini`, `real_weather`) live in the test context, `/root/agentic/test`, with the rest of the test world: the `agentic` context contains only what is agentic.
