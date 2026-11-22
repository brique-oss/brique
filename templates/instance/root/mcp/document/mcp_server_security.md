# The MCP server: a local boundary, and two profiles

## Intention

The MCP server is a Brique wrapper: it is **internal**. But its HTTP listener is the place where the outside (Claude Code, a browser, another process) joins that inside, and what it forwards leaves with the authority of a trusted local wrapper. This boundary must therefore be narrow in two ways: who can talk to it, and what the engine lets it do once it has the floor.

## Who can talk to it

- **Local only.** The server listens on `127.0.0.1`. An address that is not the loopback is refused at startup, unless `BRIQUE_MCP_ALLOW_REMOTE=true`: opening the network is a decision, said out loud. (Before, it listened on all interfaces.)
- **Not from a browser.** A web page open in the browser can send `localhost` a simple POST request whose response it does not read but whose effect takes place. The server therefore refuses what a protocol client never sends and a page can send: an `Origin` header (a browser always sends it, Claude Code does not), a `Host` that is not the loopback (DNS rebinding), a body that is not declared `application/json` (a simple request needs no preflight). `BRIQUE_MCP_ALLOWED_ORIGINS` names, if the operator wants, an accepted origin.
- **No token.** For a local client it brings almost nothing more than these refusals, and it would require configuration on every workstation. It remains the solution for a multi-user or remote use, which is not this one.

## Everyone hears their own responses

A client opens an SSE stream and the server gives it a **session**: `/message?session=<identifier>`, an identifier that cannot be guessed. What it posts receives its response on its stream and on no other, and a progress notification goes to the one who asked the question. Before, every response was broadcast to all clients, and two clients connected together (the extension and a terminal, for example) saw each other, with JSON-RPC identifiers that could overlap. It is a correction of correctness as much as of security.

A POST without a known session is refused (404). The session lives with its stream.

## Bounds

At most 32 clients; at most 64 requests in flight for the whole server and 8 per session, beyond which the response is a `429` (refuse, never pile up); a client that does not read is dropped when its buffer is full, never waited for; read timeouts for the headers and the body, bounded header size. No write timeout: an SSE stream lasts as long as its client.

## What the engine lets it do: two profiles

A guard in Go can be bypassed if the server is: the refusal that counts is the one of the **engine**. The engine gives a restricted wrapper a principal `local.<wrapper name>` whose rights are those of `security/access.json`. A wrapper carries a single principal, so a profile is a wrapper:

| Wrapper | Port | Principal | Rights |
|---|---|---|---|
| `mcp_server_go` (architect) | 18200 | none: the local authority of its context | all |
| `mcp_reader` | 18201 | `local.mcp_reader` | `read` and `call` on `/root/**`: discover, read, observe, call the user capacities. Neither write, nor create, nor define, nor drive the lifecycle of a context |

**Why the architect is not restricted.** A first version gave it a principal without the call of `/root/llm` nor the definition and driving of `/root/mcp`. That protected nothing: the architect also modifies files directly, which the engine does not mediate (native code and files are outside the policy), and the restriction prevented it from restarting `mcp` through an intention, which it must be able to do. A profile that costs without protecting is removed.

**What "`call`" gives the reader, to be aware of.** Calling a user capacity is not only reading: the reader can execute any user capacity of `/root/**`, so those that have effects (`continuity.delete`, `pilot.activate`, `pilot.recover`) and `/root/llm/infer`, which calls the model provider and costs money. The reader does not directly modify Matters or elements, but it triggers what the capacities know how to do. If it must not spend, a one-line exception is enough: `brique-sec grant add local.mcp_reader call '/root/**' --except '/root/llm/**'`.

The same binary serves both. Each has its name, its port and its **anchor capacity** (`mcp.response`, `mcp.reader.response`): the engine returns the response of an outgoing intention to the wrapper that owns the capacity named in `from`, and two wrappers that shared one would steal each other's responses.

The profile can be **tested** without being in force: `brique-sec simulate local.mcp_reader <verb> <target>` answers allowed or refused from the source. The source is the only thing changed by hand; it is worth nothing until `brique-sec apply` is run on the machine.

## Putting it in force, and what was found while doing it

1. **The policy.** `brique-sec --root <instance>/root diff`, then `apply --reload`. It takes effect at once, including for a wrapper that is already connected: no need to restart it. `brique-sec` is compiled from the engine sources (`go build ./cmd/brique-sec`).
2. **The code.** `context.restart` of the `mcp` context recompiles and reloads both wrappers. The engine does not start them again by itself afterwards: `wrapper.start` for each. This cuts the current MCP connection; the editor reconnects by itself to the hardened server.
3. **The second wrapper** requires a `code/mcp_reader` folder: the engine compiles each wrapper in `code/<wrapper name>` and offers no other location. It is a symbolic link to `code/mcp_server_go` (a single source). A startup failure is **remembered** by the engine until the context is reloaded: `wrapper.start` then answers `wrapper_already_failed_to_be_ready`.
4. **To use the read profile**, add to `.mcp.json` an entry pointing to `http://127.0.0.1:18201/sse` (the tool will be called `mcp__brique_reader__brique`).

Rollback: `brique-sec principal remove local.mcp_reader` then `apply --reload`.

## Verified on the real servers

- Both listen on `127.0.0.1` only (18200 and 18201).
- A web page (`Origin`), a hijacked `Host`, a `text/plain` body, an unknown session: 403, 403, 415, 404, on both ports, including to open the stream.
- Two clients sending the same JSON-RPC identifier each receive a response, their own.
- Read profile: reading and calling a user capacity pass; writing, creating, defining, driving a context: `not authorized`.
- Architect profile: everything passes, as before the policy.
- The editor client (Claude Code) passes the `Origin` and `Host` checks: it sends no `Origin`.

## What this does not cover

- A process of the same machine and the same user already reads your files: no guard of the server changes anything there.
- The architect profile has all rights: it does not protect against a session that would decide to destroy a context. It is a choice, not an oversight.
