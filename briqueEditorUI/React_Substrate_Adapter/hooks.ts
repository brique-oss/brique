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

import { useState, useEffect, useCallback, useRef } from "react";
import type {
  CanonicalKey,
  SubstrateProjectionKind,
  RawResult,
  ProjectionResult,
  ExecuteInput,
  ExecuteResult,
} from "../Brique_Substrate/substrate.js";
import type { ConnectionState } from "../types.js";
import { usePulseConnectionState, useBriqueSubstrate } from "./context.js";

export type UseBriqueResult<T> = {
  data: T | undefined;
  loading: boolean;
  error: unknown;
  reload: () => Promise<void>;
};

// ---------------------------------------------------------------------------
// useBriqueRaw
// ---------------------------------------------------------------------------

export function useBriqueRaw(
  key: CanonicalKey | null
): UseBriqueResult<RawResult> {
  const substrate = useBriqueSubstrate();
  const [data, setData] = useState<RawResult | undefined>(undefined);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<unknown>(undefined);
  const mountedRef = useRef(true);
  const requestSeqRef = useRef(0);

  useEffect(() => {
    mountedRef.current = true;
    return () => { mountedRef.current = false; };
  }, []);

  const fetch = useCallback(
    async (currentKey: CanonicalKey) => {
      const requestSeq = ++requestSeqRef.current;
      setLoading(true);
      setError(undefined);
      try {
        const result = await substrate.get(currentKey);
        if (!mountedRef.current || requestSeq !== requestSeqRef.current) return;
        setData(result);
      } catch (err) {
        if (!mountedRef.current || requestSeq !== requestSeqRef.current) return;
        setError(err);
      } finally {
        if (mountedRef.current && requestSeq === requestSeqRef.current) setLoading(false);
      }
    },
    [substrate]
  );

  useEffect(() => {
    if (key === null) {
      requestSeqRef.current += 1;
      setData(undefined);
      setLoading(false);
      setError(undefined);
      return;
    }
    void fetch(key);
  }, [key?.context, key?.kind, key?.id, fetch]);

  const reload = useCallback(async () => {
    if (key !== null) await fetch(key);
  }, [fetch, key]);

  return { data, loading, error, reload };
}

// ---------------------------------------------------------------------------
// useBriqueProjection
// ---------------------------------------------------------------------------

export function useBriqueProjection<K extends SubstrateProjectionKind>(
  key: CanonicalKey | null,
  kind: K
): UseBriqueResult<ProjectionResult<K>> {
  const substrate = useBriqueSubstrate();
  const [data, setData] = useState<ProjectionResult<K> | undefined>(undefined);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<unknown>(undefined);
  const mountedRef = useRef(true);
  const requestSeqRef = useRef(0);

  useEffect(() => {
    mountedRef.current = true;
    return () => { mountedRef.current = false; };
  }, []);

  const fetch = useCallback(
    async (currentKey: CanonicalKey, currentKind: K) => {
      const requestSeq = ++requestSeqRef.current;
      setLoading(true);
      setError(undefined);
      try {
        const result = await substrate.project(currentKey, currentKind);
        if (!mountedRef.current || requestSeq !== requestSeqRef.current) return;
        setData(result);
      } catch (err) {
        if (!mountedRef.current || requestSeq !== requestSeqRef.current) return;
        setError(err);
      } finally {
        if (mountedRef.current && requestSeq === requestSeqRef.current) setLoading(false);
      }
    },
    [substrate]
  );

  useEffect(() => {
    if (key === null) {
      requestSeqRef.current += 1;
      setData(undefined);
      setLoading(false);
      setError(undefined);
      return;
    }
    void fetch(key, kind);
  }, [key?.context, key?.kind, key?.id, kind, fetch]);

  const reload = useCallback(async () => {
    if (key !== null) await fetch(key, kind);
  }, [fetch, key, kind]);

  return { data, loading, error, reload };
}

// ---------------------------------------------------------------------------
// useBriqueExecute
// ---------------------------------------------------------------------------

export function useBriqueExecute(): {
  execute: (input: ExecuteInput) => Promise<ExecuteResult>;
} {
  const substrate = useBriqueSubstrate();
  const execute = useCallback(
    (input: ExecuteInput) => substrate.execute(input),
    [substrate]
  );
  return { execute };
}

// ---------------------------------------------------------------------------
// useBriqueMutation
// ---------------------------------------------------------------------------

export function useBriqueMutation(): {
  mutate: (input: ExecuteInput) => Promise<ExecuteResult>;
} {
  const substrate = useBriqueSubstrate();
  const mutate = useCallback(
    (input: ExecuteInput) => substrate.mutate(input),
    [substrate]
  );
  return { mutate };
}

// ---------------------------------------------------------------------------
// usePulseConnection
// ---------------------------------------------------------------------------
//
// Exposes the current Pulse connection state provided by the host/runtime
// adapter. This is intentionally not resolved through BriqueSubstrate.get():
// there is no Brique capability named "pulse.connection".
//
export function usePulseConnection(): { connectionState: ConnectionState } {
  const connectionState = usePulseConnectionState();
  return { connectionState };
}
