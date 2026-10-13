"""0.3.0: engine 0.3 fields, shown=false never abandoned, coach events, surface files, settings."""

from __future__ import annotations

import json
import threading
from typing import Any
from urllib.parse import parse_qs, urlsplit

import pytest

from patterna_sdk import (
    CONTROL_SOURCE_BY_ANCHOR,
    CONTROL_SOURCE_REQUEST,
    EVENT_COACH_PRODUCT_SHOWN,
    EVENT_COACH_PRODUCT_TAPPED,
    ApiError,
    ApiErrorCode,
    ConsentError,
    ManyRequest,
    PatternaClient,
    PatternaError,
    ValidationError,
    __version__,
    coach_product_shown,
    coach_product_tapped,
    is_api_error,
    is_caller_bug,
    slate_visible,
)

BASE = "https://api.example/v1"


class FakeEngine:
    """In-memory engine: recommend honours min_slots (shown=false, no items), abandon answers
    409 not_shown for such a slate, plus the admin endpoints the 0.3 API calls."""

    def __init__(self) -> None:
        self.lock = threading.Lock()
        self.calls: list[tuple[str, str, Any]] = []
        self.surfaces: dict[str, dict[str, Any]] = {
            "home_articles": {
                "key": "home_articles",
                "status": "shadow",
                "item_kinds": ["document"],
                "slots": 5,
                "fallback": {"kind": "newest"},
                "min_slots": 0,
                "device_resort": False,
                "notes": "",
                "exploration_share": 0.1,
                "active_flow_version": None,
            }
        }
        self.items: dict[str, list[str]] = {"home_articles": []}
        self.served: dict[str, bool] = {}
        self.abandoned: dict[str, str] = {}
        self.settings: dict[str, Any] = {"slug": "fake", "phase_tags": False}

    def __call__(self, method, url, headers, body, timeout_s):
        b = json.loads(body) if body else None
        parts = urlsplit(url)
        path = parts.path.removeprefix("/v1")
        q = {k: v[0] for k, v in parse_qs(parts.query).items()}
        with self.lock:
            self.calls.append((method, path + (f"?{parts.query}" if parts.query else ""), b))
            status, payload = self.route(method, path, q, b, headers)
        return status, {}, json.dumps(payload).encode()

    def route(self, method, path, q, b, headers):
        seg = path.strip("/").split("/")
        if seg[0] == "recommendations":
            if method == "POST" and len(seg) == 1:
                sf = self.surfaces.get(b["surface"])
                if sf is None:
                    return 404, {"code": "unknown_surface"}
                rid = headers["X-Request-Id"]
                excluded = set(b.get("exclude_item_ids") or [])
                eligible = [i for i in self.items[sf["key"]] if i not in excluded]
                shown = sf["min_slots"] <= 0 or len(eligible) >= sf["min_slots"]
                self.served[rid] = shown
                out: dict[str, Any] = {
                    "request_id": rid,
                    "surface": sf["key"],
                    "arm": "treatment",
                    "policy_version": "fake",
                    "shown": shown,
                    "items": [],
                }
                if not shown:
                    return 200, out
                ids = eligible
                ctl = (b.get("context") or {}).get("control_item_ids")
                if ctl:
                    ids = ctl
                    out.update(arm="control", policy_version="editorial", control_source="request")
                out["items"] = [
                    {"item_id": i, "kind": "document", "position": n, "reason_key": "new", "reason": {}}
                    for n, i in enumerate(ids[: b.get("slots") or sf["slots"]])
                ]
                return 200, out
            if method == "POST" and len(seg) == 3 and seg[2] == "abandon":
                if seg[1] not in self.served:
                    return 404, {"code": "unknown_request"}
                if not self.served[seg[1]]:
                    return 409, {"code": "not_shown", "message": "hide the block"}
                self.abandoned[seg[1]] = b["reason"]
                return 200, {"request_id": seg[1], "client_abandoned": True}
        if path == "/surfaces" and method == "GET":
            return 200, list(self.surfaces.values())
        if seg[0] == "surfaces" and len(seg) == 2 and method == "PATCH":
            sf = self.surfaces.get(seg[1])
            if sf is None:
                return 404, {"code": "unknown_surface"}
            sf.update(b)
            return 200, {"key": seg[1], "ok": True}  # like the engine: not the surface
        if seg[0] == "surfaces" and len(seg) == 3 and seg[2] == "metrics":
            day = {
                "date": "2026-10-12",
                "impressions": 10,
                "not_shown_share": 0.25,
                "client_abandoned_rate": 0.1,
                "unmeasured_share": None,
            }
            if "block" in q:
                day["block"] = q["block"]
            return 200, {"days": [day]}
        if path == "/proof/ledger":
            row = {"surface": q.get("surface", ""), "readable": True, "readable_sample": 900}
            if q.get("by") == "block":
                return 200, [{**row, "block": "top"}, {**row, "block": "end_carousel"}]
            if "block" in q:
                return 200, [{**row, "block": q["block"]}]
            return 200, [row]
        if path == "/settings":
            if method == "PATCH":
                self.settings.update(b)
                return 200, {"ok": True}
            return 200, dict(self.settings)
        if path == "/health":
            return 200, {"status": "ok", "version": "fake", "engine_version": "0.2.0"}
        return 404, {"code": "not_found"}

    def abandon_calls(self) -> list[str]:
        return [p for m, p, _ in self.calls if p.endswith("/abandon")]


@pytest.fixture
def fake() -> tuple[PatternaClient, FakeEngine]:
    e = FakeEngine()
    c = PatternaClient(
        BASE, serve_key="pk_s", ingest_key="pk_i", admin_key="pk_a", transport=e, retry_delay_ms=0
    )
    return c, e


def test_version() -> None:
    assert __version__ == "0.4.1"


def test_shown_false_is_not_abandoned(fake) -> None:
    c, e = fake
    e.items["home_articles"] = ["doc_1", "doc_2"]
    e.surfaces["home_articles"]["min_slots"] = 3
    called = []
    rec = c.recommend_with_fallback(
        "home_articles", lambda: called.append(1) or ["mine"], mode="personalized", user_id="u_7f3a"
    )
    assert rec.source == "patterna" and rec.shown is False and rec.abandoned is False and rec.error is None
    assert rec.value["items"] == [] and not slate_visible(rec.value) and not called
    # an app that abandons anyway is stopped before anything is sent
    with pytest.raises(PatternaError) as exc:
        c.abandon(rec.request_id)
    assert exc.value.code == ApiErrorCode.NOT_SHOWN
    c.close()
    assert e.abandon_calls() == [] and e.abandoned == {}


def test_shown_false_abandon_from_another_process(fake) -> None:
    _, e = fake
    c = PatternaClient(BASE, serve_key="pk_s", transport=e, retry_delay_ms=0, slate_memory=0)
    e.surfaces["home_articles"]["min_slots"] = 1
    slate = c.recommend("home_articles", mode="personalized", user_id="u_7f3a")
    assert slate["shown"] is False
    with pytest.raises(ApiError) as exc:
        c.abandon(slate["request_id"])
    assert exc.value.status == 409 and is_api_error(exc.value, ApiErrorCode.NOT_SHOWN)
    assert e.abandoned == {}


def test_shown_slate_and_fallback_are_shown(fake) -> None:
    c, e = fake
    e.items["home_articles"] = ["doc_1", "doc_2", "doc_3"]
    e.surfaces["home_articles"]["min_slots"] = 3
    rec = c.recommend_with_fallback("home_articles", list, mode="personalized", user_id="u_7f3a")
    assert rec.shown and rec.source == "patterna" and len(rec.value["items"]) == 3
    c.abandon(rec.request_id)  # a shown slate the app did not display can still be abandoned
    assert e.abandoned == {rec.request_id: "client_fallback"}
    rec = c.recommend_with_fallback("nope", lambda: ["mine"], mode="personalized", user_id="u_7f3a")
    assert rec.source == "fallback" and rec.shown and rec.value == ["mine"]
    # an older engine without "shown" means shown
    assert slate_visible({"items": []}) and not slate_visible(None)


def test_recommend_many_shown(fake) -> None:
    c, e = fake
    e.surfaces["home_articles"]["min_slots"] = 2
    out = c.recommend_many(
        [ManyRequest("home_articles", lambda: ["x"], {"mode": "non_profiled", "session_id": "s_1"})]
    )
    assert len(out) == 1 and out[0].shown is False and out[0].source == "patterna" and not out[0].abandoned


def test_control_item_ids_and_member_of(fake) -> None:
    c, e = fake
    e.items["home_articles"] = ["doc_1"]
    slate = c.recommend(
        "home_articles",
        mode="non_profiled",
        session_id="s_1",
        context={"locale": "nl", "control_item_ids": ["ed_1", "ed_2"], "member_of": ["c_1"]},
    )
    ctx = e.calls[-1][2]["context"]
    assert ctx["control_item_ids"] == ["ed_1", "ed_2"] and ctx["member_of"] == ["c_1"]
    assert slate["control_source"] == CONTROL_SOURCE_REQUEST and slate["arm"] == "control"
    assert [i["item_id"] for i in slate["items"]] == ["ed_1", "ed_2"]
    n = len(e.calls)
    for bad in ({"control_item_ids": ["x"] * 51}, {"member_of": ["c"] * 501}):
        with pytest.raises(ValidationError):
            c.recommend("home_articles", mode="personalized", user_id="u_1", context={"locale": "nl", **bad})
    assert len(e.calls) == n


def _route_slate(body: dict[str, Any]):
    def transport(method, url, headers, data, timeout_s):
        return 200, {}, json.dumps(body).encode()

    return PatternaClient(BASE, serve_key="pk_s", transport=transport)


def test_slate_v03_fields() -> None:
    body = {
        "request_id": "r1",
        "shown": True,
        "fill_count": 2,
        "model_versions": {"persona_bayes": "pb-3", "gbdt": "g-7"},
        "items": [{"item_id": i, "kind": "document", "position": n} for n, i in enumerate("abc")],
    }
    s = _route_slate(body).recommend("s", mode="personalized", user_id="u_1")
    assert [bool(i.get("fill")) for i in s["items"]] == [False, True, True]
    assert s["fill_count"] == 2 and s["model_versions"]["gbdt"] == "g-7" and "control_source" not in s
    # fill per slot from the engine wins over the count
    body = {
        "request_id": "r2",
        "fill_count": 1,
        "control_source": CONTROL_SOURCE_BY_ANCHOR,
        "items": [{"item_id": "a", "fill": True}, {"item_id": "b"}],
    }
    s = _route_slate(body).recommend("s", mode="personalized", user_id="u_1")
    assert s["items"][0]["fill"] and "fill" not in s["items"][1] and s["control_source"] == "by_anchor"
    # a count larger than the slate marks everything
    s = _route_slate({"request_id": "r3", "fill_count": 5, "items": [{"item_id": "a"}]}).recommend(
        "s", mode="personalized", user_id="u_1"
    )
    assert s["items"][0]["fill"] is True


def test_mode_required_and_not_shown_codes() -> None:
    def transport(method, url, headers, data, timeout_s):
        if url.endswith("/profile"):
            return 422, {}, b'{"code":"mode_required","message":"set the mode first"}'
        return 409, {}, b'{"code":"not_shown","message":"hide the block"}'

    c = PatternaClient(BASE, ingest_key="pk_i", serve_key="pk_s", transport=transport, retry_delay_ms=0)
    with pytest.raises(ApiError) as exc:
        c.set_user_profile("u_7f3a", {"goals": ["sleep"]})
    assert exc.value.code == ApiErrorCode.MODE_REQUIRED and is_caller_bug(exc.value)
    with pytest.raises(ApiError) as exc:
        c.abandon("req_x", "timeout")
    assert is_api_error(exc.value, ApiErrorCode.NOT_SHOWN) and not is_caller_bug(exc.value)


def test_coach_product_events() -> None:
    shown = coach_product_shown(
        mode="personalized",
        user_id="u_7f3a",
        product_id="p_mag",
        conversation_id="conv_1",
        position=0,
        occurred_at="2026-10-13T09:00:00Z",
        event_id="e1",
    )
    assert shown == {
        "event_id": "e1",
        "type": EVENT_COACH_PRODUCT_SHOWN,
        "occurred_at": "2026-10-13T09:00:00Z",
        "source": "backend",
        "user_id": "u_7f3a",
        "props": {"product_id": "p_mag", "conversation_id": "conv_1", "position": 0},
    }
    tapped = coach_product_tapped(
        mode="non_profiled", session_id="s_1", product_id="p_mag", conversation_id="conv_1", position=2
    )
    assert tapped["type"] == EVENT_COACH_PRODUCT_TAPPED and tapped["session_id"] == "s_1"
    assert "user_id" not in tapped and tapped["event_id"] and tapped["occurred_at"].endswith("Z")
    base = {"product_id": "p", "conversation_id": "c", "position": 0}
    bad: list[tuple[dict[str, Any], type[Exception]]] = [
        ({"mode": "", "user_id": "u_1"}, ConsentError),
        ({"mode": "non_profiled", "user_id": "u_1", "session_id": "s"}, ConsentError),
        ({"mode": "non_profiled"}, ConsentError),
        ({"mode": "personalized", "session_id": "s"}, ValidationError),
        ({"mode": "cohort", "user_id": "a@b.nl"}, ValidationError),
        ({"mode": "cohort", "user_id": "u_1", "product_id": ""}, ValidationError),
        ({"mode": "cohort", "user_id": "u_1", "conversation_id": " "}, ValidationError),
        ({"mode": "cohort", "user_id": "u_1", "position": -1}, ValidationError),
    ]
    for kw, err in bad:
        with pytest.raises(err):
            coach_product_shown(**{**base, **kw})

    sent: list[Any] = []

    def transport(method, url, headers, data, timeout_s):
        sent.append(json.loads(data))
        return 202, {}, b'{"accepted":2,"duplicates":0,"rejected":[]}'

    PatternaClient(BASE, ingest_key="pk_i", transport=transport).ingest_events([shown, tapped])
    evs = sent[0]["events"]
    assert evs[0]["props"]["position"] == 0 and "user_id" not in evs[1]


def test_apply_surface(fake) -> None:
    c, e = fake
    spec = {
        "surface": "home_articles",
        "slots": 7,
        "item_kinds": ["document"],
        "min_slots": 3,
        "device_resort": True,
        "fallback": {"kind": "curated", "item_ids": ["doc_1"], "by_anchor": {"doc_9": ["p_1", "p_2"]}},
    }
    plan = c.plan_surface(spec)
    assert plan == {
        "surface": "home_articles",
        "changes": ["slots", "fallback", "min_slots", "device_resort"],
        "applied": False,
    }
    plan = c.apply_surface(spec)
    assert plan["applied"]
    patches = [b for m, p, b in e.calls if m == "PATCH"]
    assert patches == [
        {
            "slots": 7,
            "fallback": spec["fallback"],
            "min_slots": 3,
            "device_resort": True,
        }
    ]
    sf = e.surfaces["home_articles"]
    assert sf["min_slots"] == 3 and sf["fallback"]["by_anchor"] == {"doc_9": ["p_1", "p_2"]}
    again = c.apply_surface(spec)
    assert again["changes"] == [] and not again["applied"]
    assert c.apply_surface({"surface": "home_articles", "min_slots": 0})["changes"] == ["min_slots"]


def test_update_surface_and_status_read_back(fake) -> None:
    c, _ = fake
    s = c.update_surface("home_articles", {"min_slots": 2, "device_resort": True})
    assert (
        s["key"] == "home_articles" and s["min_slots"] == 2 and s["device_resort"] and s["status"] == "shadow"
    )
    assert c.set_surface_status("home_articles", "live")["status"] == "live"
    assert c.set_surface_testers("home_articles", ["u_1"])["testers"] == ["u_1"]
    with pytest.raises(ValidationError):
        c.update_surface("home_articles", {})


@pytest.mark.parametrize(
    "spec",
    [
        {},
        {"surface": "s", "slots": 0},
        {"surface": "s", "min_slots": 51},
        {"surface": "s", "min_slots": -1},
        {"surface": "s", "item_kinds": ["video"]},
        {"surface": "s", "fallback": {"kind": "random"}},
        {"surface": "s", "fallback": {"kind": "curated"}},
        {"surface": "s", "fallback": {"kind": "newest", "by_anchor": {"doc_1": []}}},
        {"surface": "s", "exploration_share": 1.5},
        {"surface": "s", "min_slot": 3},
    ],
)
def test_surface_spec_validation(fake, spec) -> None:
    c, e = fake
    with pytest.raises(ValidationError):
        c.apply_surface(spec)
    assert e.calls == []


def test_ledger_by_block(fake) -> None:
    c, e = fake
    rows = c.ledger_by_block(surface="home_articles")
    assert [r["block"] for r in rows] == ["top", "end_carousel"] and rows[0]["readable_sample"] == 900
    assert c.ledger(block="top")[0]["block"] == "top"
    assert "block" not in c.ledger()[0]
    assert [p for _, p, _ in e.calls] == [
        "/proof/ledger?surface=home_articles&by=block",
        "/proof/ledger?block=top",
        "/proof/ledger",
    ]
    for kw in ({"by": "day"}, {"weekly": True, "by": "block"}, {"block": "x" * 33}):
        with pytest.raises(ValidationError):
            c.ledger(**kw)


def test_surface_metrics(fake) -> None:
    c, e = fake
    days = c.surface_metrics("home_articles", days=7, block="top")
    assert days[0]["not_shown_share"] == 0.25 and days[0]["client_abandoned_rate"] == 0.1
    assert days[0]["block"] == "top" and days[0]["unmeasured_share"] is None
    assert e.calls[-1][1] == "/surfaces/home_articles/metrics?block=top&days=7"
    with pytest.raises(ValidationError):
        c.surface_metrics("home_articles", days=181)


def test_phase_tags_and_health(fake) -> None:
    c, e = fake
    assert c.get_settings()["phase_tags"] is False
    c.set_phase_tags(True)
    assert e.settings["phase_tags"] is True and c.get_settings()["phase_tags"] is True
    with pytest.raises(ValidationError):
        c.update_settings()
    assert c.health()["engine_version"] == "0.2.0"
