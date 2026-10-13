/**
 * Every failure the SDK surfaces is a PatternaError with a stable `code`. API errors carry the
 * code the API returned (`pii_rejected`, `mode_conflict`, ...) so a panel or a log line can show
 * the same string the engine logged; transport failures use the SDK's own codes below.
 */

export const ErrorCode = {
  /** The per-request budget ran out before a response arrived. */
  Timeout: "timeout",
  /** fetch rejected: DNS, TLS, connection reset, offline. */
  Network: "network_error",
  /** The caller's AbortSignal fired. */
  Aborted: "aborted",
  /** A consent rule was broken before the request left the process. */
  ConsentRequired: "consent_required",
  ConsentViolation: "consent_violation",
  /** The input breaks the contract (size limits, unknown context keys, PII-shaped ids). */
  InvalidArgument: "invalid_argument",
  /** No key or auth header configured for the scope the call needs. */
  NotConfigured: "not_configured",
  /** The response body was not the JSON the contract promises. */
  BadResponse: "bad_response",
} as const;

export type SdkErrorCode = (typeof ErrorCode)[keyof typeof ErrorCode];

/**
 * API codes with a meaning callers act on, and what {@link PatternaClient.recommendWithFallback}
 * and {@link withFallback} do with them:
 *
 * | code | status | in a fallback helper |
 * |---|---|---|
 * | `no_candidates` | 422 | fallback: nothing eligible to rank |
 * | `surface_off` | 409 | fallback: the kill switch |
 * | `unknown_surface` | 404 | fallback, but a configuration bug: alert on it |
 * | `unknown_request` | 404 | `abandon` / `reportInteractions` named a request never logged |
 * | `unknown_item` | 422 | `reportInteractions` named an item that was not in the slate served for that request (`details.unknown_item_ids`) |
 * | `item_not_found` | 404 | a catalog call (`getItem`, `deleteItem`, `setItemStatus`, `setItemTags`) named an item that does not exist for that id and kind |
 * | `pii_rejected` | 422 | rethrown: personal data in the request |
 * | `mode_conflict` | 422 | rethrown: the consent mode contradicts the stored one |
 * | `mode_required` | 422 | `setUserProfile` without `mode` for a user Patterna has no row for (engine 0.3) |
 * | `not_shown` | 409 | `abandon` named a slate answered `shown: false`: hide the block, never abandon (engine 0.3) |
 */
export const ApiErrorCode = {
  NoCandidates: "no_candidates",
  SurfaceOff: "surface_off",
  UnknownSurface: "unknown_surface",
  UnknownRequest: "unknown_request",
  UnknownItem: "unknown_item",
  ItemNotFound: "item_not_found",
  PiiRejected: "pii_rejected",
  ModeConflict: "mode_conflict",
  ModeRequired: "mode_required",
  NotShown: "not_shown",
} as const;

export type ApiErrorCodeValue = (typeof ApiErrorCode)[keyof typeof ApiErrorCode];

export interface PatternaErrorInit {
  code: string;
  message: string;
  op: string;
  status?: number;
  requestId?: string;
  details?: Record<string, unknown>;
  cause?: unknown;
}

export class PatternaError extends Error {
  /** Stable machine code: the API's `code` or one of {@link ErrorCode}. */
  readonly code: string;
  /** The SDK operation, e.g. `recommend`, so a log line says which call failed. */
  readonly op: string;
  /** HTTP status when the API answered. */
  readonly status: number | undefined;
  /** The API's request id (body `request_id`, else the `X-Request-Id` header). */
  readonly requestId: string | undefined;
  readonly details: Record<string, unknown> | undefined;

  constructor(init: PatternaErrorInit) {
    super(init.message, init.cause === undefined ? undefined : { cause: init.cause });
    this.name = new.target.name;
    this.code = init.code;
    this.op = init.op;
    this.status = init.status;
    this.requestId = init.requestId;
    this.details = init.details;
  }

  /** True when retrying later may succeed (the service, not the request, was the problem). */
  get transient(): boolean {
    return (
      this.code === ErrorCode.Timeout ||
      this.code === ErrorCode.Network ||
      (this.status !== undefined && (this.status >= 500 || this.status === 429))
    );
  }
}

/** The API answered with a non-2xx status. `code` is the API's code. */
export class ApiError extends PatternaError {}

/** The request did not finish within its budget. */
export class TimeoutError extends PatternaError {}

/** The request never reached the API or the connection broke. */
export class NetworkError extends PatternaError {}

/**
 * A consent rule would have been broken: no mode, or a user id in `non_profiled` mode. These are
 * programming errors, never swallowed by {@link withFallback}: a consent bug is a privacy bug.
 */
export class ConsentError extends PatternaError {}

/** The input breaks the contract and was not sent. Also never swallowed by withFallback. */
export class ValidationError extends PatternaError {}

export function isPatternaError(err: unknown): err is PatternaError {
  return err instanceof PatternaError;
}

/** True when `err` is an answer from the API with the given code, e.g. `isApiError(e, ApiErrorCode.SurfaceOff)`. */
export function isApiError(err: unknown, code: ApiErrorCodeValue | string): err is ApiError {
  return err instanceof ApiError && err.code === code;
}

/**
 * Errors no fallback may hide: consent and input bugs, and the API's `pii_rejected`,
 * `mode_conflict` and `mode_required`. A consent bug is a privacy bug.
 */
export function isCallerBug(err: unknown): boolean {
  return (
    err instanceof ConsentError ||
    err instanceof ValidationError ||
    isApiError(err, ApiErrorCode.PiiRejected) ||
    isApiError(err, ApiErrorCode.ModeConflict) ||
    isApiError(err, ApiErrorCode.ModeRequired)
  );
}
