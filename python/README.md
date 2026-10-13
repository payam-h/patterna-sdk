# patterna-sdk (Python)

Version 0.2.0 (see `CHANGELOG.md`). Features marked **engine 0.2** need the engine release that
adds the matching endpoint, header or field; against an older engine they are ignored or answer
404.

Python client for the Patterna API, with the same shape as `@patterna/sdk` (TypeScript) and the
Go SDK: serve, ingest and admin calls, a consent gate, typed errors, one retry on idempotent
calls, client-minted request ids with abandonment, and event signatures and assignment units that
share test vectors across the three SDKs. No dependencies: `urllib` and `hmac` only. Python 3.10+.

```sh
pip install -e packages/sdk-py      # not on PyPI
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
| `abandon(request_id, reason)` (engine 0.2) | serve | 5 s | once |
| `report_interactions(request_id, interactions)` | serve | 5 s | once |
| `ingest_events(events, sign=)` | ingest | 5 s | once |
| `upsert_items(items)`, `set_item_status(item_id, status, kind=)`, `set_item_tags(item_id, tags, kind=)` | ingest | 5 s | once |
| `delete_item(external_id, kind=)`, `list_items(changed_since=)`, `items_changed_since(since)` (engine 0.2) | ingest | 5 s | once |
| `set_user_mode`, `set_user_profile`, `delete_user` | admin if set, else ingest | 5 s | once |
| `create_signing_secret` (engine 0.2) and the admin methods below | admin | 30 s | idempotent ones once |

- **Deadlines and ids.** `recommend` sends `X-Deadline-Ms` (engine 0.2): 120 ms by default
  (`recommend_deadline_ms`, always below `recommend_timeout_ms`), shrunk by the same margin when
  `timeout_ms` is shorter. Every call sends a client-minted `X-Request-Id` (`request_id=`
  overrides) and errors carry it. `trace_id=` per call or the client's `trace_id` callable sets
  `X-Trace-Id`. `tester=True` sends `X-Patterna-Tester: 1` (engine 0.2).
- **Abandonment (engine 0.2).** On a timeout, network error, 5xx or unreadable answer,
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
  analytics_consent)` sets `measured` (engine 0.2): `False` keeps the request out of the ledger
  and training data. `assignment_unit=assignment_unit(experiment_secret, your_user_id)` (engine
  0.2) keeps one non-profiled person in one holdout arm; only the 64-hex shape is accepted.

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

`kind` is required for anything that is not a document (engine 0.2 refuses it otherwise); for
slates this client served, a missing kind on a community, product, event or tip raises
`ValidationError` before sending. Keep `position` as the served slot; set `displayed_position`
when the app re-sorted on the device. When one slate of 7 is split into a top slot and an end
carousel of six, report the served positions with the block (engine 0.2):

```python
top = interaction_for(slate, slate["items"][0]["item_id"], "seen", block="top")  # position 0
rest = [
    interaction_for(slate, it["item_id"], "seen", block="end_carousel") for it in slate["items"][1:]
]  # 1..6
patterna.report_interactions(slate["request_id"], [top, *rest])
```

## Admin (admin key)

For CI scripts that keep flow configs in your repository: `list_surfaces`, `get_surface`,
`set_surface_status`, `set_surface_testers`, `list_flows`, `get_flow`, `create_flow`,
`activate_flow`, `rollback_flow`, `plan_flow`, `apply_flow(surface, config, note=, activate=)`,
`list_experiments`, `start_experiment`, `end_experiment`, `ledger(surface=, date_from=,
date_to=, weekly=)`, `export_ledger`, `export_status`, `create_signing_secret` (engine 0.2),
`rotate_signing_secret`. `apply_flow` compares configs as canonical JSON; nothing happens when the
active version matches. The Go SDK ships the same as a CLI (`packages/sdk-go/cmd/patterna`).

## Signing

`sign_event(event, secret)` and `compute_event_signature`: lowercase hex HMAC-SHA256 over the
canonical JSON of the event without `signature`, keyed with the UTF-8 bytes of the secret
string exactly as the API returned it (never hex-decoded). The shared vector is `8baf7299…aac9`
with key `test-signing-key`.

```sh
cd packages/sdk-py && python -m venv .venv && .venv/bin/pip install -e '.[dev]'
.venv/bin/pytest -q && .venv/bin/ruff check . && .venv/bin/ruff format --check . && .venv/bin/mypy
```
