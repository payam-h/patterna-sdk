import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, TimeoutError, withFallback } from "../src/index.js";
import { client, mockFetch, slate } from "./helpers.js";

const req = { surface: "home_articles", mode: "cohort" as const, user_id: "u_1", context: { locale: "nl" } };
const mine = () => ["newest_1", "newest_2"];

afterEach(() => {
  vi.useRealTimers();
});

describe("withFallback", () => {
  it("returns the slate when Patterna answers", async () => {
    const m = mockFetch({ status: 200, body: slate });
    const r = await withFallback(() => client(m.fetch).recommend(req), mine);
    expect(r).toEqual({ source: "patterna", value: slate });
  });

  it("returns the customer's own list when the 150 ms budget runs out", async () => {
    vi.useFakeTimers();
    const m = mockFetch({ hang: true });
    const onFallback = vi.fn();
    const p = withFallback(() => client(m.fetch).recommend(req), mine, { onFallback });
    await vi.advanceTimersByTimeAsync(150);
    const r = await p;
    expect(r.source).toBe("fallback");
    expect(r.value).toEqual(["newest_1", "newest_2"]);
    expect(r.source === "fallback" && r.error).toBeInstanceOf(TimeoutError);
    expect(onFallback).toHaveBeenCalledOnce();
  });

  it("falls back on API errors by default so a screen never goes blank", async () => {
    const m = mockFetch({ status: 503, body: { code: "unavailable", message: "x", request_id: "r" } });
    const r = await withFallback(() => client(m.fetch).recommend(req), mine);
    expect(r.source).toBe("fallback");
    expect(r.source === "fallback" && r.error.requestId).toBe("r");
  });

  it("with on: 'timeout' rethrows anything but a timeout", async () => {
    const m = mockFetch({ status: 503, body: { code: "unavailable", message: "x", request_id: "r" } });
    await expect(withFallback(() => client(m.fetch).recommend(req), mine, { on: "timeout" })).rejects.toBeInstanceOf(ApiError);
  });

  it("rethrows errors that did not come from the SDK", async () => {
    await expect(withFallback(() => Promise.reject(new RangeError("bug")), mine)).rejects.toBeInstanceOf(RangeError);
  });

  it("supports an async fallback", async () => {
    const m = mockFetch({ networkError: "offline" });
    const r = await withFallback(() => client(m.fetch).recommend(req), async () => ["from_db"]);
    expect(r).toMatchObject({ source: "fallback", value: ["from_db"] });
  });
});
