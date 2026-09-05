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

const DEFAULT_TIMEOUT_MS = 30_000;
const RETRY_INTERVAL_MS = 200;

export async function waitForWebSocket(
  url: string,
  timeoutMs = DEFAULT_TIMEOUT_MS
): Promise<void> {
  const deadline = Date.now() + timeoutMs;

  while (Date.now() < deadline) {
    const connected = await tryConnect(url);
    if (connected) return;
    await delay(RETRY_INTERVAL_MS);
  }

  throw new Error(
    `Brique WebSocket at ${url} did not become ready within ${timeoutMs}ms`
  );
}

function tryConnect(url: string): Promise<boolean> {
  return new Promise((resolve) => {
    const ws = new WebSocket(url);

    const cleanup = () => {
      ws.onopen = null;
      ws.onerror = null;
      ws.onclose = null;
    };

    ws.onopen = () => {
      cleanup();
      ws.close();
      resolve(true);
    };

    ws.onerror = () => {
      cleanup();
      resolve(false);
    };

    ws.onclose = () => {
      cleanup();
      resolve(false);
    };
  });
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
