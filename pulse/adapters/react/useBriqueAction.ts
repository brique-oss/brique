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

import { useEffect, useRef, useState } from "react";
import type { IntentionId, UIIntentionInput } from "../../runtime/index.js";
import { usePulse } from "./usePulse.js";

export type BriqueActionStatus = "idle" | "running" | "success" | "error";

export type BriqueActionConfig = UIIntentionInput;

export type BriqueActionState = {
  status: BriqueActionStatus;
  intentionId: IntentionId | null;
  error: unknown;
};

export type BriqueAction = BriqueActionState & {
  emit: (params?: Record<string, unknown>) => Promise<IntentionId>;
  reset: () => void;
};

const IDLE_STATE: BriqueActionState = Object.freeze({
  status: "idle",
  intentionId: null,
  error: null,
});

export function useBriqueAction(config: BriqueActionConfig): BriqueAction {
  const pulse = usePulse();
  const mountedRef = useRef(false);
  const emissionRef = useRef(0);
  const configKey = createConfigKey(config);
  const configKeyRef = useRef(configKey);
  const [state, setState] = useState<BriqueActionState>(IDLE_STATE);

  useEffect(() => {
    mountedRef.current = true;

    return () => {
      mountedRef.current = false;
    };
  }, []);

  useEffect(() => {
    configKeyRef.current = configKey;
  }, [configKey]);

  async function emit(
    params?: Record<string, unknown>
  ): Promise<IntentionId> {
    const emissionId = emissionRef.current + 1;
    emissionRef.current = emissionId;
    const emittedConfigKey = configKeyRef.current;

    setState({
      status: "running",
      intentionId: null,
      error: null,
    });

    try {
      const intentionId = await pulse.emitUIIntention({
        ...config,
        params: params === undefined ? config.params : {
          ...(config.params ?? {}),
          ...params,
        },
      });

      if (isCurrentEmission(emissionId, emittedConfigKey)) {
        setState({
          status: "success",
          intentionId,
          error: null,
        });
      }

      return intentionId;
    } catch (err) {
      if (isCurrentEmission(emissionId, emittedConfigKey)) {
        setState({
          status: "error",
          intentionId: null,
          error: err,
        });
      }

      throw err;
    }
  }

  function reset(): void {
    emissionRef.current += 1;
    setState(IDLE_STATE);
  }

  function isCurrentEmission(
    emissionId: number,
    emittedConfigKey: string
  ): boolean {
    return (
      mountedRef.current &&
      emissionRef.current === emissionId &&
      configKeyRef.current === emittedConfigKey
    );
  }

  return {
    ...state,
    emit,
    reset,
  };
}

function createConfigKey(config: BriqueActionConfig): string {
  return JSON.stringify({
    to: config.to,
    targetPathUI: config.targetPathUI ?? null,
    sourcePathUI: config.sourcePathUI ?? null,
    params: config.params ?? null,
    identity: config.identity ?? null,
    matters: config.matters ?? null,
    correlation: config.correlation ?? null,
  });
}
