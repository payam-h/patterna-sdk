package patterna

import "time"

// Hand-written mirror of contracts/openapi.yaml. When the contract changes, change this file in
// the same pull request (see contracts/README.md).

// Mode is the user's consent mode.
type Mode string

const (
	ModePersonalized Mode = "personalized"
	ModeCohort       Mode = "cohort"
	// ModeNonProfiled stores no user id: requests carry a rotating session id and context only.
	ModeNonProfiled Mode = "non_profiled"
)

// Valid reports whether m is one of the three contract modes.
func (m Mode) Valid() bool {
	return m == ModePersonalized || m == ModeCohort || m == ModeNonProfiled
}

// RecommendContext is the request context. Nothing about the person goes here.
type RecommendContext struct {
	Locale         string   `json:"locale"`
	AppVersion     string   `json:"app_version,omitempty"`
	Platform       string   `json:"platform,omitempty"` // ios | android | web
	HourBucket     *int     `json:"hour_bucket,omitempty"`
	AnchorItemID   string   `json:"anchor_item_id,omitempty"`
	Premium        *bool    `json:"premium,omitempty"`
	CandidateKinds []string `json:"candidate_kinds,omitempty"`
	// Measured (engine 0.2) says whether this request may count in the ledger and the learned
	// rankers' training data. nil means true. Set it from the user's analytics consent with
	// SetMeasured.
	Measured *bool `json:"measured,omitempty"`
	// ControlItemIDs (engine 0.3) is your own editorial list for this page: the article's
	// "pairs with" products, the editor's related articles. In the control arm it is the slate, in
	// this order, under the request's rules (logged with ControlSource "request"); in treatment it
	// is the list short slates are filled from. At most 50 ids.
	ControlItemIDs []string `json:"control_item_ids,omitempty"`
	// MemberOf (engine 0.3) lists the community ids the user is in, so they are never offered.
	// Meant for non_profiled requests, which have no identity to look membership up by; in
	// personalized and cohort mode the engine also reads community_joined/community_left events.
	// At most 500 ids.
	MemberOf []string `json:"member_of,omitempty"`
}

// SetMeasured sets Measured from the user's analytics consent: false keeps the request out of
// measurement (the slate is still ranked and shown). Elvou: the "analytics" consent type.
func (rc *RecommendContext) SetMeasured(analyticsConsent bool) {
	rc.Measured = &analyticsConsent
}

// RecommendRequest asks for a slate on one surface.
type RecommendRequest struct {
	Surface        string           `json:"surface"`
	Mode           Mode             `json:"mode"`
	UserID         string           `json:"user_id,omitempty"`
	SessionID      string           `json:"session_id,omitempty"`
	Context        RecommendContext `json:"context"`
	Slots          int              `json:"slots,omitempty"`
	ExcludeItemIDs []string         `json:"exclude_item_ids,omitempty"`
	Shadow         bool             `json:"shadow,omitempty"`
	// AssignmentUnit (engine 0.2) keeps one person in one holdout arm across rotating sessions.
	// Compute it with AssignmentUnit(secret, yourUserID); the SDK accepts only that shape (64
	// lowercase hex characters), so a raw user id can never travel here.
	AssignmentUnit string `json:"assignment_unit,omitempty"`
}

// Slate is a ranked list with a reason per slot. Echo RequestID back in ReportInteractions.
type Slate struct {
	RequestID     string      `json:"request_id"`
	Surface       string      `json:"surface"`
	Arm           string      `json:"arm"` // control | treatment | none
	PolicyVersion string      `json:"policy_version"`
	FlowVersion   int         `json:"flow_version,omitempty"`
	Items         []SlateItem `json:"items"`
	Fallback      bool        `json:"fallback,omitempty"`
	LatencyMS     int         `json:"latency_ms,omitempty"`
	// Shown (engine 0.2) is false when the slate is shorter than the surface's min_slots: hide
	// the block. Nil (an older engine) means shown. Use Visible.
	Shown *bool `json:"shown,omitempty"`
	// ControlSource (engine 0.3) says where an editorial slate came from: ControlSourceRequest
	// (Context.ControlItemIDs), ControlSourceByAnchor (the surface's fallback.by_anchor list for the
	// anchor) or ControlSourceSurface (the surface's own list). Empty on ranked slates.
	ControlSource ControlSource `json:"control_source,omitempty"`
	// FillCount (engine 0.3) is the number of trailing positions filled from the editorial list
	// because the ranked candidates ran out (propensity 1). Those items have Fill set.
	FillCount int `json:"fill_count,omitempty"`
	// ModelVersions names the model version each learner that ranked the slate read (persona_bayes,
	// user_bayes, item_sim, ranker, gbdt). Engine 0.3 logs it on the impression (trace, export)
	// and does not send it with the answer yet, so it is nil there; it is decoded when present.
	ModelVersions map[string]string `json:"model_versions,omitempty"`
}

// ControlSource is where an editorial (control) slate came from.
type ControlSource string

const (
	ControlSourceRequest  ControlSource = "request"   // RecommendContext.ControlItemIDs
	ControlSourceByAnchor ControlSource = "by_anchor" // the surface's fallback.by_anchor for the anchor
	ControlSourceSurface  ControlSource = "surface"   // the surface's own fallback list
)

// markFills sets Fill on the trailing FillCount items when the engine did not mark them itself
// (engine 0.3 sends fill_count on the slate and fill per slot only in its log).
func (s *Slate) markFills() {
	if s == nil || s.FillCount <= 0 {
		return
	}
	for _, it := range s.Items {
		if it.Fill {
			return
		}
	}
	for i := max(0, len(s.Items)-s.FillCount); i < len(s.Items); i++ {
		s.Items[i].Fill = true
	}
}

// Visible reports whether the block should be rendered: false only when the engine said
// shown=false (fewer items than the surface's min_slots).
func (s *Slate) Visible() bool {
	return s != nil && (s.Shown == nil || *s.Shown)
}

// ItemIDs returns the external ids in slate order.
func (s *Slate) ItemIDs() []string {
	if s == nil {
		return nil
	}
	ids := make([]string, len(s.Items))
	for i, it := range s.Items {
		ids[i] = it.ItemID
	}
	return ids
}

// Interaction builds an interaction for one of the slate's items with the served Kind and
// Position filled in. ok is false when itemID is not in the slate.
func (s *Slate) Interaction(itemID string, typ InteractionType, at time.Time) (in Interaction, ok bool) {
	if s == nil {
		return Interaction{}, false
	}
	for _, it := range s.Items {
		if it.ItemID == itemID {
			return Interaction{ItemID: it.ItemID, Kind: it.Kind, Position: it.Position, Type: typ, OccurredAt: at}, true
		}
	}
	return Interaction{}, false
}

// SlateItem is one slot. ItemID is the customer's external id.
type SlateItem struct {
	ItemID    string            `json:"item_id"`
	Kind      string            `json:"kind"`
	Position  int               `json:"position"`
	ReasonKey string            `json:"reason_key"`
	Reason    map[string]string `json:"reason"` // locale -> sentence
	Generator string            `json:"generator,omitempty"`
	Score     float64           `json:"score,omitempty"`
	PhaseFit  []string          `json:"phase_fit,omitempty"`
	// Fill (engine 0.3) is true when the slot was filled from the editorial list behind the ranked
	// items (Slate.FillCount); the SDK sets it from FillCount when the engine does not send it.
	Fill bool `json:"fill,omitempty"`
}

// InteractionType is what happened to a slot.
type InteractionType string

const (
	InteractionSeen    InteractionType = "seen"
	InteractionClick   InteractionType = "click"
	InteractionDismiss InteractionType = "dismiss"
)

// Interaction reports one seen, click or dismiss.
type Interaction struct {
	ItemID string `json:"item_id"`
	// Kind is the item kind (document, product, community, event, tip). Required for anything that
	// is not a document (engine 0.2 refuses it otherwise); empty means document.
	Kind     string `json:"kind,omitempty"`
	Position int    `json:"position"` // the 0-based slot Patterna served
	// DisplayedPosition is the 0-based slot where the app actually showed the item, when it
	// re-sorted on the device (Elvou's cycle-phase sort). Position stays the served slot.
	DisplayedPosition *int `json:"displayed_position,omitempty"`
	// Block (engine 0.2) names the UI block that showed the slot when one slate is split across
	// blocks, e.g. "top" for position 0 and "end_carousel" for positions 1..6 of a slate of 7.
	// Position stays the served slot; the README has the pattern.
	Block      string          `json:"block,omitempty"`
	Type       InteractionType `json:"type"`
	OccurredAt time.Time       `json:"occurred_at"`
}

// EventContext is a closed set by design: no field here can carry personal data.
type EventContext struct {
	Locale     string `json:"locale,omitempty"`
	AppVersion string `json:"app_version,omitempty"`
	Platform   string `json:"platform,omitempty"`
	HourBucket *int   `json:"hour_bucket,omitempty"`
}

// Event is a canonical event (contracts/events.md).
type Event struct {
	EventID    string         `json:"event_id"`
	Type       string         `json:"type"`
	OccurredAt time.Time      `json:"occurred_at"`
	UserID     string         `json:"user_id,omitempty"`
	SessionID  string         `json:"session_id,omitempty"`
	Surface    string         `json:"surface,omitempty"`
	RequestID  string         `json:"request_id,omitempty"`
	Source     string         `json:"source"` // backend | sdk | posthog | ga4 | shopify | csv | synthetic
	Signature  string         `json:"signature,omitempty"`
	Synthetic  bool           `json:"synthetic,omitempty"`
	Context    *EventContext  `json:"context,omitempty"`
	Props      map[string]any `json:"props,omitempty"`
}

// Rejection is one event the API refused inside an accepted batch.
type Rejection struct {
	EventID string `json:"event_id"`
	Reason  string `json:"reason"`
}

// IngestResult is the answer to IngestEvents.
type IngestResult struct {
	Accepted   int         `json:"accepted"`
	Duplicates int         `json:"duplicates"`
	Rejected   []Rejection `json:"rejected"`
}

// ItemKind is the catalog kind.
type ItemKind string

const (
	KindDocument  ItemKind = "document"
	KindProduct   ItemKind = "product"
	KindCommunity ItemKind = "community"
	KindEvent     ItemKind = "event"
	KindTip       ItemKind = "tip"
)

// Valid reports whether k is one of the contract kinds.
func (k ItemKind) Valid() bool {
	switch k {
	case KindDocument, KindProduct, KindCommunity, KindEvent, KindTip:
		return true
	}
	return false
}

// ItemStatus is the catalog status.
type ItemStatus string

const (
	StatusDraft     ItemStatus = "draft"
	StatusScheduled ItemStatus = "scheduled"
	StatusPublished ItemStatus = "published"
	StatusArchived  ItemStatus = "archived"
)

// ItemUpsert creates or updates a catalog item.
type ItemUpsert struct {
	ExternalID  string         `json:"external_id"`
	Kind        ItemKind       `json:"kind"`
	Collection  string         `json:"collection,omitempty"`
	Title       string         `json:"title"` // editorial title only
	Locale      string         `json:"locale"`
	Status      ItemStatus     `json:"status,omitempty"`
	Premium     bool           `json:"premium"`
	PublishedAt *time.Time     `json:"published_at,omitempty"`
	Attributes  map[string]any `json:"attributes,omitempty"`
	// Text is body text for enrichment; stored only as embedding and tags, never returned.
	Text string    `json:"text,omitempty"`
	Tags []ItemTag `json:"tags,omitempty"`
}

// ItemTag is a tag with an optional weight; Approved applies to SetItemTags.
type ItemTag struct {
	Tag      string   `json:"tag"`
	Weight   *float64 `json:"weight,omitempty"`
	Approved *bool    `json:"approved,omitempty"`
}

// UpsertItemsResult is the answer to UpsertItems.
type UpsertItemsResult struct {
	Upserted         int `json:"upserted"`
	EnrichmentQueued int `json:"enrichment_queued"`
}

// Item is a catalog item as the API returns it.
type Item struct {
	ExternalID  string         `json:"external_id"`
	Kind        ItemKind       `json:"kind"`
	Collection  string         `json:"collection,omitempty"`
	Title       string         `json:"title"`
	Locale      string         `json:"locale"`
	Status      ItemStatus     `json:"status"`
	Premium     bool           `json:"premium"`
	PublishedAt *time.Time     `json:"published_at,omitempty"`
	UpdatedAt   *time.Time     `json:"updated_at,omitempty"` // engine 0.2
	Attributes  map[string]any `json:"attributes,omitempty"`
	Tags        []ItemTag      `json:"tags,omitempty"`
}

// ItemPage is one page of ListItems.
type ItemPage struct {
	Items      []Item `json:"items"`
	NextCursor string `json:"next_cursor"`
}

// ListItemsParams filters ListItems. Zero values are left out.
type ListItemsParams struct {
	Kind   ItemKind
	Status ItemStatus
	// ChangedSince (engine 0.2) returns items created, updated, archived or deleted after it, for
	// reconciliation against your own catalog.
	ChangedSince time.Time
	Cursor       string
	Limit        int // at most 500; default 100
}

// UserProfile holds declared attributes as tag affinities. Never health values.
type UserProfile struct {
	Mode           Mode     `json:"mode,omitempty"`
	Premium        *bool    `json:"premium,omitempty"`
	Goals          []string `json:"goals,omitempty"`
	ActivityLevel  string   `json:"activity_level,omitempty"` // low | moderate | high
	StressLevel    string   `json:"stress_level,omitempty"`   // low | moderate | high
	Wearable       string   `json:"wearable,omitempty"`
	SupplementsUse string   `json:"supplements_use,omitempty"` // none | some | stack
	Locale         string   `json:"locale,omitempty"`
}

// DeletionRequest is the receipt for DeleteUser.
type DeletionRequest struct {
	ID                string     `json:"id"`
	UserID            string     `json:"user_id"`
	RequestedAt       time.Time  `json:"requested_at"`
	RowsDeletedAt     *time.Time `json:"rows_deleted_at"`
	ModelsRetrainedAt *time.Time `json:"models_retrained_at"`
}

type apiErrorBody struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	RequestID string         `json:"request_id"`
	Details   map[string]any `json:"details"`
}
