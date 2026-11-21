/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

export const BROWSER_BRIDGE_PROTOCOL = "brique.browser.bridge/1" as const;
// Leaves room for base64url expansion and the JSON envelope below the 256 KiB native frame.
export const MAX_BRIDGE_PAYLOAD_BYTES = 180 << 10;
export const MAX_BRIDGE_DESCRIPTOR_BYTES = 64 << 10;

export type BridgeResponse = {
  format: typeof BROWSER_BRIDGE_PROTOCOL;
  kind: "hello" | "open" | "message" | "state" | "close" | "disconnect" | "error";
  sequence: number;
  ok: boolean;
  error_code?: string;
  session_id?: string;
  channel_id?: string;
  gateway_alias?: string;
  gateway_revision?: number;
  open?: boolean;
};

export type BridgeEvent = {
  format: typeof BROWSER_BRIDGE_PROTOCOL;
  kind: "event";
  sequence: number;
  session_id: string;
  channel_id: string;
  payload_b64: string;
};

const coordinate = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/u;
const generatedSession = /^bbs1_[A-Za-z0-9_-]{32}$/u;
const generatedChannel = /^bbc1_[A-Za-z0-9_-]{32}$/u;
const errorCode = /^[a-z][a-z0-9_]{0,63}$/u;

export function hello(sequence: number): Record<string, unknown> {
  requireSequence(sequence);
  return { format: BROWSER_BRIDGE_PROTOCOL, kind: "hello", sequence };
}

export function openChannel(input: {
  sequence: number;
  sessionId: string;
  origin: string;
  documentId: string;
  frameId: number;
  uiName: string;
  userActivated: boolean;
  descriptor?: Uint8Array;
}): Record<string, unknown> {
  requireSequence(input.sequence);
  if (!generatedSession.test(input.sessionId) || canonicalOrigin(input.origin) !== input.origin ||
      !coordinate.test(input.documentId) || !Number.isSafeInteger(input.frameId) || input.frameId < 0 ||
      !/^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/u.test(input.uiName) || input.userActivated !== true ||
      (input.descriptor !== undefined && (input.descriptor.byteLength === 0 || input.descriptor.byteLength > MAX_BRIDGE_DESCRIPTOR_BYTES))) {
    throw new Error("invalid browser bridge open request");
  }
  return {
    format: BROWSER_BRIDGE_PROTOCOL,
    kind: "open",
    sequence: input.sequence,
    session_id: input.sessionId,
    origin: input.origin,
    document_id: input.documentId,
    frame_id: input.frameId,
    ui_name: input.uiName,
    user_activated: true,
    ...(input.descriptor === undefined ? {} : { descriptor_b64: encodeBase64URL(input.descriptor) }),
  };
}

export function channelMessage(sequence: number, sessionId: string, channelId: string, payload: Uint8Array): Record<string, unknown> {
  requireSequence(sequence);
  if (!generatedSession.test(sessionId) || !generatedChannel.test(channelId) || payload.byteLength === 0 || payload.byteLength > MAX_BRIDGE_PAYLOAD_BYTES) {
    throw new Error("invalid browser bridge message");
  }
  return { format: BROWSER_BRIDGE_PROTOCOL, kind: "message", sequence, session_id: sessionId,
    channel_id: channelId, payload_b64: encodeBase64URL(payload) };
}

export function channelCommand(kind: "state" | "close", sequence: number, sessionId: string, channelId: string): Record<string, unknown> {
  requireSequence(sequence);
  if (!generatedSession.test(sessionId) || !generatedChannel.test(channelId)) throw new Error("invalid browser bridge channel");
  return { format: BROWSER_BRIDGE_PROTOCOL, kind, sequence, session_id: sessionId, channel_id: channelId };
}

export function disconnect(sequence: number, sessionId: string): Record<string, unknown> {
  requireSequence(sequence);
  if (!generatedSession.test(sessionId)) throw new Error("invalid browser bridge session");
  return { format: BROWSER_BRIDGE_PROTOCOL, kind: "disconnect", sequence, session_id: sessionId };
}

export function parseHostMessage(value: unknown): BridgeResponse | BridgeEvent {
  if (!isRecord(value) || value.format !== BROWSER_BRIDGE_PROTOCOL || !positiveSafeInteger(value.sequence) || typeof value.kind !== "string") {
    throw new Error("invalid browser bridge host message");
  }
  if (value.kind === "event") {
    exactKeys(value, ["format", "kind", "sequence", "session_id", "channel_id", "payload_b64"]);
    if (typeof value.session_id !== "string" || !generatedSession.test(value.session_id) ||
        typeof value.channel_id !== "string" || !generatedChannel.test(value.channel_id) ||
        typeof value.payload_b64 !== "string") throw new Error("invalid browser bridge event");
    decodeBase64URL(value.payload_b64, MAX_BRIDGE_PAYLOAD_BYTES);
    return value as BridgeEvent;
  }
  const allowedKinds = new Set(["hello", "open", "message", "state", "close", "disconnect", "error"]);
  if (!allowedKinds.has(value.kind) || typeof value.ok !== "boolean") throw new Error("invalid browser bridge response");
  if (value.kind === "error") {
    exactKeys(value, ["format", "kind", "sequence", "ok", "error_code"]);
    if (value.ok !== false || typeof value.error_code !== "string" || !errorCode.test(value.error_code)) throw new Error("invalid error response");
  } else if (value.kind === "hello") {
    exactKeys(value, ["format", "kind", "sequence", "ok", "session_id", "gateway_alias", "gateway_revision"]);
    if (value.ok !== true || typeof value.session_id !== "string" || !generatedSession.test(value.session_id) ||
        typeof value.gateway_alias !== "string" || !coordinate.test(value.gateway_alias) || !positiveSafeInteger(value.gateway_revision)) {
      throw new Error("invalid hello response");
    }
  } else if (value.kind === "disconnect") {
    exactKeys(value, ["format", "kind", "sequence", "ok"]);
    if (value.ok !== true) throw new Error("invalid disconnect response");
  } else {
    const keys = ["format", "kind", "sequence", "ok", "session_id", "channel_id"];
    if (value.kind === "state") keys.push("open");
    exactKeys(value, keys);
    if (value.ok !== true || typeof value.session_id !== "string" || !generatedSession.test(value.session_id) ||
        typeof value.channel_id !== "string" || !generatedChannel.test(value.channel_id) ||
        (value.kind === "state" && typeof value.open !== "boolean")) throw new Error("invalid channel response");
  }
  return value as BridgeResponse;
}

export function canonicalOrigin(value: string): string {
  let parsed: URL;
  try { parsed = new URL(value); } catch { throw new Error("invalid browser origin"); }
  if ((parsed.protocol !== "https:" && parsed.protocol !== "http:") || parsed.username || parsed.password ||
      parsed.pathname !== "/" || parsed.search || parsed.hash || parsed.origin !== value) throw new Error("invalid browser origin");
  if (parsed.protocol === "http:" && parsed.hostname !== "localhost" && parsed.hostname !== "127.0.0.1" && parsed.hostname !== "[::1]") {
    throw new Error("cleartext browser origin is not loopback");
  }
  return parsed.origin;
}

function requireSequence(value: number): void {
  if (!positiveSafeInteger(value)) throw new Error("invalid browser bridge sequence");
}

function positiveSafeInteger(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value > 0;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function exactKeys(value: Record<string, unknown>, allowed: string[]): void {
  const set = new Set(allowed);
  if (Object.keys(value).some((key) => !set.has(key))) throw new Error("unknown browser bridge field");
  if (Object.keys(value).length !== allowed.length || allowed.some((key) => !(key in value))) throw new Error("missing browser bridge field");
}

function encodeBase64URL(value: Uint8Array): string {
  return Buffer.from(value).toString("base64url");
}

function decodeBase64URL(value: string, limit: number): Uint8Array {
  if (!/^[A-Za-z0-9_-]+$/u.test(value)) throw new Error("invalid base64url");
  const decoded = Buffer.from(value, "base64url");
  if (decoded.byteLength === 0 || decoded.byteLength > limit || decoded.toString("base64url") !== value) throw new Error("invalid base64url payload");
  return decoded;
}
