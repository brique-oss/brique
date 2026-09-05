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

import { createContext, useContext, type ReactNode } from "react";
import type { BriqueSubstrate } from "../Brique_Substrate/substrate.js";
import type { ConnectionState } from "../types.js";

const BriqueSubstrateContext = createContext<BriqueSubstrate | null>(null);
const PulseConnectionContext = createContext<ConnectionState>("disconnected");

export type BriqueSubstrateProviderProps = {
  substrate: BriqueSubstrate;
  pulseConnectionState?: ConnectionState;
  children: ReactNode;
};

export function BriqueSubstrateProvider({
  substrate,
  pulseConnectionState = "disconnected",
  children,
}: BriqueSubstrateProviderProps) {
  return (
    <BriqueSubstrateContext.Provider value={substrate}>
      <PulseConnectionContext.Provider value={pulseConnectionState}>
        {children}
      </PulseConnectionContext.Provider>
    </BriqueSubstrateContext.Provider>
  );
}

export function useBriqueSubstrate(): BriqueSubstrate {
  const substrate = useContext(BriqueSubstrateContext);
  if (substrate === null) {
    throw new Error("useBriqueSubstrate must be used inside BriqueSubstrateProvider");
  }
  return substrate;
}

export type PulseConnectionProviderProps = {
  connectionState: ConnectionState;
  children: ReactNode;
};

export function PulseConnectionProvider({
  connectionState,
  children,
}: PulseConnectionProviderProps) {
  return (
    <PulseConnectionContext.Provider value={connectionState}>
      {children}
    </PulseConnectionContext.Provider>
  );
}

export function usePulseConnectionState(): ConnectionState {
  return useContext(PulseConnectionContext);
}
