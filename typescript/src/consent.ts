import { ConsentError, ErrorCode, ValidationError } from "./errors.js";
import type { Mode } from "./types.js";

export const MODES: readonly Mode[] = ["personalized", "cohort", "non_profiled"];

export function isMode(value: unknown): value is Mode {
  return typeof value === "string" && (MODES as readonly string[]).includes(value);
}

/**
 * Throws unless `mode` is one of the three consent modes. Defaulting a missing mode would mean
 * guessing consent, so the SDK refuses instead.
 */
export function assertMode(mode: unknown, op: string): asserts mode is Mode {
  if (mode === undefined || mode === null || mode === "") {
    throw new ConsentError({
      code: ErrorCode.ConsentRequired,
      op,
      message: "consent mode is required (personalized, cohort or non_profiled)",
    });
  }
  if (!isMode(mode)) {
    throw new ConsentError({
      code: ErrorCode.ConsentRequired,
      op,
      message: `unknown consent mode "${String(mode)}"`,
    });
  }
}

/**
 * In `non_profiled` mode Patterna must never learn who the user is: the request carries a
 * rotating session id and context only.
 */
export function assertIdentityAllowed(
  mode: Mode,
  ids: { user_id?: string | undefined; session_id?: string | undefined },
  op: string,
): void {
  if (mode !== "non_profiled") return;
  if (ids.user_id !== undefined && ids.user_id !== "") {
    throw new ConsentError({
      code: ErrorCode.ConsentViolation,
      op,
      message: "non_profiled requests must not carry a user_id",
    });
  }
  if (!ids.session_id) {
    throw new ConsentError({
      code: ErrorCode.ConsentViolation,
      op,
      message: "non_profiled requests need a rotating session_id",
    });
  }
}

// Identity-provider subjects ("auth0|...", "google-oauth2|...") and emails are personal data. The
// contract wants a random per-tenant id minted by the customer; catch the common mistakes early.
const EMAIL_LIKE = /[^\s@]+@[^\s@]+/;
const IDP_SUBJECT = /^[a-z0-9-]+\|/i;

export function assertOpaqueUserId(userId: string | undefined, op: string): void {
  if (userId === undefined) return;
  if (userId.trim() === "") {
    throw new ValidationError({ code: ErrorCode.InvalidArgument, op, message: "user_id is empty" });
  }
  if (EMAIL_LIKE.test(userId) || IDP_SUBJECT.test(userId)) {
    // The id itself is deliberately left out of the message: it may be the PII we refuse.
    throw new ValidationError({
      code: ErrorCode.InvalidArgument,
      op,
      message:
        "user_id looks like an email or an identity-provider subject; send the random per-tenant id you minted instead",
    });
  }
}
