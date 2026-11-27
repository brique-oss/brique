# Brique instance

```text
root/        the root Context and the Contexts below it (what the engine is given)
wrapper/     the runtime libraries of the wrappers (Python, Go, C++)
security/    who may do what: the access source, signed by `brique-sec apply`
envs/        the Python environments of the wrappers (envs/<name>/requirements.txt, and a venv/ built from it)
ui_block/    the guide to writing a UI block
```

The Contexts that come with it: `agentic` (conversations with pilots), `llm` (the provider adapters), `mcp` (MCP servers for agents: an
architect, a reader and a remote profile; and, in `mcp/client`, the gateway through which pilots call external MCP servers), `web` (the home page of the published interfaces), and the engine's own `projection/`.

## Provider API keys

The `llm` Context can call four model providers: Anthropic, OpenAI, Mistral and Scaleway. Each needs an API key, and **you only set up the
providers you use**: the others are simply not available. A key is never a file of the instance. It is a secret of Brique Sec, and for each
provider you use there are two things to do (this instance already declares the third):

1. **The value** goes to the security folder of this machine, outside the instance. It is read from the standard input, never from an
   argument, and is never printed:

   ```bash
   brique-sec --root <this-instance>/root secret add anthropic
   ```

2. **The right** for the wrapper `llm_llm` to receive it is part of the signed policy:

   ```bash
   brique-sec --root <this-instance>/root secret allow anthropic llm_llm
   brique-sec --root <this-instance>/root apply --reload
   ```

3. **The need** is already declared: `root/llm/context.json` lists the four providers under `run.secrets`, each `"optional": true`:

   | Provider | Secret name | Variable |
   |---|---|---|
   | Anthropic | `anthropic` | `ANTHROPIC_API_KEY` |
   | OpenAI | `openai` | `OPENAI_API_KEY` |
   | Mistral | `mistral` | `MISTRAL_API_KEY` |
   | Scaleway | `scaleway` | `SCW_SECRET_KEY` |

   Optional means the wrapper starts with whichever of them have both a right and a value, and without the others. For a provider you
   set up, use its secret name in steps 1 and 2.

`brique-sec secret list` shows the values stored, and `brique-sec diff` shows a right that is not applied yet. A provider you did not set up
answers its calls with an error that says its key is missing. A wrapper restarts to pick up a new secret: `wrapper.restart` on `llm_llm`.

## MCP

The `mcp` Context serves Brique to MCP clients on the loopback interface (`mcp_server_go`, the architect, and `mcp_reader`, read and
call only); `mcp_remote` is the profile for remote clients, with bearer tokens. In the other direction, `mcp/client` holds the gateway
(`client_mcp_gateway`) through which a pilot calls external MCP servers as native tools. Its capacities are protected
(`security/access.json`, `protected`): only `agentic_core` and registered plans reach them; a reader, the UI and remote clients are
refused (`sh security/check_mcp_policy.sh` checks it).

Nothing external is called until you set it up:

1. **The authority key of Agentic**, which signs each call it sends to the gateway: a secret `mcp_authority_key` (an Ed25519 seed),
   allowed to `agentic_core`. Without it no pilot is offered a tool.
2. **The registry** of the external servers, their tools, credentials and limits, with the public key of that authority, installed in
   the private store of the gateway: `brique-sec --root <this-instance>/root wrapper-file install client_mcp_gateway registry.json <file>`.
   Without it every call is refused.
3. **A pilot** names the servers it may use in its definition (`mcp.servers`).

A server with a static token or OAuth needs its own secrets or a login (`client_mcp_gateway login <server>`). The steps, the registry
format, the tokens of the remote profile and how to read the journal of the gateway are in `root/mcp/document/mcp_operations.md`.
