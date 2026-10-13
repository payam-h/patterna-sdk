import { describe, expect, it } from "vitest";
import {
  ConsentError,
  ErrorCode,
  ValidationError,
  newSessionId,
  withFallback,
  type RecommendRequest,
} from "../src/index.js";
import { client, mockFetch, slate } from "./helpers.js";

describe("consent gate", () => {
  it("refuses a request without a mode and sends nothing", async () => {
    const m = mockFetch({ status: 200, body: slate });
    const req = { surface: "home_articles", context: { locale: "nl" } } as unknown as RecommendRequest;
    const err = await client(m.fetch).recommend(req).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ConsentError);
    expect(err).toMatchObject({ code: ErrorCode.ConsentRequired });
    expect(m.calls).toHaveLength(0);
  });

  it("refuses an unknown mode", async () => {
    const m = mockFetch({ status: 200, body: slate });
    const req = { surface: "s", mode: "everything", context: { locale: "nl" } } as unknown as RecommendRequest;
    await expect(client(m.fetch).recommend(req)).rejects.toMatchObject({ code: ErrorCode.ConsentRequired });
  });

  it("throws when a non_profiled request carries a user id", async () => {
    const m = mockFetch({ status: 200, body: slate });
    // The type system already forbids this; untyped callers hit the runtime check.
    const req = {
      surface: "home_articles",
      mode: "non_profiled",
      user_id: "u_7f3a",
      session_id: "s_1",
      context: { locale: "nl" },
    } as unknown as RecommendRequest;
    const err = await client(m.fetch).recommend(req).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ConsentError);
    expect(err).toMatchObject({ code: ErrorCode.ConsentViolation });
    expect(m.calls).toHaveLength(0);
  });

  it("requires a session id in non_profiled mode", async () => {
    const m = mockFetch({ status: 200, body: slate });
    const req = { surface: "s", mode: "non_profiled", context: { locale: "nl" } } as unknown as RecommendRequest;
    await expect(client(m.fetch).recommend(req)).rejects.toMatchObject({ code: ErrorCode.ConsentViolation });
  });

  it("sends a non_profiled request with session id and context only", async () => {
    const m = mockFetch({ status: 200, body: slate });
    await client(m.fetch).recommend({
      surface: "home_articles",
      mode: "non_profiled",
      session_id: newSessionId(),
      context: { locale: "nl", hour_bucket: 9 },
    });
    expect(m.calls[0]!.body).not.toHaveProperty("user_id");
    expect((m.calls[0]!.body as { session_id: string }).session_id).toMatch(/^s_[0-9a-f]{32}$/);
  });

  it("is never swallowed by withFallback", async () => {
    const m = mockFetch({ status: 200, body: slate });
    const req = { surface: "s", mode: "non_profiled", user_id: "u_1", session_id: "s", context: { locale: "nl" } };
    await expect(
      withFallback(() => client(m.fetch).recommend(req as unknown as RecommendRequest), () => ["mine"]),
    ).rejects.toBeInstanceOf(ConsentError);
  });
});

describe("no PII in user ids", () => {
  const pii = ["anna@example.com", "auth0|64f1c2", "google-oauth2|1093", "sms|+31612345678"];
  for (const id of pii) {
    it(`refuses ${id.split(/[@|]/)[0]}-shaped ids everywhere and keeps them out of the message`, async () => {
      const m = mockFetch({ status: 200, body: {} });
      const c = client(m.fetch);
      const calls = [
        c.recommend({ surface: "s", mode: "personalized", user_id: id, context: { locale: "nl" } }),
        c.setUserMode(id, "cohort"),
        c.setUserProfile(id, {}),
        c.deleteUser(id),
        c.ingestEvents([{ event_id: "e", type: "item_viewed", occurred_at: "2026-10-13T10:00:00Z", source: "backend", user_id: id }]),
      ];
      for (const p of calls) {
        const err = await p.catch((e: unknown) => e);
        expect(err).toBeInstanceOf(ValidationError);
        expect((err as Error).message).not.toContain(id);
      }
      expect(m.calls).toHaveLength(0);
    });
  }
});
