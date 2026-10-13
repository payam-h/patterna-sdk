import { assertIdentityAllowed, assertMode, assertOpaqueUserId } from "./consent.js";
import { ApiError, ApiErrorCode, ErrorCode, isCallerBug, PatternaError, ValidationError } from "./errors.js";
import { newRequestId, Transport, type FetchLike, type RequestSpec } from "./http.js";
import { checkCoarseContext, checkSessionItems, itemWire, validateFlowConfig } from "./signals.js";
import { canonicalJson } from "./signing.js";
import type {
  AbandonReason,
  ApplyResult,
  CallOptions,
  DeletionRequest,
  Event,
  Experiment,
  ExperimentInput,
  ExportJob,
  Flow,
  FlowConfig,
  Health,
  IngestEventsResult,
  Interaction,
  InteractionType,
  Item,
  ItemKind,
  ItemPage,
  ItemTag,
  ItemUpsert,
  LedgerQuery,
  LedgerRow,
  LedgerWeek,
  ListItemsParams,
  MetricsDay,
  MetricsQuery,
  Mode,
  RecommendRequest,
  SetItemStatusBody,
  Settings,
  SettingsPatch,
  SigningSecret,
  Slate,
  Surface,
  SurfaceFallback,
  SurfacePlan,
  SurfaceSpec,
  SurfaceStatus,
  UpsertItemsResult,
  UserProfile,
} from "./types.js";

export const DEFAULT_RECOMMEND_TIMEOUT_MS = 150;
/** The deadline the engine is asked to answer within (X-Deadline-Ms), below the client budget. */
export const DEFAULT_RECOMMEND_DEADLINE_MS = 120;
export const DEFAULT_INGEST_TIMEOUT_MS = 5_000;
export const DEFAULT_ADMIN_TIMEOUT_MS = 30_000;
const MIN_SERVER_DEADLINE_MS = 10;
const DEFAULT_SLATE_MEMORY = 4096;
const MAX_BLOCK = 64;
const ITEM_KINDS = new Set(["document", "product", "community", "event", "tip"]);
const SURFACE_STATUSES = new Set(["shadow", "testers", "live", "off"]);
const HMAC_HEX = /^[0-9a-f]{64}$/;

// Contract limits (contracts/openapi.yaml). Checked here so an oversized batch fails fast with a
// clear message instead of a 400 after a network round trip.
const MAX_INTERACTIONS = 50;
const MAX_EVENTS = 500;
const MAX_ITEMS = 500;
const MAX_EXCLUDES = 200;
const MAX_CONTROL_ITEMS = 50;
const MAX_MEMBER_OF = 500;
const MAX_LEDGER_BLOCK = 32;
const FALLBACK_KINDS = new Set(["newest", "popular", "curated"]);
const EVENT_CONTEXT_KEYS = new Set([
  "locale",
  "app_version",
  "platform",
  "hour_bucket",
  "weekday",
  "region",
  "subscription_tier",
  "tenure_days",
]);

export interface PatternaClientOptions {
  /** e.g. `https://api.patterna.ainur.dev/v1`, or the customer's proxy that forwards to it. */
  baseUrl: string;
  /**
   * Scoped keys. A backend holds them; an app never does (point `baseUrl` at your proxy and use
   * `headers` for the user's session instead).
   */
  keys?: { serve?: string; ingest?: string; admin?: string };
  headers?: () => Record<string, string> | Promise<Record<string, string>>;
  /** Defaults to the global fetch (Node 22, browsers, React Native). */
  fetch?: FetchLike;
  /**
   * `recommendMs` is the client budget (150 ms). `recommendDeadlineMs` is sent as X-Deadline-Ms
   * (engine 0.2): default 120 ms, or 4/5 of `recommendMs` when that is shorter; it must be below
   * `recommendMs`. `adminMs` bounds admin calls (30 s).
   */
  timeouts?: { recommendMs?: number; recommendDeadlineMs?: number; ingestMs?: number; adminMs?: number };
  /** Pause before the single retry of an idempotent call. Default 100 ms. */
  retryDelayMs?: number;
  /** Your trace id for every call (X-Trace-Id), e.g. the active Sentry span's trace id. */
  traceId?: () => string | undefined;
  /** Failures of background sends (abandonments). Never log the request bodies. */
  onBackgroundError?: (error: PatternaError) => void;
  /**
   * How many recent slates the client remembers to check interaction kinds before sending.
   * Default 4096; 0 turns it off.
   */
  slateMemory?: number;
}

/** One surface in {@link PatternaClient.recommendMany}. */
export interface ManyRequest<F = string[]> {
  request: RecommendRequest;
  fallback: () => F | Promise<F>;
}

/**
 * What {@link PatternaClient.recommendWithFallback} returns. `shown` (engine 0.3) says whether to
 * render the block: false only for a slate answered `shown: false` (fewer eligible items than the
 * surface's min_slots), which carries no items, is not replaced by the fallback and is never
 * abandoned. Hide the block. True for every other answer, the fallback included.
 */
export type Recommendation<F = string[]> =
  | { source: "patterna"; value: Slate; requestId: string; shown: boolean }
  | {
      source: "fallback";
      value: F;
      shown: true;
      /** The id sent as X-Request-Id, so you can log it even though no slate came back. */
      requestId: string;
      error: PatternaError;
      /** True when the abandonment was sent in the background (timeout, network error, 5xx). */
      abandoned: boolean;
    };

export class PatternaClient {
  private readonly http: Transport;
  private readonly recommendMs: number;
  private readonly recommendDeadlineMs: number;
  private readonly ingestMs: number;
  private readonly adminMs: number;
  private readonly onBackgroundError: ((error: PatternaError) => void) | undefined;
  private readonly slateMemory: number;
  /**
   * request id -> non-document kinds per item, and whether the slate was answered shown=false;
   * insertion-ordered for eviction.
   */
  private readonly kinds = new Map<string, { kinds: Map<string, string>; notShown: boolean }>();

  constructor(opts: PatternaClientOptions) {
    if (!opts.baseUrl) throw new TypeError("PatternaClient: baseUrl is required");
    const f = opts.fetch ?? (globalThis.fetch?.bind(globalThis) as FetchLike | undefined);
    if (!f) throw new TypeError("PatternaClient: no fetch available; pass options.fetch");
    this.http = new Transport({
      baseUrl: opts.baseUrl,
      keys: opts.keys ?? {},
      ...(opts.headers ? { headers: opts.headers } : {}),
      fetch: f,
      retryDelayMs: opts.retryDelayMs ?? 100,
      ...(opts.traceId ? { traceId: opts.traceId } : {}),
    });
    this.recommendMs = opts.timeouts?.recommendMs ?? DEFAULT_RECOMMEND_TIMEOUT_MS;
    this.recommendDeadlineMs =
      opts.timeouts?.recommendDeadlineMs ?? Math.min(DEFAULT_RECOMMEND_DEADLINE_MS, Math.floor((this.recommendMs * 4) / 5));
    if (this.recommendDeadlineMs >= this.recommendMs) {
      throw new TypeError("PatternaClient: timeouts.recommendDeadlineMs must be below timeouts.recommendMs");
    }
    this.ingestMs = opts.timeouts?.ingestMs ?? DEFAULT_INGEST_TIMEOUT_MS;
    this.adminMs = opts.timeouts?.adminMs ?? DEFAULT_ADMIN_TIMEOUT_MS;
    this.onBackgroundError = opts.onBackgroundError;
    this.slateMemory = Math.max(0, opts.slateMemory ?? DEFAULT_SLATE_MEMORY);
  }

  /** X-Deadline-Ms for a call with `budgetMs`: the configured deadline, shrunk by the same margin. */
  serverDeadlineMs(budgetMs: number): number {
    const margin = this.recommendMs - this.recommendDeadlineMs;
    const d = Math.min(this.recommendDeadlineMs, budgetMs - margin);
    return d < MIN_SERVER_DEADLINE_MS ? Math.max(Math.floor(budgetMs / 2), 1) : d;
  }

  // ---- serve scope -------------------------------------------------------------------------

  /**
   * Ranks a slate. Never retried: every call logs an impression with propensities, so a blind
   * resend would log a second slate the user never saw. Default budget 150 ms; wrap it in
   * {@link withFallback} so a slow answer shows your own list.
   */
  async recommend(req: RecommendRequest, opts: CallOptions = {}): Promise<Slate> {
    const requestId = opts.requestId ?? newRequestId();
    const op = "recommend";
    assertMode((req as { mode?: unknown }).mode, op);
    const ids = req as { user_id?: string; session_id?: string };
    assertIdentityAllowed(req.mode, ids, op);
    assertOpaqueUserId(ids.user_id, op);
    if (!req.surface) invalid(op, "surface is required");
    if (!req.context?.locale) invalid(op, "context.locale is required");
    if ((req.exclude_item_ids?.length ?? 0) > MAX_EXCLUDES) {
      invalid(op, `exclude_item_ids is limited to ${MAX_EXCLUDES}`);
    }
    if ((req.context.control_item_ids?.length ?? 0) > MAX_CONTROL_ITEMS) {
      invalid(op, `context.control_item_ids is limited to ${MAX_CONTROL_ITEMS}`);
    }
    if ((req.context.member_of?.length ?? 0) > MAX_MEMBER_OF) {
      invalid(op, `context.member_of is limited to ${MAX_MEMBER_OF}`);
    }
    checkCoarseContext(req.context, op);
    checkSessionItems(req.context.session_items, op);
    if (req.assignment_unit !== undefined && !HMAC_HEX.test(req.assignment_unit)) {
      invalid(op, "assignment_unit must be assignmentUnit(secret, userId): 64 lowercase hex characters, never a raw id");
    }
    const timeoutMs = opts.timeoutMs ?? this.recommendMs;
    const res = await this.http.send<Slate>({
      op,
      method: "POST",
      path: "/recommendations",
      body: req,
      scope: "serve",
      timeoutMs,
      idempotent: false,
      ...callExtras(opts),
      requestId,
      deadlineMs: this.serverDeadlineMs(timeoutMs),
      ...(opts.tester ? { tester: true } : {}),
    });
    const slate = res.data;
    if (slate && !slate.request_id) slate.request_id = res.requestId ?? requestId;
    if (slate) markFills(slate);
    this.rememberKinds(slate);
    return slate;
  }

  /**
   * `recommend`, or your own list when Patterna cannot answer in time. The request id is minted
   * here, so on a timeout, network error, 5xx or unreadable answer the client reports the
   * abandonment in the background (engine 0.2) and Patterna does not count a slate nobody saw.
   * Consent and input bugs, `pii_rejected`, `mode_conflict` and `mode_required` are rethrown,
   * never hidden.
   *
   * A `shown: false` answer (engine 0.3) is returned as it is: `shown` false, no items, no
   * fallback, nothing abandoned. Hide the block.
   */
  async recommendWithFallback<F = string[]>(
    req: RecommendRequest,
    fallback: () => F | Promise<F>,
    opts: CallOptions = {},
  ): Promise<Recommendation<F>> {
    const requestId = opts.requestId ?? newRequestId();
    try {
      const slate = await this.recommend(req, { ...opts, requestId });
      return { source: "patterna", value: slate, requestId: slate.request_id, shown: slate.shown !== false };
    } catch (err) {
      if (!(err instanceof PatternaError) || isCallerBug(err)) throw err;
      const reason = abandonReason(err);
      if (reason) {
        this.abandon(requestId, reason, opts.traceId === undefined ? {} : { traceId: opts.traceId }).catch(
          (e: unknown) => {
            if (e instanceof PatternaError) this.onBackgroundError?.(e);
          },
        );
      }
      return { source: "fallback", value: await fallback(), shown: true, requestId, error: err, abandoned: reason !== undefined };
    }
  }

  /**
   * Several surfaces at once (Home asks for articles and products together). The requests run in
   * parallel and share one budget: each gets `opts.timeoutMs` (default 150 ms) from the same
   * start, so two surfaces cost one budget. Each falls back and is abandoned on its own and is
   * its own impression. A caller bug in any request rejects the whole call.
   */
  async recommendMany<F = string[]>(reqs: ManyRequest<F>[], opts: CallOptions = {}): Promise<Recommendation<F>[]> {
    const { requestId: _ignored, ...shared } = opts;
    void _ignored;
    return Promise.all(reqs.map((r) => this.recommendWithFallback(r.request, r.fallback, shared)));
  }

  /**
   * Tells Patterna the slate of `requestId` was not shown (engine 0.2:
   * `POST /recommendations/{id}/abandon`), so the impression counts as a client fallback.
   * `recommendWithFallback` does it for you; call it when a slate arrived but you showed
   * something else. An id the engine never logged rejects with `unknown_request`.
   *
   * A slate answered `shown: false` is never abandoned (it already counts in its arm with zero
   * taps): when this client served it, this rejects with `not_shown` without sending; otherwise
   * the engine answers 409 `not_shown`.
   */
  async abandon(requestId: string, reason: AbandonReason = "client_fallback", opts: CallOptions = {}): Promise<void> {
    const op = "abandon";
    if (!requestId) invalid(op, "requestId is required");
    if (this.kinds.get(requestId)?.notShown) {
      throw new ValidationError({
        code: ApiErrorCode.NotShown,
        op,
        requestId,
        message: "the slate was answered shown=false: hide the block, do not abandon it",
      });
    }
    await this.http.send<unknown>({
      op,
      method: "POST",
      path: `/recommendations/${encodeURIComponent(requestId)}/abandon`,
      body: { reason },
      scope: "serve",
      timeoutMs: opts.timeoutMs ?? this.ingestMs,
      idempotent: true,
      ...callExtras(opts),
    });
  }

  /** Reports seen, click and dismiss for one slate. Idempotent per (request, item, type). */
  async reportInteractions(requestId: string, interactions: Interaction[], opts: CallOptions = {}): Promise<void> {
    const op = "reportInteractions";
    if (!requestId) invalid(op, "requestId is required");
    if (interactions.length === 0) return;
    if (interactions.length > MAX_INTERACTIONS) invalid(op, `at most ${MAX_INTERACTIONS} interactions per call`);
    interactions.forEach((it, i) => this.checkInteraction(requestId, it, i, op));
    await this.http.send<unknown>({
      op,
      method: "POST",
      path: "/interactions",
      body: { request_id: requestId, interactions },
      scope: "serve",
      timeoutMs: opts.timeoutMs ?? this.ingestMs,
      idempotent: true,
      ...callExtras(opts),
    });
  }

  // ---- ingest scope ------------------------------------------------------------------------

  /** Sends up to 500 canonical events. Idempotent on `event_id`. */
  async ingestEvents(events: Event[], opts: CallOptions = {}): Promise<IngestEventsResult> {
    const op = "ingestEvents";
    if (events.length > MAX_EVENTS) invalid(op, `at most ${MAX_EVENTS} events per call`);
    for (const e of events) validateEvent(e, op);
    const res = await this.http.send<IngestEventsResult>({
      op,
      method: "POST",
      path: "/events",
      body: { events },
      scope: "ingest",
      timeoutMs: opts.timeoutMs ?? this.ingestMs,
      idempotent: true,
      ...callExtras(opts),
    });
    return res.data ?? {};
  }

  /** Upserts up to 500 catalog items (documents, products, communities, events, tips). */
  async upsertItems(items: ItemUpsert[], opts: CallOptions = {}): Promise<UpsertItemsResult> {
    const op = "upsertItems";
    if (items.length > MAX_ITEMS) invalid(op, `at most ${MAX_ITEMS} items per call`);
    const wire = items.map((it, i) => itemWire(it, i, op));
    const res = await this.http.send<UpsertItemsResult>({
      op,
      method: "PUT",
      path: "/catalog/items",
      body: { items: wire },
      scope: "ingest",
      timeoutMs: opts.timeoutMs ?? this.ingestMs,
      idempotent: true,
      ...callExtras(opts),
    });
    return res.data ?? {};
  }

  /** Archive, schedule or publish an item. Setting a status twice is harmless, so it is retried. */
  async setItemStatus(kind: ItemKind, externalId: string, body: SetItemStatusBody, opts: CallOptions = {}): Promise<void> {
    const op = "setItemStatus";
    if (!externalId) invalid(op, "externalId is required");
    await this.http.send<unknown>({
      op,
      method: "POST",
      path: `/catalog/items/${encodeURIComponent(externalId)}/status`,
      query: { kind },
      body,
      scope: "ingest",
      timeoutMs: opts.timeoutMs ?? this.ingestMs,
      idempotent: true,
      ...callExtras(opts),
    });
  }

  /** Replaces the editor-approved tags of an item. */
  async setItemTags(kind: ItemKind, externalId: string, tags: ItemTag[], opts: CallOptions = {}): Promise<void> {
    const op = "setItemTags";
    if (!externalId) invalid(op, "externalId is required");
    await this.http.send<unknown>({
      op,
      method: "PUT",
      path: `/catalog/items/${encodeURIComponent(externalId)}/tags`,
      query: { kind },
      body: { tags },
      scope: "ingest",
      timeoutMs: opts.timeoutMs ?? this.ingestMs,
      idempotent: true,
      ...callExtras(opts),
    });
  }

  /** Sets a user's consent mode. `non_profiled` makes Patterna delete the user row. */
  async setUserMode(userId: string, mode: Mode, opts: CallOptions = {}): Promise<void> {
    const op = "setUserMode";
    assertOpaqueUserId(userId, op);
    assertMode(mode, op);
    await this.http.send<unknown>({
      op,
      method: "PUT",
      path: `/users/${encodeURIComponent(userId)}/mode`,
      body: { mode },
      scope: "users",
      timeoutMs: opts.timeoutMs ?? this.ingestMs,
      idempotent: true,
      ...callExtras(opts),
    });
  }

  /**
   * Declared attributes as tag affinities. Never health values. A profile never creates consent
   * (engine 0.3): without `mode`, a user Patterna has no row for rejects with `mode_required`
   * (422); set the mode in the profile or call `setUserMode` first.
   */
  async setUserProfile(userId: string, profile: UserProfile, opts: CallOptions = {}): Promise<void> {
    const op = "setUserProfile";
    assertOpaqueUserId(userId, op);
    if (profile.mode === "non_profiled") {
      throw new ValidationError({
        code: ErrorCode.InvalidArgument,
        op,
        message: "a non_profiled user has no profile; call setUserMode instead",
      });
    }
    await this.http.send<unknown>({
      op,
      method: "PUT",
      path: `/users/${encodeURIComponent(userId)}/profile`,
      body: profile,
      scope: "users",
      timeoutMs: opts.timeoutMs ?? this.ingestMs,
      idempotent: true,
      ...callExtras(opts),
    });
  }

  /** Deletes every row about the user and schedules retraining. */
  async deleteUser(userId: string, opts: CallOptions = {}): Promise<DeletionRequest> {
    const op = "deleteUser";
    assertOpaqueUserId(userId, op);
    const res = await this.http.send<DeletionRequest>({
      op,
      method: "DELETE",
      path: `/users/${encodeURIComponent(userId)}`,
      scope: "users",
      timeoutMs: opts.timeoutMs ?? this.ingestMs,
      idempotent: true,
      ...callExtras(opts),
    });
    return res.data ?? {};
  }

  // ---- catalog (engine 0.2) ----------------------------------------------------------------

  /**
   * Reads one catalog item by external id and kind (`GET /catalog/items/{id}?kind=`, ingest key):
   * title, status, `attributes`, `tags` with their source, and the `enrichment` state. `kind` is
   * required because the same external id may exist under several kinds. A missing item, or one
   * stored under another kind, rejects with `item_not_found` (404); an archived item is returned
   * with `status: "archived"`, so check it when you hydrate a card.
   */
  async getItem(kind: ItemKind, externalId: string, opts: CallOptions = {}): Promise<Item> {
    const op = "getItem";
    if (!externalId) invalid(op, "externalId is required");
    if (!ITEM_KINDS.has(kind)) invalid(op, `unknown kind "${kind}"`);
    const res = await this.http.send<Item>({
      op,
      method: "GET",
      path: `/catalog/items/${encodeURIComponent(externalId)}`,
      query: { kind },
      scope: "ingest",
      timeoutMs: opts.timeoutMs ?? this.ingestMs,
      idempotent: true,
      ...callExtras(opts),
    });
    if (!res.data || typeof res.data !== "object" || typeof res.data.external_id !== "string") {
      throw new PatternaError({ code: ErrorCode.BadResponse, op, message: "item without external_id", status: res.status });
    }
    return res.data;
  }

  /**
   * Removes an item for good (engine 0.2: `DELETE /catalog/items/{id}?kind=`). Archive with
   * setItemStatus when it may come back. A missing item rejects with `item_not_found`.
   */
  async deleteItem(kind: ItemKind, externalId: string, opts: CallOptions = {}): Promise<void> {
    const op = "deleteItem";
    if (!externalId) invalid(op, "externalId is required");
    if (!ITEM_KINDS.has(kind)) invalid(op, `unknown kind "${kind}"`);
    await this.http.send<unknown>({
      op,
      method: "DELETE",
      path: `/catalog/items/${encodeURIComponent(externalId)}`,
      query: { kind },
      scope: "ingest",
      timeoutMs: opts.timeoutMs ?? this.ingestMs,
      idempotent: true,
      ...callExtras(opts),
    });
  }

  /** One page of the catalog; `changedSince` needs engine 0.2. */
  async listItems(params: ListItemsParams = {}, opts: CallOptions = {}): Promise<ItemPage> {
    const op = "listItems";
    if (params.limit !== undefined && (params.limit < 1 || params.limit > MAX_ITEMS)) {
      invalid(op, `limit must be between 1 and ${MAX_ITEMS}`);
    }
    const query: Record<string, string> = {};
    if (params.kind) query.kind = params.kind;
    if (params.status) query.status = params.status;
    if (params.changedSince) {
      query.changed_since =
        params.changedSince instanceof Date ? params.changedSince.toISOString() : params.changedSince;
    }
    if (params.cursor) query.cursor = params.cursor;
    if (params.limit) query.limit = String(params.limit);
    const res = await this.http.send<ItemPage>({
      op,
      method: "GET",
      path: "/catalog/items",
      query,
      scope: "ingest",
      timeoutMs: opts.timeoutMs ?? this.ingestMs,
      idempotent: true,
      ...callExtras(opts),
    });
    return { items: res.data?.items ?? [], next_cursor: res.data?.next_cursor ?? null };
  }

  /**
   * Every item changed after `since`, page by page (engine 0.2), for a nightly reconciliation:
   * `for await (const item of client.itemsChangedSince(lastRun)) ...`
   */
  async *itemsChangedSince(since: string | Date, kind?: ItemKind, opts: CallOptions = {}): AsyncGenerator<Item> {
    let cursor: string | undefined;
    for (;;) {
      const page = await this.listItems(
        { changedSince: since, limit: MAX_ITEMS, ...(kind ? { kind } : {}), ...(cursor ? { cursor } : {}) },
        opts,
      );
      yield* page.items;
      if (!page.next_cursor || page.items.length === 0) return;
      cursor = page.next_cursor;
    }
  }

  // ---- admin (admin key) -------------------------------------------------------------------

  private admin<T>(spec: Omit<RequestSpec, "scope" | "timeoutMs"> & { timeoutMs?: number | undefined }, opts: CallOptions) {
    return this.http.send<T>({
      ...spec,
      scope: "admin",
      timeoutMs: opts.timeoutMs ?? spec.timeoutMs ?? this.adminMs,
      ...callExtras(opts),
    });
  }

  /**
   * Issues the tenant's first event signing secret (engine 0.2: `POST /settings/signing-secret`).
   * Shown once; when one exists the engine answers 409 and you rotate instead.
   */
  async createSigningSecret(opts: CallOptions = {}): Promise<SigningSecret> {
    const res = await this.admin<SigningSecret>(
      { op: "createSigningSecret", method: "POST", path: "/settings/signing-secret", idempotent: false },
      opts,
    );
    return res.data;
  }

  /** Replaces the signing secret (owner keys). Roll the new one out to your signers first. */
  async rotateSigningSecret(opts: CallOptions = {}): Promise<SigningSecret> {
    const res = await this.admin<SigningSecret>(
      { op: "rotateSigningSecret", method: "POST", path: "/settings/signing-secret/rotate", idempotent: false },
      opts,
    );
    return res.data;
  }

  async listSurfaces(opts: CallOptions = {}): Promise<Surface[]> {
    const res = await this.admin<Surface[]>({ op: "listSurfaces", method: "GET", path: "/surfaces", idempotent: true }, opts);
    return list(res.data, res.status);
  }

  /** One surface; rejects with `unknown_surface` when there is none. */
  async getSurface(key: string, opts: CallOptions = {}): Promise<Surface> {
    const s = (await this.listSurfaces(opts)).find((x) => x.key === key);
    if (!s) throw new ApiError({ code: "unknown_surface", op: "getSurface", status: 404, message: `no surface "${key}"` });
    return s;
  }

  /** shadow, testers, live or off (the kill switch). */
  async setSurfaceStatus(key: string, status: SurfaceStatus, opts: CallOptions = {}): Promise<Surface> {
    const op = "setSurfaceStatus";
    if (!key) invalid(op, "surface key is required");
    if (!SURFACE_STATUSES.has(status)) invalid(op, "status must be shadow, testers, live or off");
    return this.patchSurface(op, key, { status }, opts);
  }

  /** Replaces the tester roster: at most 200 random user ids, never emails. */
  async setSurfaceTesters(key: string, userIds: string[], opts: CallOptions = {}): Promise<Surface> {
    const op = "setSurfaceTesters";
    if (!key) invalid(op, "surface key is required");
    if (userIds.length > 200) invalid(op, "at most 200 testers");
    for (const id of userIds) assertOpaqueUserId(id, op);
    return this.patchSurface(op, key, { testers: userIds }, opts);
  }

  private async patchSurface(op: string, key: string, body: Record<string, unknown>, opts: CallOptions): Promise<Surface> {
    const res = await this.admin<Surface>(
      { op, method: "PATCH", path: `/surfaces/${encodeURIComponent(key)}`, body, idempotent: true },
      opts,
    );
    // The engine answers {key, ok} rather than the surface: read it back so callers see its state.
    if (res.data && typeof res.data === "object" && res.data.status) return res.data;
    return this.getSurface(key, opts);
  }

  /**
   * Engine 0.3: `PATCH /surfaces/{key}` with the fields `spec` sets (slots, item kinds, the
   * fallback with `by_anchor`, `min_slots`, `device_resort`, notes, exploration share). Returns
   * the surface after the change. `spec.surface` is ignored.
   */
  async updateSurface(key: string, spec: Omit<SurfaceSpec, "surface">, opts: CallOptions = {}): Promise<Surface> {
    const full = { ...spec, surface: key };
    validateSurfaceSpec(full, "updateSurface");
    const body = surfaceBody(full);
    if (Object.keys(body).length === 0) invalid("updateSurface", "nothing to change");
    return this.patchSurface("updateSurface", key, body, opts);
  }

  /** Lists the fields where `spec` differs from the surface as it is. Writes nothing. */
  async planSurface(spec: SurfaceSpec, opts: CallOptions = {}): Promise<SurfacePlan> {
    validateSurfaceSpec(spec, "planSurface");
    const cur = (await this.getSurface(spec.surface, opts)) as Surface & { min_slots?: number; device_resort?: boolean };
    const changes: string[] = [];
    const add = (field: string, differs: boolean) => {
      if (differs) changes.push(field);
    };
    add("item_kinds", (spec.item_kinds?.length ?? 0) > 0 && !sameSet(spec.item_kinds ?? [], cur.item_kinds ?? []));
    add("slots", spec.slots !== undefined && spec.slots !== cur.slots);
    add("fallback", spec.fallback !== undefined && !sameFallback(spec.fallback, cur.fallback as SurfaceFallback | undefined));
    add("min_slots", spec.min_slots !== undefined && spec.min_slots !== (cur.min_slots ?? 0));
    add("device_resort", spec.device_resort !== undefined && spec.device_resort !== (cur.device_resort ?? false));
    add("notes", spec.notes !== undefined && spec.notes !== (cur.notes ?? ""));
    add("exploration_share", spec.exploration_share !== undefined && spec.exploration_share !== cur.exploration_share);
    return { surface: spec.surface, changes, applied: false };
  }

  /**
   * Makes the surface match `spec` (`patterna surface apply` in CI): patches only the fields that
   * differ, nothing when none do. The fallback is replaced as a whole, so keep the full list in
   * the file.
   */
  async applySurface(spec: SurfaceSpec, opts: CallOptions = {}): Promise<SurfacePlan> {
    const plan = await this.planSurface(spec, opts);
    if (plan.changes.length === 0) return plan;
    const all = surfaceBody(spec);
    const body: Record<string, unknown> = {};
    for (const f of plan.changes) body[f] = all[f];
    await this.patchSurface("applySurface", spec.surface, body, opts);
    return { ...plan, applied: true };
  }

  /** Engine 0.3: a surface's daily metrics (UTC days, zero-filled), with `not_shown_share` and `client_abandoned_rate`. */
  async surfaceMetrics(key: string, q: MetricsQuery = {}, opts: CallOptions = {}): Promise<MetricsDay[]> {
    const op = "surfaceMetrics";
    if (!key) invalid(op, "surface key is required");
    if (q.days !== undefined && (!Number.isInteger(q.days) || q.days < 1 || q.days > 180)) invalid(op, "days must be 1 to 180");
    checkBlock(op, q.block);
    const query: Record<string, string> = {};
    if (q.days !== undefined) query.days = String(q.days);
    if (q.block) query.block = q.block;
    const res = await this.admin<{ days?: MetricsDay[] }>(
      { op, method: "GET", path: `/surfaces/${encodeURIComponent(key)}/metrics`, query, idempotent: true },
      opts,
    );
    return res.data?.days ?? [];
  }

  /** Tenant settings; `phase_tags` (engine 0.3) says whether slates carry phase_fit. */
  async getSettings(opts: CallOptions = {}): Promise<Settings> {
    const res = await this.admin<Settings>({ op: "getSettings", method: "GET", path: "/settings", idempotent: true }, opts);
    return res.data ?? {};
  }

  /** `PATCH /settings` (owners and admin keys). */
  async updateSettings(patch: SettingsPatch, opts: CallOptions = {}): Promise<void> {
    if (Object.keys(patch).length === 0) invalid("updateSettings", "nothing to change");
    await this.admin<unknown>({ op: "updateSettings", method: "PATCH", path: "/settings", body: patch, idempotent: true }, opts);
  }

  /** Engine 0.3: turns phase_fit item metadata on or off for the tenant (off by default). */
  async setPhaseTags(on: boolean, opts: CallOptions = {}): Promise<void> {
    await this.updateSettings({ phase_tags: on }, opts);
  }

  /** `GET /health`: liveness, the release and (engine 0.3) `engine_version`. */
  async health(opts: CallOptions = {}): Promise<Health> {
    const res = await this.admin<Health>({ op: "health", method: "GET", path: "/health", idempotent: true }, opts);
    return res.data;
  }

  async listFlows(surface: string, opts: CallOptions = {}): Promise<Flow[]> {
    if (!surface) invalid("listFlows", "surface key is required");
    const res = await this.admin<Flow[]>(
      { op: "listFlows", method: "GET", path: `/surfaces/${encodeURIComponent(surface)}/flows`, idempotent: true },
      opts,
    );
    return list(res.data, res.status);
  }

  /** One flow version; version 0 (default) is the active one. Undefined when there is none. */
  async getFlow(surface: string, version = 0, opts: CallOptions = {}): Promise<Flow | undefined> {
    let v = version;
    if (v === 0) {
      const s = await this.getSurface(surface, opts);
      if (s.active_flow_version === null || s.active_flow_version === undefined) return undefined;
      v = s.active_flow_version;
    }
    return (await this.listFlows(surface, opts)).find((f) => f.version === v);
  }

  /** Stores a new, inactive version. Not retried: a lost answer would create a second version. */
  async createFlow(surface: string, config: FlowConfig, note?: string, opts: CallOptions = {}): Promise<Flow> {
    const op = "createFlow";
    if (!surface) invalid(op, "surface key is required");
    validateFlowConfig(config, op);
    const res = await this.admin<Flow>(
      {
        op,
        method: "POST",
        path: `/surfaces/${encodeURIComponent(surface)}/flows`,
        body: note ? { config, note } : { config },
        idempotent: false,
      },
      opts,
    );
    return res.data;
  }

  /** Makes a version live. With the two-person rule the engine answers 409 `approval_required`. */
  async activateFlow(surface: string, version: number, note?: string, opts: CallOptions = {}): Promise<void> {
    const op = "activateFlow";
    if (!surface || !Number.isInteger(version) || version <= 0) invalid(op, "surface key and a positive version are required");
    await this.admin<unknown>(
      {
        op,
        method: "POST",
        path: `/surfaces/${encodeURIComponent(surface)}/flows/${version}/activate`,
        ...(note ? { body: { note } } : {}),
        idempotent: true,
      },
      opts,
    );
  }

  /**
   * Re-activates the version that was active before the current one and returns its number.
   * Rejects with `ValidationError` (`nothing_to_roll_back`) when there is none.
   */
  async rollbackFlow(surface: string, note = "rollback", opts: CallOptions = {}): Promise<number> {
    const s = await this.getSurface(surface, opts);
    const flows = await this.listFlows(surface, opts);
    const previous = flows
      .filter((f) => f.activated_at && f.version !== s.active_flow_version)
      .sort((a, b) => Date.parse(b.activated_at as string) - Date.parse(a.activated_at as string))[0];
    if (!previous?.version) {
      throw new ValidationError({ code: "nothing_to_roll_back", op: "rollbackFlow", message: "no earlier activated flow version" });
    }
    await this.activateFlow(surface, previous.version, note, opts);
    return previous.version;
  }

  /** What applyFlow would do, without writing anything. */
  async planFlow(surface: string, config: FlowConfig, activate = true, opts: CallOptions = {}): Promise<ApplyResult> {
    validateFlowConfig(config, "applyFlow");
    const want = canonicalJson(config);
    const s = await this.getSurface(surface, opts);
    const flows = await this.listFlows(surface, opts);
    let version = 0;
    for (const f of flows) {
      if (!f.config || canonicalJson(f.config) !== want || f.version === undefined) continue;
      if (f.version === s.active_flow_version) {
        return { surface, version: f.version, created: false, activated: false, unchanged: true };
      }
      version = Math.max(version, f.version);
    }
    return { surface, version, created: version === 0, activated: activate, unchanged: false };
  }

  /**
   * Makes the surface's flow match `config` (compared as canonical JSON): nothing when the active
   * version matches, else reuse a version with the same config or create one, then activate it
   * when `activate`. Keep flow configs in your repository and run this in CI.
   */
  async applyFlow(surface: string, config: FlowConfig, opts: CallOptions & { note?: string; activate?: boolean } = {}): Promise<ApplyResult> {
    const { note, activate = true, ...call } = opts;
    const res = await this.planFlow(surface, config, activate, call);
    if (res.unchanged) return res;
    if (res.created) res.version = (await this.createFlow(surface, config, note, call)).version ?? 0;
    if (res.activated) await this.activateFlow(surface, res.version, note, call);
    return res;
  }

  async listExperiments(opts: CallOptions = {}): Promise<Experiment[]> {
    const res = await this.admin<Experiment[]>({ op: "listExperiments", method: "GET", path: "/experiments", idempotent: true }, opts);
    return list(res.data, res.status);
  }

  /** Starts a holdout. Not retried: a lost answer would start a second one. */
  async startExperiment(input: ExperimentInput, opts: CallOptions = {}): Promise<Experiment> {
    const op = "startExperiment";
    if (!input.surface || !input.name || !input.primary_metric) invalid(op, "surface, name and primary_metric are required");
    const res = await this.admin<Experiment>({ op, method: "POST", path: "/experiments", body: input, idempotent: false }, opts);
    return res.data;
  }

  async endExperiment(id: string, opts: CallOptions = {}): Promise<void> {
    if (!id) invalid("endExperiment", "experiment id is required");
    await this.admin<unknown>(
      { op: "endExperiment", method: "POST", path: `/experiments/${encodeURIComponent(id)}/end`, idempotent: true },
      opts,
    );
  }

  /** One summary row per surface with the user-clustered interval (one block's rows with `q.block`). */
  async ledger(q: LedgerQuery = {}, opts: CallOptions = {}): Promise<LedgerRow[]> {
    checkBlock("ledger", q.block);
    const res = await this.admin<LedgerRow[]>(
      { op: "ledger", method: "GET", path: "/proof/ledger", query: ledgerQuery(q), idempotent: true },
      opts,
    );
    return list(res.data, res.status);
  }

  /**
   * Engine 0.3 (`by=block`): one row per surface and block, over the slates the client reported
   * seen in that block and the taps in it. Descriptive; the whole-slate row decides. `q.block` is
   * ignored.
   */
  async ledgerByBlock(q: LedgerQuery = {}, opts: CallOptions = {}): Promise<LedgerRow[]> {
    const { block: _ignored, ...rest } = q;
    void _ignored;
    const res = await this.admin<LedgerRow[]>(
      { op: "ledgerByBlock", method: "GET", path: "/proof/ledger", query: { ...ledgerQuery(rest), by: "block" }, idempotent: true },
      opts,
    );
    return list(res.data, res.status);
  }

  /** Weekly rows per arm (bucket=week). */
  async ledgerWeeks(q: LedgerQuery = {}, opts: CallOptions = {}): Promise<LedgerWeek[]> {
    const res = await this.admin<LedgerWeek[]>(
      { op: "ledgerWeeks", method: "GET", path: "/proof/ledger", query: { ...ledgerQuery(q), bucket: "week" }, idempotent: true },
      opts,
    );
    return list(res.data, res.status);
  }

  /** Queues an export of impressions and interactions; poll exportStatus until `done`. */
  async exportLedger(q: LedgerQuery & { from: string; to: string }, opts: CallOptions = {}): Promise<ExportJob> {
    if (!q.from || !q.to) invalid("exportLedger", "from and to are required");
    const res = await this.admin<ExportJob>(
      {
        op: "exportLedger",
        method: "POST",
        path: "/proof/export",
        body: { from: q.from, to: q.to, ...(q.surface ? { surface: q.surface } : {}) },
        idempotent: false,
      },
      opts,
    );
    return res.data;
  }

  async exportStatus(jobId: string, opts: CallOptions = {}): Promise<ExportJob> {
    if (!jobId) invalid("exportStatus", "job id is required");
    const res = await this.admin<ExportJob>(
      { op: "exportStatus", method: "GET", path: `/proof/exports/${encodeURIComponent(jobId)}`, idempotent: true },
      opts,
    );
    return res.data;
  }

  // ---- slate memory ------------------------------------------------------------------------

  private rememberKinds(slate: Slate | undefined): void {
    if (!slate?.request_id || this.slateMemory === 0) return;
    const m = new Map<string, string>();
    for (const it of slate.items ?? []) if (it.kind && it.kind !== "document") m.set(it.item_id, it.kind);
    const notShown = slate.shown === false;
    if (m.size === 0 && !notShown) return;
    this.kinds.delete(slate.request_id);
    this.kinds.set(slate.request_id, { kinds: m, notShown });
    while (this.kinds.size > this.slateMemory) {
      const oldest = this.kinds.keys().next().value as string;
      this.kinds.delete(oldest);
    }
  }

  private checkInteraction(requestId: string, it: Interaction, i: number, op: string): void {
    if (!it.item_id) invalid(op, `interaction ${i}: item_id is required`);
    if (it.position < 0 || (it.displayed_position !== undefined && it.displayed_position < 0)) {
      invalid(op, `interaction ${i}: positions are 0-based and never negative`);
    }
    if (it.block !== undefined && it.block.length > MAX_BLOCK) invalid(op, `interaction ${i}: block is limited to ${MAX_BLOCK} characters`);
    if (it.kind) {
      if (!ITEM_KINDS.has(it.kind)) invalid(op, `interaction ${i}: unknown kind "${it.kind}"`);
      return;
    }
    const k = this.kinds.get(requestId)?.kinds.get(it.item_id);
    if (k) invalid(op, `interaction ${i}: kind is required for ${k} items (only documents may leave it empty)`);
  }

}

function callExtras(opts: CallOptions): Pick<RequestSpec, "signal" | "requestId" | "traceId"> {
  const out: Pick<RequestSpec, "signal" | "requestId" | "traceId"> = {};
  if (opts.signal) out.signal = opts.signal;
  if (opts.requestId) out.requestId = opts.requestId;
  if (opts.traceId) out.traceId = opts.traceId;
  return out;
}

/** Why a sent recommend was given up on, or undefined when there is nothing to abandon. */
function abandonReason(err: PatternaError): AbandonReason | undefined {
  if (err.code === ErrorCode.Timeout) return "timeout";
  if (err.code === ErrorCode.Network) return "network_error";
  if (err.code === ErrorCode.BadResponse) return "bad_response";
  if (err instanceof ApiError && (err.status ?? 0) >= 500) return "server_error";
  return undefined;
}

/**
 * Builds an interaction for one of the slate's items with the served `kind` and `position`.
 * Returns undefined when the item is not in the slate.
 */
export function interactionFor(
  slate: Slate,
  itemId: string,
  type: InteractionType,
  extra: Partial<Pick<Interaction, "displayed_position" | "block" | "occurred_at">> = {},
): Interaction | undefined {
  const it = slate.items.find((i) => i.item_id === itemId);
  if (!it) return undefined;
  return { item_id: it.item_id, kind: it.kind, position: it.position, type, occurred_at: new Date().toISOString(), ...extra };
}

/**
 * Sets `measured` (engine 0.2) from the user's analytics consent: false keeps the request out of
 * the ledger and the training data; the slate is still ranked and shown.
 */
export function measuredContext<C extends object>(context: C, analyticsConsent: boolean): C & { measured: boolean } {
  return { ...context, measured: analyticsConsent };
}

/** A list endpoint's body: an empty body is an empty list; anything else but an array is a bad response. */
function list<T>(data: T[] | undefined, status: number): T[] {
  if (data === undefined) return [];
  if (!Array.isArray(data)) {
    throw new PatternaError({ code: ErrorCode.BadResponse, op: "admin", status, message: "expected a JSON array" });
  }
  return data;
}

function ledgerQuery(q: LedgerQuery): Record<string, string> {
  const out: Record<string, string> = {};
  if (q.surface) out.surface = q.surface;
  if (q.from) out.from = q.from;
  if (q.to) out.to = q.to;
  if (q.block) out.block = q.block;
  return out;
}

function checkBlock(op: string, block: string | undefined): void {
  if (block !== undefined && block.length > MAX_LEDGER_BLOCK) invalid(op, `block is a short name of at most ${MAX_LEDGER_BLOCK} characters`);
}

/**
 * Marks the trailing `fill_count` items `fill: true` when the engine did not mark any slot itself
 * (engine 0.3 sends `fill_count` on the slate and `fill` per slot only in its log).
 */
export function markFills(slate: Slate): Slate {
  const n = slate.fill_count ?? 0;
  const items = slate.items ?? [];
  if (n <= 0 || items.some((it) => it.fill)) return slate;
  for (let i = Math.max(0, items.length - n); i < items.length; i++) (items[i] as { fill?: boolean }).fill = true;
  return slate;
}

/** Checks a surface spec the way the engine will, so a CI run fails before anything is sent. */
export function validateSurfaceSpec(s: SurfaceSpec, op = "applySurface"): void {
  if (!s.surface) invalid(op, "surface key is required");
  if (s.slots !== undefined && (!Number.isInteger(s.slots) || s.slots < 1 || s.slots > 50)) invalid(op, "slots must be 1 to 50");
  if (s.min_slots !== undefined && (!Number.isInteger(s.min_slots) || s.min_slots < 0 || s.min_slots > 50)) {
    invalid(op, "min_slots must be 0 to 50");
  }
  if (s.exploration_share !== undefined && (s.exploration_share < 0 || s.exploration_share > 1)) {
    invalid(op, "exploration_share must be 0 to 1");
  }
  if (s.notes !== undefined && s.notes.length > 4000) invalid(op, "notes holds at most 4000 characters");
  for (const k of s.item_kinds ?? []) if (!ITEM_KINDS.has(k)) invalid(op, `unknown item kind "${k}"`);
  const f = s.fallback;
  if (f) {
    if (!FALLBACK_KINDS.has(f.kind)) invalid(op, "fallback.kind must be newest, popular or curated");
    if (f.kind === "curated" && (f.item_ids?.length ?? 0) === 0) invalid(op, "a curated fallback needs item_ids");
    for (const [anchor, ids] of Object.entries(f.by_anchor ?? {})) {
      if (!anchor || ids.length === 0) invalid(op, "fallback.by_anchor maps an anchor id to a non-empty list");
    }
  }
}

function surfaceBody(s: SurfaceSpec): Record<string, unknown> {
  const b: Record<string, unknown> = {};
  if ((s.item_kinds?.length ?? 0) > 0) b.item_kinds = s.item_kinds;
  if (s.slots !== undefined) b.slots = s.slots;
  if (s.fallback !== undefined) b.fallback = s.fallback;
  if (s.min_slots !== undefined) b.min_slots = s.min_slots;
  if (s.device_resort !== undefined) b.device_resort = s.device_resort;
  if (s.notes !== undefined) b.notes = s.notes;
  if (s.exploration_share !== undefined) b.exploration_share = s.exploration_share;
  return b;
}

function sameSet(a: string[], b: string[]): boolean {
  const x = [...new Set(a)].sort();
  const y = [...new Set(b)].sort();
  return x.length === y.length && x.every((v, i) => v === y[i]);
}

function sameFallback(want: SurfaceFallback, got: SurfaceFallback | undefined): boolean {
  if (!got) return false;
  const norm = (f: SurfaceFallback) => ({
    kind: f.kind,
    item_ids: f.item_ids?.length ? f.item_ids : undefined,
    by_anchor: f.by_anchor && Object.keys(f.by_anchor).length ? f.by_anchor : undefined,
  });
  return canonicalJson(norm(want)) === canonicalJson(norm(got));
}

function invalid(op: string, message: string): never {
  throw new ValidationError({ code: ErrorCode.InvalidArgument, op, message });
}

/** Contract checks that keep personal data out: context keys are a closed set, ids are opaque. */
export function validateEvent(e: Event, op: string): void {
  if (!e.event_id) invalid(op, "event_id is required");
  if (!e.type) invalid(op, "type is required");
  if (!e.occurred_at) invalid(op, "occurred_at is required");
  if (!e.source) invalid(op, "source is required");
  assertOpaqueUserId(e.user_id, op);
  if (e.context) {
    for (const key of Object.keys(e.context)) {
      if (!EVENT_CONTEXT_KEYS.has(key)) {
        invalid(op, `event context key "${key}" is not allowed (${[...EVENT_CONTEXT_KEYS].join(", ")})`);
      }
    }
    checkCoarseContext(e.context, op, "event context");
  }
}
