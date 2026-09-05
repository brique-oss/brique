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

import type {
  Intention,
  Response,
} from "../circulation/index.js";
import type { Identity } from "../circulation/identity.js";
import type { UIPath } from "../core/types.js";
import {
  MatterRegistry,
  normalizeMatterKey,
  type MatterKey,
  type MatterSnapshot,
} from "./MatterRegistry.js";
import type { MatterEventDispatch } from "./matterEvent.js";

export type EmitAndWait = (
  input: Omit<Intention, "intention_id">,
  targetPathUI?: UIPath
) => Promise<Response>;

export type MatterRuntimeConfig = {
  emitAndWait: EmitAndWait;
  sourceAddressForPath: (pathUI: UIPath) => string;
  identity?: Identity;
  sourcePathUI?: UIPath;
};

export type MatterReadInput = {
  context: string;
  matterId: string;
  readMode?: string | null;
};

export type MatterReadBatchInput = {
  context: string;
  matterIds: string[];
  readMode?: string | null;
};

export type MatterSubscribeInput = {
  context: string;
  matterId: string;
  readMode?: string | null;
  subId?: string;
};

export type MatterUnsubscribeInput = {
  context: string;
  matterId: string;
  readMode?: string | null;
  subId: string;
};

const DEFAULT_SOURCE_PATH_UI = "/matter";
const DEFAULT_IDENTITY: Identity = Object.freeze({
  id: "pulse-ui",
  kind: "ui",
});

export class MatterRuntime extends MatterRegistry {
  private readonly emitAndWait: EmitAndWait;
  private readonly sourceAddressForPath: (pathUI: UIPath) => string;
  private readonly identity: Identity;
  private readonly sourcePathUI: UIPath;
  private readonly pendingReads: Map<string, Promise<MatterSnapshot>> = new Map();
  private readonly pendingSubscribes: Map<string, Promise<MatterSnapshot>> = new Map();
  private readonly keyVersions: Map<string, number> = new Map();
  private readonly readRequestCounts: Map<string, number> = new Map();
  private readonly activeStreams: Map<string, ReadableStreamDefaultReader<Uint8Array>> = new Map();

  constructor(config: MatterRuntimeConfig) {
    super();
    this.emitAndWait = config.emitAndWait;
    this.sourceAddressForPath = config.sourceAddressForPath;
    this.identity = config.identity ?? DEFAULT_IDENTITY;
    this.sourcePathUI = config.sourcePathUI ?? DEFAULT_SOURCE_PATH_UI;
  }

  acquireReader(input: MatterReadInput): MatterSnapshot {
    const key = this.toKey(input);
    const snapshot = this.registerReader(key);

    if (snapshot.status === "idle") {
      void this.refresh(key).catch(noop);
    }

    return this.getSnapshot(key);
  }

  releaseReader(input: MatterReadInput): void {
    const key = this.toKey(input);
    const keyString = this.keyToString(key);

    this.unregisterReader(key);

    if (!this.has(key)) {
      this.invalidatePendingLifecycle(keyString);
    }
  }

  refresh(input: MatterReadInput): Promise<MatterSnapshot> {
    const key = this.toKey(input);
    if (!this.has(key)) {
      return Promise.resolve(this.getSnapshot(key));
    }

    return this.readActive(key, false);
  }

  async read(input: MatterReadInput): Promise<MatterSnapshot> {
    const key = this.toKey(input);
    return this.readActive(key, true);
  }

  private async readActive(
    key: MatterKey,
    allowCreate: boolean
  ): Promise<MatterSnapshot> {
    const keyString = this.keyToString(key);
    const pending = this.pendingReads.get(keyString);
    if (pending) return pending;

    if (!allowCreate && !this.has(key)) {
      return this.getSnapshot(key);
    }

    this.setLoading(key);
    const version = this.getKeyVersion(keyString);
    this.readRequestCounts.set(
      keyString,
      (this.readRequestCounts.get(keyString) ?? 0) + 1
    );

    let read!: Promise<MatterSnapshot>;
    read = (async () => {
      const response = await this.emitAndWait(this.buildMatterIntention({
        context: key.context,
        cap: "matter.read",
        params: {
          matter_id: key.matterId,
          ...(key.readMode ? { read_mode: key.readMode } : {}),
        },
      }));

      this.assertOK(response);
      if (!this.has(key) || this.getKeyVersion(keyString) !== version) {
        return this.getSnapshot(key);
      }

      const lease = extractHTTPLease(response.payload);
      if (lease) {
        await this.streamHTTPLease(key, keyString, version, lease);
      } else {
        this.setReady(key, response.payload ?? null);
      }
      return this.getSnapshot(key);
    })().catch((err) => {
      if (this.has(key) && this.getKeyVersion(keyString) === version) {
        this.setError(key, err);
      }
      throw err;
    }).finally(() => {
      if (this.pendingReads.get(keyString) === read) {
        this.pendingReads.delete(keyString);
      }
    });

    this.pendingReads.set(keyString, read);
    return read;
  }

  async readBatch(input: MatterReadBatchInput): Promise<Response> {
    const response = await this.emitAndWait(this.buildMatterIntention({
      context: input.context,
      cap: "matter.read_batch",
      params: {
        matter_ids: input.matterIds,
        ...(input.readMode ? { read_mode: input.readMode } : {}),
      },
    }));

    this.assertOK(response);
    return response;
  }

  async subscribe(input: MatterSubscribeInput): Promise<MatterSnapshot> {
    const key = this.toKey(input);
    this.registerSubscriber(key);
    return this.ensureSubscription(key, input);
  }

  acquireSubscription(input: MatterSubscribeInput): MatterSnapshot {
    const key = this.toKey(input);
    const subscriberCount = this.getSubscriberCount(key);
    const snapshot = this.registerSubscriber(key);

    if (subscriberCount === 0 && !snapshot.subId) {
      void this.ensureSubscription(key, input).catch(noop);
    }

    return this.getSnapshot(key);
  }

  releaseSubscription(input: MatterReadInput): void {
    const key = this.toKey(input);
    const keyString = this.keyToString(key);
    const subscriberCount = this.getSubscriberCount(key);
    const snapshot = this.getSnapshot(key);

    this.unregisterSubscriber(key);

    if (!this.has(key)) {
      this.invalidatePendingLifecycle(keyString);
    }

    if (subscriberCount <= 1 && snapshot.subId) {
      void this.sendUnsubscribe(key, snapshot.subId).catch(noop);
    }
  }

  private async ensureSubscription(
    key: MatterKey,
    input: MatterSubscribeInput
  ): Promise<MatterSnapshot> {
    const keyString = this.keyToString(key);
    const pending = this.pendingSubscribes.get(keyString);
    if (pending) return pending;

    const current = this.getSnapshot(key);
    if (current.subId) return current;
    const version = this.getKeyVersion(keyString);

    let subscribe!: Promise<MatterSnapshot>;
    subscribe = (async () => {
      try {
        const response = await this.emitAndWait(this.buildMatterIntention({
          context: key.context,
          cap: "matter.subscribe",
          params: {
            matter_id: key.matterId,
            ...(input.subId ? { sub_id: input.subId } : {}),
          },
        }));
  
        this.assertOK(response);
        const subId = extractSubId(response);
        if (this.has(key) && this.getKeyVersion(keyString) === version) {
          this.setSubscription(key, subId);
        } else if (subId) {
          void this.sendUnsubscribe(key, subId).catch(noop);
        }
        return this.getSnapshot(key);
      } catch (err) {
        if (this.has(key) && this.getKeyVersion(keyString) === version) {
          this.setError(key, err);
        }
        throw err;
      }
    })().finally(() => {
      if (this.pendingSubscribes.get(keyString) === subscribe) {
        this.pendingSubscribes.delete(keyString);
      }
    });

    this.pendingSubscribes.set(keyString, subscribe);
    return subscribe;
  }

  async unsubscribe(input: MatterUnsubscribeInput): Promise<MatterSnapshot> {
    const key = this.toKey(input);

    await this.sendUnsubscribe(key, input.subId);
    this.unregisterSubscriber(key);
    return this.getSnapshot(key);
  }

  private async sendUnsubscribe(
    key: MatterKey,
    subId: string
  ): Promise<void> {
    try {
      const response = await this.emitAndWait(this.buildMatterIntention({
        context: key.context,
        cap: "matter.unsubscribe",
        params: {
          matter_id: key.matterId,
          sub_id: subId,
        },
      }));

      this.assertOK(response);
      if (this.has(key)) {
        this.setSubscription(key, null);
      }
    } catch (err) {
      if (this.has(key)) {
        this.setError(key, err);
      }
      throw err;
    }
  }

  handleEvent(event: MatterEventDispatch): boolean {
    const key = this.resolveEventKey(event);
    if (!key) return false;

    if (event.event === "matter_written") {
      this.markStale(key, event);
      return true;
    }

    this.markDeleted(key, event);
    return true;
  }

  private toKey(input: MatterReadInput): MatterKey {
    return normalizeMatterKey({
      context: input.context,
      matterId: input.matterId,
      readMode: input.readMode ?? null,
    });
  }

  private keyToString(input: MatterKey): string {
    return `${input.context}::${input.matterId}::${input.readMode ?? ""}`;
  }

  getReadRequestCount(input: MatterReadInput): number {
    return this.readRequestCounts.get(this.keyToString(this.toKey(input))) ?? 0;
  }

  hasPendingRead(input: MatterReadInput): boolean {
    return this.pendingReads.has(this.keyToString(this.toKey(input)));
  }

  private getKeyVersion(keyString: string): number {
    return this.keyVersions.get(keyString) ?? 0;
  }

  private invalidatePendingLifecycle(keyString: string): void {
    this.keyVersions.set(keyString, this.getKeyVersion(keyString) + 1);
    this.pendingReads.delete(keyString);
    this.pendingSubscribes.delete(keyString);
    const stream = this.activeStreams.get(keyString);
    if (stream) {
      stream.cancel().catch(noop);
      this.activeStreams.delete(keyString);
    }
  }

  private resolveEventKey(event: MatterEventDispatch): MatterKey | null {
    if (event.subId) {
      return this.getKeyForSubId(event.subId);
    }

    const matches = this.findActiveKeysByMatterId(event.matterId);
    return matches.length === 1 ? matches[0] : null;
  }

  private buildMatterIntention(input: {
    context: string;
    cap: string;
    params: Record<string, unknown>;
  }): Omit<Intention, "intention_id"> {
    return {
      await_response: true,
      to: {
        context: input.context,
        cap: input.cap,
        type: "matter",
      },
      from: {
        context: this.sourceAddressForPath(this.sourcePathUI),
        cap: "ui",
        type: "user",
      },
      identity: this.identity,
      params: input.params,
    };
  }

  private assertOK(response: Response): void {
    if (response.status === "ok" || response.status === "result" || response.status === "success") {
      return;
    }

    throw response.error ?? new Error(`Matter response failed: ${response.status}`);
  }

  private async streamHTTPLease(
    key: MatterKey,
    keyString: string,
    version: number,
    lease: HTTPLease
  ): Promise<void> {
    this.setStreaming(key);
    let reader: ReadableStreamDefaultReader<Uint8Array> | null = null;

    try {
      const response = await fetch(lease.url, {
        ...(lease.token ? { headers: { Authorization: `Bearer ${lease.token}` } } : {}),
      });

      if (!response.ok || !response.body) {
        throw new Error(`HTTP lease fetch failed: ${response.status}`);
      }

      reader = response.body.getReader();
      this.activeStreams.set(keyString, reader);

      while (true) {
        if (!this.has(key) || this.getKeyVersion(keyString) !== version) {
          reader.cancel().catch(noop);
          return;
        }

        const { done, value } = await reader.read();
        if (done) break;

        if (this.has(key) && this.getKeyVersion(keyString) === version) {
          this.appendChunk(key, value);
        }
      }

      if (this.has(key) && this.getKeyVersion(keyString) === version) {
        const current = this.getSnapshot(key);
        this.setReady(key, current.data);
      }
    } catch (err) {
      if (this.has(key) && this.getKeyVersion(keyString) === version) {
        this.setError(key, err);
      }
    } finally {
      this.activeStreams.delete(keyString);
    }
  }
}

type HTTPLease = {
  url: string;
  token?: string;
};

function extractHTTPLease(payload: unknown): HTTPLease | null {
  if (typeof payload !== "object" || payload === null) return null;

  const data = (payload as Record<string, unknown>).data;
  if (typeof data !== "object" || data === null) return null;

  const d = data as Record<string, unknown>;
  if (d.kind !== "http") return null;

  const http = d.http;
  if (typeof http !== "object" || http === null) return null;

  const h = http as Record<string, unknown>;
  if (typeof h.url === "string" && typeof h.token === "string") {
    return { url: h.url, token: h.token };
  }

  if (
    typeof h.base_url === "string" &&
    typeof h.path === "string" &&
    typeof h.tok === "string"
  ) {
    return { url: buildBriqueHTTPLeaseURL(h.base_url, h.path, h.tok) };
  }

  return null;
}

function buildBriqueHTTPLeaseURL(baseURL: string, path: string, token: string): string {
  const trimmedBase = baseURL.replace(/\/+$/, "");
  const normalizedPath = path.startsWith("/") ? path : `/${path}`;
  const separator = normalizedPath.includes("?") ? "&" : "?";
  return `${trimmedBase}${normalizedPath}${separator}tok=${encodeURIComponent(token)}`;
}

function extractSubId(response: Response): string | null {
  const subId = response.payload?.sub_id;
  return typeof subId === "string" && subId.trim() ? subId : null;
}

function noop() {}
