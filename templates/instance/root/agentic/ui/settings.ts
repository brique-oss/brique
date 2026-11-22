// The performance settings of the interface: what its load depends on, given to the block as the property `settings`, never fixed in the code.
// Each one has a default and a minimum; a value that is not a finite number, or below its minimum, is ignored. The list of all the settings of the
// agentic context is in document/performance_settings.md.
import { createContext, useContext } from "react";

export type Settings = {
  listThrottleMs: number;   // at most one read of each Matter of the list per this long (a row does not need every line the instant it is written)
  journalTailBytes: number; // bytes read at the end of a journal (ONE value for a Matter: Pulse drops the events when two keys follow the same Matter). Bounded in bytes, not lines: a line can be tens of kilobytes, and the browser bridge carries at most 180 KiB per answer
  watchdogMs: number;       // the conversation that is open is read again every so often, even if no event came
  listPollMs: number;       // the list of the continuities is read again this often
  busyAttempts: number;     // attempts of a call that the callee refuses because it is busy (the first included)
  busyBaseMs: number;       // pause before the second attempt, doubled each time ...
  busyCapMs: number;        // ... up to this (each pause multiplied by a random factor from 0.5 to 1.5)
};

export const DEFAULT_SETTINGS: Settings = { listThrottleMs: 400, journalTailBytes: 120 * 1024, watchdogMs: 15000, listPollMs: 3000, busyAttempts: 6, busyBaseMs: 100, busyCapMs: 2000 };

const MINIMUM: Settings = { listThrottleMs: 0, journalTailBytes: 4096, watchdogMs: 1000, listPollMs: 500, busyAttempts: 1, busyBaseMs: 0, busyCapMs: 0 };

export function resolveSettings(given?: Partial<Settings> | null): Settings {
  const out: Settings = { ...DEFAULT_SETTINGS };
  if (!given || typeof given !== "object") return out;
  for (const key of Object.keys(DEFAULT_SETTINGS) as Array<keyof Settings>) {
    const value = (given as Record<string, unknown>)[key];
    if (typeof value === "number" && Number.isFinite(value) && value >= MINIMUM[key]) out[key] = value;
  }
  return out;
}

export const SettingsContext = createContext<Settings>(DEFAULT_SETTINGS);
export const useSettings = (): Settings => useContext(SettingsContext);
