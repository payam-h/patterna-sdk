# sdk-go: `github.com/payam-h/patterna-sdk/go`

Version 0.2.0 (see `CHANGELOG.md`). Features marked **engine 0.2** need the engine release that
adds the matching endpoint, header or field; against an older engine they are ignored or answer
404, and nothing else breaks.

Go client for the Patterna API, built for a customer backend in a request path (Elvou's Gin +
gqlgen backend is the first user; see `docs/elvou-integration.md`).
Standard library only. Go 1.25.

```sh
cd packages/sdk-go
go test -race ./...
go vet ./...
go run honnef.co/go/tools/cmd/staticcheck@2026.2.2 ./...
# or from the repo root: make sdk-go
```

(With an older local Go, prefix `GOTOOLCHAIN=auto`.)

| File | What |
|---|---|
| `client.go` | `Config`, `New`, serve, ingest and user methods, the transport (deadline, retry, breaker, headers) |
| `options.go` | `CallOption` (`WithTester`, `WithTraceID`, `WithRequestID`), trace ids, `NewRequestID` |
| `abandon.go` | `Abandon`, `AbandonSync` |
| `many.go` | `RecommendMany` |
| `catalog.go` | `DeleteItem`, `ListItems`, `ItemsChangedSince`, `CreateSigningSecret`, `AdminCall` |
| `assignment.go` | `AssignmentUnit` |
| `slatekinds.go` | memory of recent slates' item kinds, for the interaction `kind` check |
| `breaker.go` | consecutive-failure circuit breaker, one per scope |
| `fallback.go` | `RecommendWithFallback` and `Recommendation` |
| `consent.go` | consent gate and the opaque-user-id check |
| `signing.go` | canonical JSON and HMAC-SHA256 event signatures (same bytes as `@patterna/sdk`) |
| `errors.go` | `*Error`, codes, sentinels for `errors.Is` |
| `types.go` | hand-written mirror of `contracts/openapi.yaml` (plus the engine 0.2 fields) |
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
// rec.ItemIDs is always set; rec.Slate is nil when the fallback was used.
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
| `Abandon` (async), `AbandonSync` (engine 0.2) | serve (ingest breaker) | 5 s | once |
| `DeleteItem`, `ListItems`, `ItemsChangedSince` (engine 0.2) | ingest | 5 s per page | once |
| `CreateSigningSecret` (engine 0.2), `AdminCall`, `admin.*` | admin | 30 s | idempotent ones once |

- **Deadlines.** Each call runs under `context.WithTimeout(ctx, budget)`; a shorter caller
  deadline wins. The budget covers the retry too. A deadline is `ErrTimeout`; a caller cancel is
  `ErrAborted`.
- **Server deadline (engine 0.2).** Recommend sends `X-Deadline-Ms`, 120 ms by default
  (`Config.RecommendDeadline`, always below `RecommendTimeout`), so the engine serves its own
  fallback in time instead of answering after the client gave up. With a shorter caller deadline
  the header shrinks by the same 30 ms margin: 80 ms left sends 50.
- **Request and trace ids.** The client mints `X-Request-Id` for every call (UUIDv4;
  `WithRequestID` overrides). The engine uses it as the slate's `request_id`, and every `*Error`
  carries it, even on a timeout. `X-Trace-Id` comes from `WithTraceID(id)`, else
  `ContextWithTraceID(ctx, id)`, else `Config.TraceID(ctx)` (for Sentry or OpenTelemetry). Ids
  longer than 64 characters or with spaces are not sent.
- **Abandoned slates (engine 0.2).** When `RecommendWithFallback` falls back after the request
  reached the engine (timeout, network error, 5xx, unreadable answer) it queues
  `POST /recommendations/{id}/abandon` with the reason, so the impression is flagged
  `client_fallback` and the fallback rate and the ledger stay honest. Nothing is sent when the
  breaker was open (the request never left) or the engine refused the request with a 4xx (nothing
  was logged). Call `pc.Abandon(id, patterna.AbandonClientFallback)` yourself when a slate
  arrived but the app showed something else.
- **Testers (engine 0.2).** `pc.Recommend(ctx, req, patterna.WithTester())` sends
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
  | `ErrPIIRejected` | 422 | **returned**: personal data in the request |
  | `ErrUnknownRequest` | 404 | `AbandonSync`, `ReportInteractionsSync`: the engine never logged that id |
  | `ErrUnknownItem` | 404 | catalog calls |
  | `ErrConsentRequired`, `ErrConsentViolation`, `ErrInvalidArgument`, `ErrNotConfigured` | - | **returned**, nothing sent |
  | `ErrTimeout`, `ErrNetwork`, `ErrCircuitOpen`, other 5xx | - | fallback |

  Log `Slate.Arm` (`control`, `treatment`, `none`) next to your trace when you debug a holdout.
- **Consent.** A request needs a mode; `non_profiled` needs a session id and no user id. User ids
  that look like an email or an identity-provider subject are refused everywhere, and never
  appear in error text.

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

- **`Kind`** is required for anything that is not a document (engine 0.2 refuses it otherwise).
  When this client served the slate it knows the kinds, and an empty `Kind` on a community,
  product, event or tip is an `ErrInvalidArgument` before anything is sent.
  `slate.Interaction(itemID, type, at)` fills `Kind` and `Position` from the slate.
- **`DisplayedPosition`**: when the app re-sorts on the device (Elvou's cycle-phase sort), keep
  `Position` as the served slot and set `DisplayedPosition` to where it was shown.
- **`Block` (engine 0.2)**: when one slate is split across UI blocks, name the block. Example: a
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

- **`Measured` (engine 0.2).** `req.Context.SetMeasured(analyticsConsent)`: with `false` the
  slate is ranked and shown but left out of the ledger and the training data. Unset means
  measured. At Elvou, pass the user's `analytics` consent.
- **`AssignmentUnit` (engine 0.2).** For `non_profiled` traffic under a holdout, the rotating
  session id would put one person in both arms. Send
  `req.AssignmentUnit = patterna.AssignmentUnit(experimentSecret, yourUserID)`: hex
  HMAC-SHA256 under a secret you hold per experiment and never send. The engine uses it only for
  arm assignment and the unit hash. The SDK accepts only the 64-hex shape, so a raw id cannot slip
  through. The same function and test vector exist in the TypeScript and Python SDKs.

## Catalog reconciliation and the signing secret

```go
err := pc.ItemsChangedSince(ctx, lastRun, "", func(it patterna.Item) error { return reconcile(it) })
err = pc.DeleteItem(ctx, patterna.KindCommunity, "community_42") // engine 0.2; archive with SetItemStatus instead if it may return
secret, err := pc.CreateSigningSecret(ctx)                       // engine 0.2, admin key, shown once
signed, err := patterna.SignEvent(ev, secret.SigningSecret)        // the string as returned, never hex-decoded
```

All three SDKs sign with the bytes of the secret string; the shared vector is
`8baf7299…aac9` with key `test-signing-key` (`signing_test.go`).

## Admin package and CLI

`admin.New(pc)` on a client with an `AdminKey`: `ListFlows`, `GetFlow`, `CreateFlow`,
`ActivateFlow`, `RollbackFlow`, `PlanFlow`, `ApplyFlow`, `ListSurfaces`, `GetSurface`,
`SetSurfaceStatus`, `SetSurfaceTesters`, `ListExperiments`, `StartExperiment`, `EndExperiment`,
`Ledger`, `LedgerWeeks`, `ExportLedger`, `ExportStatus`, `CreateSigningSecret`,
`RotateSigningSecret`.

The CLI keeps flow configs in your repository and applies them in CI:

```sh
go install github.com/payam-h/patterna-sdk/go/cmd/patterna@go/v0.2.0
export PATTERNA_URL=https://patterna-api.orven.am/v1 PATTERNA_ADMIN_KEY=...   # CI secret, never a flag

patterna flow get --surface more_to_read > patterna/flows/more_to_read.json  # once, to start
patterna flow apply -f patterna/flows/ --dry-run   # in the pull request
patterna flow apply -f patterna/flows/             # on merge: create + activate what differs
patterna flow rollback --surface more_to_read
patterna surface status --surface more_to_read off # kill switch
patterna surface testers --surface more_to_read u_1,u_2
patterna experiment start -f patterna/experiments/more_to_read.json
patterna experiment end <id>
patterna ledger --surface more_to_read --from 2026-09-01 --to 2026-10-01
patterna ledger export --from 2026-09-01 --to 2026-10-01 --wait
patterna signing-secret create                     # prints the secret alone on stdout
```

A flow file is `{"surface": "...", "note": "...", "config": {FlowConfig}}` or an array of them.
`apply` compares configs as canonical JSON: unchanged when the active version matches, reuses
an existing version with the same config, otherwise creates one. `--activate=false` only stores
it. On a tenant with the two-person rule, activation answers 409 `approval_required`.

## Releases and tags

The module lives in a subdirectory, so Go expects tags prefixed with its path:
`packages/sdk-go/vX.Y.Z`. After merging to `main`:

```sh
git tag -a packages/sdk-go/v0.2.0 -m "sdk-go 0.2.0" <merge-commit>
git push origin packages/sdk-go/v0.2.0
```

Then, in a consumer (the repository is private, so tell Go not to use the public proxy and give
git a deploy key or token):

```sh
go env -w GOPRIVATE=github.com/payam-h/*
git config --global url."git@github.com:".insteadOf "https://github.com/"   # SSH deploy key
go get github.com/payam-h/patterna-sdk/go@v0.2.0
```

`go get` takes the plain version (`@v0.2.0`); the prefix is only on the tag. Bump the version in
`CHANGELOG.md` with each tag. A v2 would need `/v2` at the end of the module path.

## Status

Built: serve and ingest scopes, context deadlines and the server deadline header, per-scope
breaker, one retry for idempotent calls, bounded fire-and-forget interaction and abandonment
reporting with drain on Close, `RecommendWithFallback` and `RecommendMany`, consent and opaque-id
checks, assignment units, event signing with the cross-language vector, typed errors, the admin
package and the CLI. Table-driven tests with `httptest` (race detector on), `go vet` and
`staticcheck` clean.

Deliberately left out:

- Admin endpoints beyond flows, surfaces, experiments, ledger and the signing secret (keys,
  curation, settings): the panel owns them; `AdminCall` reaches any of them.
- Types are hand-written, not generated: an `oapi-codegen` step would pull in a dependency for
  ten structs. `make contracts` does not touch them; change `types.go` with the contract.
- No metrics library: `OnBreakerChange`, `DroppedReports` and `Recommendation.Cause` give the
  numbers; wire them into whatever the host uses.

Open questions: see `packages/sdk-ts/README.md` (same two, tracked in `docs/ISSUES.md`).
