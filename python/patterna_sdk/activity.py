"""Activity-event builders (contracts/events.md, docs/SIGNALS.md §3).

Every builder takes an `EventBase` (who and the envelope) and the event's own fields, applies the
consent rule and returns an event dict for `PatternaClient.ingest_events`.
"""

from __future__ import annotations

import re
from collections.abc import Mapping, Sequence
from dataclasses import dataclass
from datetime import datetime, timezone
from typing import Any

from .errors import ConsentError, ValidationError
from .signals import EVENT_CONTEXT_KEYS, check_coarse_context

EVENT_COMMUNITY_POST_CREATED = "community_post_created"
EVENT_COMMUNITY_REPLY = "community_reply"
EVENT_COMMUNITY_REACTION = "community_reaction"
EVENT_PAYWALL_VIEWED = "paywall_viewed"
EVENT_PUSH_OPENED = "push_opened"
EVENT_SEARCH_PERFORMED = "search_performed"
EVENT_ITEM_ENGAGED = "item_engaged"
EVENT_ITEM_COMPLETED = "item_completed"
EVENT_ORDER_PLACED = "order_placed"

_MODES = ("personalized", "cohort", "non_profiled")
_KINDS = ("document", "product", "community", "event", "tip")
_QUERY_HASH = re.compile(r"^[0-9a-f]{16,128}$")
_CURRENCY = re.compile(r"^[A-Z]{3}$")


@dataclass
class EventBase:
    """Who an event is about and the envelope fields every builder shares.

    Identity follows consent: personalized and cohort carry `user_id` (required) and `session_id`
    when given (the session generator and the paywall cooldown read it); non_profiled carries
    `session_id` only (required) and a `user_id` is refused, so the event never joins a profile.
    `surface` and `request_id` tie the event to a placement and a served slate when there is one.
    """

    mode: str
    user_id: str | None = None
    session_id: str | None = None
    surface: str | None = None
    request_id: str | None = None
    occurred_at: datetime | str | None = None  # default: now
    event_id: str | None = None  # default: a fresh UUID
    source: str = "backend"
    context: Mapping[str, Any] | None = None


def _bad(op: str, message: str) -> ValidationError:
    return ValidationError(message, op=op, code="invalid_argument")


def _blank(s: Any) -> bool:
    return not isinstance(s, str) or not s.strip()


def _count(v: Any) -> bool:
    return isinstance(v, int) and not isinstance(v, bool) and v >= 0


def _build(typ: str, b: EventBase, props: dict[str, Any]) -> dict[str, Any]:
    from .client import assert_opaque_user_id, new_request_id, rfc3339

    op = typ
    if b.mode not in _MODES:
        raise ConsentError(f"mode must be one of {_MODES}", op=op, code="consent_required")
    if b.mode == "non_profiled":
        if b.user_id:
            raise ConsentError(
                "non_profiled events must not carry a user_id", op=op, code="consent_violation"
            )
        if not b.session_id:
            raise ConsentError(
                "non_profiled events need the rotating session_id", op=op, code="consent_violation"
            )
    else:
        if not b.user_id:
            raise _bad(op, f"{b.mode} events need the user_id")
        assert_opaque_user_id(b.user_id)
    ts = b.occurred_at if b.occurred_at is not None else datetime.now(timezone.utc)
    ev: dict[str, Any] = {
        "event_id": b.event_id or new_request_id(),
        "type": typ,
        "occurred_at": rfc3339(ts) if isinstance(ts, datetime) else ts,
        "source": b.source or "backend",
        "props": props,
    }
    if b.mode != "non_profiled":
        ev["user_id"] = b.user_id
    if b.session_id:
        ev["session_id"] = b.session_id
    if b.surface:
        ev["surface"] = b.surface
    if b.request_id:
        ev["request_id"] = b.request_id
    if b.context is not None:
        extra = set(b.context) - EVENT_CONTEXT_KEYS
        if extra:
            raise _bad(op, f"event context has unknown keys {sorted(extra)}")
        check_coarse_context(b.context, op, "event context")
        ev["context"] = dict(b.context)
    return ev


def _community(
    typ: str, b: EventBase, post_id: str, community_id: str, reaction: str | None
) -> dict[str, Any]:
    if _blank(post_id) or _blank(community_id):
        raise _bad(typ, "post_id and community_id are required")
    props: dict[str, Any] = {"post_id": post_id, "community_id": community_id}
    if typ == EVENT_COMMUNITY_REACTION:
        if _blank(reaction):
            raise _bad(typ, "reaction is required")
        props["reaction"] = reaction
    elif reaction is not None:
        raise _bad(typ, "reaction belongs on community_reaction only")
    return _build(typ, b, props)


def community_post_created(b: EventBase, *, post_id: str, community_id: str) -> dict[str, Any]:
    """community_post_created: the user wrote a post (active membership, activity_rate)."""
    return _community(EVENT_COMMUNITY_POST_CREATED, b, post_id, community_id, None)


def community_reply(b: EventBase, *, post_id: str, community_id: str) -> dict[str, Any]:
    """community_reply: the user replied to a post."""
    return _community(EVENT_COMMUNITY_REPLY, b, post_id, community_id, None)


def community_reaction(b: EventBase, *, post_id: str, community_id: str, reaction: str) -> dict[str, Any]:
    """community_reaction: the user reacted to a post (e.g. reaction="heart")."""
    return _community(EVENT_COMMUNITY_REACTION, b, post_id, community_id, reaction)


def paywall_viewed(b: EventBase, *, item_id: str) -> dict[str, Any]:
    """paywall_viewed: a free user hit the paywall on item_id.

    The item stays out of that user's slates for the flow's paywall_cooldown_days. `b.request_id`,
    when set, is also sent as the props' request_id (the slate the item came from).
    """
    if _blank(item_id):
        raise _bad(EVENT_PAYWALL_VIEWED, "item_id is required")
    props: dict[str, Any] = {"item_id": item_id}
    if b.request_id:
        props["request_id"] = b.request_id
    return _build(EVENT_PAYWALL_VIEWED, b, props)


def push_opened(b: EventBase, *, item_id: str, notification_type: str | None = None) -> dict[str, Any]:
    """push_opened: the user opened a push about item_id (the item's pullback_rate)."""
    if _blank(item_id):
        raise _bad(EVENT_PUSH_OPENED, "item_id is required")
    props: dict[str, Any] = {"item_id": item_id}
    if notification_type:
        props["notification_type"] = notification_type
    return _build(EVENT_PUSH_OPENED, b, props)


def search_performed(
    b: EventBase, *, query_hash: str, result_count: int | None = None, clicked_item_id: str | None = None
) -> dict[str, Any]:
    """search_performed: never the query text.

    `query_hash` is a hex hash of the normalised query you compute (e.g. HMAC-SHA256 with a key you
    hold), 16 to 128 lowercase hex characters, so query text cannot travel here by mistake.
    """
    op = EVENT_SEARCH_PERFORMED
    if not isinstance(query_hash, str) or not _QUERY_HASH.match(query_hash):
        raise _bad(
            op, "query_hash must be 16 to 128 lowercase hex characters (hash the query; never send its text)"
        )
    props: dict[str, Any] = {"query_hash": query_hash}
    if result_count is not None:
        if not _count(result_count):
            raise _bad(op, "result_count is a whole number, never negative")
        props["result_count"] = result_count
    if clicked_item_id:
        props["clicked_item_id"] = clicked_item_id
    return _build(op, b, props)


def item_engaged(
    b: EventBase, *, item_id: str, engaged_seconds: int, max_scroll_percent: int, kind: str | None = None
) -> dict[str, Any]:
    """item_engaged: an engaged view is engaged_seconds >= 20 or max_scroll_percent >= 60."""
    op = EVENT_ITEM_ENGAGED
    if _blank(item_id):
        raise _bad(op, "item_id is required")
    if kind is not None and kind not in _KINDS:
        raise _bad(op, f"unknown kind {kind!r}")
    if not _count(engaged_seconds) or not _count(max_scroll_percent) or max_scroll_percent > 100:
        raise _bad(op, "engaged_seconds is a whole number, never negative, and max_scroll_percent 0 to 100")
    props: dict[str, Any] = {
        "item_id": item_id,
        "engaged_seconds": engaged_seconds,
        "max_scroll_percent": max_scroll_percent,
    }
    if kind:
        props["kind"] = kind
    return _build(op, b, props)


def item_completed(
    b: EventBase, *, item_id: str, kind: str | None = None, collection: str | None = None
) -> dict[str, Any]:
    """item_completed: the user finished a workout, a breathwork session, a protocol.

    The item then stays out of their slates for exclude_consumed_days. A ranking event: sign it
    (a client with a signing_secret) when it does not come from an ingest key.
    """
    op = EVENT_ITEM_COMPLETED
    if _blank(item_id):
        raise _bad(op, "item_id is required")
    if kind is not None and kind not in _KINDS:
        raise _bad(op, f"unknown kind {kind!r}")
    props: dict[str, Any] = {"item_id": item_id}
    if kind:
        props["kind"] = kind
    if collection:
        props["collection"] = collection
    return _build(op, b, props)


def order_placed(
    b: EventBase,
    *,
    order_id_hash: str,
    product_ids: Sequence[str],
    value_cents: int | None = None,
    currency: str | None = None,
) -> dict[str, Any]:
    """order_placed: product catalog ids and the value, never the customer.

    `order_id_hash` is a hash of your order id. A ranking event: sign it when it does not come
    from an ingest key.
    """
    op = EVENT_ORDER_PLACED
    if _blank(order_id_hash) or isinstance(product_ids, str) or not product_ids:
        raise _bad(op, "order_id_hash and at least one product id are required")
    if any(_blank(p) for p in product_ids):
        raise _bad(op, "product ids are never empty")
    props: dict[str, Any] = {"order_id_hash": order_id_hash, "product_ids": list(product_ids)}
    if value_cents is not None:
        if not _count(value_cents):
            raise _bad(op, "value_cents is a whole number, never negative")
        props["value_cents"] = value_cents
    if currency is not None:
        if not _CURRENCY.match(currency):
            raise _bad(op, "currency is an ISO 4217 code such as EUR")
        props["currency"] = currency
    return _build(op, b, props)
