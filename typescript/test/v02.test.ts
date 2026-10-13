import { afterEach, describe, expect, it, vi } from "vitest";
import {
  ApiErrorCode,
  ConsentError,
  PatternaClient,
  ValidationError,
  assignmentUnit,
  interactionFor,
  isApiError,
  measuredContext,
  withFallback,
  type Slate,
} from "../src/index.js";
import { BASE, KEYS, client, mockFetch, slate } from "./helpers.js";

// Cross-language vector, shared with packages/sdk-go/v02_test.go and packages/sdk-py/tests/test_v02.py.
const ASSIGNMENT_KEY = "test-assignment-key"; // a fixture string, not a secret
const ASSIGNMENT_VECTOR = "fd548dfa1627af2ab350f8cebe237014d29004eb6dfb8cfa79cb9c24adbdda42";
const UUID_V4 = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

const req = { surface: "home_articles", mode: "cohort" as const, user_id: "u_1", context: { locale: "nl" } };

interface Call {
  url: string;
  method: string;
  headers: Record<string, string>;
  body: unknown;
}

/** A fetch that answers by URL: `route` returns a status and body, or "hang". */
function routedFetch(route: (url: string, body: Record<string, unknown> | undefined) => { status: number; body?: unknown } | "hang") {
  const calls: Call[] = [];
  const fetch = (url: string, init: RequestInit): Promise<Response> => {
    const body = typeof init.body === "string" ? (JSON.parse(init.body) as Record<string, unknown>) : undefined;
    calls.push({ url, method: init.method ?? "GET", headers: { ...(init.headers as Record<string, string>) }, body });
    const r = route(url, body);
    if (r === "hang") {
      return new Promise((_, reject) => {
        init.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")), { once: true });
      });
    }
    const text = r.body === undefined ? null : JSON.stringify(r.body);
    return Promise.resolve(new Response(text, { status: r.status }));
  };
  return { fetch, calls };
}

const flush = () => new Promise((r) => setTimeout(r, 0));

afterEach(() => {
  vi.useRealTimers();
});

describe("assignment unit", () => {
  it("matches the cross-language vector", async () => {
    expect(await assignmentUnit(ASSIGNMENT_KEY, "u_7f3a")).toBe(ASSIGNMENT_VECTOR);
  });

  it("is sent as assignment_unit, and only in the HMAC shape", async () => {
    const m = mockFetch({ status: 200, body: slate });
    const c = client(m.fetch);
    await c.recommend({ ...req, assignment_unit: ASSIGNMENT_VECTOR });
    expect(m.calls[0]!.body).toMatchObject({ assignment_unit: ASSIGNMENT_VECTOR });
    await expect(c.recommend({ ...req, assignment_unit: "u_7f3a" })).rejects.toBeInstanceOf(ValidationError);
    expect(m.calls).toHaveLength(1);
  });
});

describe("headers", () => {
  it("recommend mints a request id, sends the deadline, and fills an empty slate id", async () => {
    const m = mockFetch({ status: 200, body: { ...slate, request_id: "" } });
    const s = await client(m.fetch).recommend(req);
    const h = m.calls[0]!.headers;
    expect(h["x-request-id"]).toMatch(UUID_V4);
    expect(h["x-deadline-ms"]).toBe("120");
    expect(h["x-patterna-tester"]).toBeUndefined();
    expect(h["x-trace-id"]).toBeUndefined();
    expect(s.request_id).toBe(h["x-request-id"]);
  });

  it("per-call request id, trace id and tester; a shorter budget shrinks the deadline", async () => {
    const m = mockFetch({ status: 200, body: slate });
    await client(m.fetch).recommend(req, { requestId: "req_mine", traceId: "trace-1", tester: true, timeoutMs: 80 });
    expect(m.calls[0]!.headers).toMatchObject({
      "x-request-id": "req_mine",
      "x-trace-id": "trace-1",
      "x-patterna-tester": "1",
      "x-deadline-ms": "50",
    });
  });

  it("traceId() on the client goes on every call; ingest calls carry no deadline", async () => {
    const m = mockFetch({ status: 202, body: {} });
    const c = client(m.fetch, { traceId: () => "from-client" });
    await c.deleteItem("community", "c/1");
    expect(m.calls[0]).toMatchObject({ method: "DELETE", url: `${BASE}/catalog/items/c%2F1?kind=community` });
    expect(m.calls[0]!.headers["x-trace-id"]).toBe("from-client");
    expect(m.calls[0]!.headers["x-deadline-ms"]).toBeUndefined();
    expect(m.calls[0]!.headers.authorization).toBe(`Bearer ${KEYS.ingest}`);
  });

  it("drops ids that cannot travel as a header", async () => {
    const m = mockFetch({ status: 200, body: slate });
    await client(m.fetch).recommend(req, { requestId: "has space", traceId: "x".repeat(65) });
    expect(m.calls[0]!.headers["x-request-id"]).toMatch(UUID_V4);
    expect(m.calls[0]!.headers["x-trace-id"]).toBeUndefined();
  });

  it("a deadline at or above the budget is refused", () => {
    expect(() => client(mockFetch().fetch, { timeouts: { recommendMs: 100, recommendDeadlineMs: 100 } })).toThrow(TypeError);
    const c = client(mockFetch().fetch, { timeouts: { recommendMs: 100 } });
    expect(c.serverDeadlineMs(100)).toBe(80);
  });

  it("a timeout error carries the sent request id", async () => {
    vi.useFakeTimers();
    const m = mockFetch({ hang: true });
    const p = client(m.fetch).recommend(req, { requestId: "req_t" });
    const done = expect(p).rejects.toMatchObject({ code: "timeout", requestId: "req_t" });
    await vi.advanceTimersByTimeAsync(150);
    await done;
  });
});

describe("recommendWithFallback and abandonment", () => {
  const cases: { name: string; reply: { status: number; body?: unknown } | "hang"; reason?: string; code: string }[] = [
    { name: "timeout", reply: "hang", reason: "timeout", code: "timeout" },
    { name: "5xx", reply: { status: 503, body: { code: "unavailable" } }, reason: "server_error", code: "unavailable" },
    { name: "no_candidates", reply: { status: 422, body: { code: "no_candidates" } }, code: "no_candidates" },
    { name: "surface_off", reply: { status: 409, body: { code: "surface_off" } }, code: "surface_off" },
    { name: "unknown_surface", reply: { status: 404, body: { code: "unknown_surface" } }, code: "unknown_surface" },
  ];
  for (const tc of cases) {
    it(`${tc.name}: falls back${tc.reason ? " and abandons" : " without abandoning"}`, async () => {
      const m = routedFetch((url) => (url.endsWith("/abandon") ? { status: 202 } : tc.reply));
      const c = client(m.fetch, { timeouts: { recommendMs: 30 } });
      const r = await c.recommendWithFallback(req, () => ["own_1"], { traceId: "tr" });
      await flush();
      expect(r.source).toBe("fallback");
      if (r.source !== "fallback") return;
      expect(r.value).toEqual(["own_1"]);
      expect(r.error.code).toBe(tc.code);
      const sent = m.calls[0]!.headers["x-request-id"];
      expect(r.requestId).toBe(sent);
      const abandons = m.calls.filter((x) => x.url.endsWith("/abandon"));
      if (!tc.reason) {
        expect(abandons).toHaveLength(0);
        expect(r.abandoned).toBe(false);
        return;
      }
      expect(r.abandoned).toBe(true);
      expect(abandons).toHaveLength(1);
      expect(abandons[0]).toMatchObject({ method: "POST", url: `${BASE}/recommendations/${sent}/abandon`, body: { reason: tc.reason } });
      expect(abandons[0]!.headers.authorization).toBe(`Bearer ${KEYS.serve}`);
      expect(abandons[0]!.headers["x-trace-id"]).toBe("tr");
    });
  }

  it("returns the slate and its id when Patterna answers", async () => {
    const m = mockFetch({ status: 200, body: slate });
    const r = await client(m.fetch).recommendWithFallback(req, () => []);
    expect(r).toEqual({ source: "patterna", value: slate, requestId: "req_1" });
  });

  it("rethrows mode_conflict, pii_rejected and consent bugs", async () => {
    for (const code of ["mode_conflict", "pii_rejected"]) {
      const m = mockFetch({ status: 422, body: { code } });
      await expect(client(m.fetch).recommendWithFallback(req, () => [])).rejects.toMatchObject({ code });
      await expect(withFallback(() => client(m.fetch).recommend(req), () => [])).rejects.toMatchObject({ code });
    }
    const bad = { surface: "s", mode: "non_profiled" as const, session_id: "", context: { locale: "nl" } };
    await expect(client(mockFetch().fetch).recommendWithFallback(bad, () => [])).rejects.toBeInstanceOf(ConsentError);
  });

  it("a failed background abandon goes to onBackgroundError", async () => {
    const m = routedFetch((url) => (url.endsWith("/abandon") ? { status: 404, body: { code: "unknown_request" } } : { status: 500 }));
    const onBackgroundError = vi.fn();
    await client(m.fetch, { onBackgroundError }).recommendWithFallback(req, () => []);
    await flush();
    await flush();
    expect(onBackgroundError).toHaveBeenCalledOnce();
    expect(isApiError(onBackgroundError.mock.calls[0]![0], ApiErrorCode.UnknownRequest)).toBe(true);
  });

  it("abandon() reports a slate the app did not show", async () => {
    const m = mockFetch({ status: 202 });
    await client(m.fetch).abandon("req_9");
    expect(m.calls[0]).toMatchObject({ url: `${BASE}/recommendations/req_9/abandon`, body: { reason: "client_fallback" } });
    await expect(client(m.fetch).abandon("")).rejects.toBeInstanceOf(ValidationError);
  });
});

describe("recommendMany", () => {
  it("runs surfaces in parallel under one budget; each falls back on its own", async () => {
    const m = routedFetch((url, body) => {
      if (url.endsWith("/abandon")) return { status: 202 };
      return body?.surface === "home_products" ? "hang" : { status: 200, body: slate };
    });
    const c = client(m.fetch, { timeouts: { recommendMs: 60 } });
    const start = Date.now();
    const [articles, products] = await c.recommendMany(
      [
        { request: req, fallback: () => ["own_a"] },
        { request: { ...req, surface: "home_products" }, fallback: () => ["own_p"] },
      ],
      { tester: true, requestId: "ignored" },
    );
    expect(Date.now() - start).toBeLessThan(200);
    expect(articles!.source).toBe("patterna");
    expect(products).toMatchObject({ source: "fallback", value: ["own_p"], abandoned: true });
    const recs = m.calls.filter((x) => x.url.endsWith("/recommendations"));
    expect(recs.every((x) => x.headers["x-patterna-tester"] === "1")).toBe(true);
    expect(new Set(recs.map((x) => x.headers["x-request-id"])).size).toBe(2);
    expect(recs.some((x) => x.headers["x-request-id"] === "ignored")).toBe(false);
  });
});

describe("interactions", () => {
  const mixed: Slate = {
    request_id: "req_mix",
    surface: "home",
    arm: "none",
    policy_version: "p",
    items: [
      { item_id: "doc_1", kind: "document", position: 0, reason_key: "new", reason: {} },
      { item_id: "com_1", kind: "community", position: 1, reason_key: "new", reason: {} },
    ],
  };
  const at = "2026-10-13T10:00:00Z";

  it("requires kind for non-documents of a slate this client served", async () => {
    const m = mockFetch({ status: 200, body: mixed }, { status: 202 });
    const c = client(m.fetch);
    await c.recommend(req);
    await expect(
      c.reportInteractions("req_mix", [{ item_id: "com_1", position: 1, type: "click", occurred_at: at }]),
    ).rejects.toThrow(/kind is required for community/);
    await c.reportInteractions("req_mix", [
      { item_id: "doc_1", position: 0, type: "seen", occurred_at: at },
      { item_id: "com_1", kind: "community", position: 1, displayed_position: 0, block: "top", type: "click", occurred_at: at },
    ]);
    // Another process's slate is unknown here: the engine decides.
    await c.reportInteractions("req_other", [{ item_id: "com_1", position: 1, type: "seen", occurred_at: at }]);
    expect(m.calls).toHaveLength(3);
    expect(m.calls[1]!.body).toMatchObject({ interactions: [{}, { kind: "community", displayed_position: 0, block: "top" }] });
  });

  it("validates kind values, positions and block length", async () => {
    const c = client(mockFetch({ status: 202 }).fetch);
    const base = { item_id: "d", position: 0, type: "seen" as const, occurred_at: at };
    await expect(c.reportInteractions("r", [{ ...base, kind: "group" }])).rejects.toBeInstanceOf(ValidationError);
    await expect(c.reportInteractions("r", [{ ...base, displayed_position: -1 }])).rejects.toBeInstanceOf(ValidationError);
    await expect(c.reportInteractions("r", [{ ...base, block: "b".repeat(65) }])).rejects.toBeInstanceOf(ValidationError);
    await expect(c.reportInteractions("r", [{ ...base, item_id: "" }])).rejects.toBeInstanceOf(ValidationError);
  });

  it("forgets the oldest slates beyond slateMemory", async () => {
    const m = mockFetch({ status: 200, body: mixed }, { status: 200, body: { ...mixed, request_id: "req_2" } }, { status: 202 });
    const c = client(m.fetch, { slateMemory: 1 });
    await c.recommend(req);
    await c.recommend(req);
    await c.reportInteractions("req_mix", [{ item_id: "com_1", position: 1, type: "seen", occurred_at: at }]);
    await expect(
      c.reportInteractions("req_2", [{ item_id: "com_1", position: 1, type: "seen", occurred_at: at }]),
    ).rejects.toBeInstanceOf(ValidationError);
  });

  it("interactionFor fills kind and the served position", () => {
    expect(interactionFor(mixed, "com_1", "click", { occurred_at: at, block: "end_carousel" })).toEqual({
      item_id: "com_1",
      kind: "community",
      position: 1,
      type: "click",
      occurred_at: at,
      block: "end_carousel",
    });
    expect(interactionFor(mixed, "missing", "click")).toBeUndefined();
  });
});

describe("measured", () => {
  it("measuredContext sets measured from analytics consent", async () => {
    const m = mockFetch({ status: 200, body: slate });
    await client(m.fetch).recommend({ ...req, context: measuredContext({ locale: "nl" }, false) });
    expect(m.calls[0]!.body).toMatchObject({ context: { locale: "nl", measured: false } });
  });
});

describe("catalog", () => {
  it("itemsChangedSince pages with changed_since and the cursor", async () => {
    const m = mockFetch(
      { status: 200, body: { items: [{ external_id: "a" }, { external_id: "b" }], next_cursor: "c2" } },
      { status: 200, body: { items: [{ external_id: "c", updated_at: "2026-10-13T10:00:00Z" }], next_cursor: null } },
    );
    const ids: string[] = [];
    for await (const it of client(m.fetch).itemsChangedSince(new Date("2026-10-01T00:00:00Z"), "document")) {
      ids.push(it.external_id ?? "");
    }
    expect(ids).toEqual(["a", "b", "c"]);
    expect(m.calls[0]!.url).toBe(`${BASE}/catalog/items?kind=document&changed_since=2026-10-01T00%3A00%3A00.000Z&limit=500`);
    expect(m.calls[1]!.url).toContain("cursor=c2");
  });

  it("deleteItem refuses unknown kinds and passes unknown_item through", async () => {
    const m = mockFetch({ status: 404, body: { code: "unknown_item" } });
    const c = client(m.fetch);
    await expect(c.deleteItem("thing" as "document", "x")).rejects.toBeInstanceOf(ValidationError);
    const err = await c.deleteItem("document", "x").catch((e: unknown) => e);
    expect(isApiError(err, ApiErrorCode.UnknownItem)).toBe(true);
  });
});

describe("admin", () => {
  const admin = (fetch: ReturnType<typeof mockFetch>["fetch"]) =>
    new PatternaClient({ baseUrl: BASE, keys: { ...KEYS, admin: "pk_a_test" }, fetch, retryDelayMs: 0 });

  it("createSigningSecret needs the admin key", async () => {
    const m = mockFetch({ status: 201, body: { signing_secret: "abc" } });
    await expect(client(m.fetch).createSigningSecret()).rejects.toMatchObject({ code: "not_configured" });
    expect(await admin(m.fetch).createSigningSecret()).toEqual({ signing_secret: "abc" });
    expect(m.calls[0]).toMatchObject({ method: "POST", url: `${BASE}/settings/signing-secret` });
    expect(m.calls[0]!.headers.authorization).toBe("Bearer pk_a_test");
  });

  const cfgA = { generators: [{ kind: "item_to_item" as const, weight: 1 }], ranker: { kind: "linear" as const }, exploration: { kind: "none" as const } };
  const cfgA2 = { exploration: { kind: "none" as const }, ranker: { kind: "linear" as const }, generators: [{ weight: 1, kind: "item_to_item" as const }] };

  it("applyFlow: unchanged, reuse, create", async () => {
    const surfaces = [{ key: "more_to_read", status: "live", active_flow_version: 1 }];
    const flows = [{ version: 1, config: cfgA, activated_at: "2026-10-01T00:00:00Z" }];
    // Same config in another key order: unchanged, two reads only.
    let m = mockFetch({ status: 200, body: surfaces }, { status: 200, body: flows });
    expect(await admin(m.fetch).applyFlow("more_to_read", cfgA2)).toMatchObject({ unchanged: true, version: 1 });
    expect(m.calls).toHaveLength(2);
    // A new config: create (POST) then activate.
    const cfgB = { ...cfgA, exploration: { kind: "plackett_luce" as const } };
    m = mockFetch({ status: 200, body: surfaces }, { status: 200, body: flows }, { status: 201, body: { version: 2, config: cfgB } }, { status: 200 });
    expect(await admin(m.fetch).applyFlow("more_to_read", cfgB, { note: "ci" })).toEqual({
      surface: "more_to_read",
      version: 2,
      created: true,
      activated: true,
      unchanged: false,
    });
    expect(m.calls[2]).toMatchObject({ method: "POST", url: `${BASE}/surfaces/more_to_read/flows`, body: { config: cfgB, note: "ci" } });
    expect(m.calls[3]).toMatchObject({ method: "POST", url: `${BASE}/surfaces/more_to_read/flows/2/activate`, body: { note: "ci" } });
    // planFlow writes nothing.
    m = mockFetch({ status: 200, body: surfaces }, { status: 200, body: flows });
    expect(await admin(m.fetch).planFlow("more_to_read", cfgB, false)).toMatchObject({ created: true, activated: false, version: 0 });
    expect(m.calls.every((x) => x.method === "GET")).toBe(true);
  });

  it("rollbackFlow activates the previously active version", async () => {
    const m = mockFetch(
      { status: 200, body: [{ key: "s", active_flow_version: 3 }] },
      {
        status: 200,
        body: [
          { version: 1, activated_at: "2026-10-01T00:00:00Z" },
          { version: 2, activated_at: "2026-10-05T00:00:00Z" },
          { version: 3, activated_at: "2026-10-09T00:00:00Z" },
          { version: 4, activated_at: null },
        ],
      },
      { status: 200 },
    );
    expect(await admin(m.fetch).rollbackFlow("s")).toBe(2);
    expect(m.calls[2]!.url).toBe(`${BASE}/surfaces/s/flows/2/activate`);
    const none = mockFetch({ status: 200, body: [{ key: "s", active_flow_version: null }] }, { status: 200, body: [] });
    await expect(admin(none.fetch).rollbackFlow("s")).rejects.toMatchObject({ code: "nothing_to_roll_back" });
  });

  it("surfaces, experiments, ledger and export", async () => {
    // One reply for every call: a list, which the list endpoints need.
    const m = mockFetch({ status: 200, body: [{ key: "s", status: "off" }] });
    const a = admin(m.fetch);
    await a.setSurfaceStatus("s", "off");
    await a.setSurfaceTesters("s", ["u_1"]);
    await expect(a.setSurfaceTesters("s", ["anna@example.com"])).rejects.toBeInstanceOf(ValidationError);
    await expect(a.setSurfaceStatus("s", "on" as "off")).rejects.toBeInstanceOf(ValidationError);
    await a.startExperiment({ surface: "s", name: "h1", primary_metric: "tap_rate" });
    await a.endExperiment("exp 1");
    await a.ledger({ surface: "s", from: "2026-09-01", to: "2026-10-01" });
    await a.ledgerWeeks({ surface: "s" });
    await a.exportLedger({ from: "2026-09-01", to: "2026-10-01" });
    await a.exportStatus("job_1");
    await expect(a.getSurface("missing")).rejects.toMatchObject({ code: "unknown_surface" });
    const notList = admin(mockFetch({ status: 200, body: { key: "s" } }).fetch);
    await expect(notList.listSurfaces()).rejects.toMatchObject({ code: "bad_response" });
    expect(m.calls.map((x) => `${x.method} ${x.url.slice(BASE.length)}`)).toEqual([
      "PATCH /surfaces/s",
      "PATCH /surfaces/s",
      "POST /experiments",
      "POST /experiments/exp%201/end",
      "GET /proof/ledger?surface=s&from=2026-09-01&to=2026-10-01",
      "GET /proof/ledger?surface=s&bucket=week",
      "POST /proof/export",
      "GET /proof/exports/job_1",
      "GET /surfaces",
    ]);
    expect(m.calls[0]!.body).toEqual({ status: "off" });
    expect(m.calls[1]!.body).toEqual({ testers: ["u_1"] });
  });
});
