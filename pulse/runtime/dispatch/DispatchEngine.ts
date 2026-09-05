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
  type CirculationMessage,
  isIntentionMessage,
  isResponseMessage,
} from "../circulation/index.js";
import type { IntentionId } from "../core/types.js";
import { SubscriptionRegistry } from "../flow/SubscriptionRegistry.js";
import { FlowRegistry } from "../flow/FlowRegistry.js";
import { ProjectionState } from "../projection/ProjectionState.js";
import type { MatterRuntime } from "../matter/MatterRuntime.js";
import { extractMatterEvent } from "../matter/matterEvent.js";

export class DispatchEngine {
  private readonly subscriptions: SubscriptionRegistry;
  private readonly flows: FlowRegistry;
  private readonly projection: ProjectionState;
  private readonly matter?: MatterRuntime;

  constructor(
    subscriptions: SubscriptionRegistry,
    flows: FlowRegistry,
    projection: ProjectionState,
    matter?: MatterRuntime
  ) {
    this.subscriptions = subscriptions;
    this.flows = flows;
    this.projection = projection;
    this.matter = matter;
  }

  dispatch(message: CirculationMessage): void {
    const matterEvent = extractMatterEvent(message);
    if (matterEvent) {
      this.matter?.handleEvent(matterEvent);
    }

    const intentionId = this.extractIntentionId(message);
    if (!intentionId) return;

    const uiPath = this.extractUIPath(message);
    if (uiPath) {
      this.projection.paths.activatePath(uiPath, intentionId);
    }

    this.flows.update(intentionId, message);
    this.subscriptions.notify(intentionId, message);
  }

  private extractIntentionId(message: CirculationMessage): IntentionId | null {
    if (isIntentionMessage(message)) {
      return message.intention.intention_id;
    }

    if (isResponseMessage(message)) {
      return message.response?.intention_id ?? null;
    }

    const unknown = message as unknown;
    if (this.hasStringField(unknown, "intention_id")) {
      return unknown.intention_id;
    }

    return null;
  }

  private extractUIPath(message: CirculationMessage): string | null {
    const context = this.extractDestinationContext(message);
    if (!context) return null;

    const match = /^@ui_[^:]+:(\/.*)$/.exec(context);
    return match?.[1] ?? null;
  }

  private extractDestinationContext(message: CirculationMessage): string | null {
    if (isIntentionMessage(message)) {
      return message.intention.to.context;
    }

    if (isResponseMessage(message)) {
      return message.response?.to?.context ?? null;
    }

    return null;
  }

  private hasStringField<T extends string>(
    value: unknown,
    field: T
  ): value is Record<T, string> {
    return (
      typeof value === "object" &&
      value !== null &&
      typeof (value as Record<T, unknown>)[field] === "string"
    );
  }
}
