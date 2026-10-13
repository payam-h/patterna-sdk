# Canonical events

Every signal Patterna learns from is one of these. Connectors map external events into them; the
customer's backend sends them directly; the SDK and the serve path emit the recommendation events.
Fields marked `pii` never exist. A connector that sees one drops it before the event is built;
`POST /v1/events` refuses an event that carries one (see "Personal data" below).

## Envelope (every event)

| Field | Type | Notes |
|---|---|---|
| `tenant_id` | uuid | from the API key, never from the body |
| `event_id` | string | idempotency key, unique per tenant; connectors derive it from the source id |
| `type` | enum below | |
| `occurred_at` | RFC 3339 | event time at the source; `received_at` is set by ingest |
| `user_id` | string, optional | the customer's random per-tenant id; absent in `non_profiled` mode |
| `session_id` | string, optional | rotating id; the only identifier in `non_profiled` mode |
| `surface` | string, optional | surface key when the event happened on a placement |
| `request_id` | string, optional | the serve request the event belongs to |
| `source` | string | `backend`, `sdk`, `posthog`, `ga4`, `shopify`, `csv`, `synthetic` |
| `signed` | bool | true when the event came from the backend or carries a valid HMAC |
| `synthetic` | bool | generated rows; excluded from the ledger |
| `context` | object | `locale`, `app_version`, `platform`, `hour_bucket`; nothing else |
| `props` | object | type-specific fields below |

Precedence for identity: `user_id` from the backend, then the source's user id mapped through the
customer's mapping, then `session_id`. A row never carries email, name, IP, device id, free text,
coordinates, or any health value (`pii`).

## Types

| Type | Props | Weight class | Who sends |
|---|---|---|---|
| `rec_seen` | `item_id`, `position`, `displayed_position`, `policy_version` | impression | SDK/backend by request id |
| `rec_click` | `item_id`, `position`, `displayed_position` | positive | SDK/backend |
| `rec_dismiss` | `item_id`, `position`, `displayed_position` | negative | SDK/backend |
| `item_viewed` | `item_id`, `collection` | weak positive | backend, PostHog `content_viewed` |
| `item_engaged` | `item_id`, `engaged_seconds`, `max_scroll_percent` | positive (thresholded) | PostHog `content_engaged` |
| `item_completed` | `item_id`, `collection` | strong positive | backend (workout, breathwork, protocol) |
| `item_pinned`, `item_unpinned` | `item_id` | explicit preference | app (new event) |
| `community_joined`, `community_left` | `community_id`, `source` | strong positive / negative | backend |
| `community_post_viewed` | `post_id`, `community_id` | weak positive | PostHog |
| `event_rsvp`, `event_attended` | `event_id` | strong positive | backend |
| `search_performed` | `query_hash`, `result_count`, `clicked_item_id` | intent | app (query hashed, never the text) |
| `paywall_viewed` | `item_id`, `request_id` | funnel | app |
| `subscription_started`, `subscription_cancelled` | `tier`, `source` | outcome | backend webhooks |
| `product_viewed` | `product_id`, `request_id` | positive | shop link parameter |
| `order_placed` | `order_id_hash`, `product_ids[]`, `value_cents`, `currency` | outcome | Shopify webhook or export |
| `push_sent`, `push_opened` | `notification_type`, `item_id` | | backend |
| `tip_shown`, `tip_tapped` | `tip_id` | | app |
| `coach_question` | `topic_cluster` | intent | backend (never the text) |
| `coach_product_shown` | `product_id`, `conversation_id`, `position` | | backend; a product an LLM answer named, shown under the answer. `product_id` is the product's catalog `external_id`; `position` is its 0-based place among the answer's products. No slate and no propensity: a learning signal and a descriptive count, never in the ledger |
| `coach_product_tapped` | `product_id`, `conversation_id`, `position` | positive | backend; the user tapped such a product |
| `consent_changed` | `mode` (`personalized`, `cohort`, `non_profiled`) | control | backend; `non_profiled` is the same opt-out as `PUT /users/{id}/mode`: the user row goes, a salted marker stays, the deletion request stores only the salted hash and the jobs unlink the user's impressions (kept for the ledger) |
| `user_deleted` | | control | backend; triggers the deletion job (hashed request, impressions unlinked, never deleted) |

Identity on the coach events, as on every event: `user_id` only in `personalized` and `cohort`
mode; a `non_profiled` user's event carries the rotating `session_id` only (the one the app sends
with its recommendation requests), so it never joins a profile. `conversation_id` is the
customer's own opaque id for the conversation (never message text).

`rec_*` events with a `request_id` follow the rules of `POST /v1/interactions`: the item must be
in the slate served for that request (`unknown_item`), the kind and the served position are taken
from the logged slate (a `kind` the client sends is ignored), and on a surface with
`device_resort` the event must carry `displayed_position` (`displayed_position_required`).

## Personal data

An event that carries personal data is rejected, never stored with the field silently removed:
the sender has a bug to fix. `POST /v1/events` answers 202 with the event in `rejected` with code
`pii_rejected` and the offending field paths in `fields` (names only, never values), and counts it
in `pii_rejected`. Personal data is: a PII field name anywhere in `props` or `context` (`email`,
`name`, `first_name`, `phone`, `ip`, `$ip`, `device_id`, `address`, `latitude`, `location`, `text`,
`body`, `message`, `note`, `birthday`, `$set`, … the list in `apps/engine/internal/ingest` and
`jobs/patterna_jobs/pii.py`), an email address as `user_id` or `session_id`, or an
identity-provider subject (`provider|id`) as `user_id`. Any other field outside the type's
allowlist is not personal data by its name: it is dropped and counted in `dropped_fields`.

## Identity across connectors

`user_id` is always the customer's random per-tenant id. A connector that only sees a source id
(a PostHog distinct id, which at Elvou is the Auth0 subject) never stores it raw: an id Patterna's
importer minted (`u_` + 26 base32 characters) is kept, anything else is minted with the tenant's
`PATTERNA_ID_SECRET` exactly as the importer does (`u_` + base32 of HMAC-SHA256 over `user`, US,
the id), so connector events join the imported users; without the secret the identity is dropped.

## Signatures

Every event that carries a `signature` is verified, whatever key sent it; events from an ingest
or admin key need none (the key is the backend's proof). There is no separate SDK key scope: sign
on the backend, never in an app.

The secret: an owner creates it once with `POST /v1/settings/signing-secret` and replaces it with
`POST /v1/settings/signing-secret/rotate`; both return it once, as a string (`pss_` and 64 hex
characters). The HMAC key is that string's UTF-8 bytes, exactly as given: nothing is decoded. A
tenant without a secret rejects signed events (`no_signing_secret`).

The signed form, shared by the SDKs and the engine: the event object as sent minus `signature`,
serialised as JSON with object keys sorted in ASCII order, no whitespace, HTML characters
unescaped, U+2028 and U+2029 escaped; `signature` = lowercase hex HMAC-SHA256 over those UTF-8
bytes with the secret string as the key. Keep numbers as integers for cross-language stability.
The cross-language test vector lives in `packages/sdk-go/signing_test.go` (key: the string
`test-signing-key`) and is verified by the engine in `apps/engine/internal/ingest/ingest_test.go`.

## Positions

Positions start at 0. `position` is always the slot Patterna served; the engine records it from
the impression log.

A surface may be re-sorted on the device when it is flagged `device_resort`
(`PATCH /v1/surfaces/{key}`). Re-sorting is then allowed under a holdout: both arms are re-sorted
by the same device rule, so the ledger stays intention to treat on served slates. Every
interaction and `rec_*` event on such a surface must carry `displayed_position` (where the app
showed the item, 0-based); without the flag it stays optional. The offline estimators use the
displayed position for anything about examination (position bias in persona_bayes, the learned
rankers' examination weights and features); the logged propensity still belongs to the served
slot.

One slate may be rendered in two blocks of a page (for example the product near the top of an
article and the products at its end): request one slate, render position 0 at the top and 1 to n
at the end, and send `block` (a short name such as `top` or `end`) with each interaction. Dedup and
propensities stay in one impression. A `seen` with `block` records the block on the impression
(`blocks_shown`); `GET /proof/ledger?by=block` and `GET /surfaces/{key}/metrics?block=` then split
the numbers per block (slates seen in the block, taps in the block).

## Measured and not shown

A slate requested with `context.measured: false` (the user did not allow usage analytics) is
served and logged with exact propensities but reports no interactions and stays out of the ledger,
the power calculation and all learning, in both arms. Consent is decided before the list is
drawn, so excluding these slates from both arms keeps the comparison unbiased.

Not shown is different: whether a block is shown could depend on the arm, so those slates are
never dropped. The engine decides `shown` before the arm, from the number of items that pass the
request's rules, against the surface's `min_slots`. Below it the answer is `shown: false` with no
items in either arm, logged `not_shown`; the slate stays in its arm in the ledger as served with
zero taps (intention to treat). The app hides the block and reports nothing; it must not abandon
the slate (409 `not_shown`). A block the app hides for its own reasons after a shown answer is
abandoned (below) and also stays in its arm, so engine-hidden and app-hidden blocks count alike.

## Abandoned slates

A client that gave up on a slate (its deadline passed, or it hid the block) calls
`POST /v1/recommendations/{request_id}/abandon`. The impression is flagged `fallback` and
`client_abandoned`: it counts in the fallback rate and stays in its arm in the ledger.

## Weights (default flow config, editable per tenant)

`rec_click` 1.0 · `item_completed` 1.5 · `community_joined` 1.5 · `event_rsvp` 1.0 ·
`item_engaged` 0.6 when `engaged_seconds >= 20` else 0 · `item_viewed` 0.2 · `item_pinned` 2.0 ·
`rec_dismiss` -1.0 · `community_left` -1.0 · `order_placed` 2.0 per product.

## PostHog mapping

Status: built (the webhook `POST /v1/webhooks/posthog`, since engine 0.2) and off until a tenant's
owner sets a PostHog token (`PATCH /settings`, `posthog_token`; only its SHA-256 hash is stored).
Without a token the webhook accepts nothing. For Elvou it stays off. When on, the mapping is:

| PostHog event | Canonical | Dropped |
|---|---|---|
| `content_viewed` | `item_viewed` with `content_type` → collection | `content_title`, `content_slug` |
| `content_engaged` | `item_engaged` | |
| `content_scroll_depth` | folded into `item_engaged` | |
| `community_joined` / `community_left` | same | |
| `cyclus_visited`, `cyclus_action`, `breathing_*`, `*_location_*`, `$screen` | not ingested | everything |
| person properties | not ingested | `email`, `name` |
