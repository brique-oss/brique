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

import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { usePulseConnectionState, useBriqueSubstrate } from "../../React_Substrate_Adapter/context.js";
import { createCapabilityClient } from "../../Brique_Substrate/capability/index.js";
import { applyBriqueTheme } from "./applyBriqueTheme.js";
import { DEFAULT_THEME_INTENT, type ThemeIntent } from "./contracts.js";
import { generateThemeCss } from "./themeGenerator.js";
import { loadThemeIntent, saveThemeIntent } from "./themeStorage.js";
import { ThemeConfiguratorDialog } from "./ThemeConfiguratorDialog.js";

type ThemeController = {
  intent: ThemeIntent;
  openConfigurator: () => void;
};

const ThemeContext = createContext<ThemeController | null>(null);

export type ThemeProviderProps = {
  children: ReactNode;
};

export function ThemeProvider({ children }: ThemeProviderProps) {
  const substrate = useBriqueSubstrate();
  const connectionState = usePulseConnectionState();
  const client = useMemo(() => createCapabilityClient(substrate), [substrate]);
  const [intent, setIntent] = useState<ThemeIntent>(DEFAULT_THEME_INTENT);
  const [dialogOpen, setDialogOpen] = useState(false);
  const [error, setError] = useState<string | undefined>();
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    let cancelled = false;
    applyBriqueTheme(generateThemeCss(DEFAULT_THEME_INTENT));
    if (connectionState !== "connected") {
      return () => { cancelled = true; };
    }
    void loadThemeIntent(client).then((loaded) => {
      if (cancelled) return;
      setIntent(loaded);
      applyBriqueTheme(generateThemeCss(loaded));
    }).catch(() => {
      if (cancelled) return;
      setIntent(DEFAULT_THEME_INTENT);
      applyBriqueTheme(generateThemeCss(DEFAULT_THEME_INTENT));
    });
    return () => { cancelled = true; };
  }, [client, connectionState]);

  const applyIntent = useCallback(async (nextIntent: ThemeIntent) => {
    setSaving(true);
    setError(undefined);
    try {
      await saveThemeIntent(client, nextIntent);
      setIntent(nextIntent);
      applyBriqueTheme(generateThemeCss(nextIntent));
      setDialogOpen(false);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Unable to save UI theme");
    } finally {
      setSaving(false);
    }
  }, [client]);

  const controller = useMemo<ThemeController>(() => ({
    intent,
    openConfigurator: () => {
      setError(undefined);
      setDialogOpen(true);
    },
  }), [intent]);

  return (
    <ThemeContext.Provider value={controller}>
      {children}
      {dialogOpen && (
        <ThemeConfiguratorDialog
          initialIntent={intent}
          error={error}
          saving={saving}
          onCancel={() => setDialogOpen(false)}
          onApply={(nextIntent) => void applyIntent(nextIntent)}
        />
      )}
    </ThemeContext.Provider>
  );
}

export function useThemeController(): ThemeController {
  const controller = useContext(ThemeContext);
  if (!controller) {
    throw new Error("useThemeController must be used inside ThemeProvider");
  }
  return controller;
}
