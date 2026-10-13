import { createHmac, webcrypto } from "node:crypto";
import { describe, expect, it } from "vitest";
import { canonicalJson, computeEventSignature, signEvent, type Event } from "../src/index.js";

// Built from parts so no tool or editor turns the escape into the raw character.
const LS = String.fromCharCode(0x2028);
const LS_ESCAPED = "\\" + "u2028";
const KEY = "test-signing-key"; // a fixture string, not a secret

// Shared with packages/sdk-go/signing_test.go: both SDKs must produce this exact signature.
const VECTOR_EVENT: Event = {
  type: "rec_click",
  event_id: "ev_42",
  occurred_at: "2026-10-13T09:15:00Z",
  source: "backend",
  user_id: "u_7f3a",
  session_id: "s_abc",
  request_id: "req_1",
  context: { platform: "ios", locale: "nl", hour_bucket: 9 },
  props: { position: 2, item_id: "doc_1", title: `<b>Zon & zee</b>${LS}x` },
};
const VECTOR_SIGNATURE = "8baf729960f0d6390df4232b164a7bffab057e3c8a174148dedd0f324504aac9";

describe("canonicalJson", () => {
  it("sorts keys at every level, drops undefined members and keeps array order", () => {
    expect(canonicalJson({ b: 1, a: { d: [3, 1, { z: true, y: null }], c: undefined } })).toBe(
      '{"a":{"d":[3,1,{"y":null,"z":true}]},"b":1}',
    );
  });

  it("escapes U+2028 like Go and leaves HTML characters alone", () => {
    expect(canonicalJson(`<a&b>${LS}`)).toBe(`"<a&b>${LS_ESCAPED}"`);
  });

  it("refuses values JSON cannot carry", () => {
    expect(() => canonicalJson(Number.NaN)).toThrow(TypeError);
    expect(() => canonicalJson(() => 1)).toThrow(TypeError);
  });
});

describe("event signing", () => {
  it("matches an HMAC computed independently with node:crypto", async () => {
    const canonical = canonicalJson(VECTOR_EVENT);
    const expected = createHmac("sha256", KEY).update(canonical).digest("hex");
    expect(await computeEventSignature(VECTOR_EVENT, KEY)).toBe(expected);
  });

  it("matches the cross-language test vector", async () => {
    expect(await computeEventSignature(VECTOR_EVENT, KEY)).toBe(VECTOR_SIGNATURE);
  });

  it("ignores an existing signature and returns a signed copy", async () => {
    const signed = await signEvent({ ...VECTOR_EVENT, signature: "stale" }, KEY);
    expect(signed.signature).toBe(VECTOR_SIGNATURE);
    expect(VECTOR_EVENT.signature).toBeUndefined();
  });

  it("accepts an injected SubtleCrypto (React Native polyfills)", async () => {
    const sig = await computeEventSignature(VECTOR_EVENT, KEY, webcrypto.subtle as unknown as SubtleCrypto);
    expect(sig).toBe(VECTOR_SIGNATURE);
  });

  it("refuses an empty secret", async () => {
    await expect(computeEventSignature(VECTOR_EVENT, "")).rejects.toThrow(TypeError);
  });
});
