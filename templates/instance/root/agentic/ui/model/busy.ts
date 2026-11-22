// A refusal "busy" is transient: the callee refused BEFORE running the call (the Matter loop of the engine, the pending-work queue of a wrapper), so asking
// again is safe. The interface asks again a bounded number of times, with growing, random pauses (the same rule as the port of agentic_core).
// A call that did not answer is not asked again (it may have run); any other error is the person's to see.

export const BUSY_REASONS = ["busy", "loop busy"];

export type BusySettings = { busyAttempts: number; busyBaseMs: number; busyCapMs: number };

/** Is this canonical Response a refusal because the callee is busy? */
export function isBusy(response: any): boolean {
  if (!response || response.status !== "error") return false;
  const reason = response.error?.details?.reason;
  return typeof reason === "string" && BUSY_REASONS.includes(reason);
}

/** Pause before the attempt that follows `attempt` (0 for the first): doubled each time up to the cap, times a random factor from 0.5 to 1.5. */
export function delayBeforeRetry(attempt: number, baseMs: number, capMs: number, random: () => number = Math.random): number {
  return Math.min(capMs, baseMs * 2 ** attempt) * (0.5 + random());
}

const wait = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));

/** Sends, and sends again while the answer is "busy"; returns the last Response (busy included, for the caller to show). */
export async function sendWithBusyRetry<T = any>(
  send: () => Promise<T>,
  settings: BusySettings,
  sleep: (ms: number) => Promise<void> = wait,
  random: () => number = Math.random,
): Promise<T> {
  const attempts = Math.max(1, Math.floor(settings.busyAttempts));
  let response = await send();
  for (let attempt = 0; attempt < attempts - 1 && isBusy(response); attempt++) {
    await sleep(delayBeforeRetry(attempt, settings.busyBaseMs, settings.busyCapMs, random));
    response = await send();
  }
  return response;
}
