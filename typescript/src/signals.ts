/**
 * Round-5 signals (engine round 5, API 0.3.0, docs/SIGNALS.md): the coarse request context, the
 * editorial item attributes the engine reads, the reason keys and the flow model's generators
 * and filters, with the checks the engine applies.
 */
import { ErrorCode, ValidationError } from "./errors.js";
import type { FlowConfig, ItemUpsert } from "./types.js";

function invalid(op: string, message: string): never {
  throw new ValidationError({ code: ErrorCode.InvalidArgument, op, message });
}

// ---- request context ----------------------------------------------------------------------

/** Subscription tiers the engine knows by name; any other name of at most 32 characters is a tenant tier. */
export const TIER_FREE = "free";
export const TIER_PREMIUM = "premium";

const MAX_SESSION_ITEMS = 20;
const MAX_SESSION_ITEM_LEN = 200;
const MAX_REGION = 64;
const MAX_TIER = 32;

const WEEKDAYS: Record<string, number> = { Mon: 0, Tue: 1, Wed: 2, Thu: 3, Fri: 4, Sat: 5, Sun: 6 };

/**
 * The hour (0 to 23) and the contract weekday (0 = Monday ... 6 = Sunday) of `at` in `timeZone`
 * (an IANA name such as "Europe/Amsterdam"), to spread into a request or event context:
 * `context: { locale: "nl", ...timeContext(new Date(), user.timeZone) }`. Without a time zone
 * the device's own clock is used. They select the time bucket the `time_fit` generator reads.
 */
export function timeContext(at: Date = new Date(), timeZone?: string): { hour_bucket: number; weekday: number } {
  if (Number.isNaN(at.getTime())) invalid("timeContext", "at is not a valid date");
  if (!timeZone) return { hour_bucket: at.getHours(), weekday: (at.getDay() + 6) % 7 };
  let parts: Intl.DateTimeFormatPart[];
  try {
    parts = new Intl.DateTimeFormat("en-US", { timeZone, hour: "2-digit", hourCycle: "h23", weekday: "short" }).formatToParts(at);
  } catch {
    invalid("timeContext", `unknown time zone "${timeZone}"`);
  }
  const hour = Number(parts.find((p) => p.type === "hour")?.value);
  const weekday = WEEKDAYS[parts.find((p) => p.type === "weekday")?.value ?? ""];
  if (!Number.isInteger(hour) || weekday === undefined) invalid("timeContext", "this runtime cannot resolve time zones");
  return { hour_bucket: hour % 24, weekday };
}

interface CoarseContext {
  hour_bucket?: number;
  weekday?: number;
  tenure_days?: number;
  subscription_tier?: string;
  region?: string;
}

/** Checks the coarse context keys the way the engine does (prefix names the object in errors). */
export function checkCoarseContext(c: CoarseContext, op: string, prefix = "context"): void {
  const int = (v: unknown) => typeof v === "number" && Number.isInteger(v);
  if (c.hour_bucket !== undefined && (!int(c.hour_bucket) || c.hour_bucket < 0 || c.hour_bucket > 23)) {
    invalid(op, `${prefix}.hour_bucket is an hour from 0 to 23`);
  }
  if (c.weekday !== undefined && (!int(c.weekday) || c.weekday < 0 || c.weekday > 6)) {
    invalid(op, `${prefix}.weekday is 0 (Monday) to 6 (Sunday)`);
  }
  if (c.tenure_days !== undefined && (!int(c.tenure_days) || c.tenure_days < 0)) {
    invalid(op, `${prefix}.tenure_days is a whole number of days, zero or more`);
  }
  if (c.subscription_tier !== undefined && (typeof c.subscription_tier !== "string" || c.subscription_tier.trim().length > MAX_TIER)) {
    invalid(op, `${prefix}.subscription_tier is a tier name of at most ${MAX_TIER} characters`);
  }
  if (c.region !== undefined && (typeof c.region !== "string" || c.region.trim().length > MAX_REGION)) {
    invalid(op, `${prefix}.region is a region name of at most ${MAX_REGION} characters`);
  }
}

export function checkSessionItems(items: readonly string[] | undefined, op: string): void {
  if (items === undefined) return;
  if (items.length > MAX_SESSION_ITEMS) invalid(op, `context.session_items is limited to ${MAX_SESSION_ITEMS}`);
  for (const id of items) {
    if (typeof id !== "string" || id === "" || id.length > MAX_SESSION_ITEM_LEN) {
      invalid(op, `context.session_items holds item ids of 1 to ${MAX_SESSION_ITEM_LEN} characters`);
    }
  }
}

// ---- editorial attributes -----------------------------------------------------------------

export type ItemFormat = "text" | "video" | "audio" | "recipe" | "workout" | "tool" | "product";
export type Difficulty = "easy" | "medium" | "hard";
const FORMATS = new Set<string>(["text", "video", "audio", "recipe", "workout", "tool", "product"]);
const DIFFICULTIES = new Set<string>(["easy", "medium", "hard"]);

/**
 * Typed editorial attributes of a catalog item, set as `ItemUpsert.editorial`; the SDK writes
 * them into `attributes` (a typed key wins over the same key there) and `region` into the
 * upsert's top-level `region`, the field the engine validates and ranks on. Every key optional.
 */
export interface ItemAttributes {
  /** Feeds the `format_fit` generator and a ranker feature. */
  format?: ItemFormat;
  /** Minutes (video, audio, workouts); a ranker feature and the time_fit heuristic. */
  duration_min?: number;
  /** Minutes (text); used when duration_min is absent. */
  reading_time_min?: number;
  difficulty?: Difficulty;
  /** An author id or name, as you keep it. */
  author?: string;
  /** Products: false is never served while the flow's hide_unavailable is on. */
  available?: boolean;
  /** Never served after it (RFC 3339 string or Date). */
  expires_at?: string | Date;
  /** Events: never served after it, boosted in the 7 days before. */
  starts_at?: string | Date;
  /** Products: a buyer is not offered it again inside this window (wins over the flow's). */
  repurchase_days?: number;
  /** One of the tenant's regions; sent as the upsert's top-level region. */
  region?: string;
  /** Only returned when the tenant's phase_tags is on; never ranks. */
  phase_fit?: string[];
  // Kept for the panel's product cards.
  price_cents?: number;
  compare_at_cents?: number;
  /** ISO 4217, e.g. "EUR". */
  currency?: string;
  image_url?: string;
  url?: string;
  product_type?: string;
  /** Folded into tags at ingest. */
  categories?: string[];
}

const RFC3339 = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$/;
const CURRENCY = /^[A-Z]{3}$/;

function wireTime(v: string | Date): string {
  return typeof v === "string" ? v : v.toISOString().replace(/\.\d{3}Z$/, "Z");
}

/** Throws `ValidationError` when an attribute is outside the contract. */
export function validateItemAttributes(a: ItemAttributes, op = "upsertItems", prefix = ""): void {
  const p = `${prefix}attributes.`;
  if (a.format !== undefined && !FORMATS.has(a.format)) invalid(op, `${p}format must be text, video, audio, recipe, workout, tool or product`);
  if (a.difficulty !== undefined && !DIFFICULTIES.has(a.difficulty)) invalid(op, `${p}difficulty must be easy, medium or hard`);
  for (const k of ["duration_min", "reading_time_min"] as const) {
    const v = a[k];
    if (v !== undefined && (typeof v !== "number" || !Number.isFinite(v) || v < 0)) invalid(op, `${p}${k} is a number of minutes, never negative`);
  }
  for (const k of ["repurchase_days", "price_cents", "compare_at_cents"] as const) {
    const v = a[k];
    if (v !== undefined && (!Number.isInteger(v) || v < 0)) invalid(op, `${p}${k} is a whole number, never negative`);
  }
  for (const k of ["expires_at", "starts_at"] as const) {
    const v = a[k];
    if (v === undefined) continue;
    const ok = v instanceof Date ? !Number.isNaN(v.getTime()) : typeof v === "string" && RFC3339.test(v) && !Number.isNaN(Date.parse(v));
    if (!ok) invalid(op, `${p}${k} must be an RFC 3339 time with a zone or a Date`);
  }
  if (a.currency !== undefined && !CURRENCY.test(a.currency)) invalid(op, `${p}currency is an ISO 4217 code such as EUR`);
  if (a.region !== undefined && a.region.length > MAX_REGION) invalid(op, `${prefix}region is a region name of at most ${MAX_REGION} characters`);
  if (a.available !== undefined && typeof a.available !== "boolean") invalid(op, `${p}available is true or false`);
}

/** The item as sent: `editorial` merged into `attributes`, its region lifted to `region`. */
export function itemWire(item: ItemUpsert, i: number, op = "upsertItems"): Record<string, unknown> {
  const { editorial, ...rest } = item;
  const prefix = `item ${i}: `;
  if (rest.region !== undefined && rest.region.length > MAX_REGION) invalid(op, `${prefix}region is a region name of at most ${MAX_REGION} characters`);
  if (!editorial) return rest;
  validateItemAttributes(editorial, op, prefix);
  const { region, ...attrs } = editorial;
  if (region && rest.region && region !== rest.region) invalid(op, `${prefix}region and editorial.region name different regions`);
  const typed: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(attrs)) {
    if (v === undefined) continue;
    typed[k] = k === "expires_at" || k === "starts_at" ? wireTime(v as string | Date) : v;
  }
  const out: Record<string, unknown> = { ...rest };
  const merged = { ...(rest.attributes ?? {}), ...typed };
  if (Object.keys(merged).length > 0) out.attributes = merged;
  if (region && !rest.region) out.region = region;
  return out;
}

// ---- reasons -------------------------------------------------------------------------------

/** Engine round 5: nothing true could be said about the slot; the text is a plain "Recommended". */
export const REASON_RECOMMENDED = "recommended";

/** Every reason key in the contract. The set may grow: treat an unknown key like `recommended`. */
export const REASON_KEYS = [
  "similar_to_read",
  "similar",
  "pairs_with",
  "matches_goals",
  "more_about_tag",
  "people_like_you",
  "trending",
  "editors_pick",
  "new",
  "popular",
  REASON_RECOMMENDED,
] as const;
export type ReasonKey = (typeof REASON_KEYS)[number];

// ---- flows ---------------------------------------------------------------------------------

export type GeneratorKind = FlowConfig["generators"][number]["kind"];
export type FlowFilters = NonNullable<FlowConfig["filters"]>;

/**
 * Every generator kind. `time_fit` and `format_fit` (engine round 5) are silent: they move scores
 * and never name a reason.
 */
export const GENERATOR_KINDS: readonly GeneratorKind[] = [
  "trending",
  "curated",
  "item_to_item",
  "content_affinity",
  "persona_bayes",
  "user_bayes",
  "surface_specific",
  "session_items",
  "time_fit",
  "format_fit",
];

const FILTER_KEYS = new Set([
  "exclude_seen_days",
  "exclude_dismissed_days",
  "exclude_consumed_days",
  "paywall_cooldown_days",
  "repurchase_days",
  "hide_unavailable",
  "relevance_floor",
  "locales",
  "premium_policy",
  "premium_cap",
  "per_collection_cap",
  "freshness_days",
  "diversity",
  "region_boost",
]);
const TOP_KEYS = new Set(["generators", "filters", "ranker", "exploration", "reasons", "diversity"]);
const PLACEHOLDERS = new Set(["anchor_title", "tag", "segment"]);

const isObject = (v: unknown): v is Record<string, unknown> => typeof v === "object" && v !== null && !Array.isArray(v);

/**
 * Checks a flow config the way the engine does on save, plus unknown filter keys (a misspelt
 * filter would otherwise do nothing). `createFlow`, `planFlow` and `applyFlow` run it before
 * anything is sent.
 */
export function validateFlowConfig(config: unknown, op = "validateFlow"): asserts config is FlowConfig {
  if (!isObject(config)) invalid(op, "config must be an object");
  for (const k of Object.keys(config)) {
    if (!TOP_KEYS.has(k)) invalid(op, `unknown config key ${k} (generators, filters, ranker, exploration, reasons)`);
  }
  const gens = config.generators;
  if (!Array.isArray(gens) || gens.length === 0) invalid(op, "generators must hold at least one generator");
  gens.forEach((g: unknown, i) => {
    if (!isObject(g) || !(GENERATOR_KINDS as readonly unknown[]).includes(g.kind)) {
      invalid(op, `generators[${i}]: unknown generator kind "${isObject(g) ? String(g.kind) : ""}" (${GENERATOR_KINDS.join(", ")})`);
    }
    if (typeof g.weight !== "number" || !Number.isFinite(g.weight)) invalid(op, `generators[${i}]: weight must be a number`);
  });
  const ranker = config.ranker;
  if (ranker !== undefined) {
    if (!isObject(ranker)) invalid(op, "ranker must be an object");
    if (ranker.kind !== undefined && ranker.kind !== "" && !["linear", "learned_linear", "gbdt"].includes(ranker.kind as string)) {
      invalid(op, "ranker.kind must be linear, learned_linear or gbdt");
    }
  }
  const exp = config.exploration;
  if (exp !== undefined) {
    if (!isObject(exp)) invalid(op, "exploration must be an object");
    if (exp.kind !== undefined && exp.kind !== "" && exp.kind !== "none" && exp.kind !== "plackett_luce") {
      invalid(op, "exploration.kind must be none or plackett_luce");
    }
    if (exp.share !== undefined && (typeof exp.share !== "number" || exp.share < 0 || exp.share > 0.5)) {
      invalid(op, "exploration.share must be between 0 and 0.5");
    }
  }
  const f = config.filters;
  if (f !== undefined && f !== null) {
    if (!isObject(f)) invalid(op, "filters must be an object");
    for (const k of Object.keys(f)) {
      if (!FILTER_KEYS.has(k)) invalid(op, `unknown filter ${k} (${[...FILTER_KEYS].join(", ")})`);
    }
    for (const k of ["diversity", "relevance_floor", "region_boost"]) {
      const v = f[k];
      if (v !== undefined && (typeof v !== "number" || v < 0 || v > 1)) invalid(op, `filters.${k} must be between 0 and 1`);
    }
    for (const k of ["exclude_dismissed_days", "exclude_consumed_days", "paywall_cooldown_days", "repurchase_days"]) {
      const v = f[k];
      if (v !== undefined && (!Number.isInteger(v) || (v as number) < 0 || (v as number) > 365)) {
        invalid(op, `filters.${k} must be a whole number of days between 0 and 365`);
      }
    }
    if (f.hide_unavailable !== undefined && typeof f.hide_unavailable !== "boolean") invalid(op, "filters.hide_unavailable is true or false");
    if (f.premium_policy !== undefined && !["allow", "cap", "exclude"].includes(f.premium_policy as string)) {
      invalid(op, "filters.premium_policy must be allow, cap or exclude");
    }
  }
  const reasons = config.reasons;
  if (reasons !== undefined) {
    if (!isObject(reasons)) invalid(op, "reasons must be an object");
    for (const k of Object.keys(reasons).sort()) {
      if (!(REASON_KEYS as readonly string[]).includes(k) && !(GENERATOR_KINDS as readonly string[]).includes(k)) {
        invalid(op, `reasons: unknown reason key or generator ${k} (reason keys: ${REASON_KEYS.join(", ")}; or a generator kind)`);
      }
      const byLocale = reasons[k];
      if (!isObject(byLocale)) invalid(op, `reasons.${k} must map locales to texts`);
      for (const [loc, t] of Object.entries(byLocale)) {
        if (!/^[a-z]{2,3}$/.test(loc)) invalid(op, `reasons.${k}: locale ${loc} is not a language code such as nl or en`);
        if (typeof t !== "string") invalid(op, `reasons.${k}.${loc} must be a string`);
        if (t.length > 200) invalid(op, `reasons.${k}.${loc} is longer than 200 characters`);
        if ((t.match(/\{/g) ?? []).length !== (t.match(/\}/g) ?? []).length) invalid(op, `reasons.${k}.${loc} has an unbalanced brace`);
        for (const m of t.matchAll(/\{([^{}]*)\}/g)) {
          if (!PLACEHOLDERS.has(m[1] ?? "")) {
            invalid(op, `reasons.${k}.${loc}: unknown placeholder {${m[1] ?? ""}} (allowed: {anchor_title}, {tag}, {segment})`);
          }
        }
      }
    }
  }
}
