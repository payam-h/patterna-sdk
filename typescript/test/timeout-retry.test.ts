import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, ErrorCode, NetworkError, TimeoutError } from "../src/index.js";
import { client, event, mockFetch, slate } from "./helpers.js";

const req = { surface: "home_articles", mode: "cohort" as const, user_id: "u_1", context: { locale: "nl" } };

afterEach(() => {
  vi.useRealTimers();
});

describe("timeouts", () => {
  it("recommend gives up after 150 ms by default", async () => {
    vi.useFakeTimers();
    const m = mockFetch({ hang: true });
    const p = client(m.fetch).recommend(req).catch((e: unknown) => e);
    await vi.advanceTimersByTimeAsync(149);
    expect(m.calls).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(1);
    const err = await p;
    expect(err).toBeInstanceOf(TimeoutError);
    expect(err).toMatchObject({ code: ErrorCode.Timeout, op: "recommend" });
    expect(m.calls).toHaveLength(1);
  });

  it("ingest calls get 5 s by default and timeouts are not retried", async () => {
    vi.useFakeTimers();
    const m = mockFetch({ hang: true });
    const p = client(m.fetch).ingestEvents([event(1)]).catch((e: unknown) => e);
    await vi.advanceTimersByTimeAsync(4_999);
    await vi.advanceTimersByTimeAsync(1);
    expect(await p).toBeInstanceOf(TimeoutError);
    expect(m.calls).toHaveLength(1);
  });

  it("per-call and per-client budgets override the defaults", async () => {
    vi.useFakeTimers();
    const m = mockFetch({ hang: true });
    const c = client(m.fetch, { timeouts: { recommendMs: 80 } });
    const p1 = c.recommend(req).catch((e: unknown) => e);
    await vi.advanceTimersByTimeAsync(80);
    expect(await p1).toBeInstanceOf(TimeoutError);
    const p2 = c.recommend(req, { timeoutMs: 30 }).catch((e: unknown) => e);
    await vi.advanceTimersByTimeAsync(30);
    expect(await p2).toBeInstanceOf(TimeoutError);
  });

  it("a caller abort is reported as aborted, not as a timeout", async () => {
    const m = mockFetch({ hang: true });
    const ac = new AbortController();
    const p = client(m.fetch).recommend(req, { signal: ac.signal }).catch((e: unknown) => e);
    ac.abort();
    expect(await p).toMatchObject({ code: ErrorCode.Aborted });
  });
});

describe("retries", () => {
  it("retries an idempotent call once on 5xx", async () => {
    const m = mockFetch({ status: 503, body: { code: "unavailable", message: "x", request_id: "r1" } }, { status: 202, body: { accepted: 1 } });
    const res = await client(m.fetch).ingestEvents([event(1)]);
    expect(res).toEqual({ accepted: 1 });
    expect(m.calls).toHaveLength(2);
  });

  it("retries an idempotent call once on a network error", async () => {
    const m = mockFetch({ networkError: "connection reset" }, { status: 202 });
    await client(m.fetch).reportInteractions("req_1", [
      { item_id: "d", position: 0, type: "seen", occurred_at: "2026-10-13T10:00:00Z" },
    ]);
    expect(m.calls).toHaveLength(2);
  });

  it("gives up after one retry and surfaces the last error with its request id", async () => {
    const m = mockFetch(
      { status: 500, body: { code: "internal", message: "boom", request_id: "r1" } },
      { status: 502, body: { code: "bad_gateway", message: "boom", request_id: "r2" } },
    );
    const err = await client(m.fetch).upsertItems([]).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ code: "bad_gateway", status: 502, requestId: "r2" });
    expect(m.calls).toHaveLength(2);
  });

  it("never retries recommend: a resend would log a second impression", async () => {
    const m = mockFetch({ status: 503, body: { code: "unavailable", message: "x", request_id: "r1" } }, { status: 200, body: slate });
    await expect(client(m.fetch).recommend(req)).rejects.toBeInstanceOf(ApiError);
    const n = mockFetch({ networkError: "offline" }, { status: 200, body: slate });
    await expect(client(n.fetch).recommend(req)).rejects.toBeInstanceOf(NetworkError);
    expect(m.calls).toHaveLength(1);
    expect(n.calls).toHaveLength(1);
  });

  it("does not retry 4xx", async () => {
    const m = mockFetch({ status: 400, body: { code: "bad_request", message: "x", request_id: "r1" } }, { status: 200 });
    await expect(client(m.fetch).setUserMode("u_1", "cohort")).rejects.toMatchObject({ code: "bad_request" });
    expect(m.calls).toHaveLength(1);
  });

  it("retries every idempotent method", async () => {
    const ops: [string, (c: ReturnType<typeof client>) => Promise<unknown>][] = [
      ["reportInteractions", (c) => c.reportInteractions("r", [{ item_id: "d", position: 0, type: "seen", occurred_at: "2026-10-13T10:00:00Z" }])],
      ["ingestEvents", (c) => c.ingestEvents([event(1)])],
      ["upsertItems", (c) => c.upsertItems([])],
      ["setItemStatus", (c) => c.setItemStatus("document", "d", { status: "archived" })],
      ["setItemTags", (c) => c.setItemTags("document", "d", [])],
      ["setUserMode", (c) => c.setUserMode("u_1", "cohort")],
      ["setUserProfile", (c) => c.setUserProfile("u_1", { premium: false })],
      ["deleteUser", (c) => c.deleteUser("u_1")],
    ];
    for (const [name, call] of ops) {
      const m = mockFetch({ status: 500 }, { status: 200, body: {} });
      await call(client(m.fetch));
      expect(m.calls, name).toHaveLength(2);
    }
  });
});
