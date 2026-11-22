# Capacity — `pilot.infer`

## Intention

Pass **one prompt** from a Capacity-defined pilot through the Continuity: record it, query the model, validate and record the response, then return it to the deterministic plan. This makes the cognition phase of `pilot.activate` callable and provides one cognition path for both pilot kinds. The design is documented in `capacity_defined_pilot`.

## Input

### `pilot.infer`

| Field | Description |
|---|---|
| `continuity_id` | Required Continuity being driven. |
| `run_id` | Required run that owns it. |
| `step` | Required stable step name, written literally in the plan. |
| `instructions` | Step prompt as one text. Required unless `prompt_parts` or `prompt_matters` is provided; never accepted with either of them. |
| `prompt_parts` | Prompt as an ordered list of parts: literal text, or any other JSON value written as compact JSON with sorted keys. Step data enters its prompt this way; there is no `input` parameter. |
| `prompt_matters` | Prompt as an ordered list of Matters linked in `@matter` with `cardinality: many`, each read as text. |
| `prompt_order` | `matters_first` or `parts_first`. Required when both lists are provided. |
| `output_schema` | Required JSON Schema for structured output: the contract between reasoning and the deterministic continuation. |
| `model` | Required `{ provider, model, parameters? }`; a Capacity pilot has no `pilot_definition`. |
| `projection` | Optional `projection_definition`, providing the journal view added to the step context. |
| `framework` | Optional, false by default, with `null` treated as false. Adds the Continuity's reasoning framework at the beginning of the step's system text. It matters only without `projection`; see “Reasoning framework.” |
| `max_inferences` | Optional maximum number of prompts for the current message, supplied by `pilot.activate`. |

## Output

| Field | Description |
|---|---|
| `output` | Structured output validated against `output_schema`. Always the complete value, even when the journal carries only a reference. |
| `usage` | Usage reported by the provider. |
| `step` | Step name. |
| `replayed` | `true` when the step had already completed and was returned without calling the model. |

## Protocol

1. **Validate** inputs and `output_schema`, including closed objects and the small number of optional or union parameters required by providers.
2. **Verify ownership**: only the owning run may infer.
3. **Memoize**: a step key is the fingerprint of `(current message, step, prompt, output_schema, model)`, where `prompt` is the assembled text sent to the model regardless of how it was provided. The same text means the same step, so a plan whose two turns could read identically must distinguish them inside the prompt. `pilot.activate` does this for resumption stimuli with the wait-group identifier or the post-crash run identifier. A key incorporating everything newly entered into the conversation was tested and rejected because a message arriving during an in-flight inference changed the key and duplicated the prompt on resumption. A step whose `completion` is already in the journal is returned **without calling the model** or appending anything.
4. **Enforce the bound**: explicitly refuse once `max_inferences` prompts have been used for the message.
5. **Record the prompt** with `kind: prompt` and an identifier derived from the key before querying the model.
6. **Infer** by requesting structured output from the provider.
7. **Validate** the output against `output_schema`. Invalid output is traced as `error` and refused, never recorded as a `completion`.
8. **Record the response** as `kind: completion`, correlated with the prompt.
9. **Measure**: an `inference_measured` `event` line is written only by a step whose context includes a projection, in the same append as the step's other lines.

**Inference window.** From reading the projection, when requested, until writing the `completion`, the step holds the Continuity's inference window described by `agentic_core_concurrency_model`. No other line can be written between its `prompt` and `completion`, and no other inference can run for that Continuity. The window is released in every case, including errors. A replayed, memoized step does not acquire it.

## Journal

| `kind` | Content |
|---|---|
| `prompt` | `{ step, instructions, model }`, where `instructions` is the assembled text; `correlation.causation_id` is the current message |
| `completion` | `{ step, output, usage }`; `correlation.causation_id` is the prompt |

These are historical facts without mailbox entries and never wake the Continuity. They are not carried by reference: the prompt and `output` are written directly in their lines regardless of size.

The journal retains the **step prompt**—`instructions` and `model`—because memoization and recovery depend on it. It is a durable fact, not transit. The journal does not retain the **assembled provider request** containing the system text, projection, and schema, because that request is an intermediate product.

## Reasoning framework

Every Continuity opens with a `framework` line containing the pilot reasoning framework; see `continuity.create` and `pilot_framework`. Each step decides whether to carry it:

| Step | Framework behavior |
|---|---|
| with `projection` | always: the projection carries pinned lines. Their text becomes system text and is not repeated in the conversation. |
| without `projection`, with `framework: true` | added at the beginning of the system text without a journal view. A Continuity created before framework lines existed refuses the step with a consistency failure instead of silently omitting it. |
| without `projection`, with `framework` absent or false | nothing: the step sees only its prompt, as before. |

The framework never enters the recorded prompt: the journal stores `instructions` as assembled while the framework remains its own line. The memoization key therefore excludes it. The provider receives system text shaped as `framework + "\n\n" + prompt`.

## Step request

The request depends on whether the step reads the Continuity:

| Step | Request |
|---|---|
| with `projection` | **the conversation**: framework as system text marked `cache: true`, then projection lines as messages with their roles, the last also marked `cache: true`, and finally the **assembled prompt** as the only new message. See “The journal is the conversation” in `capacity_pilot_activate`. |
| without `projection` | the prompt alone, as before: system text containing the framework when requested and then the prompt, marked `cache: true`, plus a short `{ continuity_id, step }` message. |

For a step with a projection, the prompt therefore comes **after** history, never before it: the prompt is the changing part and history is stable. A plan that repeats steps on one Continuity, such as a tool loop, sends its initial prompt once; the journal retains it as a `prompt` line and resumptions send only what is new in the `stimulus`. The cache then reads nearly all history on each turn. A real measurement on the fourth `search_pilot` call showed 6,493 cached tokens out of 7,632.

- **An interrupted and restarted step** finds the prompt it previously wrote in the journal. That line is excluded from history, so the prompt is stated only once as the new message.
- The request carries `metadata: { continuity_id, step }`, which no provider reads.
- Provider limits, including the minimum length below which a cache marker is ignored without error and the five-minute lifetime, are described with the conversation in `capacity_pilot_activate`.

## Transit

`instructions`, `prompt_parts`, and `output_schema` are parameters. They arrive by value or, above 64 KiB, as `@file`; neither Capacity nor caller depends on the representation because the runtime resolves it when read. The provider request travels through transit in the same way. The Capacity contract remains unchanged: it declares a name and schema, not how the parameter arrives.

## Errors

| Case | Error |
|---|---|
| missing or malformed input, including non-boolean `framework`, or unacceptable `output_schema` | invalid |
| `framework: true` on a Continuity without a framework line | consistency failure |
| unknown Continuity | not found |
| `run_id` does not own the Continuity | refusal |
| message prompt budget exhausted | refusal |
| nonconforming output | invalid and traced |
| provider failure | provider error; no `completion` is recorded |
| revision conflicts exhausted | concurrency failure |

## Recovery

Replaying a recorded step succeeds at no model cost: after a crash, the plan replays and previously obtained responses are neither paid for again nor allowed to diverge. A recorded prompt without a `completion` is reused, and the step restarts without writing a second prompt.

## What `pilot.infer` does not do

- It applies no effects: the plan returns a `pilot_output`, which `pilot.activate` applies.
- It does not correct invalid output with a second inference; invalid output is a plan defect.
- It does not select the model; every step provides one.

## Implementation constraint

The Continuity is managed in a Matter—the journal—for persistence and duplicated in an in-memory wrapper object for performance. This is an implementation concern rather than a formalization concern; see `agentic_core_concurrency_model`. For this Capacity, step memoization searches for its `prompt` and `completion` in the in-memory object; each write appends to the journal and then updates the object.
