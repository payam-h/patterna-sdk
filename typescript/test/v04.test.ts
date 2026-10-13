import { describe, expect, it } from "vitest";
import {
  ConsentError,
  GENERATOR_KINDS,
  PatternaClient,
  REASON_KEYS,
  REASON_RECOMMENDED,
  TIER_FREE,
  ValidationError,
  communityPostCreated,
  communityReaction,
  communityReply,
  itemCompleted,
  itemEngaged,
  orderPlaced,
  paywallViewed,
  pushOpened,
  searchPerformed,
  signEvent,
  timeContext,
  validateFlowConfig,
  validateItemAttributes,
  type Event,
  type EventBase,
  type FlowConfig,
  type ItemAttributes,
  type ItemUpsert,
  type RecommendRequest,
} from "../src/index.js";
import { BASE, KEYS, client, event, mockFetch, slate } from "./helpers.js";

const base: RecommendRequest = { surface: "home_articles", mode: "personalized", user_id: "u_7f3a", context: { locale: "nl" } };
const withContext = (ctx: Record<string, unknown>): RecommendRequest =>
  ({ ...base, context: { locale: "nl", ...ctx } }) as RecommendRequest;

describe("request context (round 5)", () => {
  it("sends weekday, subscription_tier, tenure_days, region and session_items under their contract names", async () => {
    const m = mockFetch({ status: 200, body: slate });
    await client(m.fetch).recommend(
      withContext({ hour_bucket: 21, weekday: 6, subscription_tier: TIER_FREE, tenure_days: 0, region: "Noord-Holland", session_items: ["doc_9", "doc_8"] }),
    );
    expect((m.calls[0]!.body as { context: unknown }).context).toEqual({
      locale: "nl",
      hour_bucket: 21,
      weekday: 6,
      subscription_tier: "free",
      tenure_days: 0,
      region: "Noord-Holland",
      session_items: ["doc_9", "doc_8"],
    });
  });

  it.each([
    [{ weekday: -1 }, /weekday/],
    [{ weekday: 7 }, /weekday/],
    [{ weekday: 2.5 }, /weekday/],
    [{ hour_bucket: 24 }, /hour_bucket/],
    [{ tenure_days: -1 }, /tenure_days/],
    [{ tenure_days: 1.5 }, /tenure_days/],
    [{ subscription_tier: "t".repeat(33) }, /subscription_tier/],
    [{ region: "r".repeat(65) }, /region/],
    [{ session_items: Array.from({ length: 21 }, () => "doc") }, /session_items/],
    [{ session_items: [""] }, /session_items/],
    [{ session_items: ["i".repeat(201)] }, /session_items/],
  ])("refuses %j before sending", async (ctx, msg) => {
    const m = mockFetch({ status: 200, body: slate });
    await expect(client(m.fetch).recommend(withContext(ctx))).rejects.toThrow(msg);
    await expect(client(m.fetch).recommend(withContext(ctx))).rejects.toBeInstanceOf(ValidationError);
    expect(m.calls).toHaveLength(0);
  });

  it("accepts the edges", async () => {
    const m = mockFetch({ status: 200, body: slate });
    await client(m.fetch).recommend(
      withContext({ weekday: 0, hour_bucket: 23, tenure_days: 0, subscription_tier: "t".repeat(32), region: "r".repeat(64), session_items: Array.from({ length: 20 }, () => "d") }),
    );
    expect(m.calls).toHaveLength(1);
  });

  it("timeContext derives hour and weekday (0 = Monday) in the user's time zone", () => {
    // Sunday 23:30 UTC is Monday 01:30 in Amsterdam (summer time)
    const at = new Date("2026-10-11T23:30:00Z");
    expect(timeContext(at, "Europe/Amsterdam")).toEqual({ hour_bucket: 1, weekday: 0 });
    expect(timeContext(at, "UTC")).toEqual({ hour_bucket: 23, weekday: 6 });
    // winter time: Saturday 23:10 UTC is Sunday 00:10
    expect(timeContext(new Date("2026-12-05T23:10:00Z"), "Europe/Amsterdam")).toEqual({ hour_bucket: 0, weekday: 6 });
    expect(timeContext(new Date("2026-10-13T09:00:00Z"), "America/New_York")).toEqual({ hour_bucket: 5, weekday: 1 });
    // without a zone: the device clock
    const local = new Date(2026, 9, 14, 7, 15); // a Wednesday, 07:15 local
    expect(timeContext(local)).toEqual({ hour_bucket: 7, weekday: 2 });
    expect(() => timeContext(at, "Mars/Olympus")).toThrow(ValidationError);
    expect(() => timeContext(new Date("nope"))).toThrow(ValidationError);
  });

  it("spreads into a request", async () => {
    const m = mockFetch({ status: 200, body: slate });
    await client(m.fetch).recommend({ ...base, context: { locale: "nl", ...timeContext(new Date("2026-10-11T23:30:00Z"), "Europe/Amsterdam") } });
    expect((m.calls[0]!.body as { context: unknown }).context).toEqual({ locale: "nl", hour_bucket: 1, weekday: 0 });
  });
});

describe("event context (round 5)", () => {
  it("allows the new keys and checks their values", async () => {
    const m = mockFetch({ status: 202, body: { accepted: 1 } });
    const c = client(m.fetch);
    const ctx = { locale: "nl", hour_bucket: 7, weekday: 2, region: "Utrecht", subscription_tier: "premium", tenure_days: 400 };
    await c.ingestEvents([event(1, { context: ctx })]);
    expect((m.calls[0]!.body as { events: Event[] }).events[0]!.context).toEqual(ctx);
    for (const bad of [{ weekday: 7 }, { hour_bucket: -1 }, { tenure_days: -2 }, { subscription_tier: "t".repeat(33) }, { region: "r".repeat(65) }]) {
      await expect(c.ingestEvents([event(2, { context: bad })])).rejects.toBeInstanceOf(ValidationError);
    }
    await expect(c.ingestEvents([event(3, { context: { latitude: 52.1 } })])).rejects.toThrow(/context key "latitude"/);
    expect(m.calls).toHaveLength(1);
  });
});

describe("editorial attributes", () => {
  const full: ItemAttributes = {
    format: "product",
    duration_min: 12.5,
    reading_time_min: 4,
    difficulty: "easy",
    author: "auth_3",
    available: false,
    expires_at: new Date("2026-12-31T22:00:00.000Z"),
    starts_at: "2026-11-01T19:00:00+01:00",
    repurchase_days: 25,
    region: "Utrecht",
    phase_fit: ["follicular"],
    price_cents: 2495,
    compare_at_cents: 2995,
    currency: "EUR",
    image_url: "https://cdn/x.jpg",
    url: "https://shop/x",
    product_type: "supplement",
    categories: ["sleep", "minerals"],
  };

  it("writes every typed attribute into attributes and the region top-level", async () => {
    const m = mockFetch({ status: 200, body: { upserted: 2 } });
    const item: ItemUpsert = {
      external_id: "prod_1",
      kind: "product",
      title: "Magnesium",
      locale: "nl",
      attributes: { handle: "magnesium", format: "text" },
      editorial: full,
    };
    await client(m.fetch).upsertItems([item, { external_id: "doc_1", kind: "document", title: "T", locale: "nl", editorial: {} }]);
    const [sent, empty] = (m.calls[0]!.body as { items: Record<string, unknown>[] }).items;
    expect(sent).toEqual({
      external_id: "prod_1",
      kind: "product",
      title: "Magnesium",
      locale: "nl",
      region: "Utrecht",
      attributes: {
        handle: "magnesium",
        format: "product",
        duration_min: 12.5,
        reading_time_min: 4,
        difficulty: "easy",
        author: "auth_3",
        available: false,
        expires_at: "2026-12-31T22:00:00Z",
        starts_at: "2026-11-01T19:00:00+01:00",
        repurchase_days: 25,
        phase_fit: ["follicular"],
        price_cents: 2495,
        compare_at_cents: 2995,
        currency: "EUR",
        image_url: "https://cdn/x.jpg",
        url: "https://shop/x",
        product_type: "supplement",
        categories: ["sleep", "minerals"],
      },
    });
    expect(empty).toEqual({ external_id: "doc_1", kind: "document", title: "T", locale: "nl" });
    // the caller's objects are untouched
    expect(item.attributes).toEqual({ handle: "magnesium", format: "text" });
    expect(item.editorial).toBe(full);
  });

  it("leaves an item without editorial as it was", async () => {
    const m = mockFetch({ status: 200, body: { upserted: 1 } });
    await client(m.fetch).upsertItems([{ external_id: "d", kind: "document", title: "T", locale: "nl", attributes: { a: 1 }, region: "Drenthe" }]);
    expect((m.calls[0]!.body as { items: unknown[] }).items[0]).toEqual({ external_id: "d", kind: "document", title: "T", locale: "nl", attributes: { a: 1 }, region: "Drenthe" });
  });

  it.each([
    [{ format: "podcast" }, /attributes.format/],
    [{ difficulty: "expert" }, /attributes.difficulty/],
    [{ duration_min: -1 }, /attributes.duration_min/],
    [{ reading_time_min: Number.NaN }, /attributes.reading_time_min/],
    [{ repurchase_days: -1 }, /attributes.repurchase_days/],
    [{ repurchase_days: 1.5 }, /attributes.repurchase_days/],
    [{ price_cents: -1 }, /attributes.price_cents/],
    [{ compare_at_cents: 9.99 }, /attributes.compare_at_cents/],
    [{ currency: "euro" }, /attributes.currency/],
    [{ expires_at: "2026-12-31" }, /attributes.expires_at/],
    [{ starts_at: new Date("x") }, /attributes.starts_at/],
    [{ available: "no" }, /attributes.available/],
    [{ region: "r".repeat(65) }, /region/],
  ])("refuses %j", async (attrs, msg) => {
    const m = mockFetch({ status: 200, body: {} });
    const items = [
      { external_id: "ok", kind: "document" as const, title: "T", locale: "nl" },
      { external_id: "x", kind: "product" as const, title: "T", locale: "nl", editorial: attrs as ItemAttributes },
    ];
    await expect(client(m.fetch).upsertItems(items)).rejects.toThrow(msg);
    await expect(client(m.fetch).upsertItems(items)).rejects.toThrow(/item 1/);
    expect(m.calls).toHaveLength(0);
  });

  it("refuses two different regions and a long top-level region", async () => {
    const m = mockFetch({ status: 200, body: {} });
    const c = client(m.fetch);
    await expect(c.upsertItems([{ external_id: "x", kind: "product", title: "T", locale: "nl", region: "Utrecht", editorial: { region: "Drenthe" } }])).rejects.toThrow(/different regions/);
    await expect(c.upsertItems([{ external_id: "x", kind: "product", title: "T", locale: "nl", region: "r".repeat(65) }])).rejects.toThrow(/region/);
    expect(m.calls).toHaveLength(0);
    expect(() => validateItemAttributes({ format: "workout", difficulty: "hard", currency: "EUR" })).not.toThrow();
  });
});

describe("activity events", () => {
  const at = "2026-10-13T20:00:00Z";
  const pers: EventBase = { mode: "personalized", userId: "u_7f3a", sessionId: "s_1", occurredAt: at, eventId: "e1" };
  const anon: EventBase = { mode: "non_profiled", sessionId: "s_9", occurredAt: at, eventId: "e2" };
  const cases: [string, (b: EventBase) => Event, Record<string, unknown>][] = [
    ["community_post_created", (b) => communityPostCreated({ ...b, postId: "p1", communityId: "c1" }), { post_id: "p1", community_id: "c1" }],
    ["community_reply", (b) => communityReply({ ...b, postId: "p1", communityId: "c1" }), { post_id: "p1", community_id: "c1" }],
    ["community_reaction", (b) => communityReaction({ ...b, postId: "p1", communityId: "c1", reaction: "heart" }), { post_id: "p1", community_id: "c1", reaction: "heart" }],
    ["paywall_viewed", (b) => paywallViewed({ ...b, itemId: "doc_3", requestId: "req_5" }), { item_id: "doc_3", request_id: "req_5" }],
    ["push_opened", (b) => pushOpened({ ...b, itemId: "doc_4", notificationType: "weekly_digest" }), { item_id: "doc_4", notification_type: "weekly_digest" }],
    [
      "search_performed",
      (b) => searchPerformed({ ...b, queryHash: "9f86d081884c7d65", resultCount: 0, clickedItemId: "doc_2" }),
      { query_hash: "9f86d081884c7d65", result_count: 0, clicked_item_id: "doc_2" },
    ],
    ["item_engaged", (b) => itemEngaged({ ...b, itemId: "doc_1", engagedSeconds: 42, maxScrollPercent: 80 }), { item_id: "doc_1", engaged_seconds: 42, max_scroll_percent: 80 }],
    ["item_completed", (b) => itemCompleted({ ...b, itemId: "wo_1", kind: "document", collection: "workouts" }), { item_id: "wo_1", kind: "document", collection: "workouts" }],
    [
      "order_placed",
      (b) => orderPlaced({ ...b, orderIdHash: "oh_1", productIds: ["prod_1", "prod_2"], valueCents: 4990, currency: "EUR" }),
      { order_id_hash: "oh_1", product_ids: ["prod_1", "prod_2"], value_cents: 4990, currency: "EUR" },
    ],
  ];

  it.each(cases)("%s: props, identity by consent mode, defaults", (type, build, props) => {
    const e = build(pers);
    expect(e).toMatchObject({ type, user_id: "u_7f3a", session_id: "s_1", source: "backend", event_id: "e1", occurred_at: at });
    expect(e.props).toEqual(props);
    const a = build(anon);
    expect(a.user_id).toBeUndefined();
    expect(a.session_id).toBe("s_9");
    expect(() => build({ ...anon, userId: "u_7f3a" })).toThrow(ConsentError);
    expect(() => build({ ...anon, sessionId: undefined })).toThrow(ConsentError);
    expect(() => build({ mode: "cohort", sessionId: "s_1" })).toThrow(ValidationError);
    expect(() => build({ userId: "u_1" } as unknown as EventBase)).toThrow(ConsentError);
    expect(() => build({ mode: "personalized", userId: "jan@example.com" })).toThrow(/email/);
    const d = build({ mode: "cohort", userId: "u_1" });
    expect(d.event_id).toMatch(/^[0-9a-f-]{36}$/);
    expect(d.source).toBe("backend");
    expect(d.session_id).toBeUndefined();
    expect(Date.parse(d.occurred_at)).not.toBeNaN();
  });

  it("refuses bad input", () => {
    const b: EventBase = { mode: "personalized", userId: "u_1" };
    const bad: (() => Event)[] = [
      () => communityPostCreated({ ...b, postId: "", communityId: "c" }),
      () => communityReply({ ...b, postId: "p", communityId: " " }),
      () => communityReaction({ ...b, postId: "p", communityId: "c", reaction: "" }),
      () => communityPostCreated({ ...b, postId: "p", communityId: "c", reaction: "x" }),
      () => paywallViewed({ ...b, itemId: "" }),
      () => pushOpened({ ...b, itemId: "" }),
      () => searchPerformed({ ...b, queryHash: "magnesium slaap" }),
      () => searchPerformed({ ...b, queryHash: "abc123" }),
      () => searchPerformed({ ...b, queryHash: "9F86D081884C7D65" }),
      () => searchPerformed({ ...b, queryHash: "9f86d081884c7d65", resultCount: -1 }),
      () => itemEngaged({ ...b, itemId: "", engagedSeconds: 3, maxScrollPercent: 0 }),
      () => itemEngaged({ ...b, itemId: "d", kind: "video" as "document", engagedSeconds: 3, maxScrollPercent: 0 }),
      () => itemEngaged({ ...b, itemId: "d", engagedSeconds: -1, maxScrollPercent: 0 }),
      () => itemEngaged({ ...b, itemId: "d", engagedSeconds: 1, maxScrollPercent: 101 }),
      () => itemCompleted({ ...b, itemId: "" }),
      () => itemCompleted({ ...b, itemId: "d", kind: "workout" as "document" }),
      () => orderPlaced({ ...b, orderIdHash: "", productIds: ["p"] }),
      () => orderPlaced({ ...b, orderIdHash: "h", productIds: [] }),
      () => orderPlaced({ ...b, orderIdHash: "h", productIds: [""] }),
      () => orderPlaced({ ...b, orderIdHash: "h", productIds: ["p"], valueCents: -5 }),
      () => orderPlaced({ ...b, orderIdHash: "h", productIds: ["p"], currency: "eur" }),
      () => pushOpened({ ...b, itemId: "d", context: { weekday: 7 } }),
    ];
    for (const f of bad) expect(f).toThrow(ValidationError);
    // optional props are left out
    expect(searchPerformed({ ...b, queryHash: "a".repeat(64) }).props).toEqual({ query_hash: "a".repeat(64) });
    expect(itemCompleted({ ...b, itemId: "d" }).props).toEqual({ item_id: "d" });
    const pw = paywallViewed({ ...b, itemId: "d" });
    expect(pw.props).toEqual({ item_id: "d" });
    expect(pw.request_id).toBeUndefined();
    expect(orderPlaced({ ...b, orderIdHash: "h", productIds: ["p"] }).props).toEqual({ order_id_hash: "h", product_ids: ["p"] });
  });

  it("ingests and signs like any event", async () => {
    const m = mockFetch({ status: 202, body: { accepted: 1 } });
    const signed = await signEvent(
      orderPlaced({ mode: "personalized", userId: "u_1", orderIdHash: "h", productIds: ["p"], occurredAt: "2026-10-13T10:00:00Z", eventId: "o1" }),
      "test-signing-key",
    );
    await client(m.fetch).ingestEvents([signed]);
    const sent = (m.calls[0]!.body as { events: Event[] }).events[0] as Event & { signature?: string };
    expect(sent.signature).toMatch(/^[0-9a-f]{64}$/);
  });
});

describe("reasons and flows (round 5)", () => {
  it("knows the recommended reason key", async () => {
    expect(REASON_RECOMMENDED).toBe("recommended");
    expect(REASON_KEYS).toHaveLength(11);
    expect(REASON_KEYS).toContain("recommended");
    const m = mockFetch({ status: 200, body: { ...slate, items: [{ ...slate.items[0], reason_key: "recommended", reason: { nl: "Aanbevolen", en: "Recommended" } }] } });
    const s = await client(m.fetch).recommend(base);
    expect(s.items[0]!.reason_key).toBe(REASON_RECOMMENDED);
  });

  const round5: FlowConfig = {
    generators: [
      { kind: "item_to_item", weight: 0.5 },
      { kind: "time_fit", weight: 0.15 },
      { kind: "format_fit", weight: 0.15 },
    ],
    filters: {
      exclude_dismissed_days: 30,
      exclude_consumed_days: 30,
      paywall_cooldown_days: 7,
      repurchase_days: 45,
      hide_unavailable: false,
      relevance_floor: 0.35,
      region_boost: 0.3,
    },
    ranker: { kind: "gbdt", bandit: false },
    exploration: { kind: "plackett_luce", share: 0.2 },
    reasons: { recommended: { nl: "Voor jou" }, time_fit: { en: "Fits the evening" } },
  };

  it("validates the round-5 flow and its edges", () => {
    expect(GENERATOR_KINDS).toContain("time_fit");
    expect(GENERATOR_KINDS).toContain("format_fit");
    expect(() => validateFlowConfig(round5)).not.toThrow();
    const edges = { ...round5, filters: { exclude_dismissed_days: 0, exclude_consumed_days: 365, paywall_cooldown_days: 0, repurchase_days: 365, relevance_floor: 1, hide_unavailable: true, freshness_days: null } };
    expect(() => validateFlowConfig(edges)).not.toThrow();
  });

  const g = { generators: [{ kind: "time_fit", weight: 1 }], ranker: { kind: "linear" }, exploration: { kind: "none" } };
  it.each([
    [[], /must be an object/],
    [{ ...g, generators: [] }, /at least one generator/],
    [{ ...g, generators: [{ kind: "time_fits", weight: 1 }] }, /unknown generator kind "time_fits"/],
    [{ ...g, generators: [{ kind: "time_fit" }] }, /weight/],
    [{ ...g, ranker: { kind: "lambdamart" } }, /ranker.kind/],
    [{ ...g, exploration: { kind: "thompson" } }, /exploration.kind/],
    [{ ...g, exploration: { kind: "plackett_luce", share: 0.6 } }, /exploration.share/],
    [{ ...g, rankers: {} }, /unknown config key rankers/],
    [{ ...g, filters: { exclude_dismissed_days: -1 } }, /filters.exclude_dismissed_days/],
    [{ ...g, filters: { exclude_dismissed_days: 366 } }, /filters.exclude_dismissed_days/],
    [{ ...g, filters: { exclude_dismissed_days: 7.5 } }, /filters.exclude_dismissed_days/],
    [{ ...g, filters: { exclude_consumed_days: 400 } }, /filters.exclude_consumed_days/],
    [{ ...g, filters: { paywall_cooldown_days: -7 } }, /filters.paywall_cooldown_days/],
    [{ ...g, filters: { repurchase_days: 1000 } }, /filters.repurchase_days/],
    [{ ...g, filters: { relevance_floor: 1.5 } }, /filters.relevance_floor/],
    [{ ...g, filters: { relevance_floor: -0.1 } }, /filters.relevance_floor/],
    [{ ...g, filters: { region_boost: 2 } }, /filters.region_boost/],
    [{ ...g, filters: { diversity: -1 } }, /filters.diversity/],
    [{ ...g, filters: { hide_unavailable: "no" } }, /filters.hide_unavailable/],
    [{ ...g, filters: { premium_policy: "hide" } }, /filters.premium_policy/],
    [{ ...g, filters: { exclude_dismised_days: 30 } }, /unknown filter exclude_dismised_days/],
    [{ ...g, reasons: { recomended: { nl: "x" } } }, /unknown reason key or generator recomended/],
    [{ ...g, reasons: { recommended: { NL: "x" } } }, /locale NL/],
    [{ ...g, reasons: { recommended: { nl: "{user}" } } }, /unknown placeholder \{user\}/],
    [{ ...g, reasons: { recommended: { nl: "{tag" } } }, /unbalanced brace/],
    [{ ...g, reasons: { recommended: { nl: "x".repeat(201) } } }, /longer than 200/],
  ])("refuses %j", (cfg, msg) => {
    expect(() => validateFlowConfig(cfg)).toThrow(msg);
    expect(() => validateFlowConfig(cfg)).toThrow(ValidationError);
  });

  it("applyFlow, planFlow and createFlow refuse an invalid config before sending", async () => {
    const m = mockFetch({ status: 200, body: [] });
    const c = new PatternaClient({ baseUrl: BASE, keys: { ...KEYS, admin: "pk_a_test" }, fetch: m.fetch, retryDelayMs: 0 });
    const bad = { ...round5, filters: { relevance_floor: 3 } };
    await expect(c.applyFlow("more_to_read", bad)).rejects.toThrow(/relevance_floor/);
    await expect(c.planFlow("more_to_read", bad)).rejects.toThrow(/relevance_floor/);
    await expect(c.createFlow("more_to_read", bad)).rejects.toThrow(/relevance_floor/);
    expect(m.calls).toHaveLength(0);
  });
});
