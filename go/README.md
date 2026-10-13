# sdk-go: `github.com/payam-h/patterna-sdk/go`

Version 0.4.1 (see `CHANGELOG.md`): the production engine reports `engine_version: 0.3.1` on `GET /v1/health` (the contract
version), and every field in this release is live on it. Works against engine 0.2 or later;
older engines ignore the fields they do not know.

```sh
go get github.com/payam-h/patterna-sdk/go@v0.4.1   # tag go/v0.4.1 in payam-h/patterna-sdk
```

Go client for the Patterna API, built for a customer backend in a request path (the Gin + gqlgen
backend of a women's health app is the first user). Standard library only. Go 1.25.

In a checkout of the module directory (`go/` in the public repository):

```sh
go test -race ./...
go vet ./...
go run honnef.co/go/tools/cmd/staticcheck@2026.2.2 ./...
```

(With an older local Go, prefix `GOTOOLCHAIN=auto`.)

| File | What |
|---|---|
| `client.go` | `Config`, `New`, serve, ingest and user methods, the transport (deadline, retry, breaker, headers) |
| `options.go` | `CallOption` (`WithTester`, `WithTraceID`, `WithRequestID`), trace ids, `NewRequestID` |
| `abandon.go` | `Abandon`, `AbandonSync` |
| `many.go` | `RecommendMany` |
| `catalog.go` | `GetItem`, `DeleteItem`, `ListItems`, `ItemsChangedSince`, `CreateSigningSecret`, `AdminCall` |
| `assignment.go` | `AssignmentUnit` |
| `slatekinds.go` | memory of recent slates' item kinds, for the interaction `kind` check |
| `breaker.go` | consecutive-failure circuit breaker, one per scope |
| `fallback.go` | `RecommendWithFallback` and `Recommendation` |
| `consent.go` | consent gate and the opaque-user-id check |
| `signing.go` | canonical JSON and HMAC-SHA256 event signatures (same bytes as `patterna-sdk`) |
| `errors.go` | `*Error`, codes, sentinels for `errors.Is` |
| `types.go` | hand-written mirror of the OpenAPI contract (https://patterna.orven.am/docs/openapi.yaml) |
| `admin/` | flows, surfaces, experiments, ledger, signing secret (admin key) |
| `cmd/patterna/` | the CLI on top of `admin` |
| `internal/fakeengine/` | in-memory admin endpoints for the admin and CLI tests |

## Use

```go
pc, err := patterna.New(patterna.Config{
	BaseURL:   os.Getenv("PATTERNA_URL"),        // https://patterna-api.orven.am/v1
	ServeKey:  os.Getenv("PATTERNA_SERVE_KEY"),
	IngestKey: os.Getenv("PATTERNA_INGEST_KEY"),
	Logger:    slog.Default(),
})
defer pc.Close(context.Background())

rec, err := pc.RecommendWithFallback(ctx, patterna.RecommendRequest{
	Surface: "home_articles",
	Mode:    patterna.ModePersonalized,
	UserID:  patternaID,                         // the random id you minted, never an email or auth0|...
	Context: patterna.RecommendContext{Locale: "nl", Platform: "ios"},
}, newestArticleIDs)
if err != nil {
	return err // consent or input bug: never hidden behind the fallback
}
if !rec.Shown {
	return hideBlock() // shown=false: fewer eligible items than min_slots; never abandon it
}
// rec.ItemIDs is empty only when rec.Shown is false; rec.Slate is nil when the fallback was used.
// rec.RequestID is always set: log it next to your trace. On a timeout or 5xx the client has
// already told Patterna the slate was abandoned (rec.Abandoned).

if rec.Slate != nil {
	in, _ := rec.Slate.Interaction(itemID, patterna.InteractionClick, time.Now()) // served kind and slot
	_ = pc.ReportInteractions(rec.Slate.RequestID, []patterna.Interaction{in}) // fire-and-forget
}
```

| Method | Key | Budget | Retried |
|---|---|---|---|
| `Recommend`, `RecommendWithFallback` | serve | 150 ms | never (each call logs an impression) |
| `ReportInteractions` (async), `ReportInteractionsSync` | serve | 5 s | once |
| `IngestEvents`, `UpsertItems`, `SetItemStatus`, `SetItemTags` | ingest | 5 s | once |
| `SetUserMode`, `SetUserProfile`, `DeleteUser` | admin if set, else ingest | 5 s | once |
| `RecommendMany` | serve | 150 ms for all | never |
| `Abandon` (async), `AbandonSync` | serve (ingest breaker) | 5 s | once |
| `GetItem`, `DeleteItem`, `ListItems`, `ItemsChangedSince` | ingest | 5 s per page | once |
| `CreateSigningSecret`, `AdminCall`, `admin.*` | admin | 30 s | idempotent ones once |

- **Deadlines.** Each call runs under `context.WithTimeout(ctx, budget)`; a shorter caller
  deadline wins. The budget covers the retry too. A deadline is `ErrTimeout`; a caller cancel is
  `ErrAborted`.
- **Server deadline.** Recommend sends `X-Deadline-Ms`, 120 ms by default
  (`Config.RecommendDeadline`, always below `RecommendTimeout`; the engine's own default budget
  is 120 ms too), so the engine serves its own fallback in time instead of answering after the client gave up. With a shorter caller deadline
  the header shrinks by the same 30 ms margin: 80 ms left sends 50.
- **Request and trace ids.** The client mints `X-Request-Id` for every call (UUIDv4;
  `WithRequestID` overrides). The engine uses it as the slate's `request_id`, and every `*Error`
  carries it, even on a timeout. `X-Trace-Id` comes from `WithTraceID(id)`, else
  `ContextWithTraceID(ctx, id)`, else `Config.TraceID(ctx)` (for Sentry or OpenTelemetry). Ids
  longer than 64 characters or with spaces are not sent.
- **Abandoned slates.** When `RecommendWithFallback` falls back after the request
  reached the engine (timeout, network error, 5xx, unreadable answer) it queues
  `POST /recommendations/{id}/abandon` with the reason, so the impression is flagged
  `client_abandoned` (reason `client_fallback`) and the fallback rate and the ledger stay honest. Nothing is sent when the
  breaker was open (the request never left) or the engine refused the request with a 4xx (nothing
  was logged). Call `pc.Abandon(id, patterna.AbandonClientFallback)` yourself when a slate
  arrived but the app showed something else.
- **Hidden blocks (engine 0.3).** A surface with `min_slots` answers `shown: false` with no items
  when fewer items pass the request's rules (counted before the arm, so both arms hide alike).
  `RecommendWithFallback` returns it as `Recommendation{Shown: false}`: no items, no fallback, no
  abandonment. Hide the block. The slate counts in its arm with zero taps; abandoning it would be
  409 `not_shown`, so `Abandon`/`AbandonSync` refuse it with `ErrNotShown` without sending when
  this client served it. `Slate.Visible()` says the same for `Recommend`.
- **Editorial control lists (engine 0.3).** `RecommendContext.ControlItemIDs` is your own list for
  this page (an article's "pairs with" products, the editor's related articles; at most 50). In
  the control arm it is the slate, in that order, under the request's rules; in treatment it fills
  short slates. Without it the engine uses the surface's `fallback.by_anchor` list for
  `AnchorItemID`, else the surface's own list. `Slate.ControlSource` says which
  (`ControlSourceRequest`, `ControlSourceByAnchor`, `ControlSourceSurface`; empty on ranked
  slates).
- **Fill (engine 0.3).** When ranked candidates run out before `slots`, the rest comes from the
  editorial list behind them: `Slate.FillCount` trailing positions, each with `SlateItem.Fill`
  (set by the SDK from the count when the engine does not mark slots). They are ordinary slots for
  interactions.
- **Communities (engine 0.3).** `RecommendContext.MemberOf` lists the communities the user is in
  (at most 500); the engine leaves them out in every mode. It is the only source for a
  `non_profiled` request; personalized and cohort requests also use the stored
  `community_joined`/`community_left` events.
- **Model versions.** `Slate.ModelVersions` is decoded when present. Engine 0.3 logs it on the
  impression (trace, export) and does not send it with the answer, so it is nil there today.
- **Testers.** `pc.Recommend(ctx, req, patterna.WithTester())` sends
  `X-Patterna-Tester: 1`; your backend decides who is a tester, so staff need no random ids on a
  roster.
- **Retry.** One retry after 100 ms on 5xx and network errors, idempotent calls only.
- **Circuit breaker.** One per scope (`serve`, `ingest`). Opens after 5 consecutive failures
  (timeouts, network errors, 5xx, 429) and fails fast with `ErrCircuitOpen` for 30 s; then one
  probe call decides. A 4xx counts as the service being up. `OnBreakerChange` and
  `BreakerOpen(scope)` feed metrics and health checks.
- **Fire-and-forget reports.** `ReportInteractions` puts the report on a bounded queue
  (1024) served by 2 workers and returns at once; a full queue returns `ErrQueueFull` and counts
  in `DroppedReports()`. `Close(ctx)` drains it.
- **Errors.** Every error is `*Error{Code, Op, Status, RequestID, Message, Details}`. API errors
  carry the API's code. Compare with `errors.Is(err, patterna.ErrTimeout)` or
  `patterna.IsAPIError(err, "pii_rejected")`. Typed sentinels and what `RecommendWithFallback`
  does with them:

  | Sentinel | Status | In `RecommendWithFallback` |
  |---|---|---|
  | `ErrNoCandidates` | 422 | fallback (nothing eligible to rank) |
  | `ErrSurfaceOff` | 409 | fallback (the kill switch; that is its purpose) |
  | `ErrUnknownSurface` | 404 | fallback, but it is a config bug: alert on `rec.Cause` |
  | `ErrModeConflict` | 422 | **returned**: the consent mode contradicts the stored one |
  | `ErrModeRequired` | 422 | `SetUserProfile` without `Mode` for a user Patterna has no row for: a profile never creates consent; set the mode first |
  | `ErrNotShown` | 409 | `Abandon`, `AbandonSync`: the slate was answered `shown: false`; hide the block, never abandon it |
  | `ErrPIIRejected` | 422 | **returned**: personal data in the request |
  | `ErrUnknownRequest` | 404 | `AbandonSync`, `ReportInteractionsSync`: the engine never logged that id |
  | `ErrUnknownItem` | 422 | `ReportInteractionsSync`: an item id that was not in the slate served for that request (API code `unknown_item`, `Details["unknown_item_ids"]`) |
  | `ErrItemNotFound` | 404 | `GetItem`, `DeleteItem`, `SetItemStatus`, `SetItemTags`: no item with that id and kind (API code `item_not_found`) |
  | `ErrConsentRequired`, `ErrConsentViolation`, `ErrInvalidArgument`, `ErrNotConfigured` | - | **returned**, nothing sent |
  | `ErrTimeout`, `ErrNetwork`, `ErrCircuitOpen`, other 5xx | - | fallback |

  Log `Slate.Arm` (`control`, `treatment`, `none`) next to your trace when you debug a holdout.
- **Consent.** A request needs a mode; `non_profiled` needs a session id and no user id. User ids
  that look like an email or an identity-provider subject are refused everywhere, and never
  appear in error text.

## Signals: context, editorial attributes, activity events (engine round 5)

Everything the engine reads is listed at https://patterna.orven.am/docs/signals; these are the SDK's parts of it.

**Request context.** Next to `HourBucket`, `Platform`, `Region` and `SessionItems`:

```go
req.Context.SetLocalTime(time.Now(), userLoc)  // HourBucket and Weekday (0 = Monday) in the user's timezone
req.Context.SubscriptionTier = patterna.TierFree // or TierPremium, or your own tier name (<= 32 chars)
req.Context.TenureDays = patterna.Ptr(daysSinceSignup)
req.Context.Region = "Noord-Holland"            // one of the tenant's regions; never a location
req.Context.SessionItems = recentlyOpenedIDs    // most recent first, at most 20
```

`patterna.LocalTime(t, loc)` returns the two numbers when you need them elsewhere;
`EventContext.SetLocalTime` does the same for events. Out-of-range values (weekday 0..6, hour
0..23, negative tenure, a tier over 32 or a region over 64 characters, more than 20 session
items) fail before the request leaves. For `free` the engine applies the premium policy and the
paywall cooldown; none of these is stored on the user.

**Editorial attributes.** `ItemUpsert.Editorial` is the typed form of `attributes`:

```go
pc.UpsertItems(ctx, []patterna.ItemUpsert{{
	ExternalID: "p_magnesium", Kind: patterna.KindProduct, Title: "Magnesium", Locale: "nl",
	Editorial: &patterna.ItemAttributes{
		Format: patterna.FormatProduct, Available: patterna.Ptr(inStock), RepurchaseDays: patterna.Ptr(30),
		PriceCents: patterna.Ptr(2495), Currency: "EUR", ImageURL: img, URL: shopURL, ProductType: "supplement",
	},
}})
```

The engine reads `Format`, `DurationMin`/`ReadingTimeMin`, `Difficulty`, `Author`, `Available`
(false is never served), `ExpiresAt`, `StartsAt` (events), `RepurchaseDays`, `Region` and
`PhaseFit`; the panel shows `PriceCents`, `CompareAtCents`, `Currency`, `ImageURL`, `URL`,
`ProductType` and `Categories`. A typed field wins over the same key in `Attributes`, which keeps
any other keys. `Region` is sent as the upsert's top-level `region` (the field the engine
validates and ranks on); `ItemUpsert.Region` is the same field.

Products from a Shopify store do not need this call: the jobs CLI syncs the public storefront
(`python -m patterna_jobs shopify-storefront --tenant <slug> --shop-url https://<shop>`) and
turns an orders export into co-purchase pairs (`orders-csv --csv <file>`). There is no admin API
call for either; run them where the jobs run.

**Activity events.** One builder per event, all on `EventBase` (mode, ids, envelope):

```go
who := patterna.EventBase{Mode: mode, UserID: patternaID, SessionID: sessionID}
e, err := patterna.CommunityReply(who, patterna.CommunityPost{PostID: p, CommunityID: c})
e, err = patterna.CommunityPostCreated(who, patterna.CommunityPost{PostID: p, CommunityID: c})
e, err = patterna.CommunityReaction(who, patterna.CommunityPost{PostID: p, CommunityID: c, Reaction: "heart"})
e, err = patterna.PaywallViewed(who, itemID)          // who.RequestID: the slate it came from
e, err = patterna.PushOpened(who, itemID, "weekly_digest")
e, err = patterna.SearchPerformed(who, patterna.Search{QueryHash: h, ResultCount: patterna.Ptr(n), ClickedItemID: id})
e, err = patterna.ItemEngaged(who, patterna.Engagement{ItemID: id, EngagedSeconds: 42, MaxScrollPercent: 80})
e, err = patterna.ItemCompleted(who, workoutID, patterna.KindDocument, "workouts")
e, err = patterna.OrderPlaced(who, patterna.Order{OrderIDHash: h, ProductIDs: ids, ValueCents: patterna.Ptr(4990), Currency: "EUR"})
```

Identity follows consent: `personalized` and `cohort` carry the user id (required) and the
session id when given (the session generator and the paywall cooldown read it); `non_profiled`
carries the session id only and a user id is refused. `QueryHash` must be 16 to 128 lowercase
hex characters (hash the query yourself, e.g. HMAC-SHA256 with a key you hold), so query text
cannot travel by mistake. `item_completed` and `order_placed` are ranking events: sign them
with `patterna.SignEvent` when they do not come from an ingest key.

**Reasons.** A slot the engine cannot truthfully explain says `ReasonRecommended`
(`"recommended"`, "Aanbevolen"/"Recommended"). `patterna.ReasonKeys` lists every key; treat an
unknown one like `recommended`.

**Flows.** `admin.FlowConfig` is the typed recipe, with the generators `admin.GenTimeFit` and
`admin.GenFormatFit` (silent: they move scores and never name a reason) and the filters
`ExcludeDismissedDays`, `ExcludeConsumedDays`, `PaywallCooldownDays`, `RepurchaseDays`,
`HideUnavailable` and `RelevanceFloor`. `admin.ValidateFlowConfig` checks a raw config as the
engine does on save, plus unknown filter keys; `CreateFlow`, `PlanFlow`, `ApplyFlow` and
`patterna flow apply` run it before anything is sent (the CLI checks every file first).

## AI coach: products named in an answer

```go
ev, err := patterna.CoachProductShown(patterna.CoachProduct{
	Mode: patterna.ModePersonalized, UserID: patternaID, // non_profiled: SessionID only
	ProductID: "p_magnesium", ConversationID: convID, Position: 0,
})
if err != nil {
	return err // consent or input bug
}
_, err = pc.IngestEvents(ctx, []patterna.Event{ev})
```

`CoachProductTapped` is the same for a tap (a positive signal). Both carry `product_id` (the
catalog `external_id`), `conversation_id` (your opaque id, never message text) and `position`
(0-based among the answer's products), and apply the consent rules: the user id only in
`personalized` and `cohort`, the rotating session id only in `non_profiled`. No slate and no
propensity: a learning signal and a descriptive count, never in the ledger.

## Several surfaces in one call

Home asks for articles and products together. `RecommendMany` runs the requests in parallel under
one deadline (150 ms counted from the call), so two surfaces cost one budget. Each one falls back
and is abandoned on its own, and each is its own impression with its own request id and ledger
row:

```go
recs, err := pc.RecommendMany(ctx, []patterna.ManyRequest{
	{Request: articlesReq, Fallback: newestArticleIDs},
	{Request: productsReq, Fallback: editorialProductIDs},
})
// recs[0] is the articles, recs[1] the products; err is the first caller bug, if any.
```

Use it when the placements are separate on screen. One surface with mixed `candidate_kinds`
gives one ranked list and one ledger row, which you cannot split into two proofs later.

## Interactions: kind, re-sorting and split blocks

- **`Kind`** is required by this SDK for anything that is not a document, as a check on the
  caller: the engine itself ignores it and records the kind and position from the slate it logged
  (the contract says so). When this client served the slate it knows the kinds, and an empty
  `Kind` on a community, product, event or tip is an `ErrInvalidArgument` before anything is sent.
  `slate.Interaction(itemID, type, at)` fills `Kind` and `Position` from the slate. `OccurredAt`
  is required by the contract.
- **`DisplayedPosition`**: when the app re-sorts on the device (a surface with `device_resort`), keep
  `Position` as the served slot and set `DisplayedPosition` to where it was shown.
- **`Block`**: when one slate is split across UI blocks, name the block. Example: a
  slate of 7 shown as one top slot and an end carousel of six. Ask for one slate, render
  `Items[0]` in the top slot and `Items[1:7]` in the carousel, and report with the served
  position and the block:

  ```go
  slate := rec.Slate // Slots: 7
  top, carousel := slate.Items[0], slate.Items[1:]
  in, _ := slate.Interaction(top.ItemID, patterna.InteractionSeen, now)
  in.Block = "top" // Position 0
  report := []patterna.Interaction{in}
  for _, it := range carousel {
  	c, _ := slate.Interaction(it.ItemID, patterna.InteractionSeen, now)
  	c.Block = "end_carousel" // Position stays 1..6, the served slot
  	report = append(report, c)
  }
  _ = pc.ReportInteractions(slate.RequestID, report)
  ```

  Set `DisplayedPosition` only if the app also re-sorts. Report `seen` for the carousel slots
  only when they scroll into view. One slate keeps one
  impression and one propensity per slot; two requests would log two slates and the carousel's
  ranking would ignore the top slot.

## Consent: measured and assignment unit

- **`Measured`.** `req.Context.SetMeasured(analyticsConsent)`: with `false` the
  slate is ranked and shown but left out of the ledger and the training data. Unset means
  measured. Pass the user's usage-analytics consent.
- **`AssignmentUnit`.** For `non_profiled` traffic under a holdout, the rotating
  session id would put one person in both arms. Send
  `req.AssignmentUnit = patterna.AssignmentUnit(experimentSecret, yourUserID)`: hex
  HMAC-SHA256 under a secret you hold per experiment and never send. The engine uses it only for
  arm assignment and the unit hash. The SDK accepts only the 64-hex shape, so a raw id cannot slip
  through. The same function and test vector exist in the TypeScript and Python SDKs.

## Catalog reconciliation and the signing secret

```go
it, err := pc.GetItem(ctx, patterna.KindProduct, "42") // ingest key; it.Attributes, it.Tags, it.Status, it.Enrichment
if errors.Is(err, patterna.ErrItemNotFound) { /* never synced, or another kind */ }
err = pc.ItemsChangedSince(ctx, lastRun, "", func(it patterna.Item) error { return reconcile(it) })
err = pc.DeleteItem(ctx, patterna.KindCommunity, "community_42") // archive with SetItemStatus instead if it may return
secret, err := pc.CreateSigningSecret(ctx)                       // admin key, shown once
signed, err := patterna.SignEvent(ev, secret.SigningSecret)        // the string as returned, never hex-decoded
```

All three SDKs sign with the bytes of the secret string; the shared vector is
`8baf7299…aac9` with key `test-signing-key` (`signing_test.go`).

## Admin package and CLI

`admin.New(pc)` on a client with an `AdminKey`: `ListFlows`, `GetFlow`, `CreateFlow`,
`ActivateFlow`, `RollbackFlow`, `PlanFlow`, `ApplyFlow`, `ListSurfaces`, `GetSurface`,
`SetSurfaceStatus`, `SetSurfaceTesters`, `UpdateSurface`, `PlanSurface`, `ApplySurface`,
`SurfaceMetrics`, `ListExperiments`, `StartExperiment`, `EndExperiment`, `Ledger`,
`LedgerByBlock`, `LedgerWeeks`, `ExportLedger`, `ExportStatus`, `GetSettings`, `UpdateSettings`,
`SetPhaseTags`, `Health`, `CreateSigningSecret`, `RotateSigningSecret`.

The engine answers `PATCH /surfaces/{key}` with `{key, ok}` rather than the surface; the
surface calls read it back, so they return the surface after the change.

The CLI keeps flow configs in your repository and applies them in CI:

```sh
go install github.com/payam-h/patterna-sdk/go/cmd/patterna@v0.4.1   # the module tag is go/v0.4.1; go resolves it from @v0.4.1
export PATTERNA_URL=https://patterna-api.orven.am/v1 PATTERNA_ADMIN_KEY=...   # CI secret, never a flag

patterna flow get --surface more_to_read > patterna/flows/more_to_read.json  # once, to start
patterna flow apply -f patterna/flows/ --dry-run   # in the pull request
patterna flow apply -f patterna/flows/             # on merge: create + activate what differs
patterna flow rollback --surface more_to_read
patterna surface status --surface more_to_read off # kill switch
patterna surface testers --surface more_to_read u_1,u_2
patterna surface apply -f patterna/surfaces/ --dry-run   # min_slots, device_resort, by_anchor, ...
patterna surface apply -f patterna/surfaces/
patterna settings phase-tags on                    # phase_fit in slates; off by default
patterna experiment start -f patterna/experiments/more_to_read.json
patterna experiment end <id>
patterna ledger --surface more_to_read --from 2026-09-01 --to 2026-10-01
patterna ledger --by block --surface more_to_read   # one row per block; descriptive
patterna ledger export --from 2026-09-01 --to 2026-10-01 --wait
patterna signing-secret create                     # prints the secret alone on stdout
```

A flow file is `{"surface": "...", "note": "...", "config": {FlowConfig}}` or an array of them.
`apply` compares configs as canonical JSON: unchanged when the active version matches, reuses
an existing version with the same config, otherwise creates one. `--activate=false` only stores
it. On a tenant with the two-person rule, activation answers 409 `approval_required`.

A surface file sets the surface fields you keep in the repository; fields left out stay as they
are, and status and testers move with their own commands:

```json
{
  "surface": "pairs_with",
  "item_kinds": ["product"],
  "slots": 4,
  "min_slots": 2,
  "device_resort": false,
  "fallback": {"kind": "curated", "item_ids": ["p_1", "p_2"],
               "by_anchor": {"doc_magnesium_sleep": ["p_magnesium", "p_glycine"]}}
}
```

`surface apply` validates the file (unknown keys fail, `min_slots` 0..50, `slots` 1..50, known
kinds, a fallback kind of newest, popular or curated) and patches only the fields that differ.
The fallback is replaced as a whole, so keep the complete list and `by_anchor` map in the file.

## Releases and tags

Releases live in [payam-h/patterna-sdk](https://github.com/payam-h/patterna-sdk) under `go/`,
tagged `v<version>` and `go/v<version>` (the Go module tag for a module in a subdirectory). Pin a
version, never `latest`:

```sh
go get github.com/payam-h/patterna-sdk/go@v0.4.1
```

`CHANGELOG.md` lists every release. A v2 would need `/v2` at the end of the module path.

## Status

Built: serve and ingest scopes, context deadlines and the server deadline header, per-scope
breaker, one retry for idempotent calls, bounded fire-and-forget interaction and abandonment
reporting with drain on Close, `RecommendWithFallback` and `RecommendMany`, consent and opaque-id
checks, assignment units, event signing with the cross-language vector, typed errors, the admin
package and the CLI, and every engine 0.3 field (control lists, `member_of`, `control_source`,
fill, `shown: false`, coach product events, `mode_required`, `not_shown`, surface files, phase
tags, per-block ledger rows, `engine_version`) and engine round 5 (the coarse request context
and `LocalTime`, typed editorial attributes, nine activity-event builders, the `recommended`
reason key, typed and validated flow configs with `time_fit`, `format_fit` and the six new
filters). Table-driven tests with `httptest` and an
in-memory fake engine (`internal/fakeengine`; race detector on), `go vet` and `staticcheck` clean.

Deliberately left out:

- Admin endpoints beyond flows, surfaces, experiments, ledger, phase tags and the signing secret
  (keys, curation, the other settings): the panel owns them; `AdminCall` reaches any of them.
- Types are hand-written, not generated: an `oapi-codegen` step would pull in a dependency for
  ten structs. `make contracts` does not touch them; change `types.go` with the contract.
- No metrics library: `OnBreakerChange`, `DroppedReports` and `Recommendation.Cause` give the
  numbers; wire them into whatever the host uses.

Open questions and what is built so far: https://patterna.orven.am/docs/status.
