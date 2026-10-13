# Changelog: patterna-sdk

SDK 0.2 and 0.3 need engine 0.2 or later (the engine in production since release `7a7683e`).

Published as `patterna-sdk` from [payam-h/patterna-sdk](https://github.com/payam-h/patterna-sdk)
(`typescript/`). The npm publish is pending: `npm install patterna-sdk` returns 404 today. Until
then build it from that repository at tag `v0.3.0` (`npm ci && npm run build && npm pack` in
`typescript/`) and install the tarball.

## 0.3.0 (2026-10-13)

Needs engine 0.3 (`engine_version` 0.2.0) for the new fields; older engines ignore them.

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
