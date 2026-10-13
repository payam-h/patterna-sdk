"""0.4.0: engine round 5 context, editorial attributes, activity events, reason key, flow checks."""

from __future__ import annotations

from datetime import datetime, timedelta, timezone
from typing import Any

import pytest
from test_v02 import SLATE, always, make

from patterna_sdk import (
    GENERATOR_KINDS,
    REASON_KEYS,
    REASON_RECOMMENDED,
    TIER_FREE,
    ConsentError,
    EventBase,
    ItemAttributes,
    PatternaClient,
    ValidationError,
    community_post_created,
    community_reaction,
    community_reply,
    item_completed,
    item_engaged,
    order_placed,
    paywall_viewed,
    push_opened,
    search_performed,
    time_context,
    validate_flow_config,
)

REQ: dict[str, Any] = {"mode": "personalized", "user_id": "u_7f3a"}


# ---- request context ------------------------------------------------------------------------


def test_context_round5_on_the_wire() -> None:
    c, t = make(always(200, SLATE))
    ctx = {
        "locale": "nl",
        "hour_bucket": 21,
        "weekday": 6,
        "subscription_tier": TIER_FREE,
        "tenure_days": 0,
        "region": "Noord-Holland",
        "session_items": ["doc_9", "doc_8"],
    }
    c.recommend("home_articles", context=ctx, **REQ)
    assert t.calls[0]["body"]["context"] == ctx


@pytest.mark.parametrize(
    ("ctx", "msg"),
    [
        ({"weekday": -1}, "weekday"),
        ({"weekday": 7}, "weekday"),
        ({"weekday": True}, "weekday"),
        ({"hour_bucket": 24}, "hour_bucket"),
        ({"tenure_days": -1}, "tenure_days"),
        ({"tenure_days": 1.5}, "tenure_days"),
        ({"subscription_tier": "t" * 33}, "subscription_tier"),
        ({"region": "r" * 65}, "region"),
        ({"session_items": ["doc"] * 21}, "session_items"),
        ({"session_items": [""]}, "session_items"),
        ({"session_items": ["i" * 201]}, "session_items"),
        ({"session_items": "doc_1"}, "session_items"),
    ],
)
def test_context_round5_refused(ctx: dict[str, Any], msg: str) -> None:
    c, t = make(always(200, SLATE))
    with pytest.raises(ValidationError, match=msg):
        c.recommend("home_articles", context={"locale": "nl", **ctx}, **REQ)
    assert t.calls == []


def test_context_round5_edges() -> None:
    c, t = make(always(200, SLATE))
    c.recommend(
        "home_articles",
        context={
            "locale": "nl",
            "weekday": 0,
            "hour_bucket": 23,
            "tenure_days": 0,
            "subscription_tier": "t" * 32,
            "region": "r" * 64,
            "session_items": ["d"] * 20,
        },
        **REQ,
    )
    assert len(t.calls) == 1


def test_time_context() -> None:
    at = datetime(2026, 10, 11, 23, 30, tzinfo=timezone.utc)  # a Sunday
    assert time_context(at, "Europe/Amsterdam") == {"hour_bucket": 1, "weekday": 0}  # Monday 01:30
    assert time_context(at, "UTC") == {"hour_bucket": 23, "weekday": 6}
    assert time_context(at, timezone(timedelta(hours=-4))) == {"hour_bucket": 19, "weekday": 6}
    winter = datetime(2026, 12, 5, 23, 10, tzinfo=timezone.utc)  # Saturday
    assert time_context(winter, "Europe/Amsterdam") == {"hour_bucket": 0, "weekday": 6}
    # without a zone: the datetime's own wall clock
    assert time_context(datetime(2026, 10, 14, 7, 15)) == {"hour_bucket": 7, "weekday": 2}
    now = time_context(tz="Europe/Amsterdam")
    assert 0 <= now["hour_bucket"] <= 23 and 0 <= now["weekday"] <= 6
    with pytest.raises(ValidationError, match="time zone"):
        time_context(at, "Mars/Olympus")
    with pytest.raises(ValidationError, match="naive"):
        time_context(datetime(2026, 10, 11, 23, 30), "Europe/Amsterdam")


def test_event_context_round5() -> None:
    c, t = make(always(202, {"accepted": 1}))
    ctx = {
        "locale": "nl",
        "hour_bucket": 7,
        "weekday": 2,
        "region": "Utrecht",
        "subscription_tier": "premium",
        "tenure_days": 400,
    }
    ev = {
        "event_id": "e1",
        "type": "item_viewed",
        "occurred_at": "2026-10-13T10:00:00Z",
        "source": "backend",
        "user_id": "u_1",
        "context": ctx,
    }
    c.ingest_events([ev])
    assert t.calls[0]["body"]["events"][0]["context"] == ctx
    for bad in [
        {"weekday": 7},
        {"hour_bucket": -1},
        {"tenure_days": -2},
        {"subscription_tier": "t" * 33},
        {"region": "r" * 65},
    ]:
        with pytest.raises(ValidationError):
            c.ingest_events([{**ev, "context": bad}])
    with pytest.raises(ValidationError, match="latitude"):
        c.ingest_events([{**ev, "context": {"latitude": 52.1}}])
    assert len(t.calls) == 1


# ---- editorial attributes -------------------------------------------------------------------

FULL = ItemAttributes(
    format="product",
    duration_min=12.5,
    reading_time_min=4,
    difficulty="easy",
    author="auth_3",
    available=False,
    expires_at=datetime(2026, 12, 31, 23, 0, tzinfo=timezone(timedelta(hours=1))),
    starts_at="2026-11-01T19:00:00+01:00",
    repurchase_days=25,
    region="Utrecht",
    phase_fit=["follicular"],
    price_cents=2495,
    compare_at_cents=2995,
    currency="EUR",
    image_url="https://cdn/x.jpg",
    url="https://shop/x",
    product_type="supplement",
    categories=["sleep", "minerals"],
)


def test_item_attributes_on_the_wire() -> None:
    c, t = make(always(200, {"upserted": 3}))
    item = {
        "external_id": "prod_1",
        "kind": "product",
        "title": "Magnesium",
        "locale": "nl",
        "attributes": {"handle": "magnesium", "format": "text"},
        "editorial": FULL,
    }
    plain = {"external_id": "d", "kind": "document", "title": "T", "locale": "nl", "attributes": {"a": 1}}
    as_dict = {
        "external_id": "doc_1",
        "kind": "document",
        "title": "T",
        "locale": "nl",
        "editorial": {"format": "text", "reading_time_min": 6},
    }
    c.upsert_items([item, plain, as_dict, {**plain, "external_id": "e", "editorial": ItemAttributes()}])
    sent = t.calls[0]["body"]["items"]
    assert sent[0] == {
        "external_id": "prod_1",
        "kind": "product",
        "title": "Magnesium",
        "locale": "nl",
        "region": "Utrecht",
        "attributes": {
            "handle": "magnesium",
            "format": "product",
            "duration_min": 12.5,
            "reading_time_min": 4,
            "difficulty": "easy",
            "author": "auth_3",
            "available": False,
            "expires_at": "2026-12-31T22:00:00Z",
            "starts_at": "2026-11-01T19:00:00+01:00",
            "repurchase_days": 25,
            "phase_fit": ["follicular"],
            "price_cents": 2495,
            "compare_at_cents": 2995,
            "currency": "EUR",
            "image_url": "https://cdn/x.jpg",
            "url": "https://shop/x",
            "product_type": "supplement",
            "categories": ["sleep", "minerals"],
        },
    }
    assert sent[1] == plain
    assert sent[2]["attributes"] == {"format": "text", "reading_time_min": 6}
    assert sent[3] == {**plain, "external_id": "e"}
    # the caller's item is untouched
    assert item["attributes"] == {"handle": "magnesium", "format": "text"} and item["editorial"] is FULL


@pytest.mark.parametrize(
    ("attrs", "msg"),
    [
        (ItemAttributes(format="podcast"), "attributes.format"),
        (ItemAttributes(difficulty="expert"), "attributes.difficulty"),
        (ItemAttributes(duration_min=-1), "attributes.duration_min"),
        (ItemAttributes(reading_time_min=float("nan")), "attributes.reading_time_min"),
        (ItemAttributes(repurchase_days=-1), "attributes.repurchase_days"),
        (ItemAttributes(repurchase_days=1.5), "attributes.repurchase_days"),  # type: ignore[arg-type]
        (ItemAttributes(price_cents=-1), "attributes.price_cents"),
        (ItemAttributes(compare_at_cents=9.99), "attributes.compare_at_cents"),  # type: ignore[arg-type]
        (ItemAttributes(currency="euro"), "attributes.currency"),
        (ItemAttributes(expires_at="2026-12-31"), "attributes.expires_at"),
        (ItemAttributes(starts_at=datetime(2026, 11, 1, 18)), "attributes.starts_at"),
        (ItemAttributes(available="no"), "attributes.available"),  # type: ignore[arg-type]
        (ItemAttributes(region="r" * 65), "region"),
        ({"fromat": "text"}, "unknown editorial attributes"),
        ("format=text", "editorial must be"),
    ],
)
def test_item_attributes_refused(attrs: Any, msg: str) -> None:
    c, t = make(always(200, {}))
    items = [
        {"external_id": "ok", "kind": "document", "title": "T", "locale": "nl"},
        {"external_id": "x", "kind": "product", "title": "T", "locale": "nl", "editorial": attrs},
    ]
    with pytest.raises(ValidationError, match=msg) as e:
        c.upsert_items(items)
    assert "item 1" in str(e.value)
    assert t.calls == []


def test_item_regions() -> None:
    c, t = make(always(200, {}))
    with pytest.raises(ValidationError, match="different regions"):
        c.upsert_items(
            [
                {
                    "external_id": "x",
                    "kind": "product",
                    "title": "T",
                    "locale": "nl",
                    "region": "Utrecht",
                    "editorial": ItemAttributes(region="Drenthe"),
                }
            ]
        )
    with pytest.raises(ValidationError, match="region"):
        c.upsert_items(
            [{"external_id": "x", "kind": "product", "title": "T", "locale": "nl", "region": "r" * 65}]
        )
    assert t.calls == []
    ItemAttributes(format="workout", difficulty="hard", currency="EUR").validate()


# ---- activity events ------------------------------------------------------------------------

AT = datetime(2026, 10, 13, 20, 0, tzinfo=timezone.utc)
PERS = EventBase(mode="personalized", user_id="u_7f3a", session_id="s_1", occurred_at=AT, event_id="e1")
ANON = EventBase(mode="non_profiled", session_id="s_9", occurred_at=AT, event_id="e2")

CASES: list[tuple[str, Any, dict[str, Any]]] = [
    (
        "community_post_created",
        lambda b: community_post_created(b, post_id="p1", community_id="c1"),
        {"post_id": "p1", "community_id": "c1"},
    ),
    (
        "community_reply",
        lambda b: community_reply(b, post_id="p1", community_id="c1"),
        {"post_id": "p1", "community_id": "c1"},
    ),
    (
        "community_reaction",
        lambda b: community_reaction(b, post_id="p1", community_id="c1", reaction="heart"),
        {"post_id": "p1", "community_id": "c1", "reaction": "heart"},
    ),
    (
        "paywall_viewed",
        lambda b: paywall_viewed(EventBase(**{**b.__dict__, "request_id": "req_5"}), item_id="doc_3"),
        {"item_id": "doc_3", "request_id": "req_5"},
    ),
    (
        "push_opened",
        lambda b: push_opened(b, item_id="doc_4", notification_type="weekly_digest"),
        {"item_id": "doc_4", "notification_type": "weekly_digest"},
    ),
    (
        "search_performed",
        lambda b: search_performed(b, query_hash="9f86d081884c7d65", result_count=0, clicked_item_id="doc_2"),
        {"query_hash": "9f86d081884c7d65", "result_count": 0, "clicked_item_id": "doc_2"},
    ),
    (
        "item_engaged",
        lambda b: item_engaged(b, item_id="doc_1", engaged_seconds=42, max_scroll_percent=80),
        {"item_id": "doc_1", "engaged_seconds": 42, "max_scroll_percent": 80},
    ),
    (
        "item_completed",
        lambda b: item_completed(b, item_id="wo_1", kind="document", collection="workouts"),
        {"item_id": "wo_1", "kind": "document", "collection": "workouts"},
    ),
    (
        "order_placed",
        lambda b: order_placed(
            b, order_id_hash="oh_1", product_ids=("prod_1", "prod_2"), value_cents=4990, currency="EUR"
        ),
        {
            "order_id_hash": "oh_1",
            "product_ids": ["prod_1", "prod_2"],
            "value_cents": 4990,
            "currency": "EUR",
        },
    ),
]


@pytest.mark.parametrize(("typ", "build", "props"), CASES, ids=[c[0] for c in CASES])
def test_activity_event(typ: str, build: Any, props: dict[str, Any]) -> None:
    e = build(PERS)
    assert e["type"] == typ and e["props"] == props
    assert e["user_id"] == "u_7f3a" and e["session_id"] == "s_1"
    assert e["source"] == "backend" and e["event_id"] == "e1" and e["occurred_at"] == "2026-10-13T20:00:00Z"
    a = build(ANON)
    assert "user_id" not in a and a["session_id"] == "s_9"
    with pytest.raises(ConsentError):
        build(EventBase(mode="non_profiled", user_id="u_7f3a", session_id="s_9"))
    with pytest.raises(ConsentError):
        build(EventBase(mode="non_profiled"))
    with pytest.raises(ValidationError):
        build(EventBase(mode="cohort", session_id="s_1"))
    with pytest.raises(ConsentError):
        build(EventBase(mode="anonymous", user_id="u_1"))
    with pytest.raises(ValidationError):
        build(EventBase(mode="personalized", user_id="jan@example.com"))
    d = build(EventBase(mode="cohort", user_id="u_1"))
    assert len(d["event_id"]) == 36 and d["source"] == "backend" and "session_id" not in d
    assert d["occurred_at"].endswith("Z")


def test_activity_events_refuse_bad_input() -> None:
    b = EventBase(mode="personalized", user_id="u_1")
    bad = [
        lambda: community_post_created(b, post_id="", community_id="c"),
        lambda: community_reply(b, post_id="p", community_id=" "),
        lambda: community_reaction(b, post_id="p", community_id="c", reaction=""),
        lambda: search_performed(b, query_hash="magnesium slaap"),
        lambda: search_performed(b, query_hash="abc123"),
        lambda: search_performed(b, query_hash="9F86D081884C7D65"),
        lambda: search_performed(b, query_hash="9f86d081884c7d65", result_count=-1),
        lambda: paywall_viewed(b, item_id=""),
        lambda: push_opened(b, item_id=""),
        lambda: item_engaged(b, item_id="", engaged_seconds=3, max_scroll_percent=0),
        lambda: item_engaged(b, item_id="d", kind="video", engaged_seconds=3, max_scroll_percent=0),
        lambda: item_engaged(b, item_id="d", engaged_seconds=-1, max_scroll_percent=0),
        lambda: item_engaged(b, item_id="d", engaged_seconds=1, max_scroll_percent=101),
        lambda: item_completed(b, item_id=""),
        lambda: item_completed(b, item_id="d", kind="workout"),
        lambda: order_placed(b, order_id_hash="", product_ids=["p"]),
        lambda: order_placed(b, order_id_hash="h", product_ids=[]),
        lambda: order_placed(b, order_id_hash="h", product_ids="p"),
        lambda: order_placed(b, order_id_hash="h", product_ids=[""]),
        lambda: order_placed(b, order_id_hash="h", product_ids=["p"], value_cents=-5),
        lambda: order_placed(b, order_id_hash="h", product_ids=["p"], currency="eur"),
        lambda: push_opened(EventBase(mode="cohort", user_id="u_1", context={"weekday": 7}), item_id="d"),
        lambda: push_opened(EventBase(mode="cohort", user_id="u_1", context={"ip": "1.2.3.4"}), item_id="d"),
    ]
    for f in bad:
        with pytest.raises(ValidationError):
            f()
    assert search_performed(b, query_hash="a" * 64)["props"] == {"query_hash": "a" * 64}
    assert item_completed(b, item_id="d")["props"] == {"item_id": "d"}
    pw = paywall_viewed(b, item_id="d")
    assert pw["props"] == {"item_id": "d"} and "request_id" not in pw
    assert order_placed(b, order_id_hash="h", product_ids=["p"])["props"] == {
        "order_id_hash": "h",
        "product_ids": ["p"],
    }


def test_activity_events_are_signed_by_the_client() -> None:
    c, t = make(always(202, {"accepted": 1}), signing_secret="test-signing-key")
    c.ingest_events([order_placed(PERS, order_id_hash="h", product_ids=["p"])])
    assert len(t.calls[0]["body"]["events"][0]["signature"]) == 64


# ---- reasons and flows ----------------------------------------------------------------------


def test_reason_recommended() -> None:
    assert (
        REASON_RECOMMENDED == "recommended"
        and REASON_KEYS[-1] == REASON_RECOMMENDED
        and len(REASON_KEYS) == 11
    )
    slate = {
        **SLATE,
        "items": [{**SLATE["items"][0], "reason_key": "recommended", "reason": {"en": "Recommended"}}],
    }
    c, _ = make(always(200, slate))
    assert (
        c.recommend("home_articles", context={"locale": "nl"}, **REQ)["items"][0]["reason_key"]
        == "recommended"
    )


ROUND5: dict[str, Any] = {
    "generators": [
        {"kind": "item_to_item", "weight": 0.5},
        {"kind": "time_fit", "weight": 0.15},
        {"kind": "format_fit", "weight": 0.15},
    ],
    "filters": {
        "exclude_dismissed_days": 30,
        "exclude_consumed_days": 30,
        "paywall_cooldown_days": 7,
        "repurchase_days": 45,
        "hide_unavailable": False,
        "relevance_floor": 0.35,
        "region_boost": 0.3,
    },
    "ranker": {"kind": "gbdt", "bandit": False},
    "exploration": {"kind": "plackett_luce", "share": 0.2},
    "reasons": {"recommended": {"nl": "Voor jou"}, "time_fit": {"en": "Fits the evening"}},
}


def test_flow_round5_valid() -> None:
    assert "time_fit" in GENERATOR_KINDS and "format_fit" in GENERATOR_KINDS
    validate_flow_config(ROUND5)
    validate_flow_config(
        {
            **ROUND5,
            "filters": {
                "exclude_dismissed_days": 0,
                "exclude_consumed_days": 365,
                "paywall_cooldown_days": 0,
                "repurchase_days": 365,
                "relevance_floor": 1,
                "hide_unavailable": True,
                "freshness_days": None,
            },
        }
    )
    validate_flow_config({"generators": [{"kind": "curated", "weight": 1}], "diversity": {"mmr_lambda": 0.3}})


G: dict[str, Any] = {
    "generators": [{"kind": "time_fit", "weight": 1}],
    "ranker": {"kind": "linear"},
    "exploration": {"kind": "none"},
}


@pytest.mark.parametrize(
    ("cfg", "msg"),
    [
        ([], "must be a dict"),
        ({**G, "generators": []}, "at least one generator"),
        ({**G, "generators": [{"kind": "time_fits", "weight": 1}]}, "unknown generator kind 'time_fits'"),
        ({**G, "generators": [{"kind": "time_fit"}]}, "weight"),
        ({**G, "ranker": {"kind": "lambdamart"}}, "ranker.kind"),
        ({**G, "exploration": {"kind": "thompson"}}, "exploration.kind"),
        ({**G, "exploration": {"kind": "plackett_luce", "share": 0.6}}, "exploration.share"),
        ({**G, "rankers": {}}, "unknown config key rankers"),
        ({**G, "filters": {"exclude_dismissed_days": -1}}, "filters.exclude_dismissed_days"),
        ({**G, "filters": {"exclude_dismissed_days": 366}}, "filters.exclude_dismissed_days"),
        ({**G, "filters": {"exclude_dismissed_days": 7.5}}, "filters.exclude_dismissed_days"),
        ({**G, "filters": {"exclude_consumed_days": 400}}, "filters.exclude_consumed_days"),
        ({**G, "filters": {"paywall_cooldown_days": -7}}, "filters.paywall_cooldown_days"),
        ({**G, "filters": {"repurchase_days": 1000}}, "filters.repurchase_days"),
        ({**G, "filters": {"relevance_floor": 1.5}}, "filters.relevance_floor"),
        ({**G, "filters": {"relevance_floor": -0.1}}, "filters.relevance_floor"),
        ({**G, "filters": {"region_boost": 2}}, "filters.region_boost"),
        ({**G, "filters": {"diversity": -1}}, "filters.diversity"),
        ({**G, "filters": {"hide_unavailable": "no"}}, "filters.hide_unavailable"),
        ({**G, "filters": {"premium_policy": "hide"}}, "filters.premium_policy"),
        ({**G, "filters": {"exclude_dismised_days": 30}}, "unknown filter exclude_dismised_days"),
        ({**G, "reasons": {"recomended": {"nl": "x"}}}, "unknown reason key or generator recomended"),
        ({**G, "reasons": {"recommended": {"NL": "x"}}}, "locale NL"),
        ({**G, "reasons": {"recommended": {"nl": "{user}"}}}, r"unknown placeholder \{user\}"),
        ({**G, "reasons": {"recommended": {"nl": "{tag"}}}, "unbalanced brace"),
        ({**G, "reasons": {"recommended": {"nl": "x" * 201}}}, "longer than 200"),
    ],
)
def test_flow_refused(cfg: Any, msg: str) -> None:
    with pytest.raises(ValidationError, match=msg):
        validate_flow_config(cfg)


def test_flow_calls_validate_before_sending() -> None:
    c, t = make(always(200, []), admin_key="pk_a")
    bad = {**ROUND5, "filters": {"relevance_floor": 3}}
    with pytest.raises(ValidationError, match="relevance_floor"):
        c.apply_flow("more_to_read", bad)
    with pytest.raises(ValidationError, match="relevance_floor"):
        c.plan_flow("more_to_read", bad)
    with pytest.raises(ValidationError, match="relevance_floor"):
        c.create_flow("more_to_read", bad)
    assert t.calls == []
    assert isinstance(c, PatternaClient)
