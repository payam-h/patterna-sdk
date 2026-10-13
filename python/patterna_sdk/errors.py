"""Typed errors, mirroring @patterna/sdk: every failure carries op, code, status and request id."""

from __future__ import annotations

from typing import Any


class PatternaError(Exception):
    def __init__(
        self,
        message: str,
        *,
        op: str,
        code: str,
        status: int | None = None,
        request_id: str | None = None,
        details: Any = None,
    ) -> None:
        super().__init__(message)
        self.op = op
        self.code = code
        self.status = status
        self.request_id = request_id
        self.details = details

    @property
    def transient(self) -> bool:
        return isinstance(self, (TimeoutError, NetworkError)) or (
            self.status is not None and self.status >= 500
        )


class ApiError(PatternaError):
    """The API answered with an error body; `code` is the API's stable code (e.g. pii_rejected)."""


class TimeoutError(PatternaError):
    pass


class NetworkError(PatternaError):
    pass


class ConsentError(PatternaError):
    pass


class ValidationError(PatternaError):
    pass


class ApiErrorCode:
    """API codes callers act on, and what `recommend_with_fallback` / `with_fallback` do with them.

    no_candidates (422)    fallback: nothing eligible to rank
    surface_off (409)      fallback: the kill switch
    unknown_surface (404)  fallback, but a configuration bug: alert on it
    unknown_request (404)  abandon / report_interactions named a request never logged
    unknown_item (404)     catalog calls
    pii_rejected (422)     re-raised: personal data in the request
    mode_conflict (422)    re-raised: the consent mode contradicts the stored one
    mode_required (422)    set_user_profile without a mode for a user Patterna has no row for: a
                           profile never creates consent; set the mode first (engine 0.3)
    not_shown (409)        abandon named a slate answered shown=false: hide the block, never
                           abandon it (engine 0.3)
    """

    NO_CANDIDATES = "no_candidates"
    SURFACE_OFF = "surface_off"
    UNKNOWN_SURFACE = "unknown_surface"
    UNKNOWN_REQUEST = "unknown_request"
    UNKNOWN_ITEM = "unknown_item"
    PII_REJECTED = "pii_rejected"
    MODE_CONFLICT = "mode_conflict"
    MODE_REQUIRED = "mode_required"
    NOT_SHOWN = "not_shown"


def is_api_error(err: BaseException, code: str) -> bool:
    """True when `err` is an answer from the API with the given code."""
    return isinstance(err, ApiError) and err.code == code


def is_caller_bug(err: BaseException) -> bool:
    """Errors no fallback may hide: consent and input bugs, pii_rejected, mode_conflict, mode_required."""
    return isinstance(err, (ConsentError, ValidationError)) or (
        isinstance(err, ApiError)
        and err.code in (ApiErrorCode.PII_REJECTED, ApiErrorCode.MODE_CONFLICT, ApiErrorCode.MODE_REQUIRED)
    )
