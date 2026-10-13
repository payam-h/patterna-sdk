import { validateEvent } from "./client.js";
import { assertIdentityAllowed, assertMode, assertOpaqueUserId } from "./consent.js";
import { ErrorCode, ValidationError } from "./errors.js";
import { newRequestId } from "./http.js";
import type { Event, EventContext, ItemKind, Mode } from "./types.js";

/** Event types with builders (contracts/events.md, docs/SIGNALS.md §3). */
export const EVENT_COMMUNITY_POST_CREATED = "community_post_created";
export const EVENT_COMMUNITY_REPLY = "community_reply";
export const EVENT_COMMUNITY_REACTION = "community_reaction";
export const EVENT_PAYWALL_VIEWED = "paywall_viewed";
export const EVENT_PUSH_OPENED = "push_opened";
export const EVENT_SEARCH_PERFORMED = "search_performed";
export const EVENT_ITEM_ENGAGED = "item_engaged";
export const EVENT_ITEM_COMPLETED = "item_completed";
export const EVENT_ORDER_PLACED = "order_placed";

/**
 * Who an event is about and the envelope fields every builder shares.
 *
 * Identity follows consent: `personalized` and `cohort` carry `userId` (required) and `sessionId`
 * when given (the session generator and the paywall cooldown read it); `non_profiled` carries
 * `sessionId` only (required) and a `userId` is refused, so the event never joins a profile.
 */
export interface EventBase {
  mode: Mode;
  userId?: string;
  sessionId?: string;
  /** The placement and the served slate the event belongs to, when there is one. */
  surface?: string;
  requestId?: string;
  /** Defaults to now. */
  occurredAt?: string | Date;
  /** Defaults to a fresh UUID. */
  eventId?: string;
  /** Defaults to "backend". */
  source?: Event["source"];
  context?: EventContext;
}

function bad(op: string, message: string): never {
  throw new ValidationError({ code: ErrorCode.InvalidArgument, op, message });
}

const blank = (s: string | undefined) => !s || s.trim() === "";
const count = (v: number) => Number.isInteger(v) && v >= 0;

function build(type: string, b: EventBase, props: Record<string, unknown>): Event {
  const op = type;
  assertMode((b as { mode?: unknown }).mode, op);
  assertIdentityAllowed(b.mode, { user_id: b.userId, session_id: b.sessionId }, op);
  assertOpaqueUserId(b.userId, op);
  if (b.mode !== "non_profiled" && !b.userId) bad(op, `${b.mode} events need the user id`);
  const at = b.occurredAt ?? new Date();
  const e: Event = {
    event_id: b.eventId ?? newRequestId(),
    type,
    occurred_at: typeof at === "string" ? at : at.toISOString(),
    source: b.source ?? "backend",
    props,
  };
  if (b.mode !== "non_profiled") e.user_id = b.userId as string;
  if (b.sessionId) e.session_id = b.sessionId;
  if (b.surface) e.surface = b.surface;
  if (b.requestId) e.request_id = b.requestId;
  if (b.context) e.context = b.context;
  validateEvent(e, op);
  return e;
}

/** A post in a community; `reaction` (e.g. "heart") on `communityReaction` only. */
export interface CommunityPost extends EventBase {
  postId: string;
  communityId: string;
  reaction?: string;
}

function community(type: string, p: CommunityPost): Event {
  if (blank(p.postId) || blank(p.communityId)) bad(type, "postId and communityId are required");
  const props: Record<string, unknown> = { post_id: p.postId, community_id: p.communityId };
  if (type === EVENT_COMMUNITY_REACTION) {
    if (blank(p.reaction)) bad(type, "reaction is required");
    props.reaction = p.reaction;
  } else if (p.reaction !== undefined) {
    bad(type, "reaction belongs on community_reaction only");
  }
  return build(type, p, props);
}

/** `community_post_created`: the user wrote a post (active membership, the community's activity_rate). */
export function communityPostCreated(p: CommunityPost): Event {
  return community(EVENT_COMMUNITY_POST_CREATED, p);
}

/** `community_reply`: the user replied to a post. */
export function communityReply(p: CommunityPost): Event {
  return community(EVENT_COMMUNITY_REPLY, p);
}

/** `community_reaction`: the user reacted to a post (`reaction` required). */
export function communityReaction(p: CommunityPost & { reaction: string }): Event {
  return community(EVENT_COMMUNITY_REACTION, p);
}

/**
 * `paywall_viewed`: a free user hit the paywall on `itemId`; the item stays out of that user's
 * slates for the flow's paywall_cooldown_days. `requestId`, when set, is also sent as the props'
 * request_id (the slate the item came from).
 */
export function paywallViewed(p: EventBase & { itemId: string }): Event {
  if (blank(p.itemId)) bad(EVENT_PAYWALL_VIEWED, "itemId is required");
  return build(EVENT_PAYWALL_VIEWED, p, p.requestId ? { item_id: p.itemId, request_id: p.requestId } : { item_id: p.itemId });
}

/** `push_opened`: the user opened a push about `itemId` (the item's pullback_rate). */
export function pushOpened(p: EventBase & { itemId: string; notificationType?: string }): Event {
  if (blank(p.itemId)) bad(EVENT_PUSH_OPENED, "itemId is required");
  const props: Record<string, unknown> = { item_id: p.itemId };
  if (p.notificationType) props.notification_type = p.notificationType;
  return build(EVENT_PUSH_OPENED, p, props);
}

const QUERY_HASH = /^[0-9a-f]{16,128}$/;

/**
 * `search_performed`: never the query text. `queryHash` is a hex hash of the normalised query
 * you compute (e.g. HMAC-SHA256 with a key you hold), 16 to 128 lowercase hex characters, so
 * query text cannot travel here by mistake.
 */
export function searchPerformed(p: EventBase & { queryHash: string; resultCount?: number; clickedItemId?: string }): Event {
  const op = EVENT_SEARCH_PERFORMED;
  if (typeof p.queryHash !== "string" || !QUERY_HASH.test(p.queryHash)) {
    bad(op, "queryHash must be 16 to 128 lowercase hex characters (hash the query; never send its text)");
  }
  const props: Record<string, unknown> = { query_hash: p.queryHash };
  if (p.resultCount !== undefined) {
    if (!count(p.resultCount)) bad(op, "resultCount is a whole number, never negative");
    props.result_count = p.resultCount;
  }
  if (p.clickedItemId) props.clicked_item_id = p.clickedItemId;
  return build(op, p, props);
}

const KINDS = new Set(["document", "product", "community", "event", "tip"]);

/**
 * `item_engaged`: how long and how far the user read or watched an item. An engaged view is
 * `engagedSeconds >= 20` or `maxScrollPercent >= 60`. `kind` may be left out for a document.
 */
export function itemEngaged(
  p: EventBase & { itemId: string; kind?: ItemKind; engagedSeconds: number; maxScrollPercent: number },
): Event {
  const op = EVENT_ITEM_ENGAGED;
  if (blank(p.itemId)) bad(op, "itemId is required");
  if (p.kind !== undefined && !KINDS.has(p.kind)) bad(op, `unknown kind "${p.kind}"`);
  if (!count(p.engagedSeconds) || !count(p.maxScrollPercent) || p.maxScrollPercent > 100) {
    bad(op, "engagedSeconds is a whole number, never negative, and maxScrollPercent 0 to 100");
  }
  const props: Record<string, unknown> = { item_id: p.itemId, engaged_seconds: p.engagedSeconds, max_scroll_percent: p.maxScrollPercent };
  if (p.kind) props.kind = p.kind;
  return build(op, p, props);
}

/**
 * `item_completed`: the user finished a workout, a breathwork session, a protocol; the item then
 * stays out of their slates for exclude_consumed_days. A ranking event: sign it (a client with a
 * signing secret, or `signEvent`) when it does not come from an ingest key.
 */
export function itemCompleted(p: EventBase & { itemId: string; kind?: ItemKind; collection?: string }): Event {
  const op = EVENT_ITEM_COMPLETED;
  if (blank(p.itemId)) bad(op, "itemId is required");
  if (p.kind !== undefined && !KINDS.has(p.kind)) bad(op, `unknown kind "${p.kind}"`);
  const props: Record<string, unknown> = { item_id: p.itemId };
  if (p.kind) props.kind = p.kind;
  if (p.collection) props.collection = p.collection;
  return build(op, p, props);
}

/**
 * `order_placed`: product catalog ids and the value, never the customer. `orderIdHash` is a hash
 * of your order id. A ranking event: sign it when it does not come from an ingest key.
 */
export function orderPlaced(
  p: EventBase & { orderIdHash: string; productIds: string[]; valueCents?: number; currency?: string },
): Event {
  const op = EVENT_ORDER_PLACED;
  if (blank(p.orderIdHash) || !Array.isArray(p.productIds) || p.productIds.length === 0) {
    bad(op, "orderIdHash and at least one product id are required");
  }
  if (p.productIds.some((id) => blank(id))) bad(op, "product ids are never empty");
  const props: Record<string, unknown> = { order_id_hash: p.orderIdHash, product_ids: [...p.productIds] };
  if (p.valueCents !== undefined) {
    if (!count(p.valueCents)) bad(op, "valueCents is a whole number, never negative");
    props.value_cents = p.valueCents;
  }
  if (p.currency !== undefined) {
    if (!/^[A-Z]{3}$/.test(p.currency)) bad(op, "currency is an ISO 4217 code such as EUR");
    props.currency = p.currency;
  }
  return build(op, p, props);
}
