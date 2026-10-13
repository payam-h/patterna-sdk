package patterna

import (
	"regexp"
	"strings"
	"time"
)

// Event types with builders (contracts/events.md, docs/SIGNALS.md §3).
const (
	EventCommunityPostCreated = "community_post_created"
	EventCommunityReply       = "community_reply"
	EventCommunityReaction    = "community_reaction"
	EventPaywallViewed        = "paywall_viewed"
	EventPushOpened           = "push_opened"
	EventSearchPerformed      = "search_performed"
	EventItemEngaged          = "item_engaged"
	EventItemCompleted        = "item_completed"
	EventOrderPlaced          = "order_placed"
)

// EventBase is who an event is about and the envelope fields every builder shares.
//
// Identity follows consent: in personalized and cohort mode the event carries UserID (required)
// and SessionID when you give one (the session generator and the paywall cooldown read it); in
// non_profiled mode it carries SessionID only (required) and a UserID is refused, so the event
// never joins a profile.
type EventBase struct {
	Mode      Mode
	UserID    string
	SessionID string
	// Surface and RequestID tie the event to a placement and a served slate when there is one.
	Surface   string
	RequestID string
	// OccurredAt defaults to now; EventID to a fresh UUID; Source to "backend".
	OccurredAt time.Time
	EventID    string
	Source     string
	Context    *EventContext
}

func (b EventBase) build(typ string, props map[string]any) (Event, error) {
	op := typ
	if err := checkMode(op, b.Mode); err != nil {
		return Event{}, err
	}
	if err := checkIdentity(op, b.Mode, b.UserID, b.SessionID); err != nil {
		return Event{}, err
	}
	if b.Mode != ModeNonProfiled && b.UserID == "" {
		return Event{}, newError(CodeInvalidArgument, op, "%s events need the user id", b.Mode)
	}
	e := Event{
		EventID: b.EventID, Type: typ, OccurredAt: b.OccurredAt, Source: b.Source, Context: b.Context,
		Surface: b.Surface, RequestID: b.RequestID, SessionID: b.SessionID, Props: props,
	}
	if b.Mode != ModeNonProfiled {
		e.UserID = b.UserID
	}
	if e.EventID == "" {
		e.EventID = NewRequestID()
	}
	if e.OccurredAt.IsZero() {
		e.OccurredAt = time.Now().UTC()
	}
	if e.Source == "" {
		e.Source = "backend"
	}
	if err := validateEvent(op, e); err != nil {
		return Event{}, err
	}
	return e, nil
}

func blank(s string) bool { return strings.TrimSpace(s) == "" }

// CommunityPost names a post in a community. Reaction is the reaction's kind (e.g. "like"),
// used by CommunityReaction only.
type CommunityPost struct {
	PostID      string
	CommunityID string
	Reaction    string
}

// CommunityPostCreated builds community_post_created: the user wrote a post (active membership,
// the community's activity_rate).
func CommunityPostCreated(b EventBase, p CommunityPost) (Event, error) {
	return communityEvent(EventCommunityPostCreated, b, p)
}

// CommunityReply builds community_reply: the user replied to a post.
func CommunityReply(b EventBase, p CommunityPost) (Event, error) {
	return communityEvent(EventCommunityReply, b, p)
}

// CommunityReaction builds community_reaction: the user reacted to a post (Reaction required).
func CommunityReaction(b EventBase, p CommunityPost) (Event, error) {
	return communityEvent(EventCommunityReaction, b, p)
}

func communityEvent(typ string, b EventBase, p CommunityPost) (Event, error) {
	if blank(p.PostID) || blank(p.CommunityID) {
		return Event{}, newError(CodeInvalidArgument, typ, "post id and community id are required")
	}
	props := map[string]any{"post_id": p.PostID, "community_id": p.CommunityID}
	if typ == EventCommunityReaction {
		if blank(p.Reaction) {
			return Event{}, newError(CodeInvalidArgument, typ, "reaction is required")
		}
		props["reaction"] = p.Reaction
	} else if p.Reaction != "" {
		return Event{}, newError(CodeInvalidArgument, typ, "reaction belongs on community_reaction only")
	}
	return b.build(typ, props)
}

// PaywallViewed builds paywall_viewed: a free user hit the paywall on itemID. The item is then
// left out of that user's slates for the flow's paywall_cooldown_days. b.RequestID, when set,
// is also sent as the props' request_id (the slate the item came from).
func PaywallViewed(b EventBase, itemID string) (Event, error) {
	if blank(itemID) {
		return Event{}, newError(CodeInvalidArgument, EventPaywallViewed, "item id is required")
	}
	props := map[string]any{"item_id": itemID}
	if b.RequestID != "" {
		props["request_id"] = b.RequestID
	}
	return b.build(EventPaywallViewed, props)
}

// PushOpened builds push_opened: the user opened a push about itemID (the item's pullback_rate).
// notificationType is your own short name for the push (e.g. "weekly_digest"); may be empty.
func PushOpened(b EventBase, itemID, notificationType string) (Event, error) {
	if blank(itemID) {
		return Event{}, newError(CodeInvalidArgument, EventPushOpened, "item id is required")
	}
	props := map[string]any{"item_id": itemID}
	if notificationType != "" {
		props["notification_type"] = notificationType
	}
	return b.build(EventPushOpened, props)
}

// Search is one search: never the query text, only its hash.
type Search struct {
	// QueryHash is a hex hash of the normalised query you compute (e.g. HMAC-SHA256 with a key you
	// hold): 16 to 128 lowercase hex characters, so query text cannot travel here by mistake.
	QueryHash string
	// ResultCount is the number of results shown; nil leaves it out.
	ResultCount *int
	// ClickedItemID is the result the user opened, if any (+0.8, and this session's context).
	ClickedItemID string
}

var queryHashRe = regexp.MustCompile(`^[0-9a-f]{16,128}$`)

// SearchPerformed builds search_performed.
func SearchPerformed(b EventBase, s Search) (Event, error) {
	const op = EventSearchPerformed
	if !queryHashRe.MatchString(s.QueryHash) {
		return Event{}, newError(CodeInvalidArgument, op, "query hash must be 16 to 128 lowercase hex characters (hash the query; never send its text)")
	}
	props := map[string]any{"query_hash": s.QueryHash}
	if s.ResultCount != nil {
		if *s.ResultCount < 0 {
			return Event{}, newError(CodeInvalidArgument, op, "result count is never negative")
		}
		props["result_count"] = *s.ResultCount
	}
	if s.ClickedItemID != "" {
		props["clicked_item_id"] = s.ClickedItemID
	}
	return b.build(op, props)
}

// Engagement is how long and how far the user read or watched an item. It counts as an engaged
// view when EngagedSeconds >= 20 or MaxScrollPercent >= 60.
type Engagement struct {
	ItemID           string
	Kind             ItemKind // empty means document
	EngagedSeconds   int
	MaxScrollPercent int // 0 to 100
}

// ItemEngaged builds item_engaged.
func ItemEngaged(b EventBase, g Engagement) (Event, error) {
	const op = EventItemEngaged
	if blank(g.ItemID) {
		return Event{}, newError(CodeInvalidArgument, op, "item id is required")
	}
	if g.Kind != "" && !g.Kind.Valid() {
		return Event{}, newError(CodeInvalidArgument, op, "unknown kind %q", string(g.Kind))
	}
	if g.EngagedSeconds < 0 || g.MaxScrollPercent < 0 || g.MaxScrollPercent > 100 {
		return Event{}, newError(CodeInvalidArgument, op, "engaged seconds is never negative and max scroll percent is 0 to 100")
	}
	props := map[string]any{"item_id": g.ItemID, "engaged_seconds": g.EngagedSeconds, "max_scroll_percent": g.MaxScrollPercent}
	if g.Kind != "" {
		props["kind"] = string(g.Kind)
	}
	return b.build(op, props)
}

// ItemCompleted builds item_completed: the user finished a workout, a breathwork session, a
// protocol (a strong signal; left out of the user's slates for exclude_consumed_days). kind may
// be empty for a document; collection may be empty. It is a ranking event: sign it (SignEvent,
// or a client with a signing secret) when it does not come from an ingest key.
func ItemCompleted(b EventBase, itemID string, kind ItemKind, collection string) (Event, error) {
	const op = EventItemCompleted
	if blank(itemID) {
		return Event{}, newError(CodeInvalidArgument, op, "item id is required")
	}
	if kind != "" && !kind.Valid() {
		return Event{}, newError(CodeInvalidArgument, op, "unknown kind %q", string(kind))
	}
	props := map[string]any{"item_id": itemID}
	if kind != "" {
		props["kind"] = string(kind)
	}
	if collection != "" {
		props["collection"] = collection
	}
	return b.build(op, props)
}

// Order is a placed order: product catalog ids and the value, never the customer.
type Order struct {
	// OrderIDHash is a hash of your order id (never the order number itself).
	OrderIDHash string
	// ProductIDs are the products' catalog external ids (at least one).
	ProductIDs []string
	// ValueCents is the order value in minor units; nil leaves it out.
	ValueCents *int
	Currency   string // ISO 4217, e.g. EUR
}

// OrderPlaced builds order_placed (+2.0 on each product, the purchase_rate metric and the
// repurchase window). A ranking event: sign it when it does not come from an ingest key.
func OrderPlaced(b EventBase, o Order) (Event, error) {
	const op = EventOrderPlaced
	if blank(o.OrderIDHash) || len(o.ProductIDs) == 0 {
		return Event{}, newError(CodeInvalidArgument, op, "order id hash and at least one product id are required")
	}
	for _, id := range o.ProductIDs {
		if blank(id) {
			return Event{}, newError(CodeInvalidArgument, op, "product ids are never empty")
		}
	}
	props := map[string]any{"order_id_hash": o.OrderIDHash, "product_ids": o.ProductIDs}
	if o.ValueCents != nil {
		if *o.ValueCents < 0 {
			return Event{}, newError(CodeInvalidArgument, op, "value cents is never negative")
		}
		props["value_cents"] = *o.ValueCents
	}
	if o.Currency != "" {
		if !currencyCode.MatchString(o.Currency) {
			return Event{}, newError(CodeInvalidArgument, op, "currency is an ISO 4217 code such as EUR")
		}
		props["currency"] = o.Currency
	}
	return b.build(op, props)
}
