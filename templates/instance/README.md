# Brique instance

```text
root/        the root Context and the Contexts below it (what the engine is given)
wrapper/     the runtime libraries of the wrappers (Python, Go, C++)
security/    who may do what: the access source, signed by `brique-sec apply`
envs/        the Python environments of the wrappers (envs/<name>/requirements.txt, and a venv/ built from it)
ui_block/    the guide to writing a UI block
```

The Contexts that come with it: `agentic` (conversations with pilots), `llm` (the provider adapters), `mcp` (an MCP server for agents,
a reader and an architect profile), `web` (the home page of the published interfaces), and the engine's own `projection/`.

## Provider API keys

The `llm` Context calls the model providers, which need an API key. The key is never a file of the instance. It is a secret of Brique
Sec: three separate things, each yours to set up for the providers you use. This instance ships none of them.

1. **The value** goes to the security folder of this machine, outside the instance. It is read from the standard input, never from an
   argument, and is never printed:

   ```bash
   brique-sec --root <this-instance>/root secret add anthropic
   ```

2. **The right** for a wrapper to receive it is part of the signed policy:

   ```bash
   brique-sec --root <this-instance>/root secret allow anthropic llm_llm
   brique-sec --root <this-instance>/root apply --reload
   ```

3. **The need** is declared by the wrapper in its `context.json` (`run.secrets`): the secret to take and the variable to put it in. The
   `llm` Context asks for `anthropic` as `ANTHROPIC_API_KEY`. For another provider, add an entry (for example `openai` as
   `OPENAI_API_KEY`, `mistral` as `MISTRAL_API_KEY`, `scaleway` as `SCW_SECRET_KEY`) and do steps 1 and 2 with that name.

Until the three are in place, the wrapper `llm_llm` refuses to start and says which one is missing. `brique-sec secret list` shows the
values stored, and `brique-sec diff` shows a right that is not applied yet.
