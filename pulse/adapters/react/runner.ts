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

import type { ComponentType } from "react";
import React from "react";
import { createRoot } from "react-dom/client";
import type {
  AdapterMountInput,
  AdapterRootViewModule,
} from "../renderer.js";
import { PulseProvider } from "./PulseProvider.js";

type ReactRootViewProps = {
  pulse?: AdapterMountInput["pulse"];
  transportUrl?: string;
  contextPath?: string;
  childContextPaths?: string[];
  rootContextId?: string;
  hostBridge?: AdapterMountInput["hostBridge"];
};

export async function mountPulseRoot(input: AdapterMountInput): Promise<void> {
  const rootViewModule = await import(input.rootViewPath) as AdapterRootViewModule;
  const RootView =
    rootViewModule.default ??
    rootViewModule[input.rootViewName];

  if (!isComponent(RootView)) {
    throw new Error(`Root view not found: ${input.rootViewName}`);
  }

  const rootElement = React.createElement(RootView, {
    pulse: input.pulse,
    transportUrl: input.transportUrl,
    contextPath: input.contextPath,
    childContextPaths: input.childContextPaths,
    rootContextId: input.rootContextId,
    hostBridge: input.hostBridge,
  });

  createRoot(input.container)
    .render(React.createElement(PulseProvider, {
      pulse: input.pulse,
      children: rootElement,
    }));
}

function isComponent(value: unknown): value is ComponentType<ReactRootViewProps> {
  return typeof value === "function";
}
