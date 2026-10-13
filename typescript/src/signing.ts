import type { Event } from "./types.js";

/**
 * HMAC-SHA256 signing for events, so an event that travelled through an untrusted hop (an app,
 * a queue) can still count for learning.
 *
 * Canonical form (shared with sdk-go, see the test vector in both test suites):
 *   - the event object without its `signature` field;
 *   - JSON with object keys sorted (ASCII order), no whitespace, `undefined` members dropped;
 *   - strings escaped as JSON.stringify does, plus U+2028/U+2029 as the six-character escapes Go emits;
 *   - numbers as JSON.stringify prints them (keep them integers for cross-language stability).
 * The signature is the lowercase hex of HMAC-SHA256(utf8(secret), utf8(canonical)): the secret
 * string exactly as the API returned it, never hex-decoded (same in the Go and Python SDKs).
 *
 * Never ship the signing secret in an app bundle. Sign on your backend.
 */

export function canonicalJson(value: unknown): string {
  if (value === null) return "null";
  switch (typeof value) {
    case "string":
      return JSON.stringify(value).replace(/[\u2028\u2029]/g, (c) => (c === "\u2028" ? "\\u2028" : "\\u2029"));
    case "number":
      if (!Number.isFinite(value)) throw new TypeError("canonicalJson: non-finite number");
      return JSON.stringify(value);
    case "boolean":
      return value ? "true" : "false";
    case "object": {
      if (Array.isArray(value)) {
        return `[${value.map((v) => (v === undefined ? "null" : canonicalJson(v))).join(",")}]`;
      }
      const obj = value as Record<string, unknown>;
      const keys = Object.keys(obj)
        .filter((k) => obj[k] !== undefined)
        .sort();
      return `{${keys.map((k) => `${JSON.stringify(k)}:${canonicalJson(obj[k])}`).join(",")}}`;
    }
    default:
      throw new TypeError(`canonicalJson: unsupported type ${typeof value}`);
  }
}

type Subtle = Pick<SubtleCrypto, "importKey" | "sign">;

function subtleOrThrow(subtle?: Subtle): Subtle {
  const s = subtle ?? (globalThis.crypto as Crypto | undefined)?.subtle;
  if (!s) {
    throw new Error(
      "WebCrypto (crypto.subtle) is not available here; pass a SubtleCrypto implementation. Sign on the backend, not in an app.",
    );
  }
  return s;
}

async function hmacHex(secret: string, message: string, subtle?: Subtle): Promise<string> {
  const s = subtleOrThrow(subtle);
  const enc = new TextEncoder();
  const key = await s.importKey("raw", enc.encode(secret), { name: "HMAC", hash: "SHA-256" }, false, ["sign"]);
  const mac = new Uint8Array(await s.sign("HMAC", key, enc.encode(message)));
  let hex = "";
  for (const b of mac) hex += b.toString(16).padStart(2, "0");
  return hex;
}

/** Hex HMAC-SHA256 of the event's canonical form. */
export async function computeEventSignature(event: Event, secret: string, subtle?: Subtle): Promise<string> {
  if (typeof secret !== "string" || !secret) {
    throw new TypeError("computeEventSignature: secret must be a non-empty string (as returned, not hex-decoded)");
  }
  const { signature: _ignored, ...unsigned } = event;
  void _ignored;
  return hmacHex(secret, canonicalJson(unsigned), subtle);
}

/**
 * The value for `assignment_unit` (engine 0.2): lowercase hex HMAC-SHA256 of `userId` under your
 * per-experiment `secret` (UTF-8 bytes of both). Keeps one person in one holdout arm across
 * rotating sessions; the secret never leaves your backend. Same function and test vector in the
 * Go and Python SDKs.
 */
export async function assignmentUnit(secret: string, userId: string, subtle?: Subtle): Promise<string> {
  if (!secret) throw new TypeError("assignmentUnit: secret is empty");
  return hmacHex(secret, userId, subtle);
}

/** Returns a copy of the event with `signature` set. */
export async function signEvent(event: Event, secret: string, subtle?: Subtle): Promise<Event> {
  return { ...event, signature: await computeEventSignature(event, secret, subtle) };
}
