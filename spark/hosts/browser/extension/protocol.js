/* Copyright 2026 Nicolas Cassan. Licensed under Apache-2.0. */

export const BRIDGE_FORMAT = "brique.browser.bridge/1";
export const RELAY_NAME = BRIDGE_FORMAT;
export const NATIVE_HOST = "com.brique.browser";
export const MAX_PAYLOAD_BYTES = 180 << 10;
export const MAX_DESCRIPTOR_BYTES = 64 << 10;

const coordinate = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/u;
const uiName = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/u;
const session = /^bbs1_[A-Za-z0-9_-]{32}$/u;
const channel = /^bbc1_[A-Za-z0-9_-]{32}$/u;
const base64url = /^[A-Za-z0-9_-]+$/u;

export function canonicalSender(sender) {
  if (!sender || typeof sender.origin !== "string" || typeof sender.documentId !== "string" ||
      !coordinate.test(sender.documentId) || !Number.isSafeInteger(sender.frameId) || sender.frameId < 0) return null;
  let parsed;
  try { parsed = new URL(sender.origin); } catch { return null; }
  if ((parsed.protocol !== "https:" && parsed.protocol !== "http:") || parsed.username || parsed.password ||
      parsed.pathname !== "/" || parsed.search || parsed.hash || parsed.origin !== sender.origin) return null;
  if (parsed.protocol === "http:" && parsed.hostname !== "localhost" && parsed.hostname !== "127.0.0.1" && parsed.hostname !== "[::1]") return null;
  return { origin: sender.origin, documentId: sender.documentId, frameId: sender.frameId };
}

export function parsePageRequest(value) {
  if (!record(value) || typeof value.kind !== "string") return null;
  if (value.kind === "open" && exactOptional(value, ["kind", "uiName"], ["descriptorB64"]) &&
      typeof value.uiName === "string" && uiName.test(value.uiName) &&
      (value.descriptorB64 === undefined || validBase64(value.descriptorB64, MAX_DESCRIPTOR_BYTES))) {
    return value;
  }
  if (value.kind === "send" && exact(value, ["kind", "requestId", "serialized"]) &&
      positive(value.requestId) && typeof value.serialized === "string" && encodedLength(value.serialized) <= MAX_PAYLOAD_BYTES) return value;
  if (value.kind === "close" && exact(value, ["kind"])) return value;
  return null;
}

export function parseHostMessage(value) {
  if (!record(value) || value.format !== BRIDGE_FORMAT || !positive(value.sequence) || typeof value.kind !== "string") return null;
  if (value.kind === "event" && exact(value, ["format", "kind", "sequence", "session_id", "channel_id", "payload_b64"]) &&
      session.test(value.session_id) && channel.test(value.channel_id) && validBase64(value.payload_b64, MAX_PAYLOAD_BYTES)) return value;
  if (value.kind === "hello" && exact(value, ["format", "kind", "sequence", "ok", "session_id", "gateway_alias", "gateway_revision"]) &&
      value.ok === true && session.test(value.session_id) && coordinate.test(value.gateway_alias) && positive(value.gateway_revision)) return value;
  if (["open", "message", "close"].includes(value.kind) && exact(value, ["format", "kind", "sequence", "ok", "session_id", "channel_id"]) &&
      value.ok === true && session.test(value.session_id) && channel.test(value.channel_id)) return value;
  if (value.kind === "disconnect" && exact(value, ["format", "kind", "sequence", "ok"]) && value.ok === true) return value;
  if (value.kind === "error" && exact(value, ["format", "kind", "sequence", "ok", "error_code"]) && value.ok === false &&
      typeof value.error_code === "string" && /^[a-z][a-z0-9_]{0,63}$/u.test(value.error_code)) return value;
  return null;
}

export function encodeUTF8(value) {
  return bytesToBase64URL(new TextEncoder().encode(value));
}

export function decodeUTF8(value) {
  return new TextDecoder("utf-8", { fatal: true }).decode(base64URLToBytes(value, MAX_PAYLOAD_BYTES));
}

export function validBase64(value, limit) {
  try { base64URLToBytes(value, limit); return true; } catch { return false; }
}

function base64URLToBytes(value, limit) {
  if (typeof value !== "string" || !base64url.test(value)) throw new Error("invalid base64url");
  const standard = value.replace(/-/gu, "+").replace(/_/gu, "/") + "=".repeat((4 - value.length % 4) % 4);
  const binary = atob(standard);
  if (binary.length === 0 || binary.length > limit) throw new Error("invalid base64url size");
  const bytes = Uint8Array.from(binary, (character) => character.charCodeAt(0));
  if (bytesToBase64URL(bytes) !== value) throw new Error("non-canonical base64url");
  return bytes;
}

function bytesToBase64URL(bytes) {
  let binary = "";
  for (let offset = 0; offset < bytes.length; offset += 0x8000) {
    binary += String.fromCharCode(...bytes.subarray(offset, offset + 0x8000));
  }
  return btoa(binary).replace(/\+/gu, "-").replace(/\//gu, "_").replace(/=+$/u, "");
}

function encodedLength(value) { return new TextEncoder().encode(value).byteLength; }
function positive(value) { return Number.isSafeInteger(value) && value > 0; }
function record(value) { return typeof value === "object" && value !== null && !Array.isArray(value); }
function exact(value, keys) {
  const actual = Object.keys(value).sort();
  const expected = [...keys].filter((key) => value[key] !== undefined).sort();
  return actual.length === expected.length && actual.every((key, index) => key === expected[index]);
}
function exactOptional(value, required, optional) {
  const actual = Object.keys(value);
  return required.every((key) => actual.includes(key)) && actual.every((key) => required.includes(key) || optional.includes(key));
}
