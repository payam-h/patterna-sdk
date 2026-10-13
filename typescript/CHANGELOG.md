# Changelog: @patterna/sdk

"Engine 0.2" marks features that need the engine release that adds the matching endpoint,
header or field; against an older engine they are ignored or answer 404.

The package is still `private: true`: it is not published to npm until the package name and
registry are decided. Install it from the repository (`npm install ../patterna/packages/sdk-ts`,
or a git dependency on a tag) until then.

## 0.2.0 (2026-10-13)

### Added

- **Client-minted request ids.** Every call sends `x-request-id` (UUIDv4) unless
  `{ requestId }` gives one; timeout, network and abort errors carry it.
- **`recommendWithFallback(req, fallback, opts)`** returns `{ source, value, requestId }` and, on
  a fallback, `error` and `abandoned`. On a timeout, network error, 5xx or unreadable answer it
  reports the abandonment in the background (engine 0.2,
  `POST /recommendations/{id}/abandon`); failures go to `onBackgroundError`. `abandon(id, reason)`
  for a slate that arrived but was not shown.
- **Server deadline** (engine 0.2): `x-deadline-ms` on `recommend`, 120 ms by default
  (`timeouts.recommendDeadlineMs`), shrunk by the same margin when `timeoutMs` is shorter.
- **Trace ids**: `x-trace-id` from `{ traceId }` per call or the client's `traceId()`.
- **Testers** (engine 0.2): `{ tester: true }` sends `x-patterna-tester: 1`.
- **`assignment_unit`** (engine 0.2) on recommend requests and `assignmentUnit(secret, userId)`
  with the cross-language vector; only the 64-hex shape is accepted.
- **`measured`** (engine 0.2) on the request context and `measuredContext(context, analyticsConsent)`.
- **Interactions**: `kind` checked, and required for non-documents of slates this client
  served (`slateMemory`, default 4096); `block` (engine 0.2); positions checked;
  `interactionFor(slate, itemId, type)`.
- **`recommendMany`**: several surfaces in parallel with one budget.
- **Typed API codes**: `ApiErrorCode`, `isApiError`, `isCallerBug`. `withFallback` and
  `recommendWithFallback` rethrow `pii_rejected` and `mode_conflict`.
- **Catalog** (engine 0.2): `deleteItem`, `listItems({ changedSince })`, `itemsChangedSince`.
- **Admin methods** (admin key): `createSigningSecret` (engine 0.2), `rotateSigningSecret`,
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
