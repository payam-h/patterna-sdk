# Changelog: sdk-go

Module `github.com/payam-h/patterna-sdk/go`. Tags are `go/vX.Y.Z` in
[payam-h/patterna-sdk](https://github.com/payam-h/patterna-sdk); install with
`go get github.com/payam-h/patterna-sdk/go@v0.4.1`. Current release: 0.4.1. The production engine
reports `engine_version: 0.3.1` (the contract version) and every field below is live on it; SDK 0.2
to 0.4 work against engine 0.2 or later, and older engines ignore the fields they do not know.

## 0.4.1 (2026-10-13)

No wire changes: nothing the SDK sends is different, and 0.4.0 keeps working. Source-compatible.

### Added

- `GetItem(ctx, kind, externalID)`: `GET /catalog/items/{external_id}?kind=` under the ingest
  key, returning the stored `Item` with `Attributes`, `Tags` (now with `Source`), `Status`,
  `Locales`, `Region` and the new `Enrichment` (`ItemEnrichment`: status, model, updated_at).
- `ErrItemNotFound` / `CodeItemNotFound` for the catalog 404 `item_not_found` (`GetItem`,
  `DeleteItem`, `SetItemStatus`, `SetItemTags`).

### Fixed

- `ErrUnknownItem` is the 422 `unknown_item` from `ReportInteractions` (an item that was not in
  the slate), not the catalog 404; the comments in `errors.go` and the README error table said the
  opposite. `DeleteItem`'s doc names the right code.

## 0.4.0 (2026-10-13)

Source-compatible with 0.3: every change adds a field, a type, a function or a check. The
shared signing and assignment-unit vectors are unchanged.

### Added

- **Request context.** `RecommendContext.Weekday` (0 = Monday), `SubscriptionTier`
  (`TierFree`, `TierPremium` or a tenant name), `TenureDays`, and the 0.3.0 contract's `Region`
  and `SessionItems`, which 0.3 did not have. `LocalTime(t, loc)` and `SetLocalTime` on
  `RecommendContext` and `EventContext` derive the hour and weekday in the user's timezone.
  `Ptr` makes the optional fields one-liners.
- **Event context.** `EventContext.Weekday`, `Region`, `SubscriptionTier`, `TenureDays`.
- **Editorial attributes.** `ItemAttributes` (format, duration and reading time, difficulty,
  author, availability, expiry, event start, repurchase window, region, phase fit, and the
  panel's price, compare-at price, currency, image, URL, product type, categories) on
  `ItemUpsert.Editorial`, written into `attributes`; its region goes to the new top-level
  `ItemUpsert.Region`. `ItemFormat` and `Difficulty` constants.
- **Activity events.** `CommunityPostCreated`, `CommunityReply`, `CommunityReaction`,
  `PaywallViewed`, `PushOpened`, `SearchPerformed`, `ItemEngaged`, `ItemCompleted` and
  `OrderPlaced` on a shared `EventBase`, with the consent rule (user id in personalized and
  cohort, session id only in non_profiled) and the `Event*` type constants.
- **Reasons.** `ReasonRecommended` and the other reason keys as constants; `ReasonKeys`.
- **Flows.** `admin.FlowConfig`, `GeneratorSpec`, `FlowFilters` (with `ExcludeDismissedDays`,
  `ExcludeConsumedDays`, `PaywallCooldownDays`, `RepurchaseDays`, `HideUnavailable`,
  `RelevanceFloor`), `RankerSpec`, `ExplorationSpec`; generator constants with `GenTimeFit` and
  `GenFormatFit`; `admin.ValidateFlowConfig`.

### Changed

- `Recommend` (and everything built on it), `IngestEvents` and `UpsertItems` refuse
  out-of-range context and attribute values before sending (`invalid_argument`).
- `CreateFlow`, `PlanFlow`, `ApplyFlow` and `patterna flow apply` validate the config first; the
  CLI checks every file before applying the first. A config that the engine accepted but that
  names an unknown filter key now fails here.

## 0.3.0 (2026-10-13)

Source-compatible with 0.2: every change adds a field, a method or a sentinel.

### Added

- **Editorial control lists.** `RecommendContext.ControlItemIDs` (at most 50): your own list for
  the page, the control slate in that order and the fill in treatment. `Slate.ControlSource`
  (`ControlSourceRequest`, `ControlSourceByAnchor`, `ControlSourceSurface`) says where an
  editorial slate came from.
- **Communities.** `RecommendContext.MemberOf` (at most 500): communities never offered to a
  `non_profiled` session.
- **Fill.** `Slate.FillCount` and `SlateItem.Fill`: the trailing positions filled from the
  editorial list when the ranked candidates ran out. The SDK marks them from the count when the
  engine does not mark slots itself (engine 0.3 does not).
- **`Slate.ModelVersions`**, decoded when present (engine 0.3 logs it on the impression only).
- **Hidden blocks.** `Recommendation.Shown`: false only for a `shown: false` answer, which
  `RecommendWithFallback` and `RecommendMany` return as it is: no items, no fallback, never
  abandoned. `Abandon` and `AbandonSync` refuse a slate this client received as `shown: false`
  with `ErrNotShown` before sending; the engine's 409 `not_shown` maps to the same sentinel.
- **Typed errors** `ErrModeRequired` (422 `mode_required`: `SetUserProfile` without a mode for a
  user Patterna has no row for; returned, never a fallback) and `ErrNotShown` (409 `not_shown`).
- **Coach events.** `CoachProductShown` and `CoachProductTapped` build `coach_product_shown` and
  `coach_product_tapped` with `product_id`, `conversation_id`, `position` and the consent rules
  (user id only in personalized and cohort, session id only in non_profiled).
- **Admin.** `UpdateSurface`, `PlanSurface` and `ApplySurface` with `SurfaceSpec` (`min_slots`,
  `device_resort`, `item_kinds`, `slots`, `fallback` with `by_anchor`, notes, exploration share);
  `Surface.MinSlots`, `DeviceResort`, `TesterRequests7d` and `FallbackConfig()`;
  `SurfaceMetrics` with `NotShownShare` and `ClientAbandonedRate`; `LedgerByBlock`,
  `LedgerQuery.Block`, `LedgerRow.Block` and `ReadableSample`; `GetSettings`, `UpdateSettings`,
  `SetPhaseTags`; `Health` with `EngineVersion`.
- **CLI.** `patterna surface apply -f FILE|DIR [--dry-run]`, `patterna settings phase-tags
  [on|off]`, `patterna ledger --by block` and `--block NAME`.

### Fixed

- `SetSurfaceStatus` and `SetSurfaceTesters` returned an almost empty `Surface`: the engine answers
  `PATCH /surfaces/{key}` with `{key, ok}`, not the surface the contract describes. They now read
  the surface back.

## 0.2.0 (2026-10-13)

### Added

- **Client-minted request ids.** Every call sends `X-Request-Id` (a UUIDv4) unless
  `WithRequestID` gives one; errors carry it even when the engine never answered.
- **Abandonment.** `RecommendWithFallback` reports `POST
  /recommendations/{id}/abandon` in the background on a timeout, network error, 5xx or
  unreadable answer, so the engine flags the impression `client_abandoned` with the reason
  `client_fallback`.
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
