"""0.2.0: request ids, abandonment, deadlines, assignment units, interactions, catalog, admin."""

from __future__ import annotations

import json
import re
import threading
import time
from typing import Any

import pytest

from patterna_sdk import (
    ApiError,
    ApiErrorCode,
    ConsentError,
    ManyRequest,
    PatternaClient,
    PatternaError,
    ValidationError,
    assignment_unit,
    compute_event_signature,
    interaction_for,
    is_api_error,
    measured_context,
    with_fallback,
)

# Cross-language vector, shared with packages/sdk-go/v02_test.go and packages/sdk-ts/test/v02.test.ts.
ASSIGNMENT_KEY = "test-assignment-key"  # a fixture string, not a secret
ASSIGNMENT_VECTOR = "fd548dfa1627af2ab350f8cebe237014d29004eb6dfb8cfa79cb9c24adbdda42"
UUID4 = re.compile(r"^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$")

SLATE = {
    "request_id": "req_1",
    "surface": "home_articles",
    "arm": "none",
    "policy_version": "p",
    "items": [
        {"item_id": "doc_1", "kind": "document", "position": 0, "reason_key": "new", "reason": {}},
        {"item_id": "com_1", "kind": "community", "position": 1, "reason_key": "new", "reason": {}},
    ],
}

Reply = Any  # (status, body) or "hang"


class Routed:
    """A transport answering by path; "hang" raises the builtin TimeoutError like urllib does."""

    def __init__(self, route) -> None:
        self.route = route
        self.calls: list[dict[str, Any]] = []
        self.lock = threading.Lock()

    def __call__(self, method, url, headers, body, timeout_s):
        b = json.loads(body) if body else None
        with self.lock:
            self.calls.append(
                {"method": method, "url": url, "headers": dict(headers), "body": b, "timeout": timeout_s}
            )
        r = self.route(url, b)
        if r == "hang":
            time.sleep(timeout_s)
            raise TimeoutError("timed out")
        status, payload = r
        return status, {}, (json.dumps(payload).encode() if payload is not None else b"")

    def to(self, suffix: str) -> list[dict[str, Any]]:
        return [c for c in self.calls if c["url"].split("?")[0].endswith(suffix)]


def make(route, **kw) -> tuple[PatternaClient, Routed]:
    t = Routed(route)
    c = PatternaClient(
        "https://api.example/v1",
        serve_key="pk_s",
        ingest_key="pk_i",
        transport=t,
        retry_delay_ms=0,
        **kw,
    )
    return c, t


def always(status: int, body: Any = None):
    return lambda url, b: (status, body)


REQ = {"mode": "cohort", "user_id": "u_1", "context": {"locale": "nl"}}


def test_assignment_unit_vector_and_shape() -> None:
    assert assignment_unit(ASSIGNMENT_KEY, "u_7f3a") == ASSIGNMENT_VECTOR
    with pytest.raises(TypeError):
        assignment_unit(b"bytes", "u")  # type: ignore[arg-type]
    c, t = make(always(200, SLATE))
    c.recommend("home", assignment_unit=ASSIGNMENT_VECTOR, **REQ)
    assert t.calls[0]["body"]["assignment_unit"] == ASSIGNMENT_VECTOR
    with pytest.raises(ValidationError):
        c.recommend("home", assignment_unit="u_7f3a", **REQ)
    assert len(t.calls) == 1


def test_signing_takes_the_secret_string_only() -> None:
    with pytest.raises(TypeError):
        compute_event_signature({"event_id": "e"}, b"test-signing-key")  # type: ignore[arg-type]


def test_headers_request_id_deadline_trace_tester() -> None:
    c, t = make(always(200, dict(SLATE, request_id="")), trace_id=lambda: "from-client")
    s = c.recommend("home", **REQ)
    h = t.calls[0]["headers"]
    assert UUID4.match(h["X-Request-Id"]) and s["request_id"] == h["X-Request-Id"]
    assert h["X-Deadline-Ms"] == "120" and h["X-Trace-Id"] == "from-client" and "X-Patterna-Tester" not in h
    c.recommend("home", request_id="req_mine", trace_id="tr", tester=True, timeout_ms=80, **REQ)
    h = t.calls[1]["headers"]
    assert (h["X-Request-Id"], h["X-Trace-Id"], h["X-Patterna-Tester"], h["X-Deadline-Ms"]) == (
        "req_mine",
        "tr",
        "1",
        "50",
    )
    c.recommend("home", request_id="has space", **REQ)
    assert UUID4.match(t.calls[2]["headers"]["X-Request-Id"])
    c.upsert_items([])
    assert "X-Deadline-Ms" not in t.calls[3]["headers"] and t.calls[3]["headers"]["X-Request-Id"]


def test_deadline_config() -> None:
    with pytest.raises(ValueError):
        PatternaClient("https://x/v1", recommend_timeout_ms=100, recommend_deadline_ms=100)
    assert PatternaClient("https://x/v1", recommend_timeout_ms=100).recommend_deadline_ms == 80


def test_timeout_error_carries_the_sent_request_id() -> None:
    c, _ = make(lambda url, b: "hang", recommend_timeout_ms=20)
    with pytest.raises(PatternaError) as e:
        c.recommend("home", request_id="req_t", **REQ)
    assert e.value.code == "timeout" and e.value.request_id == "req_t"


@pytest.mark.parametrize(
    ("reply", "reason", "code"),
    [
        ("hang", "timeout", "timeout"),
        ((503, {"code": "unavailable"}), "server_error", "unavailable"),
        ((422, {"code": "no_candidates"}), None, "no_candidates"),
        ((409, {"code": "surface_off"}), None, "surface_off"),
        ((404, {"code": "unknown_surface"}), None, "unknown_surface"),
    ],
)
def test_fallback_and_abandon(reply, reason, code) -> None:
    def route(url, b):
        return (202, None) if url.endswith("/abandon") else reply

    c, t = make(route, recommend_timeout_ms=20)
    r = c.recommend_with_fallback("home", lambda: ["own_1"], trace_id="tr", **REQ)
    c.close()
    assert r.source == "fallback" and r.value == ["own_1"] and r.error is not None and r.error.code == code
    sent = t.calls[0]["headers"]["X-Request-Id"]
    assert r.request_id == sent
    ab = t.to("/abandon")
    if reason is None:
        assert ab == [] and not r.abandoned
        return
    assert r.abandoned and len(ab) == 1
    assert ab[0]["url"] == f"https://api.example/v1/recommendations/{sent}/abandon"
    assert ab[0]["body"] == {"reason": reason}
    assert ab[0]["headers"]["Authorization"] == "Bearer pk_s" and ab[0]["headers"]["X-Trace-Id"] == "tr"


def test_fallback_answers_and_raises_caller_bugs() -> None:
    c, _ = make(always(200, SLATE))
    r = c.recommend_with_fallback("home", list, **REQ)
    assert (r.source, r.request_id, r.value["items"][0]["item_id"]) == ("patterna", "req_1", "doc_1")
    for code in ("mode_conflict", "pii_rejected"):
        c, _ = make(always(422, {"code": code}))
        with pytest.raises(ApiError):
            c.recommend_with_fallback("home", list, **REQ)
        with pytest.raises(ApiError):
            with_fallback(lambda c=c: c.recommend("home", **REQ), dict)
    with pytest.raises(ConsentError):
        c.recommend_with_fallback("home", list, mode="non_profiled", user_id="u", session_id="s")


def test_background_abandon_errors_are_reported() -> None:
    def route(url, b):
        return (404, {"code": "unknown_request"}) if url.endswith("/abandon") else (500, {})

    seen: list[PatternaError] = []
    c, _ = make(route, on_background_error=seen.append)
    c.recommend_with_fallback("home", list, **REQ)
    c.close()
    assert len(seen) == 1 and is_api_error(seen[0], ApiErrorCode.UNKNOWN_REQUEST)


def test_abandon_manual() -> None:
    c, t = make(always(202))
    c.abandon("req_9")
    assert t.calls[0]["body"] == {"reason": "client_fallback"}
    with pytest.raises(ValidationError):
        c.abandon("")
    with pytest.raises(ValidationError):
        c.abandon("req_9", "because")


def test_recommend_many_shares_one_budget() -> None:
    def route(url, b):
        if url.endswith("/abandon"):
            return (202, None)
        return "hang" if b and b["surface"] == "home_products" else (200, SLATE)

    c, t = make(route, recommend_timeout_ms=60)
    start = time.monotonic()
    out = c.recommend_many(
        [
            ManyRequest("home_articles", lambda: ["own_a"], REQ),
            ManyRequest("home_products", lambda: ["own_p"], REQ),
        ],
        tester=True,
        request_id="ignored",
    )
    elapsed = time.monotonic() - start
    c.close()
    assert elapsed < 0.3
    assert out[0].source == "patterna" and out[1].source == "fallback" and out[1].abandoned
    recs = t.to("/recommendations")
    assert all(x["headers"]["X-Patterna-Tester"] == "1" for x in recs)
    assert len({x["headers"]["X-Request-Id"] for x in recs}) == 2
    assert "ignored" not in {x["headers"]["X-Request-Id"] for x in recs}


def test_interaction_kind_block_positions() -> None:
    c, t = make(always(200, SLATE))
    c.recommend("home", **REQ)
    with pytest.raises(ValidationError, match="kind is required for community"):
        c.report_interactions("req_1", [{"item_id": "com_1", "position": 1, "type": "click"}])
    c.report_interactions(
        "req_1",
        [
            {"item_id": "doc_1", "position": 0, "type": "seen"},
            {
                "item_id": "com_1",
                "kind": "community",
                "position": 1,
                "displayed_position": 0,
                "block": "top",
                "type": "click",
            },
        ],
    )
    sent = t.calls[1]["body"]["interactions"][1]
    assert (sent["kind"], sent["displayed_position"], sent["block"]) == ("community", 0, "top")
    c.report_interactions("req_other", [{"item_id": "com_1", "position": 1, "type": "seen"}])
    base = {"item_id": "d", "position": 0, "type": "seen"}
    for bad in ({"kind": "group"}, {"displayed_position": -1}, {"block": "b" * 65}, {"item_id": ""}):
        with pytest.raises(ValidationError):
            c.report_interactions("r", [{**base, **bad}])


def test_slate_memory_is_bounded() -> None:
    c, _ = make(always(200, SLATE), slate_memory=1)
    c.recommend("home", **REQ)
    c._remember(dict(SLATE, request_id="req_2"))
    assert list(c._kinds) == ["req_2"]


def test_interaction_for_and_measured() -> None:
    assert interaction_for(SLATE, "com_1", "click", block="end_carousel") == {
        "item_id": "com_1",
        "kind": "community",
        "position": 1,
        "type": "click",
        "block": "end_carousel",
    }
    assert interaction_for(SLATE, "missing", "click") is None
    c, t = make(always(200, SLATE))
    c.recommend("home", mode="cohort", user_id="u_1", context=measured_context({"locale": "nl"}, False))
    assert t.calls[0]["body"]["context"] == {"locale": "nl", "measured": False}


def test_catalog_delete_and_changed_since() -> None:
    pages = iter(
        [
            (200, {"items": [{"external_id": "a"}, {"external_id": "b"}], "next_cursor": "c2"}),
            (200, {"items": [{"external_id": "c"}], "next_cursor": None}),
        ]
    )
    c, t = make(lambda url, b: next(pages))
    ids = [it["external_id"] for it in c.items_changed_since("2026-10-01T00:00:00Z", kind="document")]
    assert ids == ["a", "b", "c"]
    assert t.calls[0]["url"] == (
        "https://api.example/v1/catalog/items?kind=document&changed_since=2026-10-01T00%3A00%3A00Z&limit=500"
    )
    assert "cursor=c2" in t.calls[1]["url"]

    c, t = make(always(404, {"code": "item_not_found"}))
    with pytest.raises(ValidationError):
        c.delete_item("x", kind="thing")
    with pytest.raises(ApiError) as e:
        c.delete_item("c/1", kind="community")
    assert e.value.code == ApiErrorCode.ITEM_NOT_FOUND
    assert not is_api_error(e.value, ApiErrorCode.UNKNOWN_ITEM)
    assert t.calls[0]["method"] == "DELETE"
    assert t.calls[0]["url"] == "https://api.example/v1/catalog/items/c%2F1?kind=community"
    assert t.calls[0]["headers"]["Authorization"] == "Bearer pk_i"


ITEM_42 = {
    "external_id": "42",
    "kind": "product",
    "title": "Magnesium",
    "locale": "nl",
    "status": "published",
    "premium": False,
    "updated_at": "2026-10-13T08:00:00Z",
    "attributes": {"price_cents": 1995, "currency": "EUR", "available": True, "format": "product"},
    "tags": [{"tag": "sleep", "weight": 0.8, "source": "enrich", "approved": True}],
    "enrichment": {"status": "done", "model": "claude-haiku-5-5", "updated_at": "2026-10-13T08:00:00Z"},
}


def test_get_item_reads_one_item_under_the_ingest_key() -> None:
    c, t = make(always(200, ITEM_42))
    item = c.get_item("product", "42")
    assert item == ITEM_42
    assert item["attributes"]["available"] is True
    assert item["tags"][0]["source"] == "enrich"
    assert item["enrichment"]["status"] == "done"
    assert t.calls[0]["method"] == "GET"
    assert t.calls[0]["url"] == "https://api.example/v1/catalog/items/42?kind=product"
    assert t.calls[0]["headers"]["Authorization"] == "Bearer pk_i"
    assert "X-Deadline-Ms" not in t.calls[0]["headers"]
    # the id is path-escaped; bad input never reaches the wire
    c.get_item("community", "c/1")
    assert t.calls[1]["url"] == "https://api.example/v1/catalog/items/c%2F1?kind=community"
    with pytest.raises(ValidationError):
        c.get_item("thing", "x")
    with pytest.raises(ValidationError):
        c.get_item("product", "")
    assert len(t.calls) == 2


def test_get_item_not_found_and_retry() -> None:
    c, t = make(always(404, {"code": "item_not_found", "message": "no item with that id and kind"}))
    with pytest.raises(ApiError) as e:
        c.get_item("product", "gone")
    assert e.value.code == ApiErrorCode.ITEM_NOT_FOUND
    assert e.value.status == 404
    assert e.value.op == "get_item"
    assert is_api_error(e.value, ApiErrorCode.ITEM_NOT_FOUND)
    assert not is_api_error(e.value, ApiErrorCode.UNKNOWN_ITEM)
    assert len(t.calls) == 1  # a 404 is not retried

    replies = iter([(503, {"code": "unavailable"}), (200, {**ITEM_42, "status": "archived"}), (200, {"ok": True})])
    c, t = make(lambda url, b: next(replies))
    assert c.get_item("product", "42")["status"] == "archived"
    assert len(t.calls) == 2  # one retry on 5xx
    with pytest.raises(PatternaError) as e2:
        c.get_item("product", "42")
    assert e2.value.code == "bad_response"


CFG_A = {
    "generators": [{"kind": "item_to_item", "weight": 1}],
    "ranker": {"kind": "linear"},
    "exploration": {"kind": "none"},
}
CFG_A2 = {
    "exploration": {"kind": "none"},
    "ranker": {"kind": "linear"},
    "generators": [{"weight": 1, "kind": "item_to_item"}],
}


class FakeAdmin:
    """In-memory surfaces, flows and experiments."""

    def __init__(self) -> None:
        self.surfaces = {"s": {"key": "s", "status": "shadow", "active_flow_version": None}}
        self.flows: list[dict[str, Any]] = []
        self.clock = 0

    def __call__(self, url: str, b: Any) -> Reply:
        path = url.split("/v1", 1)[1]
        if path == "/surfaces":
            return 200, list(self.surfaces.values())
        if path == "/surfaces/s/flows" and b is None:
            return 200, self.flows
        if path == "/surfaces/s/flows":
            f = {"version": len(self.flows) + 1, "config": b["config"], "activated_at": None}
            self.flows.append(f)
            return 201, f
        m = re.match(r"^/surfaces/s/flows/(\d+)/activate$", path)
        if m:
            self.clock += 1
            v = int(m.group(1))
            self.flows[v - 1]["activated_at"] = f"2026-10-13T10:00:{self.clock:02d}Z"
            self.surfaces["s"]["active_flow_version"] = v
            return 200, {"ok": True}
        if path == "/surfaces/s":
            self.surfaces["s"].update(b)
            return 200, self.surfaces["s"]
        return 404, {"code": "not_found"}


def test_admin_flows() -> None:
    fake = FakeAdmin()
    c, t = make(fake, admin_key="pk_a")
    assert c.apply_flow("s", CFG_A) == {
        "surface": "s",
        "version": 1,
        "created": True,
        "activated": True,
        "unchanged": False,
    }
    assert c.apply_flow("s", CFG_A2)["unchanged"]
    cfg_b = dict(CFG_A, exploration={"kind": "plackett_luce"})
    assert c.plan_flow("s", cfg_b, False) == {
        "surface": "s",
        "version": 0,
        "created": True,
        "activated": False,
        "unchanged": False,
    }
    assert c.apply_flow("s", cfg_b, note="ci", activate=False)["version"] == 2
    assert c.apply_flow("s", cfg_b)["activated"]
    assert c.get_flow("s")["version"] == 2  # type: ignore[index]
    assert c.rollback_flow("s") == 1
    assert c.get_flow("s", 2)["config"] == cfg_b  # type: ignore[index]
    assert all(x["headers"]["Authorization"] == "Bearer pk_a" for x in t.calls)
    with pytest.raises(ApiError) as e:
        c.get_surface("nope")
    assert e.value.code == "unknown_surface"


def test_admin_surfaces_experiments_ledger_secret() -> None:
    fake = FakeAdmin()
    c, t = make(fake, admin_key="pk_a")
    assert c.set_surface_status("s", "off")["status"] == "off"
    assert c.set_surface_testers("s", ["u_1"])["testers"] == ["u_1"]
    with pytest.raises(ValidationError):
        c.set_surface_status("s", "on")
    with pytest.raises(ValidationError):
        c.set_surface_testers("s", ["anna@example.com"])

    c, t = make(always(200, [{"job_id": "x"}]), admin_key="pk_a")
    c.ledger(surface="s", date_from="2026-09-01", date_to="2026-10-01")
    c.ledger(surface="s", weekly=True)
    c.list_experiments()
    assert [x["url"].split("/v1", 1)[1] for x in t.calls] == [
        "/proof/ledger?surface=s&from=2026-09-01&to=2026-10-01",
        "/proof/ledger?surface=s&bucket=week",
        "/experiments",
    ]
    c, t = make(
        always(200, {"job_id": "job_1", "status": "done", "signing_secret": "abc", "id": "exp_1"}),
        admin_key="pk_a",
    )
    assert c.start_experiment({"surface": "s", "name": "h", "primary_metric": "tap_rate"})["id"] == "exp_1"
    c.end_experiment("exp 1")
    assert c.export_ledger("2026-09-01", "2026-10-01", surface="s")["job_id"] == "job_1"
    assert c.export_status("job_1")["status"] == "done"
    assert c.create_signing_secret()["signing_secret"] == "abc"
    assert [(x["method"], x["url"].split("/v1", 1)[1]) for x in t.calls] == [
        ("POST", "/experiments"),
        ("POST", "/experiments/exp%201/end"),
        ("POST", "/proof/export"),
        ("GET", "/proof/exports/job_1"),
        ("POST", "/settings/signing-secret"),
    ]
    with pytest.raises(ValidationError):
        c.start_experiment({"surface": "s"})
    with pytest.raises(PatternaError) as e:
        c.list_surfaces()  # an object where a list is due
    assert e.value.code == "bad_response"
    no_admin, _ = make(always(200, []))
    with pytest.raises(ValidationError):
        no_admin.create_signing_secret()
