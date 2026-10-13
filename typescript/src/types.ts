/**
 * Friendly names over the generated contract types. `generated/types.ts` comes from
 * contracts/openapi.yaml via `make contracts`; edit the contract, never the generated file.
 */
import type { components, operations } from "./generated/types.js";

type Schemas = components["schemas"];
type JsonBody<Op> = Op extends { requestBody: { content: { "application/json": infer B } } } ? B : never;
type JsonResponse<Op, Status extends number> = Op extends {
  responses: { [K in Status]: { content: { "application/json": infer R } } };
}
  ? R
  : never;

export type Mode = Schemas["Mode"];
export type ApiErrorBody = Schemas["Error"];
/**
 * One slot. `fill` (engine 0.3) is true when the slot was filled from the editorial list behind
 * the ranked items (`Slate.fill_count`); the SDK sets it from `fill_count` when the engine does
 * not send it per slot.
 */
export type SlateItem = Schemas["SlateItem"] & { fill?: boolean };
/**
 * A ranked list. Engine 0.3 adds `control_source` (where an editorial slate came from),
 * `fill_count` (trailing slots filled from the editorial list) and `shown: false` answers with no
 * items: hide the block and never abandon them. `model_versions` names the model version each
 * learner read; engine 0.3 logs it on the impression (trace, export) and does not send it with
 * the answer yet, so it is usually absent.
 */
export type Slate = Omit<Schemas["Slate"], "items"> & {
  items: SlateItem[];
  model_versions?: Record<string, string>;
};
export type ControlSource = NonNullable<Schemas["Slate"]["control_source"]>;
/**
 * One seen, click or dismiss. `kind` is required for anything that is not a document (engine 0.2
 * refuses it otherwise); `position` is the served 0-based slot; `displayed_position` is where
 * the app showed it after a device re-sort; `block` (engine 0.2) names the UI block when one
 * slate is split across blocks.
 */
export type Interaction = Schemas["Interaction"] & { block?: string };
export type InteractionType = Interaction["type"];
export type Event = Schemas["Event"];
export type EventContext = NonNullable<Event["context"]>;
export type ItemUpsert = Schemas["ItemUpsert"];
export type ItemKind = ItemUpsert["kind"];
export type ItemStatus = NonNullable<ItemUpsert["status"]>;
export type UserProfile = Schemas["UserProfile"];
export type DeletionRequest = Schemas["DeletionRequest"];

/** The contract's request shape, mode-agnostic. Prefer {@link RecommendRequest}. */
export type RecommendRequestBody = Schemas["RecommendRequest"];
/**
 * `measured` (engine 0.2): whether the request counts in the ledger and the training data;
 * absent means true. Set it from the user's analytics consent with {@link measuredContext}.
 */
export type RecommendContext = RecommendRequestBody["context"] & { measured?: boolean };
// control_item_ids (at most 50: your own editorial list for this page, the control slate) and
// member_of (at most 500 community ids the user is in, never offered; for non_profiled) come
// from the contract (engine 0.3).

type RecommendCommon = Omit<RecommendRequestBody, "mode" | "user_id" | "session_id" | "context"> & {
  context: RecommendContext;
  /**
   * Engine 0.2: keeps one person in one holdout arm across rotating sessions. Compute it with
   * `assignmentUnit(secret, userId)`; only that shape (64 lowercase hex) is accepted.
   */
  assignment_unit?: string;
};

/**
 * The consent gate in the type system: a `non_profiled` request cannot name a user, and needs a
 * rotating session id. The same rules are enforced at runtime for callers without types.
 */
export type RecommendRequest =
  | (RecommendCommon & { mode: "personalized" | "cohort"; user_id?: string; session_id?: string })
  | (RecommendCommon & { mode: "non_profiled"; user_id?: never; session_id: string });

export type IngestEventsResult = JsonResponse<operations["ingestEvents"], 202>;
export type UpsertItemsResult = JsonResponse<operations["upsertItems"], 200>;
export type SetItemStatusBody = JsonBody<operations["setItemStatus"]>;
export type ItemTag = NonNullable<JsonBody<operations["setItemTags"]>["tags"]>[number];

/** Per-call options. */
export interface CallOptions {
  /**
   * Overrides the client's default budget for this call, in milliseconds. On `recommend` the
   * `X-Deadline-Ms` sent to the engine shrinks with it, keeping the same network margin.
   */
  timeoutMs?: number;
  /** Cancels the call; rejects with code `aborted`. */
  signal?: AbortSignal;
  /** Sent as `X-Request-Id` instead of a freshly minted UUID. At most 64 visible ASCII characters. */
  requestId?: string;
  /** Sent as `X-Trace-Id` (e.g. your Sentry trace id), overriding the client's `traceId()`. */
  traceId?: string;
  /** `recommend` only: sends `X-Patterna-Tester: 1` (engine 0.2). */
  tester?: boolean;
}

/** A catalog item as the API returns it. `updated_at` comes with engine 0.2. */
export type Item = Schemas["Item"] & { updated_at?: string };

export interface ItemPage {
  items: Item[];
  next_cursor?: string | null;
}

export interface ListItemsParams {
  kind?: ItemKind;
  status?: ItemStatus;
  /** Engine 0.2: items created, updated, archived or deleted after this instant (ISO 8601 or Date). */
  changedSince?: string | Date;
  cursor?: string;
  /** At most 500; default 100. */
  limit?: number;
}

/** Why the client gave up on a slate. */
export type AbandonReason = "timeout" | "server_error" | "network_error" | "bad_response" | "client_fallback";

export interface SigningSecret {
  /** Sign with this string as it is: the SDKs use its bytes, never a hex-decoded form. */
  signing_secret: string;
  note?: string;
}

// ---- admin ------------------------------------------------------------------------------------

export type SurfaceStatus = "shadow" | "testers" | "live" | "off";
export type Surface = Schemas["Surface"];
export type SurfaceInput = Schemas["SurfaceInput"];
export type FlowConfig = Schemas["FlowConfig"];
export type Flow = Schemas["Flow"];
export type ExperimentInput = Schemas["ExperimentInput"];
export type Experiment = Schemas["Experiment"];
export type LedgerRow = Schemas["LedgerRow"];
export type LedgerWeek = Schemas["LedgerWeek"];

export interface LedgerQuery {
  surface?: string;
  /** UTC date, YYYY-MM-DD. */
  from?: string;
  to?: string;
  /** Engine 0.3: rows for this UI block only (at most 32 characters). For every block use `ledgerByBlock`. */
  block?: string;
}

// ---- engine 0.3 admin ---------------------------------------------------------------------------

/**
 * A surface's editorial list: the control arm, the rollout gate, the safety net and the fill for
 * short slates. `by_anchor` maps an anchor's external id to the editor's list for that page.
 */
export interface SurfaceFallback {
  kind: "newest" | "popular" | "curated";
  item_ids?: string[];
  by_anchor?: Record<string, string[]>;
}

/**
 * The part of a surface kept in the customer's repository and applied with `applySurface`.
 * Fields left out stay as they are. Status and testers move with their own calls.
 */
export interface SurfaceSpec {
  surface: string;
  item_kinds?: string[];
  /** 1..50 */
  slots?: number;
  fallback?: SurfaceFallback;
  /** 0..50; below this many eligible items the answer is shown=false with no items. 0 = always shown. */
  min_slots?: number;
  /** The app re-sorts on the device; every interaction must then carry displayed_position. */
  device_resort?: boolean;
  notes?: string;
  exploration_share?: number;
}

/** What `applySurface` did or `planSurface` would do. `changes` is empty when unchanged. */
export interface SurfacePlan {
  surface: string;
  changes: string[];
  applied: boolean;
}

export interface MetricsQuery {
  /** 1..180; default 28. */
  days?: number;
  /** Only slates seen in this block, and taps in it. */
  block?: string;
}

/** One UTC day of a surface; `not_shown_share` and `client_abandoned_rate` come with engine 0.3. */
export type MetricsDay = NonNullable<Schemas["SurfaceMetrics"]["days"]>[number];
export type Settings = Schemas["Settings"];
export type SettingsPatch = Schemas["SettingsPatch"];

export interface Health {
  status: string;
  postgres?: string;
  redis?: string;
  /** The release (commit). */
  version?: string;
  /** Engine 0.3: the API version the engine serves (the contract's info.version). */
  engine_version?: string;
}

export interface ExportJob {
  job_id: string;
  status?: string;
  uri?: string;
  files?: string[];
}

/** What applyFlow did (or planFlow would do). */
export interface ApplyResult {
  surface: string;
  /** The version matching the config; 0 in a plan that would create one. */
  version: number;
  created: boolean;
  activated: boolean;
  unchanged: boolean;
}
