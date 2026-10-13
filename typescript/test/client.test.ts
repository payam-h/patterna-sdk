import { describe, expect, it } from "vitest";
import { ApiError, ErrorCode, PatternaClient, PatternaError, ValidationError } from "../src/index.js";
import { BASE, KEYS, client, event, mockFetch, slate } from "./helpers.js";

describe("serve scope", () => {
  it("recommend posts the request with the serve key and returns the slate", async () => {
    const m = mockFetch({ status: 200, body: slate, headers: { "x-request-id": "req_1" } });
    const res = await client(m.fetch).recommend({
      surface: "home_articles",
      mode: "personalized",
      user_id: "u_7f3a",
      context: { locale: "nl", platform: "ios" },
      slots: 6,
    });
    expect(res).toEqual(slate);
    expect(m.calls).toHaveLength(1);
    const c = m.calls[0]!;
    expect(c.method).toBe("POST");
    expect(c.url).toBe(`${BASE}/recommendations`);
    expect(c.headers.authorization).toBe(`Bearer ${KEYS.serve}`);
    expect(c.headers["content-type"]).toBe("application/json");
    expect(c.body).toMatchObject({ surface: "home_articles", mode: "personalized", user_id: "u_7f3a" });
  });

  it("recommend validates surface, locale and the exclude limit before sending", async () => {
    const m = mockFetch({ status: 200, body: slate });
    const c = client(m.fetch);
    await expect(
      c.recommend({ surface: "", mode: "cohort", user_id: "u_1", context: { locale: "nl" } }),
    ).rejects.toBeInstanceOf(ValidationError);
    await expect(
      c.recommend({ surface: "s", mode: "cohort", user_id: "u_1", context: { locale: "" } }),
    ).rejects.toBeInstanceOf(ValidationError);
    await expect(
      c.recommend({
        surface: "s",
        mode: "cohort",
        user_id: "u_1",
        context: { locale: "nl" },
        exclude_item_ids: Array.from({ length: 201 }, (_, i) => `d${i}`),
      }),
    ).rejects.toMatchObject({ code: ErrorCode.InvalidArgument });
    expect(m.calls).toHaveLength(0);
  });

  it("reportInteractions posts request_id and interactions", async () => {
    const m = mockFetch({ status: 202 });
    const interactions = [
      { item_id: "doc_1", position: 0, type: "seen" as const, occurred_at: "2026-10-13T10:00:00Z" },
      { item_id: "doc_1", position: 0, type: "click" as const, occurred_at: "2026-10-13T10:00:03Z" },
    ];
    await client(m.fetch).reportInteractions("req_1", interactions);
    expect(m.calls[0]).toMatchObject({
      method: "POST",
      url: `${BASE}/interactions`,
      body: { request_id: "req_1", interactions },
    });
    expect(m.calls[0]!.headers.authorization).toBe(`Bearer ${KEYS.serve}`);
  });

  it("reportInteractions skips empty lists and refuses more than 50", async () => {
    const m = mockFetch({ status: 202 });
    const c = client(m.fetch);
    await c.reportInteractions("req_1", []);
    const many = Array.from({ length: 51 }, (_, i) => ({
      item_id: `d${i}`,
      position: i,
      type: "seen" as const,
      occurred_at: "2026-10-13T10:00:00Z",
    }));
    await expect(c.reportInteractions("req_1", many)).rejects.toBeInstanceOf(ValidationError);
    await expect(c.reportInteractions("", many.slice(0, 1))).rejects.toBeInstanceOf(ValidationError);
    expect(m.calls).toHaveLength(0);
  });
});

describe("ingest scope", () => {
  it("ingestEvents posts the batch with the ingest key and returns counts", async () => {
    const m = mockFetch({ status: 202, body: { accepted: 2, duplicates: 0, rejected: [] } });
    const res = await client(m.fetch).ingestEvents([event(1), event(2)]);
    expect(res).toEqual({ accepted: 2, duplicates: 0, rejected: [] });
    expect(m.calls[0]).toMatchObject({ method: "POST", url: `${BASE}/events` });
    expect(m.calls[0]!.headers.authorization).toBe(`Bearer ${KEYS.ingest}`);
    expect((m.calls[0]!.body as { events: unknown[] }).events).toHaveLength(2);
  });

  it("ingestEvents refuses oversize batches, context keys outside the contract and PII-shaped ids", async () => {
    const m = mockFetch({ status: 202, body: {} });
    const c = client(m.fetch);
    await expect(c.ingestEvents(Array.from({ length: 501 }, (_, i) => event(i)))).rejects.toBeInstanceOf(ValidationError);
    await expect(
      c.ingestEvents([event(1, { context: { locale: "nl", ip: "10.0.0.1" } })]),
    ).rejects.toThrow(/context key "ip"/);
    await expect(c.ingestEvents([event(1, { user_id: "anna@example.com" })])).rejects.toThrow(/email/);
    expect(m.calls).toHaveLength(0);
  });

  it("upsertItems puts the items", async () => {
    const m = mockFetch({ status: 200, body: { upserted: 1, enrichment_queued: 1 } });
    const res = await client(m.fetch).upsertItems([
      { external_id: "doc_1", kind: "document", title: "Magnesium", locale: "nl", collection: "recipes" },
    ]);
    expect(res).toEqual({ upserted: 1, enrichment_queued: 1 });
    expect(m.calls[0]).toMatchObject({ method: "PUT", url: `${BASE}/catalog/items` });
    expect(m.calls[0]!.headers.authorization).toBe(`Bearer ${KEYS.ingest}`);
  });

  it("setItemStatus encodes the id and passes kind as a query parameter", async () => {
    const m = mockFetch({ status: 200 });
    await client(m.fetch).setItemStatus("document", "a/b c", { status: "scheduled", published_at: "2026-11-01T08:00:00Z" });
    expect(m.calls[0]).toMatchObject({
      method: "POST",
      url: `${BASE}/catalog/items/a%2Fb%20c/status?kind=document`,
      body: { status: "scheduled", published_at: "2026-11-01T08:00:00Z" },
    });
  });

  it("setItemTags puts the tags", async () => {
    const m = mockFetch({ status: 200 });
    await client(m.fetch).setItemTags("product", "p_9", [{ tag: "magnesium", weight: 1 }]);
    expect(m.calls[0]).toMatchObject({
      method: "PUT",
      url: `${BASE}/catalog/items/p_9/tags?kind=product`,
      body: { tags: [{ tag: "magnesium", weight: 1 }] },
    });
  });

  it("setUserMode uses the ingest key when no admin key is configured, the admin key otherwise", async () => {
    const m = mockFetch({ status: 200 });
    await client(m.fetch).setUserMode("u_7f3a", "cohort");
    expect(m.calls[0]).toMatchObject({ method: "PUT", url: `${BASE}/users/u_7f3a/mode`, body: { mode: "cohort" } });
    expect(m.calls[0]!.headers.authorization).toBe(`Bearer ${KEYS.ingest}`);

    await client(m.fetch, { keys: { ...KEYS, admin: "pk_admin_test" } }).setUserMode("u_7f3a", "non_profiled");
    expect(m.calls[1]!.headers.authorization).toBe("Bearer pk_admin_test");
  });

  it("setUserProfile puts the profile and refuses a non_profiled profile", async () => {
    const m = mockFetch({ status: 200 });
    const c = client(m.fetch);
    await c.setUserProfile("u_7f3a", { premium: true, goals: ["sleep"], activity_level: "moderate" });
    expect(m.calls[0]).toMatchObject({ method: "PUT", url: `${BASE}/users/u_7f3a/profile`, body: { premium: true } });
    await expect(c.setUserProfile("u_7f3a", { mode: "non_profiled" })).rejects.toBeInstanceOf(ValidationError);
  });

  it("deleteUser sends DELETE and returns the deletion request", async () => {
    const del = { id: "del_1", user_id: "u_7f3a", requested_at: "2026-10-13T10:00:00Z", rows_deleted_at: null };
    const m = mockFetch({ status: 202, body: del });
    const res = await client(m.fetch).deleteUser("u_7f3a");
    expect(res).toEqual(del);
    expect(m.calls[0]).toMatchObject({ method: "DELETE", url: `${BASE}/users/u_7f3a`, body: undefined });
  });
});

describe("errors", () => {
  it("carries the API code, status and request id from the error body", async () => {
    const m = mockFetch({
      status: 422,
      body: { code: "pii_rejected", message: "field email is not allowed", request_id: "req_body" },
      headers: { "x-request-id": "req_header" },
    });
    const err = await client(m.fetch).ingestEvents([event(1)]).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ code: "pii_rejected", status: 422, requestId: "req_body", op: "ingestEvents" });
    expect((err as PatternaError).transient).toBe(false);
  });

  it("falls back to the X-Request-Id header and an http_<status> code when the body is not an Error", async () => {
    const m = mockFetch({ status: 401, body: "nope", headers: { "x-request-id": "req_header" } });
    const err = await client(m.fetch)
      .recommend({ surface: "s", mode: "cohort", user_id: "u_1", context: { locale: "nl" } })
      .catch((e: unknown) => e);
    expect(err).toMatchObject({ code: "http_401", status: 401, requestId: "req_header" });
  });

  it("rejects a 2xx body that is not JSON", async () => {
    const m = mockFetch({ status: 200, body: "<html>" });
    await expect(
      client(m.fetch).recommend({ surface: "s", mode: "cohort", user_id: "u_1", context: { locale: "nl" } }),
    ).rejects.toMatchObject({ code: ErrorCode.BadResponse });
  });

  it("refuses to call without a key for the scope", async () => {
    const m = mockFetch({ status: 200, body: slate });
    const c = new PatternaClient({ baseUrl: BASE, keys: { ingest: "k" }, fetch: m.fetch });
    await expect(
      c.recommend({ surface: "s", mode: "cohort", user_id: "u_1", context: { locale: "nl" } }),
    ).rejects.toMatchObject({ code: ErrorCode.NotConfigured });
    expect(m.calls).toHaveLength(0);
  });

  it("uses a headers() provider instead of keys when talking to the customer's proxy", async () => {
    const m = mockFetch({ status: 200, body: slate });
    const c = new PatternaClient({
      baseUrl: "https://api.elvou.test/patterna",
      fetch: m.fetch,
      headers: async () => ({ authorization: "Bearer user-session-token" }),
    });
    await c.recommend({ surface: "home_articles", mode: "non_profiled", session_id: "s_1", context: { locale: "nl" } });
    expect(m.calls[0]!.url).toBe("https://api.elvou.test/patterna/recommendations");
    expect(m.calls[0]!.headers.authorization).toBe("Bearer user-session-token");
  });
});
