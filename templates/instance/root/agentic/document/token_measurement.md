# Token measurement: the absolute cost of the plumbing

## Question

A real pilot spends tokens on every inference. The question is: **how many tokens does each part of the Brique and agentic plumbing cost**—reasoning framework, output schema, guide, rules, known Capacities, policy, state block, response envelopes, decision structure, corrections, and compaction? How much is paid on every inference regardless of circumstances, how much does each turn add, and how much history is sent again?

The measurement never compares plumbing with the amount of “information” in a test. That ratio depends on test size and says nothing about the mechanism. It reports **absolute tokens**, per element, per inference, and cumulatively.

## Source

For every real inference, the bench's `real_calls` journal stores the canonical request sent—`messages` and `output`—the billed usage—`input_tokens`, `cached_input_tokens`, and `output_tokens`—and the output. The journal is split into slices of at most 120 KiB, from `real_calls` through `real_calls_7`: an inline Matter read stops at 256 KiB, and a single journal caused recording to fail after the provider Response had been billed, losing that Response. A recording failure no longer loses the Response.

The adapter sends the provider the system block, messages, and output schema. `metadata` is not sent and is never counted.

## Exact counting

Each element is counted **by the provider** through the `tokens.count` Capacity in the `llm` context. This is a counting endpoint with no inference or generation. The bench never reads the provider key; it asks the Capacity for the count.

An element is counted alone, then the cost of a base request containing one `x` message is subtracted to isolate its contribution:

- a system-block section is counted as a system block;
- a user message is counted as the only message;
- an assistant message is counted after a base user message;
- the output schema is the difference between a request with and without the format.

Each distinct text is counted once, keyed by role and text fingerprint. An unchanged framework costs one count, not one hundred. When a message is divided into two categories, such as envelope and payload for a tool return or structure and content for a decision, its exact total is split **inside that message alone** in proportion to character counts. Only this internal allocation is approximate.

When exact counting is unavailable, measurement falls back to estimating from billed tokens in proportion to characters and reports `exact: false`.

## Segments and categories

The system block is divided at markers written by `build_request`; the framework is recognized by its text read from the Continuity journal. A request is divided into the output schema, system-block sections, then each message.

| Category | Recognition | Family |
|---|---|---|
| `framework` | beginning of the `system` message: reasoning framework | fixed plumbing |
| `output_guide` | output-format guide | fixed plumbing |
| `output_contract` | `output` schema | fixed plumbing |
| `pilot_prompt`, `pilot_rules`, `known_capacities`, `policy`, `initial_intention` | system blocks written by `build_request` | fixed pilot definition |
| `step_prompt` | instruction and data for a plan step | content |
| `state_block` | JSON user message carrying `current_intention` | per-turn plumbing |
| `compaction_instruction` | state block whose active stimulus is `compaction` | plumbing |
| `correction` | JSON user message carrying `validation_error` | per-failure plumbing |
| `tool_envelope` / `tool_payload` | JSON user message carrying `intention_id`: response envelope / payload without engine metadata | plumbing / content |
| `tool_error` | same message when the Response is an error | per-failure plumbing |
| `decision_structure` / `decision_content` | assistant message: identifiers, family, flags / message texts | plumbing / content |
| `summary`, `user_message`, `continuity_message` | succession summary, user text, message from another Continuity | content |
| `unknown` | everything else | reported and never discarded |

Family classification is a judgment encoded in measurement constants. Changing it is an architect decision.

## Report contents

Everything is expressed in tokens, never as a plumbing-to-content percentage.

1. **Fixed cost per inference:** exact tokens for every fixed element—framework, guide, schema, rules, Capacities, policy, prompt, and Intention—and their sum. Every call pays this regardless of conversation state.
2. **Per-turn cost:** occurrence count, tokens per occurrence, and total tokens for every variable category such as state block, response envelope, decision structure, correction, and tool error.
3. **Tool returns by Capacity:** payload, envelope, and error tokens plus call count.
4. **History resent:** on every inference, history tokens outside fixed content, divided into new tokens and replayed tokens, plus cumulative totals for the run. This is the price of choosing “the journal is the conversation.”
5. **Cache:** cached tokens by inference, attributed to the prefix in order under the assumption that the output schema precedes messages, plus cumulative total.
6. **Difference from billing:** sum of counted segment tokens versus billed `input_tokens` for each inference, so the decomposition never hides what it does not cover.
7. **Output:** output tokens per inference divided between content and structure in proportion to characters, with compaction labeled separately.
8. **By phase and Continuity:** ordinary turns and compaction, with input and output tokens.

## Measurement invariants

1. Every segment has exactly one category.
2. Token counts for message parts sum to the exact message total.
3. Each distinct text is counted once.
4. Cached tokens form a prefix, and their sum equals `cached_input_tokens`, bounded by the input.
5. New plus replayed equals total, by category and inference.
6. A non-empty `unknown` category is displayed and never merged into another category.
7. The difference from billed usage is always displayed.

## Out of scope

Timing, already available as `elapsed_ms`, and any optimization. Measurement supports decisions; it does not make them.
