import { assertIdentityAllowed, assertMode, assertOpaqueUserId } from "./consent.js";
import { ErrorCode, ValidationError } from "./errors.js";
import { newRequestId } from "./http.js";
import type { Event, EventContext, Mode } from "./types.js";

/** Event types for products an AI coach answer named (engine 0.3, contracts/events.md). */
export const EVENT_COACH_PRODUCT_SHOWN = "coach_product_shown";
export const EVENT_COACH_PRODUCT_TAPPED = "coach_product_tapped";

/**
 * One product an LLM answer named and the app showed under the answer. No slate and no
 * propensity: the events are a learning signal and a descriptive count, never in the ledger.
 */
export interface CoachProduct {
  /** The user's consent mode; it decides the identity the event carries. */
  mode: Mode;
  /** personalized and cohort: your random per-tenant id (required there). */
  userId?: string;
  /** non_profiled: the rotating session id the app sends with recommendations (required there). */
  sessionId?: string;
  /** The product's catalog external_id. */
  productId: string;
  /** Your own opaque id for the conversation, never message text. */
  conversationId: string;
  /** The product's 0-based place among the answer's products. */
  position: number;
  /** Defaults to now. */
  occurredAt?: string | Date;
  /** Defaults to a fresh UUID. */
  eventId?: string;
  /** Defaults to "backend". */
  source?: Event["source"];
  context?: EventContext;
}

/** A `coach_product_shown` event for `ingestEvents`, with the consent rules applied. */
export function coachProductShown(p: CoachProduct): Event {
  return coachEvent(EVENT_COACH_PRODUCT_SHOWN, p);
}

/** A `coach_product_tapped` event (a positive signal): the user tapped such a product. */
export function coachProductTapped(p: CoachProduct): Event {
  return coachEvent(EVENT_COACH_PRODUCT_TAPPED, p);
}

function coachEvent(type: string, p: CoachProduct): Event {
  const op = type;
  assertMode((p as { mode?: unknown }).mode, op);
  assertIdentityAllowed(p.mode, { user_id: p.userId, session_id: p.sessionId }, op);
  assertOpaqueUserId(p.userId, op);
  const invalid = (message: string): never => {
    throw new ValidationError({ code: ErrorCode.InvalidArgument, op, message });
  };
  if (p.mode !== "non_profiled" && !p.userId) invalid(`${p.mode} events need the user id`);
  if (!p.productId?.trim() || !p.conversationId?.trim()) invalid("productId and conversationId are required");
  if (!Number.isInteger(p.position) || p.position < 0) invalid("position is 0-based and never negative");
  const at = p.occurredAt ?? new Date();
  const e: Event = {
    event_id: p.eventId ?? newRequestId(),
    type,
    occurred_at: typeof at === "string" ? at : at.toISOString(),
    source: p.source ?? "backend",
    props: { product_id: p.productId, conversation_id: p.conversationId, position: p.position },
  };
  if (p.mode === "non_profiled") e.session_id = p.sessionId as string;
  else e.user_id = p.userId as string;
  if (p.context) e.context = p.context;
  return e;
}
