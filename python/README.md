# patterna-sdk (Python)

Version 0.4.0 (see `CHANGELOG.md`): the production engine reports `engine_version: 0.3.1` on `GET /v1/health` (the contract
version), and every field in this release is live on it. Works against engine 0.2 or later;
older engines ignore the fields they do not know.

Python client for the Patterna API, with the same shape as `patterna-sdk` (TypeScript) and the
Go SDK: serve, ingest and admin calls, a consent gate, typed errors, one retry on idempotent
calls, client-minted request ids with abandonment, and event signatures and assignment units that
share test vectors across the three SDKs. No dependencies: `urllib` and `hmac` only. Python 3.10+.

Not on PyPI (by decision). Install it from the public repository at tag `v0.4.0`:

```sh
pip install "patterna-sdk @ git+https://github.com/payam-h/patterna-sdk@v0.4.0#subdirectory=python"
```

```python
from patterna_sdk import PatternaClient, interaction_for, measured_context

patterna = PatternaClient(
    "https://patterna-api.orven.am/v1",
    serve_key=os.environ["PATTERNA_SERVE_KEY"],
    ingest_key=os.environ["PATTERNA_INGEST_KEY"],
    signing_secret=os.environ.get("PATTERNA_SIGNING_SECRET"),  # the string as returned; never hex-decoded
    trace_id=lambda: current_trace_id(),  # optional: X-Trace-Id on every call
)

rec = patterna.recommend_with_fallback(
    "home_articles",
    lambda: newest_article_ids(),
    mode="personalized",
    user_id="u_7f3a",
    context=measured_context({"locale": "nl"}, analytics_consent=True),
)
if rec.source == "patterna":
    slate = rec.value
    click = interaction_for(slate, slate["items"][0]["item_id"], "click")  # served kind and position
    patterna.report_interactions(slate["request_id"], [click])
else:
    log.info("patterna fallback", request_id=rec.request_id, code=rec.error.code, abandoned=rec.abandoned)

patterna.close()  # on shutdown: waits for background abandonments
```

| Method | Key | Budget | Retried |
|---|---|---|---|
| `recommend(surface, mode=, user_id= / session_id=, context=, ...)` | serve | 150 ms | never |
| `recommend_with_fallback(surface, fallback, **recommend_kwargs)`, `recommend_many(requests)` | serve | 150 ms | never |
| `abandon(request_id, reason)` | serve | 5 s | once |
| `report_interactions(request_id, interactions)` | serve | 5 s | once |
| `ingest_events(events, sign=)` | ingest | 5 s | once |
| `upsert_items(items)`, `set_item_status(item_id, status, kind=)`, `set_item_tags(item_id, tags, kind=)` | ingest | 5 s | once |
| `delete_item(external_id, kind=)`, `list_items(changed_since=)`, `items_changed_since(since)` | ingest | 5 s | once |
| `set_user_mode`, `set_user_profile`, `delete_user` | admin if set, else ingest | 5 s | once |
| `create_signing_secret` and the admin methods below | admin | 30 s | idempotent ones once |

- **Deadlines and ids.** `recommend` sends `X-Deadline-Ms`: 120 ms by default
  (`recommend_deadline_ms`, always below `recommend_timeout_ms`; the engine's own default budget is
  120 ms too), shrunk by the same margin when
  `timeout_ms` is shorter. Every call sends a client-minted `X-Request-Id` (`request_id=`
  overrides) and errors carry it. `trace_id=` per call or the client's `trace_id` callable sets
  `X-Trace-Id`. `tester=True` sends `X-Patterna-Tester: 1`.
- **Abandonment.** On a timeout, network error, 5xx or unreadable answer,
  `recommend_with_fallback` sends `POST /recommendations/{id}/abandon` on a background thread so
  the impression counts as a client fallback, not as a slate with zero taps. A 4xx is not
  abandoned (nothing was logged). Call `abandon(request_id)` when a slate arrived but you showed
  something else.
- **Errors.** Every failure is a `PatternaError` with `op`, `code`, `status`, `request_id`,
  `details` and `.transient`. `ApiErrorCode` and `is_api_error(err, code)`:
  `no_candidates` (422), `surface_off` (409) and `unknown_surface` (404) fall back (alert on
  the last, it is a configuration bug); `pii_rejected` and `mode_conflict` (422) are raised by
  the fallback helpers, as are `ConsentError` and `ValidationError`; `unknown_request` and
  `unknown_item` (404) come from `abandon`, `report_interactions` and catalog calls.
- **Consent.** `recommend` raises `ConsentError` unless `mode` is `personalized`, `cohort` or
  `non_profiled`; user ids must be opaque (no email, no spaces). `measured_context(context,
  analytics_consent)` sets `measured`: `False` keeps the request out of the ledger
  and training data. `assignment_unit=assignment_unit(experiment_secret, your_user_id)` keeps one non-profiled person in one holdout arm; only the 64-hex shape is accepted.

## Several surfaces in one call

```python
from patterna_sdk import ManyRequest

articles, products = patterna.recommend_many(
    [
        ManyRequest("home_articles", newest_article_ids, {"mode": "cohort", "user_id": uid, "context": ctx}),
        ManyRequest(
            "home_products", editorial_product_ids, {"mode": "cohort", "user_id": uid, "context": ctx}
        ),
    ],
    trace_id=trace,
)
```

The requests run in parallel threads with the same budget from the same start, so two surfaces
cost one budget. Each falls back and is abandoned on its own and is its own impression and ledger
row. Use it when the placements are separate on screen.

## Interactions: kind, re-sorting and split blocks

`kind` is required for anything that is not a document; for
slates this client served, a missing kind on a community, product, event or tip raises
`ValidationError` before sending. Keep `position` as the served slot; set `displayed_position`
when the app re-sorted on the device. When one slate of 7 is split into a top slot and an end
carousel of six, report the served positions with the block:

```python
top = interaction_for(slate, slate["items"][0]["item_id"], "seen", block="top")  # position 0
rest = [
    interaction_for(slate, it["item_id"], "seen", block="end_carousel") for it in slate["items"][1:]
]  # 1..6
patterna.report_interactions(slate["request_id"], [top, *rest])
```

## Admin (admin key)

For CI scripts that keep flow configs in your repository: `list_surfaces`, `get_surface`,
`set_surface_status`, `set_surface_testers`, `update_surface`, `plan_surface`, `apply_surface`,
`surface_metrics`, `list_flows`, `get_flow`, `create_flow`,
`activate_flow`, `rollback_flow`, `plan_flow`, `apply_flow(surface, config, note=, activate=)`,
`list_experiments`, `start_experiment`, `end_experiment`, `ledger(surface=, date_from=,
date_to=, weekly=, by=, block=)`, `ledger_by_block`, `export_ledger`, `export_status`,
`get_settings`, `update_settings(phase_tags=)`, `set_phase_tags`, `health`,
`create_signing_secret`, `rotate_signing_secret`. `apply_flow` compares configs as canonical JSON; nothing happens when the
active version matches. The Go SDK ships the same as a CLI (`patterna`, in the `go/` module).

## Engine 0.3

**Control list per page.** `context.control_item_ids` (at most 50) is your own editorial list for
this page: in the control arm it is the slate, in this order; in treatment it fills short slates.
`context.member_of` (at most 500) lists communities the user is in, never offered (meant for
`non_profiled`). Both limits are checked before sending.

```python
slate = patterna.recommend(
    "pairs_with",
    mode="non_profiled",
    session_id=sid,
    context={"locale": "nl", "anchor_item_id": article_id, "control_item_ids": editor_picks},
)
slate.get("control_source")  # "request" | "by_anchor" | "surface" on editorial slates
slate.get("fill_count")  # trailing items filled from the editorial list; they carry "fill": True
```

`model_versions` is decoded when present; engine 0.3 logs it on the impression (trace, export)
and does not send it on the answer.

**`shown: false`.** Below the surface's `min_slots` the answer has no items. Hide the block and
never abandon it: `recommend_with_fallback` returns `source="patterna"`, `shown=False`, does not
call your fallback and abandons nothing; `abandon()` on such a slate raises `not_shown` without
sending (the engine would answer 409 `not_shown`). `slate_visible(slate)` does the check on a
plain slate. Fallbacks and ordinary slates have `shown=True`.

**Errors.** `ApiErrorCode.MODE_REQUIRED` (422: `set_user_profile` without `mode` for a user
Patterna has no row for; set the mode first) and `ApiErrorCode.NOT_SHOWN` (409).

**Coach products.** Products an AI coach answer named, for `ingest_events`:

```python
patterna.ingest_events(
    [
        coach_product_shown(
            mode="personalized", user_id=pid, product_id="p_mag", conversation_id=conv_id, position=0
        ),
    ]
)
```

`coach_product_tapped` takes the same arguments. A `non_profiled` user passes `session_id`
instead of `user_id`; the helper enforces it.

**Surface files.** `apply_surface({"surface": "pairs_with", "min_slots": 3, "device_resort": True,
"fallback": {"kind": "curated", "item_ids": [...], "by_anchor": {"doc_9": ["p_1", "p_2"]}}})`
patches only the fields that differ (`plan_surface` says which, writing nothing). The fallback
is replaced as a whole. `surface_metrics(key, days=, block=)` adds `not_shown_share` and
`client_abandoned_rate`; `ledger_by_block()` returns one row per surface and block;
`set_phase_tags(True)` turns `phase_fit` on; `health()["engine_version"]`.

## Engine round 5: context, editorial attributes, activity events

Everything the engine reads is listed at https://patterna.orven.am/docs/signals; these are the SDK's parts of it.

```python
from patterna_sdk import TIER_FREE, time_context

patterna.recommend(
    "home_articles",
    mode="personalized",
    user_id=pid,
    context={
        "locale": "nl",
        **time_context(now, user_tz),  # hour_bucket and weekday (0 = Monday) in the user's zone
        "subscription_tier": TIER_FREE,  # "premium" or your own tier name (<= 32 chars)
        "tenure_days": days_since_signup,
        "region": "Noord-Holland",  # one of the tenant's regions; never a location
        "session_items": recently_opened,  # most recent first, at most 20
    },
)
```

`time_context(at, tz)` takes an aware datetime and an IANA name or tzinfo (stdlib `zoneinfo`;
on Windows install `tzdata`); without `tz` it reads the datetime's own wall clock.
`RecommendContext` and `EventContext` are the TypedDicts. Out-of-range values raise
`ValidationError` before sending; the same keys are allowed (and checked) in an event's context.

**Editorial attributes.** `editorial` on an item is the typed form of `attributes`
(`ItemAttributes`, or a dict of its fields):

```python
patterna.upsert_items(
    [
        {
            "external_id": "p_magnesium",
            "kind": "product",
            "title": "Magnesium",
            "locale": "nl",
            "editorial": ItemAttributes(
                format="product",
                available=in_stock,
                repurchase_days=30,
                price_cents=2495,
                currency="EUR",
                image_url=img,
                url=shop_url,
            ),
        }
    ]
)
```

The engine reads `format`, `duration_min`/`reading_time_min`, `difficulty`, `author`,
`available` (False is never served), `expires_at`, `starts_at` (events; aware datetimes or RFC
3339 strings), `repurchase_days`, `region` and `phase_fit`; the panel shows `price_cents`,
`compare_at_cents`, `currency`, `image_url`, `url`, `product_type` and `categories`. A typed key
wins over the same key in `attributes`, which keeps any others. `region` is sent as the item's
top-level `region`, the field the engine validates and ranks on.

Products from a Shopify store do not need this call: the jobs CLI syncs the public storefront
(`python -m patterna_jobs shopify-storefront --tenant <slug> --shop-url https://<shop>`) and
turns an orders export into co-purchase pairs (`orders-csv --csv <file>`). There is no admin API
call for either; run them where the jobs run.

**Activity events**, each on an `EventBase(mode, user_id=, session_id=, surface=, request_id=,
occurred_at=, event_id=, source=, context=)`:

```python
who = EventBase(mode=mode, user_id=pid, session_id=sid)
community_post_created(who, post_id=p, community_id=c)
community_reply(who, post_id=p, community_id=c)
community_reaction(who, post_id=p, community_id=c, reaction="heart")
paywall_viewed(who, item_id=item)  # who.request_id: the slate it came from
push_opened(who, item_id=item, notification_type="weekly_digest")
search_performed(who, query_hash=h, result_count=n, clicked_item_id=item)
item_engaged(who, item_id=item, engaged_seconds=42, max_scroll_percent=80)
item_completed(who, item_id=workout, kind="document", collection="workouts")
order_placed(who, order_id_hash=h, product_ids=ids, value_cents=4990, currency="EUR")
```

Identity follows consent: personalized and cohort carry the user id (required) and the session
id when given; non_profiled carries the session id only and a user id is refused. `query_hash`
must be 16 to 128 lowercase hex characters (hash the query yourself, e.g. HMAC-SHA256 with a key
you hold), so query text cannot travel by mistake. `item_completed` and `order_placed` are
ranking events: a client with a `signing_secret` signs them in `ingest_events`.

**Reasons.** A slot the engine cannot truthfully explain says `REASON_RECOMMENDED`
(`"recommended"`, "Aanbevolen"/"Recommended"); `REASON_KEYS` lists every key. Treat an unknown
key like `recommended`.

**Flows.** `GENERATOR_KINDS` includes `time_fit` and `format_fit` (silent: they move scores and
never name a reason); `FlowFilters` (TypedDict) has `exclude_dismissed_days`,
`exclude_consumed_days`, `paywall_cooldown_days`, `repurchase_days`, `hide_unavailable` and
`relevance_floor`. `validate_flow_config` checks a config as the engine does on save, plus
unknown filter keys; `create_flow`, `plan_flow` and `apply_flow` run it first.

## Signing

`sign_event(event, secret)` and `compute_event_signature`: lowercase hex HMAC-SHA256 over the
canonical JSON of the event without `signature`, keyed with the UTF-8 bytes of the secret
string exactly as the API returned it (never hex-decoded). The shared vector is `8baf7299…aac9`
with key `test-signing-key`.

In a checkout of `python/`:

```sh
python -m venv .venv && .venv/bin/pip install -e '.[dev]'
.venv/bin/pytest -q && .venv/bin/ruff check . && .venv/bin/ruff format --check . && .venv/bin/mypy
```
