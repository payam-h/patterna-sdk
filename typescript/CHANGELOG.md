# Changelog: patterna-sdk

Current release: 0.4.1, published on npm as `patterna-sdk` (`npm install patterna-sdk@0.4.1`) from
[payam-h/patterna-sdk](https://github.com/payam-h/patterna-sdk) (`typescript/`). The production
engine reports `engine_version: 0.3.1` (the contract version) and every field below is live on it;
SDK 0.2 to 0.4 work against engine 0.2 or later, and older engines ignore the fields they do not
know.

## 0.4.1 (2026-10-13)

No wire changes: nothing the SDK sends is different, and 0.4.0 keeps working. Source-compatible.

### Added

- `getItem(kind, externalId, opts?)`: `GET /catalog/items/{id}?kind=` under the ingest key,
  returning the stored `Item` with `attributes`, `tags` (with `source`), `status` and `enrichment`.
- `ApiErrorCode.ItemNotFound` (`item_not_found`, 404) for the catalog calls (`getItem`,
  `deleteItem`, `setItemStatus`, `setItemTags`).

### Fixed

- `unknown_item` is the 422 from `reportInteractions` (an item that was not in the slate,
  `details.unknown_item_ids`), not the catalog 404; the error table said the opposite.

## 0.4.0 (2026-10-13)

Needs engine round 5 (API 0.3.0) for the new fields; older engines ignore them. Source-compatible
with 0.3. The shared signing and assignment-unit vectors are unchanged.

### Added

- **Request context**: `weekday` (0 = Monday), `subscription_tier`, `tenure_days` next to
  `hour_bucket`, `platform`, `region` and `session_items` (from the generated contract types),
  checked before sending; `timeContext(date, timeZone?)` returns `{ hour_bucket, weekday }` in
  the user's zone; `TIER_FREE`, `TIER_PREMIUM`.
- **Event context**: `weekday`, `region`, `subscription_tier`, `tenure_days` are allowed and
  checked.
- **Editorial attributes**: `ItemAttributes` (format, duration and reading time, difficulty,
  author, availability, expiry, event start, repurchase window, region, phase fit, and the
  panel's price, compare-at price, currency, image, URL, product type, categories) as
  `ItemUpsert.editorial`, written into `attributes` with `Date`s as RFC 3339 and `region` lifted
  to the top-level `region`; `validateItemAttributes`; types `ItemFormat`, `Difficulty`.
- **Activity events**: `communityPostCreated`, `communityReply`, `communityReaction`,
  `paywallViewed`, `pushOpened`, `searchPerformed`, `itemEngaged`, `itemCompleted`,
  `orderPlaced` on `EventBase`, with the consent rule (user id in personalized and cohort,
  session id only in non_profiled) and the `EVENT_*` type constants.
- **Reasons**: `REASON_RECOMMENDED`, `REASON_KEYS`, type `ReasonKey`.
- **Flows**: `validateFlowConfig`, `GENERATOR_KINDS` (with `time_fit`, `format_fit`), types
  `GeneratorKind` and `FlowFilters` (with the six new filters).

### Changed

- `createFlow`, `planFlow` and `applyFlow` validate the config before sending; a config that names
  an unknown filter key now fails here.
- `upsertItems` sends each item through the editorial merge and refuses out-of-range attributes.

## 0.3.0 (2026-10-13)

Needs engine 0.3 or later for the new fields (that release reported `engine_version` 0.2.0; production is on 0.3.1 now); older engines ignore them.

### Added

- **Control lists and communities**: `context.control_item_ids` (at most 50, the control slate
  for this page) and `context.member_of` (at most 500 community ids, never offered), checked
  before sending.
- **Slate fields**: `control_source` (`request`, `by_anchor`, `surface`; type `ControlSource`),
  `fill_count`, `fill` per item (set from `fill_count` when the engine does not mark slots;
  `markFills`), `model_versions` (typed; engine 0.3 logs it on the impression and does not send
  it with the answer yet).
- **`shown` on every result** of `recommendWithFallback`, `recommendMany` and `withFallback`:
  false only for a `shown: false` answer, which keeps `source: "patterna"`, has no items, does not
  call the fallback and is never abandoned. Hide the block.
- **`abandon` refuses a `shown: false` slate** this client served (`not_shown`, nothing sent).
- **Error codes**: `ApiErrorCode.ModeRequired` (`mode_required`, 422 on `setUserProfile` without
  `mode` for an unknown user; a caller bug, never hidden by a fallback) and
  `ApiErrorCode.NotShown` (`not_shown`, 409 on abandoning a `shown: false` slate).
- **Coach events**: `coachProductShown`, `coachProductTapped` (`product_id`, `conversation_id`,
  `position`) with the consent rules applied; `EVENT_COACH_PRODUCT_SHOWN`,
  `EVENT_COACH_PRODUCT_TAPPED`.
- **Admin**: `updateSurface`, `planSurface`, `applySurface` with `SurfaceSpec` (`min_slots`,
  `device_resort`, `fallback.by_anchor`, `item_kinds`, `slots`, notes, exploration share) and
  `validateSurfaceSpec`; `ledgerByBlock` and `LedgerQuery.block`; `surfaceMetrics` with
  `not_shown_share` and `client_abandoned_rate`; `getSettings`, `updateSettings`, `setPhaseTags`;
  `health` with `engine_version`.

### Changed

- `setSurfaceStatus` and `setSurfaceTesters` read the surface back after the PATCH: the engine
  answers `{ key, ok }`, not the surface the contract describes.
- The `Recommendation` and `FallbackResult` types gained the required `shown` field; code that
  builds these objects itself must add it.

## 0.2.0 (2026-10-13)

### Added

- **Client-minted request ids.** Every call sends `x-request-id` (UUIDv4) unless
  `{ requestId }` gives one; timeout, network and abort errors carry it.
- **`recommendWithFallback(req, fallback, opts)`** returns `{ source, value, requestId }` and, on
  a fallback, `error` and `abandoned`. On a timeout, network error, 5xx or unreadable answer it
  reports the abandonment in the background
  (`POST /recommendations/{id}/abandon`); failures go to `onBackgroundError`. `abandon(id, reason)`
  for a slate that arrived but was not shown.
- **Server deadline**: `x-deadline-ms` on `recommend`, 120 ms by default
  (`timeouts.recommendDeadlineMs`), shrunk by the same margin when `timeoutMs` is shorter.
- **Trace ids**: `x-trace-id` from `{ traceId }` per call or the client's `traceId()`.
- **Testers**: `{ tester: true }` sends `x-patterna-tester: 1`.
- **`assignment_unit`** on recommend requests and `assignmentUnit(secret, userId)`
  with the cross-language vector; only the 64-hex shape is accepted.
- **`measured`** on the request context and `measuredContext(context, analyticsConsent)`.
- **Interactions**: `kind` checked, and required for non-documents of slates this client
  served (`slateMemory`, default 4096); `block`; positions checked;
  `interactionFor(slate, itemId, type)`.
- **`recommendMany`**: several surfaces in parallel with one budget.
- **Typed API codes**: `ApiErrorCode`, `isApiError`, `isCallerBug`. `withFallback` and
  `recommendWithFallback` rethrow `pii_rejected` and `mode_conflict`.
- **Catalog**: `deleteItem`, `listItems({ changedSince })`, `itemsChangedSince`.
- **Admin methods** (admin key): `createSigningSecret`, `rotateSigningSecret`,
  `listSurfaces`, `getSurface`, `setSurfaceStatus`, `setSurfaceTesters`, `listFlows`, `getFlow`,
  `createFlow`, `activateFlow`, `rollbackFlow`, `planFlow`, `applyFlow`, `listExperiments`,
  `startExperiment`, `endExperiment`, `ledger`, `ledgerWeeks`, `exportLedger`, `exportStatus`.

### Changed

- `computeEventSignature` refuses a non-string secret: sign with the secret string as the API
  returned it, never hex-decoded (same as the Go and Python SDKs).
- Requests now carry `x-request-id` (and `x-trace-id`, `x-deadline-ms` when set). Browsers that
  call Patterna directly would need these headers allowed by CORS; apps should call their own
  backend proxy anyway.

## 0.1.0

First version: serve and ingest client, typed errors, timeouts, one retry for idempotent calls,
consent gate, `withFallback`, `EventQueue`, event signing, session ids.
