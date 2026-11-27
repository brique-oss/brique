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

# MCP operations

The runbook of the operator for MCP in a Brique instance: the outbound gateway that lets pilots call external MCP servers, and the inbound servers that let MCP clients reach Brique. Decisions and status: [mcp_realization_plan.md](mcp_realization_plan.md).

Commands below use these names; adapt the paths to the instance.

```text
SEC        the brique-sec of the runtime the instance runs (for example <runtime>/bin/brique-sec)
ROOT       <instance>/root
MACHINE    the machine directory of the instance (~/.brique/<instance id>)
GATEWAY    <instance>/root/mcp/client/build/client_mcp_gateway/client_mcp_gateway (built by the engine)
SERVER     <instance>/root/mcp/build/mcp_remote/mcp_server_go (built by the engine)
```

## 1. What runs where

| Part | Context, wrapper | Listens | Holds state in |
|---|---|---|---|
| Outbound gateway | `/root/mcp/client`, `client_mcp_gateway` | nothing (it only calls out) | `MACHINE/wrappers/client_mcp_gateway/`: `registry.json`, `journal/attempts.jsonl`, `projections/` |
| Local MCP server (architect) | `/root/mcp`, `mcp_server_go` | 127.0.0.1:18200 (legacy SSE), 127.0.0.1:18210 (SDK: `/sse`, `/mcp`) | nothing |
| Local MCP server (reader) | `/root/mcp`, `mcp_reader` | 127.0.0.1:18201, 127.0.0.1:18211 | nothing |
| Remote MCP server | `/root/mcp`, `mcp_remote` | 127.0.0.1:18220 (`/mcp`, bearer tokens), reached through the proxy | `MACHINE/wrappers/mcp_remote/tokens.json` |

Only `local.agentic_core` may call `/root/mcp/client/**` (protected capacities, `security/access.json`). Agentic signs every model-selected call with the machine secret `mcp_authority_key`; the gateway verifies it with the public key written in the registry.

## 2. Registry of external servers

The source is `<instance>/security/mcp_registry.json` (versioned with the instance). It takes effect only when installed on the machine:

```sh
GATEWAY registry check security/mcp_registry.json        # exactly the gateway's validation; prints one line per server
SEC --root ROOT wrapper-file install client_mcp_gateway registry.json security/mcp_registry.json
```

The gateway reads the installed copy again when it changes: no restart. Raise `version` at each change; every attempt records the version it used. To revoke a server or a tool, remove it (or set `"enabled": false`) and install: the next call is refused. A registry that is missing or invalid refuses every call (fail closed); `GATEWAY registry check` tells why before it happens.

A server with a credential names machine secrets in `auth.secrets`. For each one:

```sh
printf %s "$VALUE" | SEC --root ROOT secret add crm_token
SEC --root ROOT secret allow crm_token client_mcp_gateway
SEC --root ROOT apply --reload
```

and declare it in `root/mcp/client/context.json`, `run.secrets`: `{"name": "crm_token", "env": "MCP_SECRET_CRM_TOKEN", "optional": true}` (the engine refuses a secret delivered in a `BRIQUE_*` variable). A new value of a declared secret reaches the gateway at its next start (`wrapper.restart`). A new declaration in `run.secrets` is read only when the context starts: restart `/root/mcp/client` (`context.restart`, type control, from `/root/mcp` with `{"child": "client"}`), then `wrapper.start`; a `wrapper.restart` alone starts the gateway without it, and a server that needs it is left out of the projection with `authentication_required`.

### OAuth servers

A server that asks for OAuth is registered with `"auth": {"mode": "oauth", "issuer": "https://as.example.com", "client_id": "<registered client>", "scopes": ["read"], "secrets": {"client_secret": "crm_client_secret"}, "redirect_port": 8765}`. The client is registered with the issuer beforehand (a public client has no `client_secret`); `scopes` is the most the grant may hold; `redirect_port` is needed only when the client was registered with a fixed loopback redirect (`http://127.0.0.1:<port>/callback`), otherwise any free port is used.

The instance holds one grant per server, obtained by the operator on the machine, never by a model or a request:

```sh
GATEWAY login crm --store MACHINE/wrappers/client_mcp_gateway     # prints a URL: open it, grant access; the grant is stored
GATEWAY logout crm --store MACHINE/wrappers/client_mcp_gateway    # deletes it
```

The login discovers the server's authorization server, checks that it is the `issuer` of the registry, asks only scopes within the ceiling, uses PKCE and checks the state and the issuer of the answer, listens on the loopback interface for this login only, and proves the grant by listing the tools. The grant is `MACHINE/wrappers/client_mcp_gateway/oauth/<alias>.json` (owner-only, never in the instance). The running gateway uses it at once and refreshes it when it is about to expire, one refresh at a time; a rotated refresh token is written back before it is used.

When the grant is missing, revoked, cannot be refreshed, or no longer matches the registry entry (endpoint, issuer, client, scope ceiling), the server's calls are refused with `authentication_required` and nothing is sent; the gateway never retries a call after a 401 or 403 and never falls back to another credential. The remedy is a new `login`.

## 3. Pilots that use MCP

A Matter pilot gets native MCP tools with a section of its `pilot_definition`: `"mcp": {"servers": ["crm"], "mandatory": [], "max_calls_per_turn": 8}`. Only qualified providers accept tools (today Anthropic; [provider_qualification.md](provider_qualification.md)). A Capacity pilot calls `/root/mcp/client/call` from its plan with `origin: "plan"`, its own `call_id`, `server`, `tool` and `arguments`; the plan must be listed in `plans` of the registry as `/root/<context>/capacity/<name>`.

## 4. Authority key of Agentic

Created once, never displayed:

```sh
# a 32-byte Ed25519 seed in base64url into the secret, its public key into the registry
SEC --root ROOT secret add mcp_authority_key      # value on standard input
SEC --root ROOT secret allow mcp_authority_key agentic_core
SEC --root ROOT apply --reload
```

`root/agentic/context.json` declares it for `agentic_core` (`MCP_AUTHORITY_KEY`, optional: without it no tool is offered). Rotation: make a new key, put its public key in the registry (raise `version`), replace the secret, install the registry, then restart the context `/root/agentic`. Envelopes live five minutes: a call in flight during the swap is refused with `policy_denied` and the model is told so; nothing is sent twice.

## 5. Tokens of the remote server

```sh
SERVER token add claude-laptop --expires 720h --store MACHINE/wrappers/mcp_remote    # prints the token once: give it to the client
SERVER token list --store MACHINE/wrappers/mcp_remote
SERVER token revoke claude-laptop --store MACHINE/wrappers/mcp_remote
```

Only the SHA-256 of a token is stored. A revocation takes effect on the next request. A session belongs to the token that opened it. Each token has 10 requests per second (`BRIQUE_MCP_TOKEN_RATE` of the wrapper). The remote profile has the rights of the reader (`local.mcp_remote`): it reads the whole instance and may call any capacity except the outbound gateway.

## 6. Start, stop, replace the gateway

The gateway holds a journal that must be closed in order. Stop sequence: no new work, then `wrapper.stop` of `client_mcp_gateway` on `/root/mcp/client`; the wrapper cancels the calls in flight first (each commits a terminal state), then waits for the handlers, then closes sessions and journal, within `stop_timeout_ms` (45 s). To deploy new gateway code: stop it, build it (`go build -o ../../build/client_mcp_gateway/client_mcp_gateway .` in `root/mcp/client/code/client_mcp_gateway`), start it. The engine builds a compiled wrapper only when its artifact is missing: a `wrapper.restart` after a source change runs the old binary. Source changes are never picked up while it runs.

A forced kill is safe: at its next start the gateway reconciles its journal before taking any call.

## 7. Reading the journal and resolving an unknown outcome

`MACHINE/wrappers/client_mcp_gateway/journal/attempts.jsonl` holds one JSON line per state of an attempt: `call_id`, `state`, `transmission`, `server`, `tool`, `origin`, `continuity`, `registry_version`, and for a terminal state the bounded result. The last line of a `call_id` is its state.

| State | Meaning | What the operator does |
|---|---|---|
| `completed` | the server answered with a result | nothing |
| `remote_error` | the server answered with an error (tool error, protocol error, 401/403 `authentication_required`) | read the reason; fix the credential or the call |
| `failed_before_transmission` | nothing left Brique (proved), or the gateway restarted before sending | nothing: the call may be made again |
| `outcome_unknown` | the request may have reached the server and its answer was lost | check on the remote system whether the effect took place, before anyone repeats it |
| `reserved`, `transmitting` | only while the gateway runs; at its next start they become `failed_before_transmission` and `outcome_unknown` | nothing |

An `outcome_unknown` is never retried by Brique. The pilot is told that the outcome is unknown. The operator settles it on the remote side (read the record, look at the remote logs) and, when needed, tells the pilot through a message to its continuity. The gateway keeps terminal attempts 30 days, then drops them at its next start.

## 8. Checks

```sh
SEC --root ROOT doctor        # modes of the machine directory (wrapper stores included), protected targets without a context,
                              # wrapper stores that no context declares, drift between source and installed policy
sh security/check_mcp_policy.sh    # who may call the gateway and what the remote profile may do (brique-sec simulate)
```

Tests: `go test -race ./gateway/` in `root/mcp/client/code/client_mcp_gateway`; `go test -race .` and `go test -tags live .` in `root/mcp/code/mcp_server_go`; the contract and integration suites of Agentic; `BRIQUE_REAL_LLM=1 ... tests.real.test_mcp_tool_pilot` with the test MCP server running (`go run ./testserver` in the gateway module). The OAuth and secret tests need the other two test servers: `go run ./testserver -addr 127.0.0.1:18298 -oauth -token-life 40s` (server `oauthcrm`, then `login oauthcrm`) and `go run ./testserver -addr 127.0.0.1:18297 -bearer-file MACHINE/secrets/mcp_statcrm_token` (server `statcrm`, static token); `tests.real.test_mcp_secret_scan` searches every file a run wrote for these secrets.
