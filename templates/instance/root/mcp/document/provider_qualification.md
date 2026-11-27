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

# Provider qualification for native tool use

## Status

Qualification of native tool calls, for the MCP realization (work package WP0.2 of [mcp_realization_plan.md](mcp_realization_plan.md)).

| Provider | State |
|---|---|
| Anthropic | Qualified on `claude-haiku-4-5-20251001`, 2026-11-23, with real requests (below). Repeat the probes for every other Anthropic model that a pilot uses |
| OpenAI | Not qualified: no account is available for the tests. Tool use stays disabled; an adapter given tools fails explicitly |
| Mistral | Not qualified, same reason |
| Scaleway | Not qualified, same reason. Its documentation pages could not be read either |

A provider is enabled for native tools only after its probes have been run and recorded here. Until then its adapter refuses a request that carries tools.

## How the Anthropic probes were run

The script [probe_anthropic_tools.py](../../agentic/code/agentic_core/tests/provider/qualification/probe_anthropic_tools.py) sends about thirty small requests to the Messages API with the real `pilot_output` schema of a Matter pilot (the `optional` style that Anthropic gets), a test tool `get_weather`, and `output_config.format` set to `json_schema` as the adapter does. It records only status codes, error messages, stop reasons, and block types. The key comes from the environment and is never printed. The operator approved the run.

```text
ANTHROPIC_API_KEY=... python3 probe_anthropic_tools.py <results.json>
```

## Results

| Probe | Question | Answer |
|---|---|---|
| A1 | Tools and the `pilot_output` json_schema together, prompt that needs the tool | 200. `stop_reason` is `tool_use`, the content holds one `tool_use` block and no JSON text |
| A2 | Same request, prompt that needs no tool | 200. `end_turn`, a text block with JSON that conforms (`{}`) |
| A3 | A tool declared `strict: true` together with the json_schema | 200, accepted for a small tool schema |
| A4 | The turn after a tool result, under the json_schema | 200. `end_turn`, a text block with a valid `pilot_output` |
| B1 | History with `tool_use` and `tool_result`, `tools` absent | 200, accepted |
| B2 | Same history with `tools` present | 200 |
| F1 | `tool_choice` none, tools present, history of tool blocks | 200, text answer |
| G1 | The history names a tool that is no longer in `tools` | 200, accepted |
| H3 | Text before the `tool_result` in the same user message | 400: the `tool_use` id has no `tool_result` immediately after |
| H4 | `tool_result` with an unknown `tool_use_id` | 400 |
| H5 | `tool_result` with `is_error` and a short synthetic message | 200, the model reasons about it |
| C | Tool name `customer.read`, `crm__customer_read`, 64, 65, 128 and 129 characters, accents, a space | `^[a-zA-Z0-9_-]{1,128}$` is enforced: the dot, the accent, the space, and 129 characters are refused (400); `__` and 128 characters are accepted |
| D1 | Two independent cities | 200, two `tool_use` blocks in one assistant turn |
| D2 | Both results in one user message, reversed order | 200 |
| D3 | Only one of the two results | 400 |
| D4 | The same `tool_use_id` answered twice | 400: each `tool_use` must have a single result |
| D5 | Results in two consecutive user messages | 200, accepted |
| E1 | Extended thinking with tools | 200, blocks `thinking` then `tool_use` |
| E2 | Continue with the thinking block unmodified | 200 |
| E3 | Continue without the thinking block | 200, accepted on this model, although the documentation says to return it |
| E4 | Extended thinking, tools, and the json_schema together | 200, blocks `thinking` then `tool_use` |
| E5 | `tool_choice` none with extended thinking | 200, `end_turn` with a `thinking` block and no text block |

## Consequences for the design

1. The flow of a Matter pilot works end to end on Anthropic: a tool call under the strict output schema, the result, then the final `pilot_output` (A1, A4). The adapter distinguishes the two decisions by `stop_reason` and block types.
2. Compaction and technical inferences may send a history that holds tool blocks without declaring tools (B1), or with `tool_choice` none (F1). Both are accepted on this model. The design may choose either; sending the same `tools` keeps the cache prefix (the tools are first in the hierarchy), so `tool_choice` none with the pinned projection is the default, and omitting `tools` is the fallback.
3. The pairing rule is strict and must be enforced by Agentic before the request leaves: one result for each `tool_use`, none unknown, none repeated, results immediately after the tool turn (H3, H4, D3, D4). Results may be split into several user messages (D5), in any order (D2). A call that was not executed is answered with an `is_error` result carrying a short bounded message (H5).
4. The model-visible name rule of the design (`[a-zA-Z0-9_-]`, at most 64 characters) is inside what Anthropic accepts (C). A dot is refused, so a remote name such as `customer.read` must be normalized.
5. Thinking blocks are returned unmodified when thinking is on (E2). They are persisted with the turn even though this model did not require it (E3), because the documentation requires it and the behavior may change.
6. A final turn can contain no text block and no `tool_use` (E5). The adapter must treat that as an explicit empty or invalid decision, never as a silent `None`.
7. MCP tools are sent without `strict`. The strict form was accepted here (A3) for a small schema, but Anthropic limits union-typed parameters in strict schemas (the adapter already shapes `pilot_output` around that limit), remote schemas are arbitrary, and the gateway validates the arguments against the projected schema anyway.
8. One trivial tool plus the `pilot_output` schema cost about 1,600 input tokens (A1). The weight of the tool definitions in real use is to be measured with the real projections.

## Not tested

- OpenAI, Mistral, Scaleway (see the status table).
- The prompt cache behavior when the tool set changes: it is documented by Anthropic (changing the tool definitions invalidates the whole cache) and not measured here.
- Streaming, `parallel_tool_calls` disabled, models other than Haiku 4.5, and very large tool sets.
