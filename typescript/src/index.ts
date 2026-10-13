export {
  PatternaClient,
  DEFAULT_ADMIN_TIMEOUT_MS,
  DEFAULT_INGEST_TIMEOUT_MS,
  DEFAULT_RECOMMEND_DEADLINE_MS,
  DEFAULT_RECOMMEND_TIMEOUT_MS,
  interactionFor,
  markFills,
  measuredContext,
  validateSurfaceSpec,
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
export { coachProductShown, coachProductTapped, EVENT_COACH_PRODUCT_SHOWN, EVENT_COACH_PRODUCT_TAPPED } from "./coach.js";
export type { CoachProduct } from "./coach.js";
export {
  communityPostCreated,
  communityReaction,
  communityReply,
  EVENT_COMMUNITY_POST_CREATED,
  EVENT_COMMUNITY_REACTION,
  EVENT_COMMUNITY_REPLY,
  EVENT_ITEM_COMPLETED,
  EVENT_ITEM_ENGAGED,
  EVENT_ORDER_PLACED,
  EVENT_PAYWALL_VIEWED,
  EVENT_PUSH_OPENED,
  EVENT_SEARCH_PERFORMED,
  itemCompleted,
  itemEngaged,
  orderPlaced,
  paywallViewed,
  pushOpened,
  searchPerformed,
} from "./activity.js";
export type { CommunityPost, EventBase } from "./activity.js";
export {
  GENERATOR_KINDS,
  REASON_KEYS,
  REASON_RECOMMENDED,
  TIER_FREE,
  TIER_PREMIUM,
  timeContext,
  validateFlowConfig,
  validateItemAttributes,
} from "./signals.js";
export type { Difficulty, FlowFilters, GeneratorKind, ItemAttributes, ItemFormat, ReasonKey } from "./signals.js";
export type * from "./types.js";
