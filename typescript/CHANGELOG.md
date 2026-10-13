# Changelog: patterna-sdk

SDK 0.2 needs engine 0.2 or later (the engine in production since release `7a7683e`).

Published as `patterna-sdk` from [payam-h/patterna-sdk](https://github.com/payam-h/patterna-sdk)
(`typescript/`). The npm publish is pending: `npm install patterna-sdk` returns 404 today. Until
then build it from that repository at tag `v0.2.0` (`npm ci && npm run build && npm pack` in
`typescript/`) and install the tarball.

## Unreleased

- Generated types follow contract 0.2.0 (engine 0.3): `context.control_item_ids`,
  `context.member_of`, `control_source` and `fill_count` are typed. A `shown: false` answer (no
  items) is a normal 200 and is never abandoned; abandoning it would answer 409 `not_shown`.

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
