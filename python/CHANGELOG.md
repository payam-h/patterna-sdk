# Changelog: patterna-sdk (Python)

SDK 0.2 needs engine 0.2 or later (the engine in production since release `7a7683e`).

Not on PyPI. Install from the public repository:
`pip install "patterna-sdk @ git+https://github.com/payam-h/patterna-sdk@v0.3.0#subdirectory=python"`.

## 0.3.0 (2026-10-13)

Typed support for engine 0.3 (API 0.2.0). Still works against engine 0.2: the new keys are
simply absent there.

### Added

- **`context.control_item_ids`** (at most 50) and **`context.member_of`** (at most 500) on
  `recommend`, checked before sending.
- **Slates**: `control_source` (constants `CONTROL_SOURCE_REQUEST`, `CONTROL_SOURCE_BY_ANCHOR`,
  `CONTROL_SOURCE_SURFACE`), `fill_count`, and `fill: True` on the trailing filled items (set
  from `fill_count` when the engine does not mark them), `model_versions` when present.
- **`Recommendation.shown`**: False only for a `shown: false` answer, which is returned as
  `source="patterna"` with no items; the fallback is not called and nothing is abandoned.
  `slate_visible(slate)` for plain slates.
- **`abandon()`** refuses a slate this client received as `shown: false` (`not_shown`, nothing
  sent).
- **Error codes** `ApiErrorCode.MODE_REQUIRED` (a caller bug, never hidden by a fallback) and
  `ApiErrorCode.NOT_SHOWN`.
- **Coach events**: `coach_product_shown(...)`, `coach_product_tapped(...)` and
  `EVENT_COACH_PRODUCT_SHOWN` / `EVENT_COACH_PRODUCT_TAPPED`, with the consent identity rules.
- **Admin**: `update_surface`, `plan_surface`, `apply_surface` (item_kinds, slots, fallback with
  `by_anchor`, `min_slots`, `device_resort`, notes, exploration_share), `surface_metrics` (with
  `not_shown_share`, `client_abandoned_rate`), `ledger(by="block" | block=...)` and
  `ledger_by_block`, `get_settings`, `update_settings(phase_tags=)`, `set_phase_tags`, `health`
  (`engine_version`).

### Fixed

- `set_surface_status` and `set_surface_testers` return the surface after the change: the
  engine answers `PATCH /surfaces/{key}` with `{key, ok}`, so the client reads the surface back.

## 0.2.0 (2026-10-13)

### Added

- **Client-minted request ids**: every call sends `X-Request-Id` (UUIDv4) unless `request_id=`
  gives one; timeout and network errors carry it.
- **`recommend_with_fallback(surface, fallback, **recommend_kwargs)`** returns a
  `Recommendation(source, value, request_id, error, abandoned)`. On a timeout, network error,
  5xx or unreadable answer it sends the abandonment
  (`POST /recommendations/{id}/abandon`) on a background thread; `close()` waits for it, and
  failures go to `on_background_error`. `abandon(request_id, reason)` for a slate that arrived
  but was not shown.
- **Server deadline**: `X-Deadline-Ms` on `recommend`, 120 ms by default
  (`recommend_deadline_ms`), shrunk by the same margin when `timeout_ms` is shorter.
- **Trace ids**: `X-Trace-Id` from `trace_id=` per call or the client's `trace_id` callable.
- **Testers**: `recommend(..., tester=True)` sends `X-Patterna-Tester: 1`.
- **`assignment_unit`** on `recommend` and `assignment_unit(secret, user_id)` with
  the cross-language vector; only the 64-hex shape is accepted.
- **`measured`**: `measured_context(context, analytics_consent)`.
- `recommend` also takes `slots` and `exclude_item_ids`.
- **Interactions**: `kind` checked, and required for non-documents of slates this client
  served (`slate_memory`, default 4096); `block`; positions checked;
  `interaction_for(slate, item_id, type)`.
- **`recommend_many([ManyRequest(surface, fallback, kwargs), ...])`**: parallel, one budget.
- **Typed API codes**: `ApiErrorCode`, `is_api_error`, `is_caller_bug`.
- **Catalog**: `delete_item(external_id, kind=)`, `list_items(changed_since=)`,
  `items_changed_since(since)`.
- **Admin methods** (admin key): `create_signing_secret`, `rotate_signing_secret`,
  `list_surfaces`, `get_surface`, `set_surface_status`, `set_surface_testers`, `list_flows`,
  `get_flow`, `create_flow`, `activate_flow`, `rollback_flow`, `plan_flow`, `apply_flow`,
  `list_experiments`, `start_experiment`, `end_experiment`, `ledger`, `export_ledger`,
  `export_status`.

### Changed (breaking)

- `compute_event_signature`, `sign_event` take the secret as a `str` only (bytes raise
  `TypeError`): sign with the secret string as the API returned it, never hex-decoded.
- `with_fallback` re-raises consent and validation errors, `pii_rejected` and `mode_conflict`
  instead of falling back.
- `recommend` in `non_profiled` mode raises `ConsentError` when given a `user_id` (it used to drop
  it silently).
- User ids in `/users/...` paths are now URL-escaped.

## 0.1.0

First version: serve and ingest calls, consent gate, typed errors, one retry, event signing.
