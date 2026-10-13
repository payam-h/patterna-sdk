# Changelog: sdk-go

Module `github.com/payam-h/patterna-sdk/go`. Tags are `go/vX.Y.Z` in
[payam-h/patterna-sdk](https://github.com/payam-h/patterna-sdk); install with
`go get github.com/payam-h/patterna-sdk/go@v0.2.0`.
SDK 0.2 needs engine 0.2 or later (the engine in production since release `7a7683e`).

## Unreleased

- Works with engine 0.3 (API 0.2.0) unchanged: a `shown: false` answer (no items) is a normal 200
  and is never abandoned; abandoning it would answer 409 `not_shown`. Not yet typed:
  `context.control_item_ids`, `context.member_of`, and `control_source`, `fill_count` on `Slate`.

## 0.2.0 (2026-10-13)

### Added

- **Client-minted request ids.** Every call sends `X-Request-Id` (a UUIDv4) unless
  `WithRequestID` gives one; errors carry it even when the engine never answered.
- **Abandonment.** `RecommendWithFallback` reports `POST
  /recommendations/{id}/abandon` in the background on a timeout, network error, 5xx or
  unreadable answer, so the engine flags the impression `client_fallback`.
  `Recommendation.RequestID` and `Recommendation.Abandoned` say what happened. `Abandon` and
  `AbandonSync` let the app report a slate it received but did not show. Not sent when the breaker
  was open (nothing reached the engine) or the engine refused the request (4xx).
- **Server deadline.** Recommend sends `X-Deadline-Ms`: `Config.RecommendDeadline`
  (default 120 ms, below the 150 ms budget), shrunk by the same margin when the caller's
  context deadline is shorter.
- **Trace ids.** `X-Trace-Id` on every call from `WithTraceID`, `ContextWithTraceID` or
  `Config.TraceID`.
- **Testers.** `WithTester()` sends `X-Patterna-Tester: 1`.
- **Shown.** `Slate.Shown` and `Slate.Visible()`: false when the slate is shorter
  than the surface's `min_slots`; hide the block.
- **Assignment unit.** `RecommendRequest.AssignmentUnit` and
  `AssignmentUnit(secret, userID)` (hex HMAC-SHA256), with a cross-language vector. Only the
  64-hex shape is accepted, so a raw id cannot be sent by mistake.
- **Measured.** `RecommendContext.Measured` and `SetMeasured(analyticsConsent)`.
- **Interactions.** `Kind` is validated, and required for non-documents when this client served
  the slate (`Config.SlateMemory`, default 4096 slates). `Block` and checks on
  `DisplayedPosition`. `Slate.Interaction` builds an interaction with the served kind and slot.
- **`RecommendMany`**: several surfaces in parallel under one deadline, each with its own
  fallback and abandonment.
- **Typed errors**: `ErrNoCandidates`, `ErrSurfaceOff`, `ErrUnknownSurface`,
  `ErrUnknownRequest`, `ErrUnknownItem`, `ErrPIIRejected`, `ErrModeConflict`. `pii_rejected` and
  `mode_conflict` are returned by `RecommendWithFallback`, never hidden behind a fallback.
- **Catalog**: `DeleteItem`, `ListItems` with `ChangedSince`, `ItemsChangedSince`.
- **Signing secret**: `CreateSigningSecret` (admin key).
- **`AdminCall`**, `Config.AdminTimeout` (30 s) and an `admin` breaker scope.
- **`admin` package**: flows (list, get, create, activate, rollback, plan, apply), surface status
  and testers, experiments (start, end, list), ledger, weekly ledger, export and its status,
  signing secret create and rotate.
- **`cmd/patterna` CLI** on top of it, for flow configs kept in the customer's repository.

### Changed (breaking)

- `EventSignature` and `SignEvent` take the secret as a `string` and sign with its bytes. Pass
  the secret exactly as the API returned it; never hex-decode it.
- Every method takes trailing `...CallOption`. Source-compatible for callers; method values
  assigned to typed function variables need the new signature.
- `New` refuses a `RecommendDeadline` that is not below `RecommendTimeout`.

## 0.1.0

First version: serve and ingest scopes, deadlines, per-scope breaker, one retry for idempotent
calls, fire-and-forget interaction queue, `RecommendWithFallback`, consent checks, event signing.
