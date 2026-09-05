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

import type { ContextID } from "../circulation/address.js";

export type MatterAddress = {
  context: ContextID;
  matterId: string;
};

export type MatterKey = MatterAddress & {
  readMode: string | null;
};

export type MatterStatus =
  | "idle"
  | "loading"
  | "streaming"
  | "ready"
  | "error"
  | "stale"
  | "deleted";

export type MatterEventSnapshot = {
  event: "matter_written" | "matter_deleted";
  matterId: string;
  revision: number | null;
  substanceMode: string | null;
  sourceIntentionId: string | null;
};

export type MatterSnapshot = {
  address: MatterAddress;
  readMode: string | null;
  status: MatterStatus;
  data: unknown;
  payload: unknown;
  error: unknown;
  revision: number | null;
  subId: string | null;
  subscribed: boolean;
  lastEvent: MatterEventSnapshot | null;
  streamBytesReceived: number;
  streamChunksReceived: number;
};

type MatterEntry = {
  key: MatterKey;
  readers: number;
  subscribers: number;
  snapshot: MatterSnapshot;
};

const EMPTY_MATTER_SNAPSHOT = Object.freeze({
  address: Object.freeze({
    context: "",
    matterId: "",
  }),
  readMode: null,
  status: "idle",
  data: null,
  payload: null,
  error: null,
  revision: null,
  subId: null,
  subscribed: false,
  lastEvent: null,
  streamBytesReceived: 0,
  streamChunksReceived: 0,
}) satisfies MatterSnapshot;

export class MatterRegistry {
  private readonly entries: Map<string, MatterEntry> = new Map();
  private readonly listeners: Map<string, Set<() => void>> = new Map();
  private readonly subscriptionIndex: Map<string, MatterKey> = new Map();

  registerReader(input: MatterKey): MatterSnapshot {
    const entry = this.ensureEntry(input);
    entry.readers += 1;
    return entry.snapshot;
  }

  unregisterReader(input: MatterKey): void {
    const entry = this.entries.get(matterKeyToString(input));
    if (!entry) return;

    entry.readers = Math.max(0, entry.readers - 1);
    this.cleanupIfUnused(entry);
  }

  registerSubscriber(input: MatterKey): MatterSnapshot {
    const entry = this.ensureEntry(input);
    entry.subscribers += 1;
    this.updateEntry(entry, {
      subscribed: true,
    });
    return entry.snapshot;
  }

  unregisterSubscriber(input: MatterKey): void {
    const entry = this.entries.get(matterKeyToString(input));
    if (!entry) return;

    entry.subscribers = Math.max(0, entry.subscribers - 1);

    if (entry.subscribers === 0) {
      this.updateEntry(entry, {
        subscribed: false,
        subId: null,
      });
    }

    this.cleanupIfUnused(entry);
  }

  getSnapshot(input: MatterKey | null): MatterSnapshot {
    if (!input) return EMPTY_MATTER_SNAPSHOT;
    return this.entries.get(matterKeyToString(input))?.snapshot ?? EMPTY_MATTER_SNAPSHOT;
  }

  has(input: MatterKey): boolean {
    return this.entries.has(matterKeyToString(input));
  }

  getReaderCount(input: MatterKey): number {
    return this.entries.get(matterKeyToString(input))?.readers ?? 0;
  }

  getSubscriberCount(input: MatterKey): number {
    return this.entries.get(matterKeyToString(input))?.subscribers ?? 0;
  }

  setLoading(input: MatterKey): void {
    const entry = this.ensureEntry(input);
    this.updateEntry(entry, {
      status: "loading",
      data: null,
      payload: null,
      error: null,
      streamBytesReceived: 0,
      streamChunksReceived: 0,
    });
  }

  setReady(input: MatterKey, payload: unknown): void {
    const entry = this.ensureEntry(input);
    const data = extractMatterData(payload);
    const revision = extractRevision(payload);

    this.updateEntry(entry, {
      status: "ready",
      payload,
      data,
      error: null,
      revision,
    });
  }

  setStreaming(input: MatterKey): void {
    const entry = this.ensureEntry(input);
    this.updateEntry(entry, {
      status: "streaming",
      data: null,
      error: null,
      streamBytesReceived: 0,
      streamChunksReceived: 0,
    });
  }

  appendChunk(input: MatterKey, chunk: Uint8Array): void {
    const entry = this.entries.get(matterKeyToString(input));
    if (!entry) return;

    const prevBytes = toUint8Array(entry.snapshot.data) ?? new Uint8Array(0);
    const chunkBytes = toUint8Array(chunk) ?? new Uint8Array(0);
    const merged = new Uint8Array(prevBytes.length + chunkBytes.length);
    merged.set(prevBytes);
    merged.set(chunkBytes, prevBytes.length);

    this.updateEntry(entry, {
      data: merged,
      streamBytesReceived: merged.length,
      streamChunksReceived: entry.snapshot.streamChunksReceived + 1,
    });
  }

  setError(input: MatterKey, error: unknown): void {
    const entry = this.ensureEntry(input);
    this.updateEntry(entry, {
      status: "error",
      error,
    });
  }

  setSubscription(input: MatterKey, subId: string | null): void {
    const entry = this.ensureEntry(input);
    const previousSubId = entry.snapshot.subId;
    if (previousSubId) {
      this.subscriptionIndex.delete(previousSubId);
    }

    if (subId) {
      this.subscriptionIndex.set(subId, entry.key);
    }

    this.updateEntry(entry, {
      subId,
      subscribed: subId !== null || entry.subscribers > 0,
    });
  }

  markStale(input: MatterKey, event: MatterEventSnapshot): void {
    const entry = this.entries.get(matterKeyToString(input));
    if (!entry) return;

    this.updateEntry(entry, {
      status: "stale",
      revision: event.revision,
      lastEvent: event,
    });
  }

  markDeleted(input: MatterKey, event: MatterEventSnapshot): void {
    const entry = this.entries.get(matterKeyToString(input));
    if (!entry) return;

    this.updateEntry(entry, {
      status: "deleted",
      data: null,
      payload: null,
      revision: event.revision,
      lastEvent: event,
    });
  }

  subscribeToSnapshot(input: MatterKey, listener: () => void): () => void {
    const key = matterKeyToString(input);
    let set = this.listeners.get(key);
    if (!set) {
      set = new Set();
      this.listeners.set(key, set);
    }

    set.add(listener);

    return () => {
      const current = this.listeners.get(key);
      if (!current) return;

      current.delete(listener);

      if (current.size === 0) {
        this.listeners.delete(key);
      }
    };
  }

  clear(): void {
    const keys = Array.from(this.entries.keys());
    this.entries.clear();
    this.subscriptionIndex.clear();

    for (const key of keys) {
      this.notify(key);
    }
  }

  clearListeners(): void {
    this.listeners.clear();
  }

  normalizeKey(input: MatterKey): MatterKey {
    return normalizeMatterKey(input);
  }

  getKeyForSubId(subId: string): MatterKey | null {
    return this.subscriptionIndex.get(subId) ?? null;
  }

  findActiveKeysByMatterId(matterId: string): MatterKey[] {
    const normalizedMatterId = normalizeMatterId(matterId);
    const keys: MatterKey[] = [];

    for (const entry of this.entries.values()) {
      if (entry.key.matterId === normalizedMatterId) {
        keys.push(entry.key);
      }
    }

    return keys;
  }

  private ensureEntry(input: MatterKey): MatterEntry {
    const normalized = normalizeMatterKey(input);
    const key = matterKeyToString(normalized);
    const current = this.entries.get(key);
    if (current) return current;

    const entry: MatterEntry = {
      key: normalized,
      readers: 0,
      subscribers: 0,
      snapshot: createMatterSnapshot(normalized),
    };

    this.entries.set(key, entry);
    return entry;
  }

  private updateEntry(
    entry: MatterEntry,
    patch: Partial<Omit<MatterSnapshot, "address" | "readMode">>
  ): void {
    const next = Object.freeze({
      ...entry.snapshot,
      ...patch,
    });

    entry.snapshot = next;
    this.notify(matterKeyToString(entry.key));
  }

  private cleanupIfUnused(entry: MatterEntry): void {
    if (entry.readers > 0 || entry.subscribers > 0) return;

    const key = matterKeyToString(entry.key);
    if (entry.snapshot.subId) {
      this.subscriptionIndex.delete(entry.snapshot.subId);
    }
    this.entries.delete(key);
    this.notify(key);
  }

  private notify(key: string): void {
    const listeners = this.listeners.get(key);
    if (!listeners) return;

    for (const listener of listeners) {
      listener();
    }
  }
}

export function normalizeMatterKey(input: MatterKey): MatterKey {
  return Object.freeze({
    context: normalizeContext(input.context),
    matterId: normalizeMatterId(input.matterId),
    readMode: normalizeReadMode(input.readMode),
  });
}

export function matterKeyToString(input: MatterKey): string {
  const key = normalizeMatterKey(input);
  return `${key.context}::${key.matterId}::${key.readMode ?? ""}`;
}

function createMatterSnapshot(key: MatterKey): MatterSnapshot {
  return Object.freeze({
    address: Object.freeze({
      context: key.context,
      matterId: key.matterId,
    }),
    readMode: key.readMode,
    status: "idle",
    data: null,
    payload: null,
    error: null,
    revision: null,
    subId: null,
    subscribed: false,
    lastEvent: null,
    streamBytesReceived: 0,
    streamChunksReceived: 0,
  });
}

function normalizeContext(context: string): string {
  const normalized = context.trim();
  if (!normalized) {
    throw new Error("Matter context is required");
  }
  return normalized;
}

function normalizeMatterId(matterId: string): string {
  const normalized = matterId.trim();
  if (!normalized) {
    throw new Error("Matter id is required");
  }
  return normalized;
}

function normalizeReadMode(readMode: string | null | undefined): string | null {
  const normalized = readMode?.trim();
  return normalized || null;
}

function extractMatterData(payload: unknown): unknown {
  const bytes = toUint8Array(payload);
  if (bytes) {
    return bytes;
  }

  if (
    typeof payload === "object" &&
    payload !== null &&
    "data" in payload
  ) {
    return (payload as { data?: unknown }).data ?? null;
  }

  return null;
}

function toUint8Array(value: unknown): Uint8Array | null {
  if (value instanceof Uint8Array) {
    return value;
  }

  if (value instanceof ArrayBuffer) {
    return new Uint8Array(value);
  }

  if (ArrayBuffer.isView(value)) {
    return new Uint8Array(value.buffer, value.byteOffset, value.byteLength);
  }

  return null;
}

function extractRevision(payload: unknown): number | null {
  if (
    typeof payload === "object" &&
    payload !== null &&
    "revision" in payload
  ) {
    const revision = (payload as { revision?: unknown }).revision;
    return typeof revision === "number" ? revision : null;
  }

  return null;
}
