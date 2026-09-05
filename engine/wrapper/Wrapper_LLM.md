# Wrapper — LLM Reference

Condensed operational reference. Full rationale: `wrapper/Wrapper.md`. Read that once if anything here is ambiguous; use this file for fast lookup while coding.

## Core facts

- Wrapper = runtime membrane, engine ↔ code. One process per `wrapper_name` per boundary context.
- Engine projects only capacities + matters. Descriptors carry no runtime symbols — bindings live in code (`bindings.py`), never JSON.
- Binding key: `(context_relative_path, brique_name) → live callable/accessor`. Dict lookup, static, loaded once at bootstrap.
- Wrapper resolution is **strictly local per context, no parent inheritance**. A wrapper declared in `parent/context.json` is NOT reachable from `parent/child`. Every capacity that runs inside a shared wrapper process must be declared in the owning context's own tree, even if its code physically imports from elsewhere.
- Hosted code stays Brique-agnostic: no transport, no envelopes, no routing logic inside capacity code.
- Capacity is stateless between calls. Anything persistent → matter, not capacity-local state.

## Setting up a new Python wrapper

1. Copy verbatim into `<context_dir>/code/<wrapper_name>/`:
   - `wrapper/python/code/main.py`
   - `wrapper/python/code/requirements.txt`
   - `wrapper/python/code/wrapper/` (entire dir: runtime.py, registry.py, router.py, execution.py, matter.py, outbound.py, correlator.py, transport.py, constants.py, models.py, util.py)
   - **Never modify these files.** They are the generic runtime, identical across every wrapper in this instance.
2. Declare in `context.json`:
```json
{
  "engine_config": {
    "communication": {
      "interfaces": [{"driver":"ws","name":"<wrapper_name>","type":"wrapper","config":{"path":"/wrapper/<wrapper_name>","egress_enabled":true,"ingress_enabled":true}}],
      "wrapper_boundary": ["<wrapper_name>"]
    },
    "execution": {
      "wrappers": [{"wrapper_name":"<wrapper_name>","mode":"interpreted","ready_timeout_ms":5000,"stop_timeout_ms":1000,
        "run":{"cmd":["python3","main.py"],"env_name":"env1","shell":false,"env":{"BRIQUE_WRAPPER_NAME":"<wrapper_name>"}}}]
    }
  }
}
```
3. Capacity descriptor (`capacity/<name>.json`) must set `brique.kind:"interpreted"`, `brique.lang:"python"`, `brique.wrapper:"<wrapper_name>"`, `brique.rel_ctx:"<context relative to wrapper root, "" if same as boundary>"`.

## The one rule that matters most

**`bindings.py` = pure import + assembly. Zero business logic.**

If `bindings.py` contains a function or class that does real work → wrong. Extract it.

## File layout — one file per capacity

Capacity name → filename: replace every `.` with `_`, add `.py`.
`import.recolte` → `import_recolte.py`. `image.photo_au_hasard` → `photo_au_hasard.py` (or `image_photo_au_hasard.py` if name collision across contributing contexts).

```
<boundary_ctx>/code/<wrapper_name>/
  bindings.py                 # pure import/assembly only
  <own_capacity>.py           # one per capacity owned by boundary ctx itself
<child_ctx>/                  # contributing subcontext, no wrapper of its own
  capacity/<name>.json
  code/<wrapper_name>/
    <child_capacity>.py       # one per capacity owned by child, physically lives here
```

`bindings.py` imports every capacity file directly and flat — no per-contributor aggregator module, no `capacities.py` grab-bag. Use `importlib` (contributing contexts are not on `sys.path`):

```python
# bindings.py — PURE IMPORT, nothing else
from __future__ import annotations
import importlib.util
from pathlib import Path
from typing import Any

THIS_DIR = Path(__file__).resolve().parent

def _load(rel_path: str, symbol: str):
    p = THIS_DIR / rel_path
    spec = importlib.util.spec_from_file_location(p.stem, p)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return getattr(module, symbol)

demo_echo = _load("demo_echo.py", "demo_echo")
import_recolte = _load("../../image/code/import/import_recolte.py", "import_recolte")

def build_bindings(runtime) -> dict[str, Any]:
    return {
        "capacities": {
            ("", "demo.echo"): demo_echo,
            ("image", "import.recolte"): import_recolte,
        },
        "matters": {},
        "shutdown": [],
    }
```

`build_bindings` may be `def` or `async def` — registry awaits it only if awaitable.

Tuple key `(rel_ctx, cap_name)`: `rel_ctx` must equal that capacity's own `brique.rel_ctx` in its descriptor, exactly.

## Writing one capacity file

```python
# import_recolte.py
from __future__ import annotations

# <brique:capacity name="import.recolte">
async def import_recolte(params: dict) -> dict:
    folder_path = str(params.get("folder_path", "")).strip()
    if not folder_path:
        raise ValueError("folder_path is required")
    # ... work ...
    return {"created_matter_ids": [...], "deleted_matter_ids": [...], "skipped": [...]}
# </brique:capacity>
```

Rules:
- Sync or async function, or bound method of a small local class (helpers OK; shared cross-file service object NOT OK — that much shared state belongs in a matter).
- Params: read exactly what the capacity descriptor's `functional.#root.inputs.params` declares. Never invent extra fields.
- Return dict shape must match `functional.#root.outputs.payload` exactly.
- `@matter` side effects only if declared in `functional.#root.outputs.@matter`.
- **Always read the capacity's own `capacity/<name>.json` before writing its body.** It is the contract, not a convention to guess.
- Wrap the function body in `# <brique:capacity name="...">` / `# </brique:capacity>` comment tags (traceability metadata, not runtime-read).

## Calling out from a capacity (outbound intentions)

Native direct access only within the same wrapper root for a bound matter (`./...` ref). Everything else → outbound intention via `runtime.outbound.emit_intention(...)` / `emit_and_wait(...)`.

```python
async def import_recolte(runtime, params: dict) -> dict:
    resp = await runtime.outbound.emit_and_wait(
        to_context="/root/photographie/image",
        to_cap="matter.create",
        to_type="matter",
        from_context="image",        # THIS capacity's own ctx, relative to wrapper root — never "" if shared wrapper
        from_cap="import.recolte",   # THIS capacity's exact name — never a placeholder
        params={"matter_id": "photo_x", "matter": {...}},
        timeout_s=15,
    )
```

Bind `runtime` via `functools.partial(import_recolte, runtime)` or a closure at load time in `bindings.py` — still assembly, not logic.

**Critical failure mode**: `from_context=""` / `from_cap=""` defaults are only safe when the wrapper root is the sole owner of the calling capacity. In a shared wrapper root (§ contributing subcontext above), omitting these silently breaks response routing — the call hangs to timeout, no error pointing at the real cause. Always pass both explicitly once more than one context contributes to a wrapper.

## Matter bindings

Same discipline: accessor functions in a small dedicated file, imported into `bindings.py`, never inlined there.

```python
# demo_cache_state.py
_cache = "cold"
def read_cache() -> str: return _cache
def write_cache(value: object) -> dict:
    global _cache; _cache = str(value)
    return {"ok": True, "value": _cache}
```
```python
# bindings.py
"matters": {
    ("", "demo_cache"): {"read": read_cache, "write": write_cache, "notify_on_write": True},
}
```
`notify_on_write: True` → runtime auto-wraps writer, emits `matter_written` to subscribers. Don't hand-roll notification in the accessor.

## Long-running capacities

Call `await runtime.notify_running()` zero/one/many times while still working, before final `return`. No-op outside capacity execution — always safe to call. Does not replace the final return value. No descriptor flag needed — purely a runtime call inside the hosted function.

## User traces from wrapper code

Model as an outbound intention to a trace-writing capability (`trace_kind:"user"`, optional `intention_id`/`reason_code`/`user_text`). Never target `trace.inspect` (read-only reflexive capability).

## Traceability tags (comments only, not runtime bindings)

```
# <brique:capacity name="...">   ...   # </brique:capacity>
# <brique:matter name="...">     ...   # </brique:matter>
# <brique:schema name="...">                      (optional, only if schema has native repr — e.g. a type)
<brique:section id="#...">                          (DSL section entry point only, never internals, never flow ops)
<brique:import name="..." signature="capacity|matter|native|schema" resolution="wrapper_internal|engine_external">
```
`wrapper_internal` = same wrapper root, native call. `engine_external` = outside root, via intention.

## Invariants (do not violate)

- One wrapper runtime process per wrapper name + boundary context. No per-subcontext process.
- Every binding resolves to a live symbol — no dynamic runtime registration (`register_capacity(...)` doesn't exist; bindings load once from `bindings.py` at bootstrap).
- Ordering guaranteed per addressed binding / per matter. Unrelated bindings run in parallel.
- Matter writes: no intermediate invalid state exposed — visible only as completed.
- Communication loop must stay responsive; never block it on outbound waits.
- Excess concurrent work queues; never breaks the runtime.
- Wrapper never does global routing / global context reconstruction / meaning interpretation — that's the engine's job.

## Known-bad pattern still present in this instance — do not copy

`root/rag_old/code/rag/bindings.py` and `root/musique/favorits/radio_france/code/rf_favoris_scrap/bindings.py` implement full service classes with business logic directly inside `bindings.py` (hundreds of lines). This predates the one-file-per-capacity rule above. Do not use them as a template. If asked to touch one, prefer extracting into the layout above over adding more inline logic.

`root/photographie/code/import/bindings.py` and `root/atelier/code/prompt_engine/bindings.py` are close — `bindings.py` is pure aggregation — but their contributed code is one `capacities.py` per contributing context (grouping several capacities in one file/class), not one file per capacity. Follow the stricter one-file-per-capacity rule in this document for new code, not this older per-context grouping.
