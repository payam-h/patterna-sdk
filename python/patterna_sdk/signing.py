"""Event signatures, identical to @patterna/sdk and the Go SDK (one shared test vector).

Canonical form: the event without `signature`, as JSON with object keys sorted at every level,
no whitespace, non-ASCII and HTML characters left as they are, U+2028 and U+2029 escaped.
The signature is the lowercase hex HMAC-SHA256 of that form, keyed with the UTF-8 bytes of the
secret string exactly as the API returned it (never hex-decoded; same in every SDK).
"""

from __future__ import annotations

import hashlib
import hmac
import json
from typing import Any


def canonical_json(value: Any) -> str:
    s = json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False, allow_nan=False)
    return s.replace("\u2028", "\\u2028").replace("\u2029", "\\u2029")


def _key(secret: str, what: str) -> bytes:
    if not isinstance(secret, str):
        raise TypeError(f"patterna: {what} must be the secret string as returned, not bytes")
    if not secret:
        raise ValueError(f"patterna: {what} is empty")
    return secret.encode("utf-8")


def compute_event_signature(event: dict[str, Any], secret: str) -> str:
    body = {k: v for k, v in event.items() if k != "signature"}
    return hmac.new(
        _key(secret, "signing secret"), canonical_json(body).encode("utf-8"), hashlib.sha256
    ).hexdigest()


def assignment_unit(secret: str, user_id: str) -> str:
    """The value for `assignment_unit` (engine 0.2): hex HMAC-SHA256 of user_id under your secret.

    Keeps one non-profiled person in one holdout arm across rotating sessions. The secret is yours,
    per experiment, and never sent. Same function and test vector in the Go and TypeScript SDKs.
    """
    return hmac.new(_key(secret, "assignment secret"), user_id.encode("utf-8"), hashlib.sha256).hexdigest()


def sign_event(event: dict[str, Any], secret: str) -> dict[str, Any]:
    """Return a copy of the event with `signature` set."""
    out = dict(event)
    out["signature"] = compute_event_signature(event, secret)
    return out
