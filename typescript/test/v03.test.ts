import { describe, expect, it } from "vitest";
import {
  ApiErrorCode,
  ConsentError,
  EVENT_COACH_PRODUCT_SHOWN,
  EVENT_COACH_PRODUCT_TAPPED,
  PatternaClient,
  ValidationError,
  coachProductShown,
  coachProductTapped,
  isApiError,
  isCallerBug,
  markFills,
  validateSurfaceSpec,
  withFallback,
  type CoachProduct,
  type Slate,
  type SurfaceSpec,
} from "../src/index.js";
import { BASE, KEYS, client, mockFetch, slate } from "./helpers.js";

const req = { surface: "home_articles", mode: "cohort" as const, user_id: "u_1", context: { locale: "nl" } };
const flush = () => new Promise((r) => setTimeout(r, 0));

interface FakeSurface {
  key: string;
  status: string;
  item_kinds: string[];
  slots: number;
  fallback: Record<string, unknown>;
  min_slots: number;
  device_resort: boolean;
  notes: string;
  testers: string[];
  active_flow_version: number | null;
  /** the fake's catalog for the surface; not part of the answer */
  items: string[];
}

/**
 * An in-memory engine behind a fetch: recommend honours min_slots (shown=false with no items),
 * control_item_ids become the control slate, abandoning a not-shown slate is 409 not_shown, and
 * PATCH /surfaces answers {key, ok} like the real engine.
 */
function fakeEngine() {
  const surfaces = new Map<string, FakeSurface>();
  const served = new Map<string, boolean>();
  const abandoned = new Map<string, string>();
  const settings: Record<string, unknown> = { slug: "fake", name: "Fake", phase_tags: false };
  const calls: { method: string; path: string; body: Record<string, unknown> | undefined }[] = [];
  surfaces.set("home_articles", {
    key: "home_articles",
    status: "shadow",
    item_kinds: ["document"],
    slots: 5,
    fallback: { kind: "newest" },
    min_slots: 0,
    device_resort: false,
    notes: "",
    testers: [],
    active_flow_version: null,
    items: [],
  });
  const json = (status: number, body: unknown) => Promise.resolve(new Response(JSON.stringify(body), { status }));
  const fail = (status: number, code: string) => json(status, { code, message: code, request_id: "req_fake" });
  let n = 0;
  const fetch = (url: string, init: RequestInit): Promise<Response> => {
    const u = new URL(url);
    const path = u.pathname.replace(/^\/v1/, "") + u.search;
    const method = init.method ?? "GET";
    const body = typeof init.body === "string" ? (JSON.parse(init.body) as Record<string, unknown>) : undefined;
    calls.push({ method, path, body });
    const parts = u.pathname.replace(/^\/v1\//, "").split("/");
    const auth = (init.headers as Record<string, string>).Authorization ?? (init.headers as Record<string, string>).authorization;
    if (parts[0] === "recommendations") {
      if (auth !== `Bearer ${KEYS.serve}`) return fail(401, "unauthorized");
      if (method === "POST" && parts.length === 1) {
        const r = body as { surface: string; slots?: number; context: { control_item_ids?: string[] }; exclude_item_ids?: string[] };
        const sf = surfaces.get(r.surface);
        if (!sf) return fail(404, "unknown_surface");
        const rid = (init.headers as Record<string, string>)["X-Request-Id"] ?? `req_${++n}`;
        const eligible = sf.items.filter((id) => !(r.exclude_item_ids ?? []).includes(id));
        const shown = sf.min_slots <= 0 || eligible.length >= sf.min_slots;
        served.set(rid, shown);
        const out: Record<string, unknown> = { request_id: rid, surface: sf.key, arm: "treatment", policy_version: "fake", shown, items: [] };
        if (!shown) return json(200, out);
        let ids = eligible;
        if (r.context.control_item_ids?.length) {
          ids = r.context.control_item_ids;
          Object.assign(out, { arm: "control", policy_version: "editorial", control_source: "request" });
        }
        out.items = ids.slice(0, r.slots ?? sf.slots).map((id, i) => ({ item_id: id, kind: "document", position: i, reason_key: "popular", reason: { nl: "Populair" } }));
        return json(200, out);
      }
      if (method === "POST" && parts[2] === "abandon") {
        const shown = served.get(parts[1]!);
        if (shown === undefined) return fail(404, "unknown_request");
        if (!shown) return fail(409, "not_shown");
        abandoned.set(parts[1]!, String(body?.reason));
        return json(200, { request_id: parts[1], fallback: true, client_abandoned: true });
      }
      return fail(404, "not_found");
    }
    if (u.pathname === "/v1/health") return json(200, { status: "ok", postgres: "ok", version: "fake", engine_version: "0.2.0" });
    if (auth !== "Bearer pk_a_test") return fail(401, "unauthorized");
    if (method === "GET" && u.pathname === "/v1/surfaces") {
      return json(
        200,
        [...surfaces.values()].map((s) => {
          const { items, ...rest } = s;
          void items;
          return rest;
        }),
      );
    }
    if (method === "PATCH" && parts[0] === "surfaces" && parts.length === 2) {
      const sf = surfaces.get(parts[1]!);
      if (!sf) return fail(404, "unknown_surface");
      Object.assign(sf, body);
      return json(200, { key: sf.key, ok: true });
    }
    if (method === "GET" && parts[0] === "surfaces" && parts[2] === "metrics") {
      if (!surfaces.has(parts[1]!)) return fail(404, "unknown_surface");
      const block = u.searchParams.get("block");
      return json(200, {
        days: [
          {
            date: "2026-10-12",
            impressions: 10,
            taps: 1,
            tap_rate: 0.1,
            fallback_rate: 0.2,
            client_abandoned_rate: 0.1,
            not_shown_share: 0.25,
            unmeasured_share: null,
            ...(block ? { block } : {}),
          },
        ],
      });
    }
    if (u.pathname === "/v1/settings") {
      if (method === "PATCH") {
        Object.assign(settings, body);
        return json(200, { ok: true });
      }
      return json(200, settings);
    }
    if (u.pathname === "/v1/proof/ledger") {
      const row = (block?: string) => ({ surface: "home_articles", metric: "tap_rate", control_rate: 0.1, treatment_rate: 0.13, readable_sample: 900, ...(block ? { block } : {}) });
      const by = u.searchParams.get("by");
      const block = u.searchParams.get("block");
      if (by === "block") return json(200, [row("top"), row("end_carousel")]);
      return json(200, [row(block ?? undefined)]);
    }
    return fail(404, "not_found");
  };
  return { fetch, surfaces, served, abandoned, settings, calls };
}

function fakeClient(e: ReturnType<typeof fakeEngine>, extra: Record<string, unknown> = {}) {
  return new PatternaClient({ baseUrl: BASE, keys: { ...KEYS, admin: "pk_a_test" }, fetch: e.fetch, retryDelayMs: 0, ...extra });
}

const abandonCalls = (e: ReturnType<typeof fakeEngine>) => e.calls.filter((c) => c.path.endsWith("/abandon"));

describe("shown=false (engine 0.3)", () => {
  it("is handed to the app as hide-the-block: no fallback, never abandoned", async () => {
    const e = fakeEngine();
    Object.assign(e.surfaces.get("home_articles")!, { items: ["doc_1", "doc_2"], min_slots: 3 });
    const c = fakeClient(e);
    let called = false;
    const r = await c.recommendWithFallback(req, () => {
      called = true;
      return ["mine"];
    });
    expect(r.source).toBe("patterna");
    expect(r.shown).toBe(false);
    expect(called).toBe(false);
    if (r.source !== "patterna") return;
    expect(r.value.items).toEqual([]);
    // an app that abandons anyway is stopped before anything is sent
    await expect(c.abandon(r.requestId)).rejects.toMatchObject({ code: ApiErrorCode.NotShown, requestId: r.requestId });
    await flush();
    expect(abandonCalls(e)).toEqual([]);
    expect(e.abandoned.size).toBe(0);
  });

  it("from another process: the engine's 409 not_shown is typed", async () => {
    const e = fakeEngine();
    e.surfaces.get("home_articles")!.min_slots = 1;
    const c = fakeClient(e, { slateMemory: 0 });
    const s = await c.recommend(req);
    expect(s.shown).toBe(false);
    const err = await c.abandon(s.request_id).catch((x: unknown) => x);
    expect(isApiError(err, ApiErrorCode.NotShown)).toBe(true);
    expect(err).toMatchObject({ status: 409 });
  });

  it("a shown slate can still be abandoned, and the fallback is shown", async () => {
    const e = fakeEngine();
    Object.assign(e.surfaces.get("home_articles")!, { items: ["doc_1", "doc_2", "doc_3"], min_slots: 3 });
    const c = fakeClient(e);
    const r = await c.recommendWithFallback(req, () => ["mine"]);
    expect(r).toMatchObject({ source: "patterna", shown: true });
    if (r.source !== "patterna") return;
    expect(r.value.items.map((i) => i.item_id)).toEqual(["doc_1", "doc_2", "doc_3"]);
    await c.abandon(r.requestId);
    expect(e.abandoned.get(r.requestId)).toBe("client_fallback");

    const down = client(mockFetch({ status: 503, body: { code: "unavailable" } }).fetch);
    const f = await down.recommendWithFallback(req, () => ["mine"]);
    expect(f).toMatchObject({ source: "fallback", shown: true, value: ["mine"] });
    // an older engine without `shown` means shown
    const old = await client(mockFetch({ status: 200, body: slate }).fetch).recommendWithFallback(req, () => []);
    expect(old.shown).toBe(true);
  });

  it("recommendMany and withFallback carry shown", async () => {
    const e = fakeEngine();
    e.surfaces.get("home_articles")!.min_slots = 2;
    const c = fakeClient(e);
    const [one] = await c.recommendMany([{ request: req, fallback: () => ["x"] }]);
    expect(one).toMatchObject({ source: "patterna", shown: false });
    const w = await withFallback(() => c.recommend(req), () => ["x"]);
    expect(w).toMatchObject({ source: "patterna", shown: false });
    await flush();
    expect(abandonCalls(e)).toEqual([]);
  });
});

describe("request context (engine 0.3)", () => {
  it("sends control_item_ids and member_of, within the limits", async () => {
    const m = mockFetch({ status: 200, body: slate });
    const c = client(m.fetch);
    await c.recommend({
      surface: "pairs_with",
      mode: "non_profiled",
      session_id: "s_1",
      context: { locale: "nl", anchor_item_id: "doc_9", control_item_ids: ["p_1", "p_2"], member_of: ["c_1"] },
    });
    expect((m.calls[0]!.body as { context: unknown }).context).toMatchObject({ control_item_ids: ["p_1", "p_2"], member_of: ["c_1"] });
    await c.recommend(req);
    expect((m.calls[1]!.body as { context: Record<string, unknown> }).context).not.toHaveProperty("control_item_ids");
    await expect(c.recommend({ ...req, context: { locale: "nl", control_item_ids: Array(51).fill("x") } })).rejects.toBeInstanceOf(ValidationError);
    await expect(c.recommend({ ...req, context: { locale: "nl", member_of: Array(501).fill("x") } })).rejects.toBeInstanceOf(ValidationError);
    expect(m.calls).toHaveLength(2);
  });

  it("control_item_ids become the control slate with control_source request", async () => {
    const e = fakeEngine();
    e.surfaces.get("home_articles")!.items = ["doc_1"];
    const s = await fakeClient(e).recommend({ ...req, context: { locale: "nl", control_item_ids: ["ed_1", "ed_2"] } });
    expect(s).toMatchObject({ arm: "control", control_source: "request" });
    expect(s.items.map((i) => i.item_id)).toEqual(["ed_1", "ed_2"]);
  });
});

describe("slate fields (engine 0.3)", () => {
  const item = (id: string, extra: Record<string, unknown> = {}) => ({ item_id: id, kind: "document", position: 0, reason_key: "new", reason: {}, ...extra });

  it("decodes fill_count, model_versions and marks the filled slots", async () => {
    const body = { ...slate, fill_count: 2, model_versions: { persona_bayes: "pb-3", gbdt: "g-7" }, items: [item("a"), item("b"), item("c")] };
    const s = await client(mockFetch({ status: 200, body }).fetch).recommend(req);
    expect(s.fill_count).toBe(2);
    expect(s.model_versions).toEqual({ persona_bayes: "pb-3", gbdt: "g-7" });
    expect(s.items.map((i) => i.fill ?? false)).toEqual([false, true, true]);
    expect(s.control_source).toBeUndefined();
  });

  it("an engine's own fill wins over the count; a large count never throws", () => {
    const s = markFills({ ...slate, fill_count: 1, control_source: "by_anchor", items: [item("a", { fill: true }), item("b")] } as Slate);
    expect(s.items.map((i) => i.fill ?? false)).toEqual([true, false]);
    expect(s.control_source).toBe("by_anchor");
    const big = markFills({ ...slate, fill_count: 5, items: [item("a")] } as Slate);
    expect(big.items[0]!.fill).toBe(true);
  });
});

describe("errors (engine 0.3)", () => {
  it("mode_required is a typed caller bug", async () => {
    const c = client(mockFetch({ status: 422, body: { code: "mode_required", message: "set the mode first" } }).fetch, {
      keys: { ...KEYS },
    });
    const err = await c.setUserProfile("u_1", { goals: ["sleep"] }).catch((x: unknown) => x);
    expect(isApiError(err, ApiErrorCode.ModeRequired)).toBe(true);
    expect(isApiError(err, ApiErrorCode.ModeConflict)).toBe(false);
    expect(isCallerBug(err)).toBe(true);
  });

  it("not_shown from the engine is typed", async () => {
    const c = client(mockFetch({ status: 409, body: { code: "not_shown", message: "hide the block" } }).fetch);
    const err = await c.abandon("req_x", "timeout").catch((x: unknown) => x);
    expect(isApiError(err, ApiErrorCode.NotShown)).toBe(true);
    expect(isApiError(err, ApiErrorCode.SurfaceOff)).toBe(false);
  });
});

describe("coach product events (engine 0.3)", () => {
  const base: CoachProduct = { mode: "personalized", userId: "u_7f3a", productId: "p_mag", conversationId: "conv_1", position: 0 };

  it("builds both events with the identity the mode allows", async () => {
    const shown = coachProductShown({ ...base, occurredAt: "2026-10-13T09:00:00Z", eventId: "e1" });
    expect(shown).toEqual({
      event_id: "e1",
      type: EVENT_COACH_PRODUCT_SHOWN,
      occurred_at: "2026-10-13T09:00:00Z",
      source: "backend",
      user_id: "u_7f3a",
      props: { product_id: "p_mag", conversation_id: "conv_1", position: 0 },
    });
    const tapped = coachProductTapped({ mode: "non_profiled", sessionId: "s_1", productId: "p_mag", conversationId: "conv_1", position: 2 });
    expect(tapped.type).toBe(EVENT_COACH_PRODUCT_TAPPED);
    expect(tapped.session_id).toBe("s_1");
    expect(tapped).not.toHaveProperty("user_id");
    expect(tapped.event_id).toMatch(/^[0-9a-f-]{36}$/);
    expect(Date.parse(tapped.occurred_at)).not.toBeNaN();

    const m = mockFetch({ status: 202, body: { accepted: 2, duplicates: 0, rejected: [] } });
    await client(m.fetch).ingestEvents([shown, tapped]);
    expect((m.calls[0]!.body as { events: unknown[] }).events).toHaveLength(2);
  });

  it("refuses broken identities and inputs", () => {
    const cases: [Partial<CoachProduct>, unknown][] = [
      [{ mode: undefined as unknown as "cohort" }, ConsentError],
      [{ mode: "non_profiled", sessionId: "s" }, ConsentError], // user id in non_profiled
      [{ mode: "non_profiled", userId: undefined }, ConsentError], // no session id
      [{ userId: undefined, sessionId: "s" }, ValidationError],
      [{ mode: "cohort", userId: "anna@example.com" }, ValidationError],
      [{ productId: "" }, ValidationError],
      [{ conversationId: " " }, ValidationError],
      [{ position: -1 }, ValidationError],
    ];
    for (const [patch, want] of cases) {
      expect(() => coachProductShown({ ...base, ...patch } as CoachProduct)).toThrow(want as new () => Error);
    }
  });
});

describe("admin (engine 0.3)", () => {
  const spec: SurfaceSpec = {
    surface: "home_articles",
    slots: 7,
    item_kinds: ["document"],
    min_slots: 3,
    device_resort: true,
    fallback: { kind: "curated", item_ids: ["doc_1", "doc_2"], by_anchor: { doc_9: ["p_1", "p_2"] } },
  };

  it("plans and applies a surface spec, patching only what differs", async () => {
    const e = fakeEngine();
    const c = fakeClient(e);
    expect(await c.planSurface(spec)).toEqual({ surface: "home_articles", changes: ["slots", "fallback", "min_slots", "device_resort"], applied: false });
    const plan = await c.applySurface(spec);
    expect(plan.applied).toBe(true);
    const patch = e.calls.filter((x) => x.method === "PATCH");
    expect(patch).toHaveLength(1);
    expect(Object.keys(patch[0]!.body!).sort()).toEqual(["device_resort", "fallback", "min_slots", "slots"]);
    expect(e.surfaces.get("home_articles")).toMatchObject({ slots: 7, min_slots: 3, device_resort: true, fallback: spec.fallback });
    expect(await c.applySurface(spec)).toEqual({ surface: "home_articles", changes: [], applied: false });
    expect((await c.planSurface({ surface: "home_articles", min_slots: 0 })).changes).toEqual(["min_slots"]);
  });

  it("updateSurface reads the surface back (the engine answers {key, ok})", async () => {
    const e = fakeEngine();
    const s = await fakeClient(e).updateSurface("home_articles", { min_slots: 2, device_resort: true });
    expect(s).toMatchObject({ key: "home_articles", min_slots: 2, device_resort: true, status: "shadow" });
    await expect(fakeClient(e).updateSurface("home_articles", {})).rejects.toBeInstanceOf(ValidationError);
    const st = await fakeClient(e).setSurfaceStatus("home_articles", "live");
    expect(st.status).toBe("live");
  });

  it("validates a spec before sending", () => {
    const bad: SurfaceSpec[] = [
      { surface: "" },
      { surface: "s", slots: 0 },
      { surface: "s", min_slots: 51 },
      { surface: "s", min_slots: -1 },
      { surface: "s", item_kinds: ["video"] },
      { surface: "s", fallback: { kind: "random" as "newest" } },
      { surface: "s", fallback: { kind: "curated" } },
      { surface: "s", fallback: { kind: "newest", by_anchor: { doc_1: [] } } },
      { surface: "s", exploration_share: 1.5 },
    ];
    for (const s of bad) expect(() => validateSurfaceSpec(s)).toThrow(ValidationError);
    expect(() => validateSurfaceSpec({ surface: "s", min_slots: 0, fallback: { kind: "newest", by_anchor: { d: ["p"] } } })).not.toThrow();
  });

  it("ledger by block and one block", async () => {
    const e = fakeEngine();
    const c = fakeClient(e);
    const rows = await c.ledgerByBlock({ surface: "home_articles", block: "ignored" });
    expect(rows.map((r) => r.block)).toEqual(["top", "end_carousel"]);
    expect(rows[0]!.readable_sample).toBe(900);
    expect((await c.ledger({ block: "top" }))[0]!.block).toBe("top");
    await expect(c.ledger({ block: "x".repeat(33) })).rejects.toBeInstanceOf(ValidationError);
    expect(e.calls.map((x) => x.path)).toEqual(["/proof/ledger?surface=home_articles&by=block", "/proof/ledger?block=top"]);
  });

  it("surface metrics carry not_shown_share and client_abandoned_rate", async () => {
    const e = fakeEngine();
    const c = fakeClient(e);
    const [d] = await c.surfaceMetrics("home_articles", { days: 7, block: "top" });
    expect(d).toMatchObject({ not_shown_share: 0.25, client_abandoned_rate: 0.1, unmeasured_share: null, block: "top" });
    expect(e.calls[0]!.path).toBe("/surfaces/home_articles/metrics?days=7&block=top");
    await expect(c.surfaceMetrics("nope")).rejects.toMatchObject({ code: "unknown_surface" });
    await expect(c.surfaceMetrics("home_articles", { days: 181 })).rejects.toBeInstanceOf(ValidationError);
  });

  it("phase tags, settings and health", async () => {
    const e = fakeEngine();
    const c = fakeClient(e);
    expect((await c.getSettings()).phase_tags).toBe(false);
    await c.setPhaseTags(true);
    expect(e.settings.phase_tags).toBe(true);
    expect((await c.getSettings()).phase_tags).toBe(true);
    await expect(c.updateSettings({})).rejects.toBeInstanceOf(ValidationError);
    expect(await c.health()).toMatchObject({ status: "ok", engine_version: "0.2.0" });
  });
});
