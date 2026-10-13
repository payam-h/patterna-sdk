"""patterna-sdk: Python client for the Patterna recommendation API."""

from .client import (
    DEFAULT_ADMIN_TIMEOUT_MS,
    DEFAULT_INGEST_TIMEOUT_MS,
    DEFAULT_RECOMMEND_DEADLINE_MS,
    DEFAULT_RECOMMEND_TIMEOUT_MS,
    MODES,
    ManyRequest,
    PatternaClient,
    Recommendation,
    assert_opaque_user_id,
    interaction_for,
    measured_context,
    new_request_id,
    rfc3339,
    with_fallback,
)
from .errors import (
    ApiError,
    ApiErrorCode,
    ConsentError,
    NetworkError,
    PatternaError,
    TimeoutError,
    ValidationError,
    is_api_error,
    is_caller_bug,
)
from .signing import assignment_unit, canonical_json, compute_event_signature, sign_event

__version__ = "0.2.0"

__all__ = [
    "DEFAULT_ADMIN_TIMEOUT_MS",
    "DEFAULT_INGEST_TIMEOUT_MS",
    "DEFAULT_RECOMMEND_DEADLINE_MS",
    "DEFAULT_RECOMMEND_TIMEOUT_MS",
    "MODES",
    "ApiError",
    "ApiErrorCode",
    "ConsentError",
    "ManyRequest",
    "NetworkError",
    "PatternaClient",
    "PatternaError",
    "Recommendation",
    "TimeoutError",
    "ValidationError",
    "assert_opaque_user_id",
    "assignment_unit",
    "canonical_json",
    "compute_event_signature",
    "interaction_for",
    "is_api_error",
    "is_caller_bug",
    "measured_context",
    "new_request_id",
    "rfc3339",
    "sign_event",
    "with_fallback",
]
