"""PatternaClient: serve, ingest and admin calls over urllib, no dependencies.

Budgets are per attempt: recommend 150 ms and never retried (each call logs an impression);
ingest calls 5 s and retried once on 5xx or a network error (they are idempotent on event id);
admin calls 30 s. Every call sends a client-minted X-Request-Id; recommend also asks the engine to
answer within X-Deadline-Ms (120 ms by default, engine 0.2).
"""

from __future__ import annotations

import json
import re
import threading
import time
import urllib.error
import urllib.request
import uuid
from collections import OrderedDict
from collections.abc import Callable, Iterable, Iterator, Sequence
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass, field
from datetime import datetime, timezone
from typing import Any, Generic, TypeVar
from urllib.parse import quote, urlencode

from .errors import (
    ApiError,
    ConsentError,
    NetworkError,
    PatternaError,
    ValidationError,
    is_caller_bug,
)
from .errors import TimeoutError as PatternaTimeout
from .signing import canonical_json, sign_event

MODES: tuple[str, ...] = ("personalized", "cohort", "non_profiled")
DEFAULT_RECOMMEND_TIMEOUT_MS = 150
DEFAULT_RECOMMEND_DEADLINE_MS = 120
DEFAULT_INGEST_TIMEOUT_MS = 5000
DEFAULT_ADMIN_TIMEOUT_MS = 30000
_MIN_SERVER_DEADLINE_MS = 10
_CONTEXT_KEYS = frozenset({"locale", "app_version", "platform", "hour_bucket"})
_ITEM_KINDS = frozenset({"document", "product", "community", "event", "tip"})
_SURFACE_STATUSES = frozenset({"shadow", "testers", "live", "off"})
_ABANDON_REASONS = frozenset({"timeout", "server_error", "network_error", "bad_response", "client_fallback"})
_HMAC_HEX = re.compile(r"^[0-9a-f]{64}$")
_HEADER_ID = re.compile(r"^[\x21-\x7e]{1,64}$")
_MAX_INTERACTIONS = 50
_MAX_ITEMS = 500
_MAX_BLOCK = 64

Transport = Callable[[str, str, dict[str, str], bytes | None, float], tuple[int, dict[str, str], bytes]]
F = TypeVar("F")


def _urllib_transport(
    method: str, url: str, headers: dict[str, str], body: bytes | None, timeout_s: float
) -> tuple[int, dict[str, str], bytes]:
    req = urllib.request.Request(url, data=body, method=method, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=timeout_s) as res:
            return res.status, {k.lower(): v for k, v in res.headers.items()}, res.read()
    except urllib.error.HTTPError as e:
        return e.code, {k.lower(): v for k, v in e.headers.items()}, e.read()


def rfc3339(ts: datetime) -> str:
    """Go-compatible timestamp: UTC, 'Z', fractional seconds only when non-zero (trailing zeros trimmed)."""
    if ts.tzinfo is None:
        ts = ts.replace(tzinfo=timezone.utc)
    ts = ts.astimezone(timezone.utc)
    base = ts.strftime("%Y-%m-%dT%H:%M:%S")
    if ts.microsecond:
        base += ("." + f"{ts.microsecond:06d}").rstrip("0")
    return base + "Z"


def assert_opaque_user_id(user_id: str) -> None:
    if not user_id or "@" in user_id or " " in user_id or len(user_id) > 128:
        raise ValidationError(
            "user_id must be an opaque id (no email, no spaces, at most 128 chars)",
            op="validate",
            code="invalid_user_id",
        )


def new_request_id() -> str:
    """A random UUIDv4, the form the engine mints itself."""
    return str(uuid.uuid4())


def _clean_id(value: str | None) -> str | None:
    if value is None:
        return None
    value = value.strip()
    return value if _HEADER_ID.match(value) else None


def _invalid(op: str, message: str) -> ValidationError:
    return ValidationError(message, op=op, code="invalid_argument")


def measured_context(context: dict[str, Any], analytics_consent: bool) -> dict[str, Any]:
    """Copy of `context` with `measured` (engine 0.2) set from the user's analytics consent.

    False keeps the request out of the ledger and the training data; the slate is still ranked.
    """
    return {**context, "measured": bool(analytics_consent)}


def interaction_for(slate: dict[str, Any], item_id: str, type: str, **extra: Any) -> dict[str, Any] | None:
    """An interaction for one of the slate's items with the served kind and position.

    `extra` may set occurred_at, displayed_position and block. None when the item is not in the slate.
    """
    for it in slate.get("items", []):
        if it.get("item_id") == item_id:
            out = {"item_id": item_id, "kind": it.get("kind"), "position": it.get("position"), "type": type}
            out.update(extra)
            return out
    return None


@dataclass
class Recommendation(Generic[F]):
    """What `recommend_with_fallback` returns.

    source is "patterna" (value is the slate) or "fallback" (value is what your fallback returned).
    request_id is always the id sent as X-Request-Id. abandoned is true when the abandonment was
    queued in the background (timeout, network error, 5xx).
    """

    source: str
    value: Any
    request_id: str
    error: PatternaError | None = None
    abandoned: bool = False


@dataclass
class ManyRequest:
    """One surface in `recommend_many`: keyword arguments of `recommend` plus a fallback."""

    surface: str
    fallback: Callable[[], Any]
    kwargs: dict[str, Any] = field(default_factory=dict)


def _abandon_reason(err: PatternaError) -> str | None:
    if isinstance(err, PatternaTimeout):
        return "timeout"
    if isinstance(err, NetworkError):
        return "network_error"
    if err.code == "bad_response" and not isinstance(err, ApiError):
        return "bad_response"
    if isinstance(err, ApiError) and (err.status or 0) >= 500:
        return "server_error"
    return None


class PatternaClient:
    def __init__(
        self,
        base_url: str,
        *,
        serve_key: str | None = None,
        ingest_key: str | None = None,
        admin_key: str | None = None,
        signing_secret: str | None = None,
        recommend_timeout_ms: int = DEFAULT_RECOMMEND_TIMEOUT_MS,
        recommend_deadline_ms: int | None = None,
        ingest_timeout_ms: int = DEFAULT_INGEST_TIMEOUT_MS,
        admin_timeout_ms: int = DEFAULT_ADMIN_TIMEOUT_MS,
        retry_delay_ms: int = 100,
        transport: Transport | None = None,
        trace_id: Callable[[], str | None] | None = None,
        on_background_error: Callable[[PatternaError], None] | None = None,
        slate_memory: int = 4096,
        user_agent: str = "patterna-sdk-python/0.2.0",
    ) -> None:
        self.base_url = base_url.rstrip("/")
        self.keys = {"serve": serve_key, "ingest": ingest_key, "admin": admin_key}
        self.signing_secret = signing_secret
        self.recommend_timeout_ms = recommend_timeout_ms
        self.recommend_deadline_ms = (
            recommend_deadline_ms
            if recommend_deadline_ms is not None
            else min(DEFAULT_RECOMMEND_DEADLINE_MS, recommend_timeout_ms * 4 // 5)
        )
        if self.recommend_deadline_ms >= recommend_timeout_ms:
            raise ValueError("recommend_deadline_ms must be below recommend_timeout_ms")
        self.ingest_timeout_ms = ingest_timeout_ms
        self.admin_timeout_ms = admin_timeout_ms
        self.retry_delay_ms = retry_delay_ms
        self._transport = transport or _urllib_transport
        self._trace_id = trace_id
        self._on_background_error = on_background_error
        self.user_agent = user_agent
        self._slate_memory = max(0, slate_memory)
        self._kinds: OrderedDict[str, dict[str, str]] = OrderedDict()
        self._lock = threading.Lock()
        self._background: ThreadPoolExecutor | None = None

    # ---- lifecycle ---------------------------------------------------------------------------

    def close(self, wait: bool = True) -> None:
        """Wait for background abandonments to be sent (call on shutdown)."""
        with self._lock:
            bg, self._background = self._background, None
        if bg:
            bg.shutdown(wait=wait)

    def __enter__(self) -> PatternaClient:
        return self

    def __exit__(self, *exc: object) -> None:
        self.close()

    def server_deadline_ms(self, budget_ms: int) -> int:
        """X-Deadline-Ms for a call with `budget_ms`: the configured deadline, shrunk by the same margin."""
        margin = self.recommend_timeout_ms - self.recommend_deadline_ms
        d = min(self.recommend_deadline_ms, budget_ms - margin)
        return max(budget_ms // 2, 1) if d < _MIN_SERVER_DEADLINE_MS else d

    # ---- serve -------------------------------------------------------------------------------

    def recommend(
        self,
        surface: str,
        *,
        mode: str,
        user_id: str | None = None,
        session_id: str | None = None,
        context: dict[str, Any] | None = None,
        slots: int | None = None,
        exclude_item_ids: Sequence[str] | None = None,
        assignment_unit: str | None = None,
        timeout_ms: int | None = None,
        request_id: str | None = None,
        trace_id: str | None = None,
        tester: bool = False,
    ) -> dict[str, Any]:
        """Rank a slate. Never retried. `tester` sends X-Patterna-Tester: 1 (engine 0.2)."""
        if mode not in MODES:
            raise ConsentError(f"mode must be one of {MODES}", op="recommend", code="consent_required")
        if mode == "non_profiled":
            if not session_id:
                raise ValidationError(
                    "non_profiled needs a session_id", op="recommend", code="invalid_request"
                )
            if user_id:
                raise ConsentError(
                    "non_profiled requests must not carry a user_id", op="recommend", code="consent_violation"
                )
            body: dict[str, Any] = {"surface": surface, "mode": mode, "session_id": session_id}
        else:
            if not user_id:
                raise ValidationError(f"{mode} needs a user_id", op="recommend", code="invalid_request")
            assert_opaque_user_id(user_id)
            body = {"surface": surface, "mode": mode, "user_id": user_id}
            if session_id:
                body["session_id"] = session_id
        if context:
            body["context"] = context
        if slots is not None:
            body["slots"] = slots
        if exclude_item_ids:
            body["exclude_item_ids"] = list(exclude_item_ids)
        if assignment_unit is not None:
            if not _HMAC_HEX.match(assignment_unit):
                raise _invalid(
                    "recommend",
                    "assignment_unit must be assignment_unit(secret, user_id): 64 lowercase hex, "
                    "never a raw id",
                )
            body["assignment_unit"] = assignment_unit
        rid = _clean_id(request_id) or new_request_id()
        budget = timeout_ms or self.recommend_timeout_ms
        slate = self._call(
            "recommend",
            "POST",
            "/recommendations",
            body,
            "serve",
            budget,
            retry=False,
            request_id=rid,
            trace_id=trace_id,
            deadline_ms=self.server_deadline_ms(budget),
            tester=tester,
        )
        if not slate.get("request_id"):
            slate["request_id"] = rid
        self._remember(slate)
        return slate

    def recommend_with_fallback(
        self, surface: str, fallback: Callable[[], F], **kwargs: Any
    ) -> Recommendation[F]:
        """`recommend`, or your own list when Patterna cannot answer in time.

        The request id is minted here, so on a timeout, network error, 5xx or unreadable answer the
        abandonment is sent in the background (engine 0.2). Consent and input bugs, pii_rejected and
        mode_conflict are raised, never hidden. kwargs are those of `recommend`.
        """
        rid = _clean_id(kwargs.pop("request_id", None)) or new_request_id()
        try:
            slate = self.recommend(surface, request_id=rid, **kwargs)
            return Recommendation("patterna", slate, str(slate.get("request_id") or rid))
        except PatternaError as e:
            if is_caller_bug(e):
                raise
            reason = _abandon_reason(e)
            if reason:
                self._submit(lambda: self.abandon(rid, reason, trace_id=kwargs.get("trace_id")))
            return Recommendation("fallback", fallback(), rid, e, reason is not None)

    def recommend_many(self, requests: Sequence[ManyRequest], **shared: Any) -> list[Recommendation[Any]]:
        """Several surfaces at once, in parallel threads, all with the same budget from the same start.

        Each falls back and is abandoned on its own and is its own impression. `shared` kwargs
        (tester, trace_id, timeout_ms) apply to every request; request_id is ignored. A caller bug in
        any request is raised after all have finished.
        """
        shared.pop("request_id", None)
        if not requests:
            return []

        def one(r: ManyRequest) -> Recommendation[Any]:
            return self.recommend_with_fallback(r.surface, r.fallback, **{**shared, **r.kwargs})

        with ThreadPoolExecutor(max_workers=len(requests)) as pool:
            futures = [pool.submit(one, r) for r in requests]
        return [f.result() for f in futures]

    def abandon(
        self,
        request_id: str,
        reason: str = "client_fallback",
        *,
        timeout_ms: int | None = None,
        trace_id: str | None = None,
    ) -> dict[str, Any]:
        """Tell Patterna the slate of request_id was not shown (engine 0.2).

        `recommend_with_fallback` does it for you; call it when a slate arrived but you showed
        something else. An id the engine never logged raises ApiError unknown_request.
        """
        if not request_id:
            raise _invalid("abandon", "request_id is required")
        if reason not in _ABANDON_REASONS:
            raise _invalid("abandon", f"reason must be one of {sorted(_ABANDON_REASONS)}")
        return self._call(
            "abandon",
            "POST",
            f"/recommendations/{quote(request_id, safe='')}/abandon",
            {"reason": reason},
            "serve",
            timeout_ms or self.ingest_timeout_ms,
            trace_id=trace_id,
        )

    def report_interactions(
        self,
        request_id: str,
        interactions: Sequence[dict[str, Any]],
        *,
        timeout_ms: int | None = None,
        trace_id: str | None = None,
    ) -> dict[str, Any]:
        """Report seen, click and dismiss for one slate.

        `kind` is required for anything that is not a document: for slates this client served, a
        missing kind on a community, product, event or tip raises ValidationError before sending.
        `displayed_position` (device re-sort) and `block` (engine 0.2, split blocks) are optional.
        """
        op = "report_interactions"
        if not request_id:
            raise _invalid(op, "request_id is required")
        if len(interactions) > _MAX_INTERACTIONS:
            raise _invalid(op, f"at most {_MAX_INTERACTIONS} interactions per call")
        for i, it in enumerate(interactions):
            self._check_interaction(request_id, it, i, op)
        items = [self._with_time(i) for i in interactions]
        return self._call(
            op,
            "POST",
            "/interactions",
            {"request_id": request_id, "interactions": items},
            "serve",
            timeout_ms or self.ingest_timeout_ms,
            trace_id=trace_id,
        )

    # ---- ingest ------------------------------------------------------------------------------

    def ingest_events(
        self, events: Iterable[dict[str, Any]], *, sign: bool | None = None, timeout_ms: int | None = None
    ) -> dict[str, Any]:
        out = []
        for e in events:
            ev = self._with_time(e)
            if "context" in ev and ev["context"] is not None:
                extra = set(ev["context"]) - _CONTEXT_KEYS
                if extra:
                    raise ValidationError(
                        f"event context has unknown keys {sorted(extra)}",
                        op="ingest_events",
                        code="invalid_context",
                    )
            if sign or (sign is None and self.signing_secret):
                if not self.signing_secret:
                    raise ValidationError(
                        "signing requested but no signing_secret configured",
                        op="ingest_events",
                        code="no_signing_secret",
                    )
                ev = sign_event(ev, self.signing_secret)
            out.append(ev)
        return self._call(
            "ingest_events",
            "POST",
            "/events",
            {"events": out},
            "ingest",
            timeout_ms or self.ingest_timeout_ms,
        )

    def upsert_items(
        self, items: Sequence[dict[str, Any]], *, timeout_ms: int | None = None
    ) -> dict[str, Any]:
        return self._call(
            "upsert_items",
            "PUT",
            "/catalog/items",
            {"items": list(items)},
            "ingest",
            timeout_ms or self.ingest_timeout_ms,
        )

    def set_item_status(
        self,
        item_id: str,
        status: str,
        *,
        kind: str = "document",
        published_at: datetime | None = None,
        timeout_ms: int | None = None,
    ) -> dict[str, Any]:
        body: dict[str, Any] = {"status": status}
        if published_at:
            body["published_at"] = rfc3339(published_at)
        return self._call(
            "set_item_status",
            "POST",
            f"/catalog/items/{quote(item_id, safe='')}/status?kind={quote(kind, safe='')}",
            body,
            "ingest",
            timeout_ms or self.ingest_timeout_ms,
        )

    def set_item_tags(
        self,
        item_id: str,
        tags: Sequence[dict[str, Any]],
        *,
        kind: str = "document",
        timeout_ms: int | None = None,
    ) -> dict[str, Any]:
        return self._call(
            "set_item_tags",
            "PUT",
            f"/catalog/items/{quote(item_id, safe='')}/tags?kind={quote(kind, safe='')}",
            {"tags": list(tags)},
            "ingest",
            timeout_ms or self.ingest_timeout_ms,
        )

    def delete_item(self, external_id: str, *, kind: str, timeout_ms: int | None = None) -> dict[str, Any]:
        """Remove an item for good (engine 0.2). A missing item raises ApiError unknown_item."""
        if not external_id:
            raise _invalid("delete_item", "external_id is required")
        if kind not in _ITEM_KINDS:
            raise _invalid("delete_item", f"unknown kind {kind!r}")
        return self._call(
            "delete_item",
            "DELETE",
            f"/catalog/items/{quote(external_id, safe='')}?kind={quote(kind, safe='')}",
            None,
            "ingest",
            timeout_ms or self.ingest_timeout_ms,
        )

    def list_items(
        self,
        *,
        kind: str | None = None,
        status: str | None = None,
        changed_since: datetime | str | None = None,
        cursor: str | None = None,
        limit: int | None = None,
        timeout_ms: int | None = None,
    ) -> dict[str, Any]:
        """One page of the catalog: {"items": [...], "next_cursor": ...}. changed_since needs engine 0.2."""
        if limit is not None and not 1 <= limit <= _MAX_ITEMS:
            raise _invalid("list_items", f"limit must be between 1 and {_MAX_ITEMS}")
        q: dict[str, str] = {}
        if kind:
            q["kind"] = kind
        if status:
            q["status"] = status
        if changed_since is not None:
            q["changed_since"] = (
                rfc3339(changed_since) if isinstance(changed_since, datetime) else changed_since
            )
        if cursor:
            q["cursor"] = cursor
        if limit:
            q["limit"] = str(limit)
        path = "/catalog/items" + (f"?{urlencode(q)}" if q else "")
        page = self._call("list_items", "GET", path, None, "ingest", timeout_ms or self.ingest_timeout_ms)
        page.setdefault("items", [])
        return page

    def items_changed_since(
        self, since: datetime | str, *, kind: str | None = None
    ) -> Iterator[dict[str, Any]]:
        """Every item changed after `since`, page by page (engine 0.2), for nightly reconciliation."""
        cursor: str | None = None
        while True:
            page = self.list_items(kind=kind, changed_since=since, cursor=cursor, limit=_MAX_ITEMS)
            yield from page["items"]
            cursor = page.get("next_cursor")
            if not cursor or not page["items"]:
                return

    # ---- users (admin key if set, else ingest) -----------------------------------------------

    def set_user_mode(self, user_id: str, mode: str, *, timeout_ms: int | None = None) -> dict[str, Any]:
        assert_opaque_user_id(user_id)
        if mode not in MODES:
            raise ConsentError(f"mode must be one of {MODES}", op="set_user_mode", code="consent_required")
        return self._call(
            "set_user_mode",
            "PUT",
            f"/users/{quote(user_id, safe='')}/mode",
            {"mode": mode},
            self._user_scope(),
            timeout_ms or self.ingest_timeout_ms,
        )

    def set_user_profile(
        self, user_id: str, profile: dict[str, Any], *, timeout_ms: int | None = None
    ) -> dict[str, Any]:
        assert_opaque_user_id(user_id)
        return self._call(
            "set_user_profile",
            "PUT",
            f"/users/{quote(user_id, safe='')}/profile",
            profile,
            self._user_scope(),
            timeout_ms or self.ingest_timeout_ms,
        )

    def delete_user(self, user_id: str, *, timeout_ms: int | None = None) -> dict[str, Any]:
        assert_opaque_user_id(user_id)
        return self._call(
            "delete_user",
            "DELETE",
            f"/users/{quote(user_id, safe='')}",
            None,
            self._user_scope(),
            timeout_ms or self.ingest_timeout_ms,
        )

    # ---- admin (admin key) -------------------------------------------------------------------

    def _admin(self, op: str, method: str, path: str, body: Any = None, *, retry: bool = True) -> Any:
        return self._request(op, method, path, body, "admin", self.admin_timeout_ms, retry=retry)

    def create_signing_secret(self) -> dict[str, Any]:
        """Issue the first signing secret (engine 0.2). Shown once; 409 when one exists (rotate it)."""
        return dict(self._admin("create_signing_secret", "POST", "/settings/signing-secret", retry=False))

    def rotate_signing_secret(self) -> dict[str, Any]:
        """Replace the signing secret (owner keys). Roll the new one out to your signers first."""
        return dict(
            self._admin("rotate_signing_secret", "POST", "/settings/signing-secret/rotate", retry=False)
        )

    def list_surfaces(self) -> list[dict[str, Any]]:
        return self._list(self._admin("list_surfaces", "GET", "/surfaces"), "list_surfaces")

    def get_surface(self, key: str) -> dict[str, Any]:
        for s in self.list_surfaces():
            if s.get("key") == key:
                return s
        raise ApiError(f"no surface {key!r}", op="get_surface", code="unknown_surface", status=404)

    def set_surface_status(self, key: str, status: str) -> dict[str, Any]:
        """shadow, testers, live or off (the kill switch)."""
        if not key:
            raise _invalid("set_surface_status", "surface key is required")
        if status not in _SURFACE_STATUSES:
            raise _invalid("set_surface_status", "status must be shadow, testers, live or off")
        return dict(
            self._admin("set_surface_status", "PATCH", f"/surfaces/{quote(key, safe='')}", {"status": status})
        )

    def set_surface_testers(self, key: str, user_ids: Sequence[str]) -> dict[str, Any]:
        """Replace the tester roster: at most 200 random user ids, never emails."""
        if not key:
            raise _invalid("set_surface_testers", "surface key is required")
        if len(user_ids) > 200:
            raise _invalid("set_surface_testers", "at most 200 testers")
        for uid in user_ids:
            assert_opaque_user_id(uid)
        return dict(
            self._admin(
                "set_surface_testers",
                "PATCH",
                f"/surfaces/{quote(key, safe='')}",
                {"testers": list(user_ids)},
            )
        )

    def list_flows(self, surface: str) -> list[dict[str, Any]]:
        if not surface:
            raise _invalid("list_flows", "surface key is required")
        return self._list(
            self._admin("list_flows", "GET", f"/surfaces/{quote(surface, safe='')}/flows"), "list_flows"
        )

    def get_flow(self, surface: str, version: int = 0) -> dict[str, Any] | None:
        """One flow version; 0 (default) is the active one. None when there is none."""
        if version == 0:
            active = self.get_surface(surface).get("active_flow_version")
            if active is None:
                return None
            version = int(active)
        return next((f for f in self.list_flows(surface) if f.get("version") == version), None)

    def create_flow(self, surface: str, config: dict[str, Any], note: str | None = None) -> dict[str, Any]:
        """Store a new, inactive version. Not retried: a lost answer would create a second version."""
        if not surface:
            raise _invalid("create_flow", "surface key is required")
        if not isinstance(config, dict):
            raise _invalid("create_flow", "config must be a dict")
        body: dict[str, Any] = {"config": config}
        if note:
            body["note"] = note
        return dict(
            self._admin(
                "create_flow", "POST", f"/surfaces/{quote(surface, safe='')}/flows", body, retry=False
            )
        )

    def activate_flow(self, surface: str, version: int, note: str | None = None) -> None:
        """Make a version live. With the two-person rule the engine answers 409 approval_required."""
        if not surface or version <= 0:
            raise _invalid("activate_flow", "surface key and a positive version are required")
        self._admin(
            "activate_flow",
            "POST",
            f"/surfaces/{quote(surface, safe='')}/flows/{int(version)}/activate",
            {"note": note} if note else None,
        )

    def rollback_flow(self, surface: str, note: str = "rollback") -> int:
        """Re-activate the version that was active before the current one; returns its number."""
        active = self.get_surface(surface).get("active_flow_version")
        earlier = [
            f for f in self.list_flows(surface) if f.get("activated_at") and f.get("version") != active
        ]
        if not earlier:
            raise ValidationError(
                "no earlier activated flow version", op="rollback_flow", code="nothing_to_roll_back"
            )
        target = int(max(earlier, key=lambda f: str(f["activated_at"]))["version"])
        self.activate_flow(surface, target, note)
        return target

    def plan_flow(self, surface: str, config: dict[str, Any], activate: bool = True) -> dict[str, Any]:
        """What apply_flow would do, without writing anything."""
        want = canonical_json(config)
        active = self.get_surface(surface).get("active_flow_version")
        version = 0
        for f in self.list_flows(surface):
            if f.get("config") is None or canonical_json(f["config"]) != want:
                continue
            if f.get("version") == active:
                return {
                    "surface": surface,
                    "version": active,
                    "created": False,
                    "activated": False,
                    "unchanged": True,
                }
            version = max(version, int(f.get("version") or 0))
        return {
            "surface": surface,
            "version": version,
            "created": version == 0,
            "activated": activate,
            "unchanged": False,
        }

    def apply_flow(
        self, surface: str, config: dict[str, Any], *, note: str | None = None, activate: bool = True
    ) -> dict[str, Any]:
        """Make the surface's flow match config (compared as canonical JSON); for CI.

        Nothing happens when the active version matches; else a version with the same config is
        reused or a new one created, then activated when `activate`.
        """
        res = self.plan_flow(surface, config, activate)
        if res["unchanged"]:
            return res
        if res["created"]:
            res["version"] = int(self.create_flow(surface, config, note)["version"])
        if res["activated"]:
            self.activate_flow(surface, res["version"], note)
        return res

    def list_experiments(self) -> list[dict[str, Any]]:
        return self._list(self._admin("list_experiments", "GET", "/experiments"), "list_experiments")

    def start_experiment(self, experiment: dict[str, Any]) -> dict[str, Any]:
        """Start a holdout. Not retried: a lost answer would start a second one."""
        if not all(experiment.get(k) for k in ("surface", "name", "primary_metric")):
            raise _invalid("start_experiment", "surface, name and primary_metric are required")
        return dict(self._admin("start_experiment", "POST", "/experiments", experiment, retry=False))

    def end_experiment(self, experiment_id: str) -> None:
        if not experiment_id:
            raise _invalid("end_experiment", "experiment id is required")
        self._admin("end_experiment", "POST", f"/experiments/{quote(experiment_id, safe='')}/end")

    def ledger(
        self,
        *,
        surface: str | None = None,
        date_from: str | None = None,
        date_to: str | None = None,
        weekly: bool = False,
    ) -> list[dict[str, Any]]:
        """Ledger rows (one per surface), or weekly rows per arm with weekly=True. Dates are YYYY-MM-DD."""
        q = {k: v for k, v in (("surface", surface), ("from", date_from), ("to", date_to)) if v}
        if weekly:
            q["bucket"] = "week"
        path = "/proof/ledger" + (f"?{urlencode(q)}" if q else "")
        return self._list(self._admin("ledger", "GET", path), "ledger")

    def export_ledger(self, date_from: str, date_to: str, *, surface: str | None = None) -> dict[str, Any]:
        """Queue an export of impressions and interactions; poll export_status until 'done'."""
        if not date_from or not date_to:
            raise _invalid("export_ledger", "date_from and date_to are required")
        body = {"from": date_from, "to": date_to, **({"surface": surface} if surface else {})}
        return dict(self._admin("export_ledger", "POST", "/proof/export", body, retry=False))

    def export_status(self, job_id: str) -> dict[str, Any]:
        if not job_id:
            raise _invalid("export_status", "job id is required")
        return dict(self._admin("export_status", "GET", f"/proof/exports/{quote(job_id, safe='')}"))

    # ---- plumbing ----------------------------------------------------------------------------

    @staticmethod
    def _list(data: Any, op: str) -> list[dict[str, Any]]:
        if data == {}:
            return []
        if not isinstance(data, list):
            raise PatternaError(f"{op}: expected a JSON array", op=op, code="bad_response")
        return data

    def _submit(self, fn: Callable[[], Any]) -> None:
        with self._lock:
            if self._background is None:
                self._background = ThreadPoolExecutor(max_workers=2, thread_name_prefix="patterna")
            bg = self._background

        def run() -> None:
            try:
                fn()
            except PatternaError as e:
                if self._on_background_error:
                    self._on_background_error(e)

        bg.submit(run)

    def _remember(self, slate: dict[str, Any]) -> None:
        rid = slate.get("request_id")
        if not rid or self._slate_memory == 0:
            return
        kinds = {
            str(it.get("item_id")): str(it["kind"])
            for it in slate.get("items", [])
            if it.get("kind") and it.get("kind") != "document"
        }
        if not kinds:
            return
        with self._lock:
            self._kinds.pop(rid, None)
            self._kinds[rid] = kinds
            while len(self._kinds) > self._slate_memory:
                self._kinds.popitem(last=False)

    def _check_interaction(self, request_id: str, it: dict[str, Any], i: int, op: str) -> None:
        if not it.get("item_id"):
            raise _invalid(op, f"interaction {i}: item_id is required")
        dp = it.get("displayed_position")
        if int(it.get("position", 0)) < 0 or (dp is not None and int(dp) < 0):
            raise _invalid(op, f"interaction {i}: positions are 0-based and never negative")
        block = it.get("block")
        if block is not None and len(str(block)) > _MAX_BLOCK:
            raise _invalid(op, f"interaction {i}: block is limited to {_MAX_BLOCK} characters")
        kind = it.get("kind")
        if kind:
            if kind not in _ITEM_KINDS:
                raise _invalid(op, f"interaction {i}: unknown kind {kind!r}")
            return
        with self._lock:
            known = self._kinds.get(request_id, {}).get(str(it["item_id"]))
        if known:
            raise _invalid(
                op, f"interaction {i}: kind is required for {known} items (only documents may leave it empty)"
            )

    def _user_scope(self) -> str:
        return "admin" if self.keys.get("admin") else "ingest"

    @staticmethod
    def _with_time(d: dict[str, Any]) -> dict[str, Any]:
        out = dict(d)
        ts = out.get("occurred_at")
        if isinstance(ts, datetime):
            out["occurred_at"] = rfc3339(ts)
        elif ts is None:
            out["occurred_at"] = rfc3339(datetime.now(timezone.utc))
        return out

    def _call(
        self, op: str, method: str, path: str, body: Any, scope: str, timeout_ms: int, **kw: Any
    ) -> dict[str, Any]:
        """_request for endpoints that answer a JSON object (or nothing)."""
        out = self._request(op, method, path, body, scope, timeout_ms, **kw)
        if not isinstance(out, dict):
            raise PatternaError(f"{op}: expected a JSON object", op=op, code="bad_response")
        return out

    def _request(
        self,
        op: str,
        method: str,
        path: str,
        body: Any,
        scope: str,
        timeout_ms: int,
        *,
        retry: bool = True,
        request_id: str | None = None,
        trace_id: str | None = None,
        deadline_ms: int | None = None,
        tester: bool = False,
    ) -> Any:
        key = self.keys.get(scope)
        if not key:
            raise ValidationError(f"no {scope} key configured for {op}", op=op, code="missing_key")
        rid = _clean_id(request_id) or new_request_id()
        headers = {
            "Authorization": f"Bearer {key}",
            "Accept": "application/json",
            "User-Agent": self.user_agent,
            "X-Request-Id": rid,
        }
        trace = _clean_id(trace_id) or (_clean_id(self._trace_id()) if self._trace_id else None)
        if trace:
            headers["X-Trace-Id"] = trace
        if deadline_ms is not None:
            headers["X-Deadline-Ms"] = str(max(1, int(deadline_ms)))
        if tester:
            headers["X-Patterna-Tester"] = "1"
        data = None
        if body is not None:
            headers["Content-Type"] = "application/json"
            data = json.dumps(body, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
        url = self.base_url + path
        attempts = 2 if retry else 1
        last: PatternaError | None = None
        for attempt in range(attempts):
            try:
                status, hdrs, raw = self._transport(method, url, headers, data, timeout_ms / 1000)
            except TimeoutError:  # the builtin: urllib raises it on a read timeout
                raise PatternaTimeout(
                    f"{op} exceeded {timeout_ms} ms", op=op, code="timeout", request_id=rid
                ) from None
            except (urllib.error.URLError, OSError) as e:
                last = NetworkError(f"{op}: {e}", op=op, code="network", request_id=rid)
                if attempt + 1 < attempts:
                    time.sleep(self.retry_delay_ms / 1000)
                    continue
                raise last from None
            hdr_rid = hdrs.get("x-request-id") or rid
            if 200 <= status < 300:
                if not raw:
                    return {}
                try:
                    return json.loads(raw)
                except ValueError:
                    raise PatternaError(
                        f"{op}: response is not JSON",
                        op=op,
                        code="bad_response",
                        status=status,
                        request_id=hdr_rid,
                    ) from None
            try:
                payload = json.loads(raw) if raw else {}
            except ValueError:
                payload = {"error": {"code": "bad_response", "message": raw[:200].decode("utf-8", "replace")}}
            err = payload.get("error", payload) if isinstance(payload, dict) else {}
            last = ApiError(
                str(err.get("message", f"{op} failed with {status}")),
                op=op,
                code=str(err.get("code", f"http_{status}")),
                status=status,
                request_id=str(err.get("request_id") or hdr_rid),
                details=err.get("details"),
            )
            if status >= 500 and attempt + 1 < attempts:
                time.sleep(self.retry_delay_ms / 1000)
                continue
            raise last
        raise last or NetworkError(f"{op}: no attempt made", op=op, code="network")  # pragma: no cover


def with_fallback(
    fn: Callable[[], dict[str, Any]], fallback: Callable[[], dict[str, Any]]
) -> tuple[dict[str, Any], PatternaError | None]:
    """Serve from Patterna, or from your own list when it fails, and tell the caller which happened.

    Consent and input bugs, pii_rejected and mode_conflict are raised, never hidden. Prefer
    `PatternaClient.recommend_with_fallback`, which also reports abandoned slates.
    """
    try:
        return fn(), None
    except PatternaError as e:
        if is_caller_bug(e):
            raise
        return fallback(), e
