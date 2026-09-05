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

import type { Address } from "../circulation/address.js";
import type { Identity } from "../circulation/identity.js";
import type {
  Correlation,
  Intention,
  MatterRef,
} from "../circulation/intention.js";
import type { UIPath } from "../core/types.js";

export type UIIntentionInput = {
  to: Address;
  targetPathUI?: UIPath;
  sourcePathUI?: UIPath;
  identity?: Identity;
  params?: Record<string, unknown>;
  matters?: MatterRef[];
  correlation?: Correlation;
  awaitResponse?: boolean;
};

export type UIActionBuilderConfig = {
  addressForPath: (pathUI: UIPath) => string;
  identity?: Identity;
  defaultSourcePathUI?: UIPath;
};

const DEFAULT_IDENTITY: Identity = Object.freeze({
  id: "pulse-ui",
  kind: "ui",
});

const DEFAULT_SOURCE_PATH_UI = "/";

export class UIActionBuilder {
  private readonly addressForPath: (pathUI: UIPath) => string;
  private readonly identity: Identity;
  private readonly defaultSourcePathUI: UIPath;

  constructor(config: UIActionBuilderConfig) {
    this.addressForPath = config.addressForPath;
    this.identity = config.identity ?? DEFAULT_IDENTITY;
    this.defaultSourcePathUI =
      config.defaultSourcePathUI ?? DEFAULT_SOURCE_PATH_UI;
  }

  create(input: UIIntentionInput): Omit<Intention, "intention_id"> {
    const sourcePathUI = input.sourcePathUI ?? input.targetPathUI ?? this.defaultSourcePathUI;

    return {
      await_response: input.awaitResponse !== false,
      to: input.to,
      from: {
        context: this.addressForPath(sourcePathUI),
        cap: "ui",
        type: "user",
      },
      identity: input.identity ?? this.identity,
      params: input.params,
      matters: input.matters,
      correlation: input.correlation,
    };
  }
}
