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

# MCP acceptance

Work package WP9 of [mcp_realization_plan.md](mcp_realization_plan.md): each acceptance scenario of the MCP design, the test that makes it true, and its status. Recorded on 2026-11-23.

Status: **covered** (a test asserts the required result), **partial** (part of it is asserted; what is missing is said), **out of scope** (not implemented, by a decision of the operator).

## Suites and commands

| Key | Suite | Command (from the instance root unless said) |
|---|---|---|
| G | gateway (`client_mcp_gateway`) | `cd root/mcp/client/code/client_mcp_gateway && go test -race ./gateway/` |
| S | inbound MCP servers (`mcp_server_go`) | `cd root/mcp/code/mcp_server_go && go test -race .`; against the running instance: `go test -tags live .` (see `live_test.go`) |
| A | Agentic contract | `cd root/agentic/code && python3 -m unittest discover -s agentic_core/tests/contract -t .` |
| P | provider adapters | `cd root/agentic/code && python3 -m unittest discover -s agentic_core/tests/provider -t .` |
| I | Agentic integration (real engine, fake model) | `cd root/agentic/code && BRIQUE_IT=1 BRIQUE_IT_WS=ws://127.0.0.1:<port>/agentic/test/ws python3 -m unittest discover -s agentic_core/tests -t .` |
| R | real model (Anthropic) | `BRIQUE_REAL_LLM=1 BRIQUE_IT_WS=... python3 -m unittest agentic_core.tests.real.test_mcp_tool_pilot agentic_core.tests.real.test_native_tools` (the MCP one needs `go run ./testserver` in the gateway module) |
| E | engine (repository `brique`) | `go test ./engine/... ./cmd/... ./platform/...` |
| M | policy matrix | `BRIQUE_SEC=<brique-sec> sh security/check_mcp_policy.sh` |
| Q | SDK qualification | `cd root/mcp/code/mcp_sdk_qualification && go test .` |

The Python suites run with `envs/env1/venv/bin/python3`.

## Scenarios

| # | Scenario | Tests | Status |
|---|---|---|---|
| 1 | Pilot has no MCP configuration | A `test_a_pilot_without_mcp_sees_no_tool_and_never_asks_the_gateway`; P `test_provider_fixtures` (52 requests byte for byte); I the whole former suite | covered |
| 2 | Inbound MCP, reader, UI, or unrelated wrapper calls `/root/mcp/client/call` | E `execution/execution_protected_test.go` (the gate for a caller with no admission); M (reader, UI, remote denied, Agentic allowed); S `live_test.go` (remote refused by the engine) | covered |
| 3 | Model invents a server or tool | A `test_a_batch_with_one_bad_call_sends_nothing_and_every_call_gets_an_explicit_result`; G `TestAnEnvelopeThatDoesNotBindTheCallIsRefused` (unknown tool, other tool, other projection) | covered |
| 4 | Catalog changes after projection | G `TestAChangedToolMakesTheProjectionStale` | covered |
| 5 | Secret is absent or ungranted | G `TestAMissingSecretFailsClosed`; E `execution/execution_wrapper_secrets_test.go` (a wrapper with an ungranted secret does not start) | covered |
| 6 | OAuth grant is absent during preparation | G `TestAnOAuthServerWithoutGrantIsLeftOutOfTheProjection` (tools omitted, reason `authentication_required` in `unavailable`, which Agentic records as an `mcp_projection` event, outside the conversation) | covered |
| 7 | OAuth grant expires before transmission | G `TestTheGatewayUsesTheGrantAndRefreshesItOnceForConcurrentCalls`, `TestARevokedGrantIsAuthenticationRequiredBeforeAnythingIsSent` | covered |
| 8 | Login completion with invalid state, issuer, redirect, or PKCE correlation | G `TestALoginWithATamperedAnswerStoresNothing` (forged state, forged issuer, invented code), `TestLoginRefusesAnotherIssuer`, `TestALoginWhoseRedirectIsDivertedOrNeverAnsweredStoresNothing` (diverted redirect, no answer before the deadline); PKCE is verified by the test authorization server in every login | covered |
| 9 | Two workers refresh the same rotating refresh token | G `TestTheGatewayUsesTheGrantAndRefreshesItOnceForConcurrentCalls` (one refresh for five calls), `TestAStaleWriterNeverReplacesANewerGrant` | covered |
| 10 | Model supplies an OAuth endpoint, token, scope, credential profile, or user identity | No field of a tool call can carry them: the model names a projected tool and gives arguments; the server, tool and credential come from the projection and the registry. G `TestAnEnvelopeThatDoesNotBindTheCallIsRefused`; G `TestCredentialsGoOnlyToTheServersHost` | covered |
| 11 | User logs out or consent is revoked | G `TestLogoutAndAChangedEntryLeaveNoUsableGrant`, `TestARevokedGrantIsAuthenticationRequiredBeforeAnythingIsSent`; a terminal attempt is never rewritten (journal) | covered |
| 12 | Model emits an intention in `pilot_output` addressed to `/root/mcp/**` | A `test_an_intention_to_the_gateway_is_refused_whatever_the_policy` | covered |
| 13 | A `tools/call` receives a 401 or 403 | G `TestA401DuringACallIsNeverSentAgain`; Q S5 (the SDK retries only when `Authorize` succeeds) | covered |
| 14 | Gateway holds a `call_id` that Agentic has no record of | — | **out of scope** (decision D17): an orphan arises only from a machine failure between the two journals, a robustness this design does not aim at |
| 15 | Wrapper crashes before transmission | G `TestARestartReconcilesWhatWasInFlight` (`reserved` becomes `failed_before_transmission`) | covered |
| 16 | Wrapper crashes after possible transmission | G `TestARestartReconcilesWhatWasInFlight` (`transmitting` becomes `outcome_unknown`, never replayed) | covered |
| 17 | Forced kill or power loss after the transmitting commit | G `TestARestartReconcilesWhatWasInFlight` (torn last line, reconciliation); every journal write is followed by `fsync` | **out of scope** (decision D22): no machine-loss simulation; durability rests on `fsync` of the file and of the directory |
| 18 | Handler panics after transmission | G `TestAPanicAfterTransmittingCommitsOutcomeUnknown` | covered |
| 19 | Busy or connection loss repeats the reservation | G `TestAgenticCallIsSentOnceAndReplaysTheSameAttempt`, `TestConcurrentDuplicatesShareOneAttempt`; A `test_a_lost_answer_is_asked_to_the_gateway_and_never_shown_as_unsent`, `test_a_terminal_state_known_by_the_gateway_is_the_answer_of_a_lost_call` | covered |
| 20 | One server is saturated | G `TestOneSaturatedServerLeavesAnotherServed`, `TestASaturatedServerRefusesAtOnce` (refused at once, nothing waits for a slot) | covered |
| 21 | Result exceeds configured bounds | G `TestResultsAreBoundedAndNonTextIsDescribed` (bounded below the SDK, cut before the journal) | covered by truncation (decision D21: no externalization) |
| 22 | Result is externalized or truncated | G `TestResultsAreBoundedAndNonTextIsDescribed` (`truncated: true` and a closing note; non-text blocks described) | covered by truncation (decision D21) |
| 23 | Tool returns `isError: true` | G `TestARemoteToolErrorIsADefiniteResult`; A conversation `test_a_result_the_view_does_not_hold_is_said_absent...` (rendering of the states) | covered |
| 24 | Model returns several tool calls | A batch test (scenario 3), conversation `test_results_follow_the_turn_in_call_order_and_a_user_line_comes_after_them`; P `test_results_may_be_split_and_reordered` | covered |
| 25 | User message arrives while native tool results are pending | A `test_a_user_message_waits_for_the_results_of_the_tool_turn` | covered |
| 26 | Succession follows an effectful MCP call | A `test_a_succession_after_an_mcp_call_carries_it_in_the_ledger` (the successor's summary lists the MCP tool and its outcome, no argument value, no remote field), `test_the_ledger_lists_an_mcp_call_by_its_outcome_only` | covered |
| 27 | Compaction sees native tool history | A `test_compaction_keeps_the_tools_and_forbids_calling_them`; P `test_tool_choice_none_keeps_the_tools_and_the_history`, `test_a_history_with_tool_blocks_and_no_tools_is_accepted`; R `test_native_tools` (third turn) | covered |
| 28 | Projection changes while a tool turn is pending | A `test_no_new_projection_while_a_tool_turn_waits_for_its_results` (with a refresh at every inference, none while the turn waits, one at the next safe boundary); the gateway checks every call against the current registry (G scenario 4, `TestARevocationTakesEffectOnTheNextCall`) | covered |
| 29 | Argument allowed structurally but forbidden for the trust domain | G `TestArgumentsAreCheckedBeforeAnythingIsSent` (denied field, secret value) | **out of scope** (decision D22): data classes beyond denied fields and secrets are not enforced |
| 30 | Result may not be sent to the selected LLM provider | — | **out of scope** (decision D18): keeping data from the provider is another property; a result the registry allows is given to the model |
| 31 | Remote content contains instructions | A `test_remote_text_carries_its_provenance_and_what_agentic_says_does_not` (a completed result and a remote error begin with the provenance label naming the tool; a refusal by Agentic has none), `test_a_tool_call_is_signed_sent_once_and_its_result_closes_the_turn` (the stable rule is in the system text of an MCP pilot), `test_a_pilot_without_mcp_sees_no_tool_and_never_asks_the_gateway` (no rule without MCP); R `test_mcp_tool_pilot`, `test_mcp_public` (Haiku 4.5 answers from labelled results); results are never system text (decision D20) | covered |
| 32 | Capacity pilot uses MCP | G `TestOnlyARegisteredPlanMayCall`; R `test_mcp_public.test_a_capacity_pilot_calls_deepwiki_from_its_plan_with_no_inference`: the Capacity pilot `deepwiki_topics` (DSL, no inference) calls the public DeepWiki server through the engine and the gateway, admitted as a registered plan | covered |
| 33 | Compaction or technical inference runs | A `test_compaction_keeps_the_tools_and_forbids_calling_them`: tools are sent with `tool_choice: none` (the model cannot select one, and the cache prefix is kept); Capacity pilot steps (`pilot.infer`) receive no tool | covered, with this deviation from "no tools supplied" (decided in WP0.2, consequence 2) |
| 34 | Outbound wrapper is unavailable | A `test_an_optional_server_that_cannot_be_prepared_leaves_the_pilot_without_tools`; S suites run without the gateway; I the whole suite | covered |
| 35 | Remote client without a token, with an unknown or revoked token | S `TestOnlyAListedTokenReachesTheRemoteServer`; S `live_test.go` | covered |
| 36 | Remote client uses a session opened with another token | S `TestASessionBelongsToTheTokenThatOpenedIt` | covered |
| 37 | Remote client asks for `/root/mcp/client/**` | S `live_test.go`; M (`local.mcp_remote` denied) | covered |

Summary of the 37 scenarios: 33 covered (21 and 22 by truncation, decision D21; 33 with its recorded deviation), 4 out of scope by decision (14, 30, 17, 29; D17, D18 and D22 of the plan); none partial.

Through real processes with OAuth: R `test_mcp_oauth.py` (the test MCP server in OAuth mode with its own authorization server, the real `client_mcp_gateway login`, Haiku 4.5) passed on 2026-11-25: the login stores the grant owner-only within the scope ceiling; the model's call completes with it; once the access token has expired the gateway refreshes it by itself and the next call completes; after a revocation the server is left out of the projection with `authentication_required`, the model is told (`mcp_unavailable` in the news of the request) and answers the person, and nothing is sent to the server (scenarios 6, 7, 11).

No downstream secret outside its places: R `test_mcp_secret_scan.py` (servers `statcrm`, static token, and `oauthcrm`, OAuth; Haiku 4.5) passed on 2026-11-25: after a run that uses both credentials and one refresh, the static token and every OAuth token issued are found only in the secrets folder of the machine and the grant file, in none of the files written by the instance, the machine directory, the launcher or the engine's temporary files, before or after the cleanup of the continuity (item 7 of the merge barrier; scenarios 5, 10).

Against a public server: R `test_mcp_public.py` reaches DeepWiki (`https://mcp.deepwiki.com/mcp`, no authentication) over TLS through the gateway's egress controls, once from a deterministic plan and once from Haiku 4.5 given DeepWiki as native tools; both passed on 2026-11-23. Large results are truncated, never externalized (D21).

## Merge barrier

See the section "Merge barrier" of [mcp_realization_plan.md](mcp_realization_plan.md) for the result of each item.
