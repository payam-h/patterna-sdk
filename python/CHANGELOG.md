# Changelog: patterna-sdk (Python)

"Engine 0.2" marks features that need the engine release that adds the matching endpoint,
header or field; against an older engine they are ignored or answer 404.

## 0.2.0 (2026-10-13)

### Added

- **Client-minted request ids**: every call sends `X-Request-Id` (UUIDv4) unless `request_id=`
  gives one; timeout and network errors carry it.
- **`recommend_with_fallback(surface, fallback, **recommend_kwargs)`** returns a
  `Recommendation(source, value, request_id, error, abandoned)`. On a timeout, network error,
  5xx or unreadable answer it sends the abandonment (engine 0.2,
  `POST /recommendations/{id}/abandon`) on a background thread; `close()` waits for it, and
  failures go to `on_background_error`. `abandon(request_id, reason)` for a slate that arrived
  but was not shown.
- **Server deadline** (engine 0.2): `X-Deadline-Ms` on `recommend`, 120 ms by default
  (`recommend_deadline_ms`), shrunk by the same margin when `timeout_ms` is shorter.
- **Trace ids**: `X-Trace-Id` from `trace_id=` per call or the client's `trace_id` callable.
- **Testers** (engine 0.2): `recommend(..., tester=True)` sends `X-Patterna-Tester: 1`.
- **`assignment_unit`** (engine 0.2) on `recommend` and `assignment_unit(secret, user_id)` with
  the cross-language vector; only the 64-hex shape is accepted.
- **`measured`** (engine 0.2): `measured_context(context, analytics_consent)`.
- `recommend` also takes `slots` and `exclude_item_ids`.
- **Interactions**: `kind` checked, and required for non-documents of slates this client
  served (`slate_memory`, default 4096); `block` (engine 0.2); positions checked;
  `interaction_for(slate, item_id, type)`.
- **`recommend_many([ManyRequest(surface, fallback, kwargs), ...])`**: parallel, one budget.
- **Typed API codes**: `ApiErrorCode`, `is_api_error`, `is_caller_bug`.
- **Catalog** (engine 0.2): `delete_item(external_id, kind=)`, `list_items(changed_since=)`,
  `items_changed_since(since)`.
- **Admin methods** (admin key): `create_signing_secret` (engine 0.2), `rotate_signing_secret`,
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
