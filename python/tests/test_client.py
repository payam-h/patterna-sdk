import json

import pytest

from patterna_sdk import ApiError, ConsentError, PatternaClient, ValidationError, with_fallback


class FakeTransport:
    def __init__(self, responses):
        self.responses = list(responses)
        self.calls = []

    def __call__(self, method, url, headers, body, timeout_s):
        self.calls.append((method, url, headers, json.loads(body) if body else None, timeout_s))
        status, payload = self.responses.pop(0)
        return status, {"x-request-id": "req_x"}, json.dumps(payload).encode()


def client(*responses, **kw):
    t = FakeTransport(responses)
    return PatternaClient(
        "https://api.example/v1", serve_key="pk_s", ingest_key="pk_i", transport=t, retry_delay_ms=0, **kw
    ), t


def test_recommend_requires_consent_mode_and_ids() -> None:
    c, _ = client()
    with pytest.raises(ConsentError):
        c.recommend("home", mode="whatever", user_id="u1")
    with pytest.raises(ValidationError):
        c.recommend("home", mode="non_profiled")
    with pytest.raises(ValidationError):
        c.recommend("home", mode="personalized", user_id="someone@example.com")


def test_recommend_is_never_retried_and_uses_the_serve_key() -> None:
    c, t = client((503, {"error": {"code": "unavailable", "message": "down"}}))
    with pytest.raises(ApiError) as e:
        c.recommend("home", mode="personalized", user_id="u1", context={"locale": "nl"})
    assert e.value.code == "unavailable" and e.value.request_id == "req_x" and e.value.transient
    assert len(t.calls) == 1
    method, url, headers, body, timeout = t.calls[0]
    assert (method, url) == ("POST", "https://api.example/v1/recommendations")
    assert (
        headers["Authorization"] == "Bearer pk_s" and body["context"] == {"locale": "nl"} and timeout == 0.15
    )


def test_ingest_retries_once_on_5xx_and_signs_when_configured() -> None:
    c, t = client((500, {}), (200, {"accepted": 1}), signing_secret="test-signing-key")
    out = c.ingest_events(
        [
            {
                "event_id": "e1",
                "type": "item_viewed",
                "source": "backend",
                "user_id": "u1",
                "props": {"item_id": "d1"},
            }
        ]
    )
    assert out == {"accepted": 1} and len(t.calls) == 2
    ev = t.calls[1][3]["events"][0]
    assert ev["signature"] and ev["occurred_at"].endswith("Z")
    with pytest.raises(ValidationError):
        c.ingest_events(
            [{"event_id": "e2", "type": "item_viewed", "source": "backend", "context": {"ip": "1.2.3.4"}}]
        )


def test_with_fallback_reports_the_error() -> None:
    c, _ = client((404, {"error": {"code": "not_found", "message": "no surface"}}))
    slate, err = with_fallback(
        lambda: c.recommend("nope", mode="cohort", user_id="u1"), lambda: {"items": [], "fallback": True}
    )
    assert slate["fallback"] is True and err is not None and err.code == "not_found"
