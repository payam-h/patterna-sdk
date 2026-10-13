export {
  PatternaClient,
  DEFAULT_ADMIN_TIMEOUT_MS,
  DEFAULT_INGEST_TIMEOUT_MS,
  DEFAULT_RECOMMEND_DEADLINE_MS,
  DEFAULT_RECOMMEND_TIMEOUT_MS,
  interactionFor,
  measuredContext,
} from "./client.js";
export type { ManyRequest, PatternaClientOptions, Recommendation } from "./client.js";
export type { FetchLike } from "./http.js";
export { newRequestId } from "./http.js";
export {
  ApiError,
  ApiErrorCode,
  ConsentError,
  ErrorCode,
  NetworkError,
  PatternaError,
  TimeoutError,
  ValidationError,
  isApiError,
  isCallerBug,
  isPatternaError,
} from "./errors.js";
export type { ApiErrorCodeValue, SdkErrorCode } from "./errors.js";
export { assertOpaqueUserId, isMode, MODES } from "./consent.js";
export { EventQueue } from "./queue.js";
export type { DropReason, EventQueueOptions, FlushResult, QueueStorage } from "./queue.js";
export { withFallback } from "./fallback.js";
export type { FallbackResult, WithFallbackOptions } from "./fallback.js";
export { assignmentUnit, canonicalJson, computeEventSignature, signEvent } from "./signing.js";
export { newSessionId } from "./session.js";
export type * from "./types.js";
