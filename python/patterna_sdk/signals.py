"""Round-5 signals (engine round 5, API 0.3.0, docs/SIGNALS.md).

The coarse request context and `time_context`, the typed editorial item attributes, the reason
keys, and the flow model's generators and filters with the checks the engine applies on save.
"""

from __future__ import annotations

import math
import re
from collections.abc import Mapping, Sequence
from dataclasses import dataclass, field, fields
from datetime import datetime, timezone, tzinfo
from typing import Any, Literal, TypedDict
from zoneinfo import ZoneInfo, ZoneInfoNotFoundError

from .errors import ValidationError

TIER_FREE = "free"
TIER_PREMIUM = "premium"

_MAX_SESSION_ITEMS = 20
_MAX_SESSION_ITEM_LEN = 200
_MAX_REGION = 64
_MAX_TIER = 32


def _invalid(op: str, message: str) -> ValidationError:
    return ValidationError(message, op=op, code="invalid_argument")


class RecommendContext(TypedDict, total=False):
    """The request context. Only `locale` is required; nothing about the person goes here."""

    locale: str
    app_version: str
    platform: Literal["ios", "android", "web"]
    hour_bucket: int  # 0-23, see time_context
    weekday: int  # 0 = Monday ... 6 = Sunday (engine round 5)
    subscription_tier: str  # "free", "premium" or a tenant tier, <= 32 chars (engine round 5)
    tenure_days: int  # days since sign-up, a ranker feature (engine round 5)
    region: str  # one of the tenant's regions, <= 64 chars; never a location
    session_items: list[str]  # at most 20 ids, most recent first
    anchor_item_id: str
    premium: bool
    candidate_kinds: list[str]
    measured: bool
    control_item_ids: list[str]
    member_of: list[str]


class EventContext(TypedDict, total=False):
    """An event's context: a closed set of coarse keys."""

    locale: str
    app_version: str
    platform: str
    hour_bucket: int
    weekday: int
    region: str
    subscription_tier: str
    tenure_days: int


EVENT_CONTEXT_KEYS = frozenset(EventContext.__annotations__)


def time_context(at: datetime | None = None, tz: str | tzinfo | None = None) -> dict[str, int]:
    """`{"hour_bucket": 0-23, "weekday": 0 = Monday ... 6 = Sunday}` of `at` in the user's zone.

    `tz` is an IANA name ("Europe/Amsterdam") or a tzinfo; `at` must then be timezone-aware
    (default: now). Without `tz`, `at`'s own wall clock is used (default: the local time). Spread it
    into a request or event context: `{"locale": "nl", **time_context(now, user_tz)}`.
    """
    if tz is None:
        local = at if at is not None else datetime.now()
    else:
        try:
            zone = ZoneInfo(tz) if isinstance(tz, str) else tz
        except (ZoneInfoNotFoundError, ValueError) as e:
            raise _invalid("time_context", f"unknown time zone {tz!r}") from e
        at = at if at is not None else datetime.now(timezone.utc)
        if at.tzinfo is None:
            raise _invalid("time_context", "a naive datetime has no instant; pass an aware one with tz")
        local = at.astimezone(zone)
    return {"hour_bucket": local.hour, "weekday": local.weekday()}


def _is_int(v: Any) -> bool:
    return isinstance(v, int) and not isinstance(v, bool)


def check_coarse_context(ctx: Mapping[str, Any], op: str, prefix: str = "context") -> None:
    """Check the coarse keys the way the engine does."""
    h = ctx.get("hour_bucket")
    if h is not None and (not _is_int(h) or not 0 <= h <= 23):
        raise _invalid(op, f"{prefix}.hour_bucket is an hour from 0 to 23")
    w = ctx.get("weekday")
    if w is not None and (not _is_int(w) or not 0 <= w <= 6):
        raise _invalid(op, f"{prefix}.weekday is 0 (Monday) to 6 (Sunday)")
    t = ctx.get("tenure_days")
    if t is not None and (not _is_int(t) or t < 0):
        raise _invalid(op, f"{prefix}.tenure_days is a whole number of days, zero or more")
    tier = ctx.get("subscription_tier")
    if tier is not None and (not isinstance(tier, str) or len(tier.strip()) > _MAX_TIER):
        raise _invalid(op, f"{prefix}.subscription_tier is a tier name of at most {_MAX_TIER} characters")
    region = ctx.get("region")
    if region is not None and (not isinstance(region, str) or len(region.strip()) > _MAX_REGION):
        raise _invalid(op, f"{prefix}.region is a region name of at most {_MAX_REGION} characters")


def check_session_items(items: Any, op: str) -> None:
    if items is None:
        return
    if not isinstance(items, (list, tuple)) or len(items) > _MAX_SESSION_ITEMS:
        raise _invalid(op, f"context.session_items is a list of at most {_MAX_SESSION_ITEMS} ids")
    for i in items:
        if not isinstance(i, str) or not i or len(i) > _MAX_SESSION_ITEM_LEN:
            raise _invalid(
                op, f"context.session_items holds item ids of 1 to {_MAX_SESSION_ITEM_LEN} characters"
            )


# ---- editorial attributes ---------------------------------------------------------------------

FORMATS: tuple[str, ...] = ("text", "video", "audio", "recipe", "workout", "tool", "product")
DIFFICULTIES: tuple[str, ...] = ("easy", "medium", "hard")
_RFC3339 = re.compile(r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$")
_CURRENCY = re.compile(r"^[A-Z]{3}$")


@dataclass
class ItemAttributes:
    """Typed editorial attributes of a catalog item, set as the upsert's `editorial`.

    The SDK writes them into `attributes` (a typed key wins over the same key there) and `region`
    into the upsert's top-level `region`, the field the engine validates and ranks on. Every field
    is optional; None is left out. The engine reads format, duration_min / reading_time_min,
    difficulty, author, available (False is never served), expires_at, starts_at (events),
    repurchase_days, region and phase_fit; the panel shows the price, image and link fields.
    """

    format: str | None = None  # text | video | audio | recipe | workout | tool | product
    duration_min: float | None = None
    reading_time_min: float | None = None
    difficulty: str | None = None  # easy | medium | hard
    author: str | None = None
    available: bool | None = None
    expires_at: datetime | str | None = None  # aware datetime or an RFC 3339 string
    starts_at: datetime | str | None = None
    repurchase_days: int | None = None
    region: str | None = None
    phase_fit: list[str] | None = None
    price_cents: int | None = None
    compare_at_cents: int | None = None
    currency: str | None = None  # ISO 4217, e.g. EUR
    image_url: str | None = None
    url: str | None = None
    product_type: str | None = None
    categories: list[str] | None = field(default=None)

    def validate(self, op: str = "upsert_items", prefix: str = "") -> None:
        p = f"{prefix}attributes."
        if self.format is not None and self.format not in FORMATS:
            raise _invalid(op, f"{p}format must be one of {', '.join(FORMATS)}")
        if self.difficulty is not None and self.difficulty not in DIFFICULTIES:
            raise _invalid(op, f"{p}difficulty must be easy, medium or hard")
        for name in ("duration_min", "reading_time_min"):
            v = getattr(self, name)
            if v is not None and (
                isinstance(v, bool) or not isinstance(v, (int, float)) or not math.isfinite(v) or v < 0
            ):
                raise _invalid(op, f"{p}{name} is a number of minutes, never negative")
        for name in ("repurchase_days", "price_cents", "compare_at_cents"):
            v = getattr(self, name)
            if v is not None and (not _is_int(v) or v < 0):
                raise _invalid(op, f"{p}{name} is a whole number, never negative")
        for name in ("expires_at", "starts_at"):
            v = getattr(self, name)
            if v is None:
                continue
            ok = (isinstance(v, datetime) and v.tzinfo is not None) or (
                isinstance(v, str) and bool(_RFC3339.match(v))
            )
            if not ok:
                raise _invalid(op, f"{p}{name} must be an aware datetime or an RFC 3339 time with a zone")
        if self.available is not None and not isinstance(self.available, bool):
            raise _invalid(op, f"{p}available is True or False")
        if self.currency is not None and not _CURRENCY.match(self.currency):
            raise _invalid(op, f"{p}currency is an ISO 4217 code such as EUR")
        if self.region is not None and len(self.region) > _MAX_REGION:
            raise _invalid(op, f"{prefix}region is a region name of at most {_MAX_REGION} characters")

    def to_dict(self) -> dict[str, Any]:
        """The attributes object (region excluded: it travels as the top-level region)."""
        from .client import rfc3339

        out: dict[str, Any] = {}
        for f in fields(self):
            v = getattr(self, f.name)
            if v is None or f.name == "region":
                continue
            if isinstance(v, datetime):
                v = rfc3339(v)
            elif isinstance(v, list):
                v = list(v)
            out[f.name] = v
        return out


def item_wire(item: Mapping[str, Any], i: int, op: str = "upsert_items") -> dict[str, Any]:
    """The item as sent: `editorial` (ItemAttributes or a dict of its fields) merged into
    `attributes`, its region lifted to `region`."""
    out = {k: v for k, v in item.items() if k != "editorial"}
    prefix = f"item {i}: "
    region = out.get("region")
    if region is not None and (not isinstance(region, str) or len(region) > _MAX_REGION):
        raise _invalid(op, f"{prefix}region is a region name of at most {_MAX_REGION} characters")
    ed = item.get("editorial")
    if ed is None:
        return out
    if isinstance(ed, Mapping):
        known = {f.name for f in fields(ItemAttributes)}
        unknown = sorted(set(ed) - known)
        if unknown:
            raise _invalid(
                op, f"{prefix}unknown editorial attributes {unknown}; put other keys in attributes"
            )
        ed = ItemAttributes(**ed)
    if not isinstance(ed, ItemAttributes):
        raise _invalid(op, f"{prefix}editorial must be ItemAttributes or a dict")
    ed.validate(op, prefix)
    if ed.region and region and ed.region != region:
        raise _invalid(op, f"{prefix}region and editorial.region name different regions")
    merged = {**(out.get("attributes") or {}), **ed.to_dict()}
    if merged:
        out["attributes"] = merged
    if ed.region and not region:
        out["region"] = ed.region
    return out


# ---- reasons ----------------------------------------------------------------------------------

REASON_RECOMMENDED = "recommended"
"""Engine round 5: nothing true could be said about the slot; the text is a plain "Recommended"."""

REASON_KEYS: tuple[str, ...] = (
    "similar_to_read",
    "similar",
    "pairs_with",
    "matches_goals",
    "more_about_tag",
    "people_like_you",
    "trending",
    "editors_pick",
    "new",
    "popular",
    REASON_RECOMMENDED,
)
"""Every reason key in the contract. The set may grow: treat an unknown key like `recommended`."""

# ---- flows ------------------------------------------------------------------------------------

GENERATOR_KINDS: tuple[str, ...] = (
    "trending",
    "curated",
    "item_to_item",
    "content_affinity",
    "persona_bayes",
    "user_bayes",
    "surface_specific",
    "session_items",
    "time_fit",  # engine round 5: the item's tap rate in the request's time bucket; silent
    "format_fit",  # engine round 5: the user's learned format preference; personalized; silent
)


class FlowFilters(TypedDict, total=False):
    """A flow's filters; a key left out keeps the engine's default."""

    exclude_seen_days: int
    exclude_dismissed_days: int  # default 30, 0..365, 0 = off
    exclude_consumed_days: int  # default 30
    paywall_cooldown_days: int  # default 7
    repurchase_days: int  # default 30; the item's own repurchase_days wins
    hide_unavailable: bool  # default True
    relevance_floor: float  # 0..1, default 0 = off
    locales: list[str]
    premium_policy: Literal["allow", "cap", "exclude"]
    premium_cap: int
    per_collection_cap: int
    freshness_days: int | None
    diversity: float
    region_boost: float


_FILTER_KEYS = frozenset(FlowFilters.__annotations__)
_TOP_KEYS = frozenset({"generators", "filters", "ranker", "exploration", "reasons", "diversity"})
_PLACEHOLDER = re.compile(r"\{([^{}]*)\}")
_LOCALE = re.compile(r"^[a-z]{2,3}$")


def _number(v: Any) -> bool:
    return isinstance(v, (int, float)) and not isinstance(v, bool)


def validate_flow_config(config: Any, op: str = "validate_flow") -> None:
    """Check a flow config the way the engine does on save, plus unknown filter keys.

    `create_flow`, `plan_flow` and `apply_flow` run it before anything is sent.
    """
    if not isinstance(config, Mapping):
        raise _invalid(op, "config must be a dict")
    for k in config:
        if k not in _TOP_KEYS:
            raise _invalid(op, f"unknown config key {k} (generators, filters, ranker, exploration, reasons)")
    gens = config.get("generators")
    if not isinstance(gens, Sequence) or isinstance(gens, str) or not gens:
        raise _invalid(op, "generators must hold at least one generator")
    for i, g in enumerate(gens):
        kind = g.get("kind") if isinstance(g, Mapping) else None
        if kind not in GENERATOR_KINDS:
            raise _invalid(
                op, f"generators[{i}]: unknown generator kind {kind!r} ({', '.join(GENERATOR_KINDS)})"
            )
        if not _number(g.get("weight")) or not math.isfinite(g["weight"]):
            raise _invalid(op, f"generators[{i}]: weight must be a number")
    ranker = config.get("ranker")
    if ranker is not None:
        if not isinstance(ranker, Mapping):
            raise _invalid(op, "ranker must be a dict")
        if ranker.get("kind") not in (None, "", "linear", "learned_linear", "gbdt"):
            raise _invalid(op, "ranker.kind must be linear, learned_linear or gbdt")
    exp = config.get("exploration")
    if exp is not None:
        if not isinstance(exp, Mapping):
            raise _invalid(op, "exploration must be a dict")
        if exp.get("kind") not in (None, "", "none", "plackett_luce"):
            raise _invalid(op, "exploration.kind must be none or plackett_luce")
        share = exp.get("share")
        if share is not None and (not _number(share) or not 0 <= share <= 0.5):
            raise _invalid(op, "exploration.share must be between 0 and 0.5")
    filters = config.get("filters")
    if filters is not None:
        if not isinstance(filters, Mapping):
            raise _invalid(op, "filters must be a dict")
        for k in filters:
            if k not in _FILTER_KEYS:
                raise _invalid(op, f"unknown filter {k} ({', '.join(sorted(_FILTER_KEYS))})")
        for k in ("diversity", "relevance_floor", "region_boost"):
            v = filters.get(k)
            if v is not None and (not _number(v) or not 0 <= v <= 1):
                raise _invalid(op, f"filters.{k} must be between 0 and 1")
        for k in (
            "exclude_dismissed_days",
            "exclude_consumed_days",
            "paywall_cooldown_days",
            "repurchase_days",
        ):
            v = filters.get(k)
            if v is not None and (not _is_int(v) or not 0 <= v <= 365):
                raise _invalid(op, f"filters.{k} must be a whole number of days between 0 and 365")
        hide = filters.get("hide_unavailable")
        if hide is not None and not isinstance(hide, bool):
            raise _invalid(op, "filters.hide_unavailable is True or False")
        if filters.get("premium_policy") not in (None, "allow", "cap", "exclude"):
            raise _invalid(op, "filters.premium_policy must be allow, cap or exclude")
    reasons = config.get("reasons")
    if reasons is not None:
        if not isinstance(reasons, Mapping):
            raise _invalid(op, "reasons must be a dict")
        for k in sorted(reasons):
            if k not in REASON_KEYS and k not in GENERATOR_KINDS:
                raise _invalid(
                    op,
                    f"reasons: unknown reason key or generator {k} "
                    f"(reason keys: {', '.join(REASON_KEYS)}; or a generator kind)",
                )
            by_locale = reasons[k]
            if not isinstance(by_locale, Mapping):
                raise _invalid(op, f"reasons.{k} must map locales to texts")
            for loc, text in by_locale.items():
                if not isinstance(loc, str) or not _LOCALE.match(loc):
                    raise _invalid(op, f"reasons.{k}: locale {loc} is not a language code such as nl or en")
                if not isinstance(text, str):
                    raise _invalid(op, f"reasons.{k}.{loc} must be a string")
                if len(text) > 200:
                    raise _invalid(op, f"reasons.{k}.{loc} is longer than 200 characters")
                if text.count("{") != text.count("}"):
                    raise _invalid(op, f"reasons.{k}.{loc} has an unbalanced brace")
                for m in _PLACEHOLDER.finditer(text):
                    if m.group(1) not in ("anchor_title", "tag", "segment"):
                        raise _invalid(
                            op,
                            f"reasons.{k}.{loc}: unknown placeholder {{{m.group(1)}}} "
                            "(allowed: {anchor_title}, {tag}, {segment})",
                        )
