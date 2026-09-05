/*
 * Copyright 2026 Nicolas Cassan
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

import {
  executeThroughBridge,
  type PulseBridge,
} from "./execution/index.js";
import type {
  CanonicalKey,
  ExecuteInput,
  ExecuteResult,
  ProjectionKind,
  ProjectionResult,
  BriqueSubstrate,
  RawEntry,
  RawError,
  RawResolution,
  RawResolver,
  RawResult,
} from "./types/index.js";
import { transformProjection } from "./transform/index.js";

export type BriqueSubstrateOptions = {
  bridge: PulseBridge;
  rawResolver: RawResolver;
};

export function createBriqueSubstrate(
  options: BriqueSubstrateOptions
): BriqueSubstrate {
  async function execute(input: ExecuteInput): Promise<ExecuteResult> {
    return executeThroughBridge(options.bridge, input);
  }

  async function get(key: CanonicalKey): Promise<RawResult> {
    const resolution = normalizeRawResolution(options.rawResolver.resolve(key));
    if (!resolution) return { status: "absent", payload: undefined };

    const result = await execute(resolution.input);
    const raw = result.response;

    if (resolution.parse) {
      const payload = resolution.parse(raw);
      return payload !== undefined && payload !== null
        ? { status: "ok", payload }
        : { status: "error", payload: undefined, error: extractError(raw) };
    }

    return unwrapEnvelope(raw);
  }

  async function project<K extends ProjectionKind>(
    key: CanonicalKey,
    kind: K
  ): Promise<ProjectionResult<K>> {
    const result = await get(key);

    if (result.status !== "ok") {
      return result.status === "error"
        ? { status: "error", model: undefined, error: result.error }
        : { status: "absent", model: undefined };
    }

    return {
      status: "ok",
      model: transformProjection({ key, kind, raw: result.payload }),
    };
  }

  async function mutate(input: ExecuteInput): Promise<ExecuteResult> {
    return execute(input);
  }

  function teardown(): void {
    // The stateless substrate owns no persistent response resources.
  }

  return {
    execute,
    get,
    project,
    mutate,
    teardown,
  };
}

function unwrapEnvelope(raw: unknown): RawResult {
  if (!isRecord(raw)) {
    return {
      status: "error",
      payload: undefined,
      error: { message: "response is not an object" },
    };
  }

  const status = raw.status;
  if (typeof status !== "string") {
    return { status: "ok", payload: raw };
  }

  if (status === "ok") {
    const payload: RawEntry = raw.payload !== undefined ? raw.payload : raw;
    return payload !== undefined && payload !== null
      ? { status: "ok", payload }
      : { status: "absent", payload: undefined };
  }

  return { status: "error", payload: undefined, error: extractError(raw) };
}

function extractError(raw: unknown): RawError {
  if (!isRecord(raw)) return {};
  const err = raw.error;
  if (!isRecord(err)) {
    return {
      code: typeof raw.status === "string" ? raw.status : undefined,
      message: typeof raw.message === "string" ? raw.message : undefined,
    };
  }
  return {
    origin: typeof err.origin === "string" ? err.origin : undefined,
    code: typeof err.code === "string" ? err.code : undefined,
    message: typeof err.message === "string" ? err.message : undefined,
    details: isRecord(err.details)
      ? err.details as Record<string, unknown>
      : undefined,
  };
}

function normalizeRawResolution(
  resolution: RawResolution | undefined
): { input: ExecuteInput; parse?: (response: unknown) => unknown } | undefined {
  if (!resolution) return undefined;
  if (isRawResolutionDescriptor(resolution)) return resolution;
  return { input: resolution };
}

function isRawResolutionDescriptor(
  resolution: RawResolution
): resolution is { input: ExecuteInput; parse?: (response: unknown) => unknown } {
  return isRecord(resolution) && "input" in resolution && isRecord(resolution.input);
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

export * from "./types/index.js";
export * from "./projection/index.js";
export * from "./execution/index.js";
export * from "./resolver/index.js";
export * from "./transform/index.js";
export * from "./capability/index.js";
