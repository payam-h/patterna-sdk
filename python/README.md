# patterna-sdk (Python)

Version 0.3.0 (see `CHANGELOG.md`). Needs engine 0.2 or later; the 0.3 additions need engine 0.3
(API 0.2.0), which is the engine in production.

Python client for the Patterna API, with the same shape as `patterna-sdk` (TypeScript) and the
Go SDK: serve, ingest and admin calls, a consent gate, typed errors, one retry on idempotent
calls, client-minted request ids with abandonment, and event signatures and assignment units that
share test vectors across the three SDKs. No dependencies: `urllib` and `hmac` only. Python 3.10+.

Not on PyPI. Install it from the public repository at tag `v0.3.0`:

```sh
pip install "patterna-sdk @ git+https://github.com/payam-h/patterna-sdk@v0.3.0#subdirectory=python"
# inside this monorepo: pip install -e packages/sdk-py
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
active version matches. The Go SDK ships the same as a CLI (`packages/sdk-go/cmd/patterna`).

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

## Signing

`sign_event(event, secret)` and `compute_event_signature`: lowercase hex HMAC-SHA256 over the
canonical JSON of the event without `signature`, keyed with the UTF-8 bytes of the secret
string exactly as the API returned it (never hex-decoded). The shared vector is `8baf7299…aac9`
with key `test-signing-key`.

```sh
cd packages/sdk-py && python -m venv .venv && .venv/bin/pip install -e '.[dev]'
.venv/bin/pytest -q && .venv/bin/ruff check . && .venv/bin/ruff format --check . && .venv/bin/mypy
```
