/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

// A user interface for end-to-end tests, built from the real parts: Spark starts the engine
// binary on an instance, then the interface is a Pulse runtime on a BridgeTransport over Spark's
// LocalWebSocketRelay — the pieces the editor's host and renderer are made of, without the
// window. A test drives it over stdin and reads what the interface saw on stdout.
//
//   node --experimental-strip-types --loader ./spark/test/ts-resolve-loader.mjs \
//        spark/test/e2e/uiDriver.ts <root/context.json> <engine binary> <name>
//
// Every protocol line, in both directions, is JSON prefixed with "@@" (the engine writes its own
// logs on the same stream). Commands: {"id":1,"press":{...}} presses a button, {"quit":true}
// stops the interface and the engine. Events: {"ready":true,...}, {"id":1,"response":...}.

import path from "node:path";
import readline from "node:readline";
import { pathToFileURL } from "node:url";

import { startSpark } from "../../core/SparkRuntime.js";
import { LocalWebSocketRelay } from "../../hosts/LocalWebSocketRelay.js";
import {
  BridgeTransport,
  PulseRuntime,
  TransportManager,
} from "../../../pulse/runtime/index.js";

type Press = {
  to: { context: string; cap: string; type: string };
  params?: Record<string, unknown>;
  path?: string;
};

const [contextPath, engineBinary, name = "interface"] = process.argv.slice(2);
if (!contextPath || !engineBinary) {
  console.error("usage: uiDriver.ts <context.json> <engine binary> [name]");
  process.exit(2);
}

function emit(event: Record<string, unknown>): void {
  process.stdout.write(`@@${JSON.stringify(event)}\n`);
}

let pulse: PulseRuntime | null = null;
let relay: LocalWebSocketRelay | null = null;

const sparkRoot = path.resolve(path.dirname(new URL(import.meta.url).pathname), "..", "..", "..");

const spark = await startSpark(
  {
    contextPath,
    briqueBinaryPath: engineBinary,
    sparkRoot,
    // The editor's own configuration, with a host that is this test.
    uiConfig: { root_view: "BriqueEditorRoot", adapter: "react", host: "e2e", name: "main" },
  },
  {
    // Nothing to build: no window is drawn.
    buildContextUI: async (input) => ({
      rootViewSourcePath: input.rootViewSourcePath,
      rootViewBuiltPath: input.rootViewSourcePath,
      rootViewBuiltUrl: pathToFileURL(input.rootViewSourcePath).href,
      outputDir: path.dirname(input.rootViewSourcePath),
    }),
    startHost: (input) => {
      relay = new LocalWebSocketRelay(input.uiTransportUrl);
      const transports = new TransportManager();
      transports.register(input.uiRuntime.uiName, new BridgeTransport(relay));
      pulse = new PulseRuntime({
        transportManager: transports,
        transportName: input.uiRuntime.uiName,
        uiName: input.uiRuntime.uiName,
        uiAddressPrefix: input.uiRuntime.uiAddressPrefix,
        responseTimeoutMs: 25_000,
        identity: { id: `pulse-${name}`, kind: "ui" },
      });
      return { stop: () => relay?.close(), onExit: () => {} };
    },
  }
);

emit({ ready: true, name, uiTransportUrl: spark.uiTransportUrl, uiAddressPrefix: spark.uiRuntime.uiAddressPrefix });

async function press(command: Press) {
  if (!pulse) throw new Error("the interface is not started");
  const path = command.path ?? "/button";
  // What a button does: an intention whose answer is delivered to its place in the interface.
  const response = await pulse.emitUIIntentionAndWait({
    to: command.to,
    params: command.params ?? {},
    sourcePathUI: path,
    targetPathUI: path,
  });
  const flow = pulse.getFlowSnapshot(response.intention_id);
  return {
    response,
    flow: { status: flow.status, terminal: flow.terminal, messages: flow.messages.length },
    address: pulse.ui.addressForPath(path),
  };
}

const lines = readline.createInterface({ input: process.stdin });
for await (const line of lines) {
  if (!line.trim()) continue;
  let command: { id?: number; press?: Press; quit?: boolean };
  try {
    command = JSON.parse(line);
  } catch {
    emit({ error: "invalid command" });
    continue;
  }
  if (command.quit) break;
  if (command.press) {
    try {
      emit({ id: command.id, ...(await press(command.press)) });
    } catch (error) {
      emit({ id: command.id, error: error instanceof Error ? error.message : String(error) });
    }
  }
}
spark.stop();
emit({ stopped: true });
process.exit(0);
