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

# UI Block Specification

## 1. Purpose

A UI block is a React component that renders a context's interface and talks to the Brique engine through `@spark/pulse`. It is the Bloc primitive from the Brique bootstrap made concrete: `Content` maps to a matter or a capacity, `Volume` is its layout space in the host window, `Rendering` is the React tree it produces.

A UI block is not a wrapper. It does not host business logic, does not bind capacities, and cannot be invoked by the engine — it is a consumer of the engine, addressed the other way around: it emits intentions toward capacities and observes matters, exactly as a human operator would, just automated through code.

## 2. Position in Brique

A UI block belongs to one context. That context declares it in `context.json`:

```json
{
  "engine_config": {
    "communication": {
      "interfaces": [
        {
          "driver": "ws",
          "name": "<context_short_name>",
          "type": "ui",
          "config": { "path": "/<context_path>/ws", "egress_enabled": true, "ingress_enabled": true }
        }
      ]
    }
  },
  "brique": {
    "ui_config": {
      "adapter": "react",
      "host": "electron",
      "name": "<context_short_name>",
      "root_view": "<RootViewComponentName>",
      "ws_path": "/<context_path>/ws"
    }
  }
}
```

`ui_config.root_view` names the component and the file: the source lives at `<context_dir>/ui/<root_view>.tsx`, and exports a component of that same name (default export too). `ws_path` must equal the `path` declared under `communication.interfaces` for the `type: "ui"` entry — this is the physical websocket path the host resolves against the instance's `communication.websocket_listener.addr` at runtime. The component itself never reads `context.json`; the Spark host resolves the full URL and passes it as the `transportUrl` prop when mounting the component. A `resolveTransportUrl()` fallback inside the component (reading `process.env.SPARK_UI_TRANSPORT_URL`, else a hardcoded default matching `ws_path`) exists only for standalone dev/testing, kept in sync with `ws_path` by convention, not read from it.

Only one root view per context. A context that needs to show state from a child context does so by importing that child's own view component and mounting it inside the parent's tree (see `PhotographieVisionneuse` mounted inside `PhotographieImport` for a real example) — not by declaring a second `ui_config`.

## 3. Build Model

`.tsx` sources are never committed as build output and never manually compiled by anything in this repository. The Spark host builds them on demand via its internal `ContextUIBuilder` (esbuild-wasm, bundled, ESM, `jsx: automatic`, target `es2020`), producing `<context_dir>/.spark/dist/ui/<root_view>.js` (+ source map). This directory is a build artifact — treat it as disposable, do not hand-edit it, and exclude it from version control.

The build treats `react`, `react/jsx-runtime`, `react/jsx-dev-runtime`, `react-dom/client`, `@spark/pulse/react`, and `@spark/pulse/runtime` as external — they are never bundled into the output. The host supplies them at runtime through its own import map. This is why source files import from the bare specifiers `@spark/pulse/react` / `@spark/pulse/runtime` even though no such npm package exists in this repository's `node_modules` — the build resolves and aliases them against Spark's own source tree at build time, then leaves the specifier untouched in the output for the host to fill in.

Startup order: the host loads `context.json`, validates `ui_config`, builds (or rebuilds) the view, starts the Brique engine for that context, waits for the websocket to be ready, then mounts the view. A build failure blocks startup for that context's UI — it does not fall back silently.

## 4. Runtime Model — `@spark/pulse`

Every block follows the same two-layer shape:

```tsx
export function MyBlock({ pulse: providedPulse, transportUrl, rootContextId }: MyBlockProps) {
  const pulse = useMemo(() => providedPulse ?? new PulseRuntime({
    webSocketUrl: transportUrl ?? resolveTransportUrl(),
    maxMessagesPerFlow: 20,
    terminalFlowTtlMs: 30000,
  }), [providedPulse, transportUrl]);

  return <PulseProvider pulse={pulse}><MyBlockInner rootContextId={rootContextId} /></PulseProvider>;
}
```

The outer component owns the `PulseRuntime` instance (or accepts one already constructed, for embedding/testing) and provides it via context. All actual logic lives in an `Inner` component that consumes the runtime through hooks — never through the outer component's props directly, so any part of the tree can reach the runtime without prop drilling.

### 4.1 `PulseRuntime` constructor

```ts
new PulseRuntime({
  webSocketUrl?: string;              // required unless transportManager given
  transportManager?: TransportManager; // inject a custom/test transport instead
  transportName?: string;              // default "websocket"
  maxMessagesPerFlow?: number;         // ring buffer cap per flow, default 100
  terminalFlowTtlMs?: number;          // cleanup delay after a flow terminates, default 60000
  uiName?: string;                     // default "main" — feeds the @ui_<name> address
  uiAddressPrefix?: string;            // overrides the default @ui_<name> prefix
  responseTimeoutMs?: number;          // default 30000 — timeout for emitIntentionAndWait
  identity?: Identity;                 // default { id: "pulse-ui", kind: "ui" }
})
```

### 4.2 Emitting intentions

Two distinct methods, chosen deliberately, not interchangeably:

**`emitUIIntention(input: UIIntentionInput): Promise<IntentionId>`** — sends the intention and resolves as soon as the message is on the wire. Does not wait for Brique's answer. Pair it with `useIntentionSubscription(intentionId)` or `useFlow(intentionId)` to observe status/payload/error as they arrive, including any intermediate `status: "running"` responses from long-running capacities (§9.1 of `Wrapper.md`). Use this whenever the UI should stay interactive while the capacity runs.

**`emitUIIntentionAndWait(input: UIIntentionInput): Promise<Response>`** — resolves only when a terminal response correlated to the generated intention arrives, or rejects on `responseTimeoutMs` (refreshed on each intermediate `"running"`, so a capacity that keeps checking in never times out on that account alone). Use this when the calling code needs the result inline — e.g. to chain a second call using the first's output, as in `PhotographieVisionneuse` reading a random photo then listing its folder.

```ts
type UIIntentionInput = {
  to: { context: string; version?: string; cap: string; type: string };
  targetPathUI?: string;
  sourcePathUI?: string;
  identity?: { id: string; kind: string };
  params?: Record<string, unknown>;
  matters?: MatterRef[];
  correlation?: Correlation;
  awaitResponse?: boolean; // only meaningful for emitUIIntention; AndWait always forces true
};
```

`to.type` is almost always `"user"` for a UI-originated intention toward a hosted capacity — matching the `identity.kind: "human"` convention used throughout this instance's blocks. Params must match exactly what the target capacity's descriptor declares under `functional.#root.inputs.params` — same discipline as a wrapper (`Wrapper.md` §9), nothing invented, nothing omitted.

### 4.3 Reading and observing matters

`useMatter(address, options?)` and `useMatterSubscription(address, options?)` both return a live `MatterSnapshot` via `useSyncExternalStore`. `useMatter` pulls once on mount (`autoRead`, default true) and optionally also subscribes (`subscribe: true`); `useMatterSubscription` always subscribes — use it when only live push updates matter, not an initial read. `refetchOnEvent: true` re-reads automatically when the snapshot goes stale after an event. `useMatterEvent(address, callback, options?)` layers a one-shot side-effect callback on top of `useMatterSubscription`, firing once per new event — useful for a toast or a log line reacting to `matter_written`/`matter_deleted` without coupling that reaction to render logic.

### 4.4 The idiomatic action hook

`useBriqueAction(config: UIIntentionInput): BriqueAction` wraps `emitUIIntention` in a small local state machine — `{ status: "idle"|"running"|"success"|"error", intentionId, error, emit(paramsOverride?), reset() }` — and guards against stale responses when the config changes mid-flight (an emission counter invalidates any update from a superseded call). Prefer this over hand-rolling `useState` + `emitUIIntention` + `useIntentionSubscription` for a straightforward "button triggers one capacity, show its state" flow. Reach for the lower-level primitives only when the flow needs something `useBriqueAction` does not offer — chaining, custom correlation, or waiting inline.

**`status` reflects the send, not the capacity's completion.** Because `useBriqueAction` wraps `emitUIIntention` (fire-and-forget), `status` becomes `"running"` only for the brief window between calling `emit()` and the message landing on the wire, then `"success"` the moment the send succeeds — regardless of how long the addressed capacity actually takes to finish. A button disabled on `action.status === "running"` re-enables almost immediately, not when the capacity is done. To reflect the capacity's real progress (including intermediate `"running"` responses per `Wrapper.md` §9.1), feed `action.intentionId` into `useIntentionSubscription` or `useFlow` and drive the UI off that state instead of `action.status`.

### 4.5 Path-scoped hooks

`usePathIntention(pathUI)` and `usePathFlow(pathUI, componentName?)` bind to a UI path (`targetPathUI` in the intention) rather than to a specific intention id — useful when a block is a generic renderer for "whatever the last intention addressed to this path was," decoupled from tracking the id itself. `usePathFlow` additionally registers the component's presence via `pulse.registerBlock`/`unregisterBlock`, observable through `pulse.getActiveBlocks()`. `useProjectionPath(pathUI)` is the lower-level snapshot these two build on.

### 4.6 Display mode

`useDisplayMode()` / `useSetDisplayMode()` read/write a display-mode string on the runtime — a UI-local concern (e.g. toggling a view variant), not a Brique concept; nothing engine-side observes it directly.

## 5. Full Hook Reference

| Hook | Returns | Use it for |
|---|---|---|
| `usePulse()` | `PulseRuntime` | Access the runtime anywhere under `PulseProvider`. |
| `useFlow(intentionId)` | `FlowSnapshot` | Full message history + status/payload/error for one intention. |
| `useIntentionSubscription(intentionId)` | `IntentionViewState` | Thin projection of `useFlow` — just `{status, message, payload, error}`. |
| `useMatter(address, options?)` | `MatterSnapshot` | Read (and optionally subscribe to) one matter. |
| `useMatterSubscription(address, options?)` | `MatterSnapshot` | Always-subscribed matter observation, no initial pull semantics distinction. |
| `useMatterEvent(address, callback, options?)` | `MatterSnapshot` | One-shot side effect per new matter event. |
| `usePathIntention(pathUI)` | `string \| null` | The intention id currently bound to a UI path. |
| `usePathFlow(pathUI, componentName?)` | `FlowSnapshot` | Flow bound to a path, plus block presence registration. |
| `useProjectionPath(pathUI)` | `PathSnapshot` | Lower-level path snapshot underlying the two above. |
| `useDisplayMode()` | `string` | Current UI display mode. |
| `useSetDisplayMode()` | `(mode) => void` | Setter for display mode. |
| `useBriqueAction(config)` | `BriqueAction` | Idiomatic "emit one intention, track its state" without hand-rolled wiring. |

`PulseProvider` and `usePulse` are the only two required for any block. Reach for the rest as the block's actual needs demand them — do not wire up hooks a block does not use.

## 6. Styling Convention

Every block in this instance uses inline `React.CSSProperties` objects in a `styles: Record<string, React.CSSProperties>` constant at the bottom of the file, colors expressed as `var(--syn-<token>, <fallback>)` so the block inherits the host's theme while still rendering something reasonable standalone. Recurring tokens: `--syn-surface-overlay`, `--syn-surface-0`, `--syn-text-primary`, `--syn-text-secondary`, `--syn-text-muted`, `--syn-text-inverse`, `--syn-border-medium`, `--syn-selected-border`, `--syn-feedback-info`, `--syn-feedback-error`, `--syn-feedback-error-bg`. Do not introduce a new styling mechanism (CSS modules, styled-components, Tailwind) into a block — every block in this instance uses this same inline-object convention, and mixing approaches would break the shared theming contract the host provides through these CSS variables.

## 7. End-to-End Example: Block ↔ Capacity

A block never exists in isolation — it always addresses one or more real capacities, hosted by a wrapper following `Wrapper.md` §15. The three pieces below are the same real, deployed feature (`photographie/image`'s folder import), shown together to make the full trajectory explicit — capacity descriptor → hosted Python code → the `.tsx` call that triggers it. Cross-check every field name across all three when writing a new pair; a mismatch anywhere in this chain is a runtime `invalid` error, not a compile-time one.

**1. The capacity descriptor** (`photographie/image/capacity/import.recolte.json`) — the source of truth for the param shape:

```json
{
  "brique": { "cap_name": "import.recolte", "kind": "interpreted", "wrapper": "import", "rel_ctx": "image", "lang": "python" },
  "functional": { "#root": {
    "inputs": { "params": { "folder_path": { "type": "string" } } },
    "outputs": { "payload": { "created_matter_ids": {"type":"array"}, "deleted_matter_ids": {"type":"array"}, "skipped": {"type":"array"} } }
  }}
}
```

**2. The hosted capacity code** (per `Wrapper.md` §15.3, one file named after the capacity):

```python
# import_recolte.py
# <brique:capacity name="import.recolte">
async def import_recolte(params: dict) -> dict:
    folder_path = str(params.get("folder_path", "")).strip()   # matches inputs.params.folder_path exactly
    if not folder_path:
        raise ValueError("folder_path is required")
    # ... work ...
    return {"created_matter_ids": [...], "deleted_matter_ids": [...], "skipped": [...]}  # matches outputs.payload exactly
# </brique:capacity>
```

**3. The block's call site** (`photographie/ui/PhotographieImport.tsx`, real code):

```tsx
const id = await pulse.emitUIIntention({
  to: { context: imageContext, cap: "import.recolte", type: "user" },  // cap name matches brique.cap_name exactly
  targetPathUI: IMPORT_PATH_UI,
  identity: { id: "photographie-user", kind: "human" },
  params: { folder_path: folderPath },   // key matches inputs.params.folder_path exactly — nothing more, nothing less
});
```

The three artifacts share exactly one vocabulary: the capacity's own name (`import.recolte`, used identically in the descriptor's `brique.cap_name`, the wrapper's `bindings.py` tuple key, and `to.cap` in the block) and its param/output field names (`folder_path`, `created_matter_ids`, …, used identically in `functional.#root.inputs/outputs`, the Python function body, and the block's `params`/response handling). Writing a new capacity+block pair means picking these names once, in the descriptor, and then repeating them verbatim everywhere else — never renaming or abbreviating between layers.

## 8. Composition

A block may mount another context's block directly as a child component — the recommended way to reuse a sub-context's view rather than reimplementing it, as long as the parent has a legitimate reason to display the child's state inline (see `PhotographieImport` mounting `PhotographieVisionneuse`). The mounted child still receives its own `pulse`/`rootContextId` props explicitly; it does not implicitly inherit the parent's context addressing.

## 9. Non-Goals

A UI block must not:

* implement business logic that belongs in a capacity — a block observes and triggers, it does not compute domain results itself beyond what is needed to render
* read or write matter substance directly outside `pulse.matter`'s API
* construct raw transport messages by hand — always go through `emitUIIntention`/`emitUIIntentionAndWait`/`prepareIntention`+`sendPrepared`
* assume a specific host (`electron` today) beyond what `ui_config.host` declares — treat host specifics as configuration, not as something the component branches on internally
* read `context.json` at runtime — configuration is either passed as props by the host or falls back to a dev-only default

## 10. Summary

A UI block is a React component, one per context, built by the Spark host from `<context_dir>/ui/<root_view>.tsx` into `.spark/dist/ui/<root_view>.js`, wired to the engine over the websocket path declared in `context.json`. It talks to Brique exclusively through `@spark/pulse`'s `PulseRuntime` and its React hooks — emitting intentions toward capacities, observing matters, tracking flow state — never through business logic of its own or a hand-rolled transport.
