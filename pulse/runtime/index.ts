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

export { PulseRuntime } from "./core/PulseRuntime.js";
export type { PulseRuntimeConfig } from "./core/PulseRuntime.js";

export * from "./core/types.js";
export * from "./circulation/index.js";

export * from "./transport/Transport.js";
export { TransportManager } from "./transport/TransportManager.js";
export { WebSocketTransport } from "./transport/WebSocketTransport.js";

export { ProjectionState } from "./projection/ProjectionState.js";
export { PathRegistry } from "./projection/PathRegistry.js";
export type { PathSnapshot } from "./projection/PathRegistry.js";
export { UIRuntimeIdentity } from "./ui/UIRuntimeIdentity.js";
export type { UIRuntimeIdentityConfig } from "./ui/UIRuntimeIdentity.js";

export { SubscriptionRegistry } from "./flow/SubscriptionRegistry.js";
export type { SubscriptionCallback } from "./flow/SubscriptionRegistry.js";
export { FlowRegistry } from "./flow/FlowRegistry.js";
export type {
  Flow,
  FlowSnapshot,
  FlowStatus,
} from "./flow/FlowRegistry.js";
export type {
  KeyedRuntimeStore,
  RuntimeStore,
} from "./store/types.js";
export {
  MatterRegistry,
  matterKeyToString,
  normalizeMatterKey,
} from "./matter/MatterRegistry.js";
export { MatterRuntime } from "./matter/MatterRuntime.js";
export type {
  MatterAddress,
  MatterEventSnapshot,
  MatterKey,
  MatterSnapshot,
  MatterStatus,
} from "./matter/MatterRegistry.js";
export type {
  EmitAndWait,
  MatterReadBatchInput,
  MatterReadInput,
  MatterRuntimeConfig,
  MatterSubscribeInput,
  MatterUnsubscribeInput,
} from "./matter/MatterRuntime.js";
export {
  extractMatterEvent,
  isMatterEventMessage,
} from "./matter/matterEvent.js";
export type { MatterEventDispatch } from "./matter/matterEvent.js";
export { UIActionBuilder } from "./action/UIActionBuilder.js";
export type {
  UIActionBuilderConfig,
  UIIntentionInput,
} from "./action/UIActionBuilder.js";

export { DispatchEngine } from "./dispatch/DispatchEngine.js";
