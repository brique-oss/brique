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

# UI Block — LLM Reference

Condensed operational reference. Full rationale: `ui_block/UIBlock.md`. Ready-to-copy template: `ui_block/UIBlock.tsx.example`.

## Core facts

- One React component per context = one Bloc. Lives at `<context_dir>/ui/<root_view>.tsx`, exported (named + default) as `<root_view>`.
- `root_view` name must exactly match `brique.ui_config.root_view` in that context's `context.json`.
- Talks to the engine only via `@spark/pulse` (`PulseRuntime` + React hooks). Never hand-rolls transport messages, never reads matter substance outside `pulse.matter`, never implements business logic that belongs in a capacity.
- Built by the Spark host (esbuild-wasm) into `<context_dir>/.spark/dist/ui/<root_view>.js` — that output dir is disposable, never hand-edit it, never commit it.
- Component never reads `context.json` at runtime. Host injects `transportUrl` prop; a `resolveTransportUrl()` fallback (env var, else hardcoded default) exists only for standalone dev.

## `context.json` wiring (read before writing a new block)

```json
{
  "engine_config": {"communication": {"interfaces": [
    {"driver":"ws","name":"<name>","type":"ui","config":{"path":"/<context_path>/ws","egress_enabled":true,"ingress_enabled":true}}
  ]}},
  "brique": {"ui_config": {
    "adapter": "react", "host": "electron", "name": "<name>",
    "root_view": "<RootViewComponentName>", "ws_path": "/<context_path>/ws"
  }}
}
```
`ui_config.ws_path` MUST equal `communication.interfaces[type=ui].config.path`. One root view per context — to show a child context's state, import and mount the child's own block component inside the parent's tree, don't add a second `ui_config`.

## Mandatory skeleton (every block in this instance follows this exactly)

```tsx
import React, { useMemo } from "react";
import { PulseProvider, usePulse /* + whichever hooks you need */ } from "@spark/pulse/react";
import { PulseRuntime } from "@spark/pulse/runtime";

export type <Name>Props = { pulse?: PulseRuntime; transportUrl?: string; rootContextId?: string };

export function <Name>({ pulse: providedPulse, transportUrl, rootContextId }: <Name>Props) {
  const pulse = useMemo(() => providedPulse ?? new PulseRuntime({
    webSocketUrl: transportUrl ?? resolveTransportUrl(),
    maxMessagesPerFlow: 20,
    terminalFlowTtlMs: 30000,
  }), [providedPulse, transportUrl]);

  return <PulseProvider pulse={pulse}><<Name>Inner rootContextId={rootContextId} /></PulseProvider>;
}

function <Name>Inner({ rootContextId }: { rootContextId?: string }) {
  const pulse = usePulse();
  // ... actual logic here, never in the outer component ...
}

function resolveTransportUrl(): string {
  const env = (globalThis as { process?: { env?: Record<string, string | undefined> } }).process?.env;
  return env?.SPARK_UI_TRANSPORT_URL ?? "ws://localhost:8080/<context_path>/ws";
}

const styles: Record<string, React.CSSProperties> = { /* var(--syn-*, fallback), see below */ };

export default <Name>;
```

Never deviate from this shape: outer component owns/builds the `PulseRuntime`, `Inner` component does everything else via hooks. Never put logic in the outer component.

## Emitting intentions — pick the right one

**`await pulse.emitUIIntention({to, targetPathUI, identity, params}) → Promise<intentionId>`**
Returns as soon as sent. Does NOT wait for the answer. Pair with `useIntentionSubscription(intentionId)` to observe status/payload/error, including intermediate `"running"` responses. Use for: UI stays interactive while capacity runs.

**`await pulse.emitUIIntentionAndWait({to, targetPathUI, identity, params}) → Promise<Response>`**
Resolves only on terminal response (or rejects on `responseTimeoutMs`, default 30s, auto-refreshed by each `"running"` progress ping). Use for: need the result inline to chain a next call.

```ts
to: { context: string; cap: string; type: string }   // type is almost always "user" for UI→capacity
identity: { id: string; kind: "human" }               // convention: "<context>-user"
params: { /* must match capacity descriptor's functional.#root.inputs.params EXACTLY — nothing invented, nothing omitted */ }
```

**`useBriqueAction(config: UIIntentionInput) → {status, intentionId, error, emit(paramsOverride?), reset()}`**
Prefer this over hand-rolled `useState`+`emitUIIntention`+subscription for a plain "button → one capacity → show state" flow. Handles stale-response guarding automatically.

**Trap**: `status` tracks the SEND, not the capacity's completion — same fire-and-forget semantics as `emitUIIntention`. `"success"` fires as soon as the message is sent, not when the capacity finishes. To show real capacity progress, pass `action.intentionId` into `useIntentionSubscription(action.intentionId)` and drive the UI off THAT status, not `action.status`.

## Matter access

```ts
useMatter({context, matterId}, {subscribe?, autoRead?, refetchOnEvent?}) → MatterSnapshot   // pull (+ optional subscribe)
useMatterSubscription({context, matterId}, options?) → MatterSnapshot                        // always subscribed, no pull semantics
useMatterEvent(address, (event, snapshot) => {...}, options?) → MatterSnapshot                // one-shot side effect per new event
```
Never read/write matter substance any other way from a block.

## Full hook list (import only what you use)

| Hook | Returns |
|---|---|
| `usePulse()` | `PulseRuntime` |
| `useFlow(intentionId)` | full `FlowSnapshot` (messages, status, payload, error) |
| `useIntentionSubscription(intentionId)` | `{status, message, payload, error}` (thin `useFlow`) |
| `useMatter(address, opts?)` | `MatterSnapshot` |
| `useMatterSubscription(address, opts?)` | `MatterSnapshot` |
| `useMatterEvent(address, cb, opts?)` | `MatterSnapshot` |
| `usePathIntention(pathUI)` | `string \| null` |
| `usePathFlow(pathUI, componentName?)` | `FlowSnapshot` (+ registers block presence) |
| `useProjectionPath(pathUI)` | `PathSnapshot` |
| `useDisplayMode()` / `useSetDisplayMode()` | `string` / setter |
| `useBriqueAction(config)` | `BriqueAction` state machine |

## `PulseRuntime` constructor options

```ts
{ webSocketUrl?, transportManager?, transportName?="websocket",
  maxMessagesPerFlow?=100, terminalFlowTtlMs?=60000,
  uiName?="main", uiAddressPrefix?, responseTimeoutMs?=30000,
  identity?={id:"pulse-ui",kind:"ui"} }
```
This instance's blocks all pass `maxMessagesPerFlow: 20, terminalFlowTtlMs: 30000` explicitly — match that unless there's a specific reason not to.

## Styling — copy this convention exactly, do not introduce another

Inline `React.CSSProperties` in a `styles` const at file bottom. Colors always `var(--syn-<token>, <fallback>)`:
`--syn-surface-overlay` (#1a1b1e), `--syn-surface-0` (rgba(0,0,0,0.25)), `--syn-text-primary` (#e8eaed), `--syn-text-secondary`, `--syn-text-muted` (rgba(232,234,237,0.55)), `--syn-text-inverse` (#071014), `--syn-border-medium` (rgba(255,255,255,0.14)), `--syn-selected-border` (rgba(138,180,248,0.6)), `--syn-feedback-info` (#8ab4f8), `--syn-feedback-error`, `--syn-feedback-error-bg`.
No CSS modules, no styled-components, no Tailwind — every block in this instance uses this exact inline-object pattern; mixing approaches breaks the host's shared theming.

## End-to-end: matching a block call to its capacity

Three artifacts, one shared vocabulary — get all three consistent or it fails at runtime (not compile time):
1. **Capacity descriptor** `capacity/<name>.json`: `brique.cap_name` + `functional.#root.inputs.params.*` + `outputs.payload.*` — the contract.
2. **Capacity file** (`Wrapper_LLM.md`): function reads exactly those param keys, returns exactly that payload shape.
3. **Block call**: `to.cap` = `brique.cap_name` verbatim; `params` keys = `inputs.params` keys verbatim.

Real example — `import.recolte`: descriptor declares `inputs.params.folder_path`; Python reads `params.get("folder_path")`; `.tsx` sends `params: { folder_path: folderPath }`. Same string, same casing, all three places. Read the descriptor FIRST, always — never guess field names from either side.

## Composition

To show a child context's state, `import` and mount its block component directly as a child — pass it its own `pulse`/`rootContextId` explicitly, it does not inherit the parent's addressing implicitly. Real example: `photographie/ui/PhotographieImport.tsx` mounts `photographie/visionneuse/ui/PhotographieVisionneuse.tsx`.

## Build (informational — never invoke this yourself)

Spark host builds `.tsx` → `.spark/dist/ui/<root_view>.js` via esbuild-wasm at context startup (build failure blocks that context's UI startup). `react`, `react/jsx-runtime`, `react-dom/client`, `@spark/pulse/react`, `@spark/pulse/runtime` stay external — the bare import specifiers in source are correct as written even though no such npm packages exist in this repo's `node_modules`; the host resolves them via its own import map at runtime. Do not add these as npm dependencies, do not try to run the build yourself.

## Non-goals — never do these in a block

- Business logic beyond what's needed to render/trigger — belongs in a capacity.
- Direct matter substance read/write outside `pulse.matter`.
- Hand-built transport/websocket messages.
- Branching on `ui_config.host` internally — treat host as external configuration.
- Reading `context.json` at runtime.
