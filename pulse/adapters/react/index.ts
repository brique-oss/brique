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

export { PulseProvider, PulseContext } from "./PulseProvider.js";
export type { PulseProviderProps } from "./PulseProvider.js";

export { usePulse } from "./usePulse.js";
export { useIntentionSubscription } from "./useIntentionSubscription.js";
export { usePathIntention } from "./usePathIntention.js";
export { useFlow } from "./useFlow.js";
export { useMatter } from "./useMatter.js";
export { useMatterEvent } from "./useMatterEvent.js";
export { useMatterSubscription } from "./useMatterSubscription.js";
export { usePathFlow } from "./usePathFlow.js";
export { useProjectionPath } from "./useProjectionPath.js";
export { useDisplayMode } from "./useDisplayMode.js";
export { useSetDisplayMode } from "./useSetDisplayMode.js";
export { useBriqueAction } from "./useBriqueAction.js";

export type { IntentionStatus, IntentionViewState } from "./types.js";
export type { MatterHookAddress, MatterOptions } from "./useMatter.js";
export type { MatterEventCallback } from "./useMatterEvent.js";
export type { ExternalStore, KeyedExternalStore } from "./store.js";
export type {
  BriqueAction,
  BriqueActionConfig,
  BriqueActionState,
  BriqueActionStatus,
} from "./useBriqueAction.js";
