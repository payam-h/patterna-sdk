"""The cross-language vector: @patterna/sdk and the Go SDK produce the same signature."""

from datetime import datetime, timezone

from patterna_sdk import canonical_json, compute_event_signature, rfc3339, sign_event

VECTOR_SIGNATURE = "8baf729960f0d6390df4232b164a7bffab057e3c8a174148dedd0f324504aac9"
KEY = "test-signing-key"  # a fixture string, not a secret


def vector_event() -> dict:
    return {
        "event_id": "ev_42",
        "type": "rec_click",
        "occurred_at": rfc3339(datetime(2026, 10, 13, 9, 15, tzinfo=timezone.utc)),
        "source": "backend",
        "user_id": "u_7f3a",
        "session_id": "s_abc",
        "request_id": "req_1",
        "context": {"platform": "ios", "locale": "nl", "hour_bucket": 9},
        "props": {"position": 2, "item_id": "doc_1", "title": "<b>Zon & zee</b>\u2028x"},
    }


def test_canonical_json_matches_go_and_ts() -> None:
    assert canonical_json({"b": 1, "a": {"d": [3, 1], "c": None}}) == '{"a":{"c":null,"d":[3,1]},"b":1}'
    assert canonical_json("<a&b>\u2028") == '"<a&b>\\u2028"'
    assert canonical_json("Zon & zee ü") == '"Zon & zee ü"'


def test_vector_signature() -> None:
    assert compute_event_signature(vector_event(), KEY) == VECTOR_SIGNATURE
    stale = dict(vector_event(), signature="stale")
    assert compute_event_signature(stale, KEY) == VECTOR_SIGNATURE
    assert sign_event(vector_event(), KEY)["signature"] == VECTOR_SIGNATURE


def test_rfc3339_trims_like_go() -> None:
    assert rfc3339(datetime(2026, 10, 13, 9, 15, tzinfo=timezone.utc)) == "2026-10-13T09:15:00Z"
    assert rfc3339(datetime(2026, 10, 13, 9, 15, 0, 120000, tzinfo=timezone.utc)) == "2026-10-13T09:15:00.12Z"
