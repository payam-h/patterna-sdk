package patterna

import (
	"strings"
	"time"
)

// Event types for products an AI coach answer named (engine 0.3, contracts/events.md).
const (
	EventCoachProductShown  = "coach_product_shown"
	EventCoachProductTapped = "coach_product_tapped"
)

// CoachProduct describes one product an LLM answer named and the app showed under the answer.
// There is no slate and no propensity: the events are a learning signal and a descriptive count,
// never in the ledger.
type CoachProduct struct {
	// Mode is the user's consent mode; it decides the identity the event carries.
	Mode Mode
	// UserID (personalized and cohort) is your random per-tenant id; SessionID (non_profiled) is
	// the rotating session id the app sends with its recommendation requests. A non_profiled event
	// carries the session id only, so it never joins a profile.
	UserID    string
	SessionID string
	// ProductID is the product's catalog external_id.
	ProductID string
	// ConversationID is your own opaque id for the conversation, never message text.
	ConversationID string
	// Position is the product's 0-based place among the answer's products.
	Position int
	// OccurredAt defaults to now; EventID to a fresh UUID; Source to "backend".
	OccurredAt time.Time
	EventID    string
	Source     string
	Context    *EventContext
}

// CoachProductShown builds a coach_product_shown event for IngestEvents. It applies the consent
// rules (user id only in personalized and cohort, session id only in non_profiled) and refuses
// a missing product or conversation id or a negative position.
func CoachProductShown(p CoachProduct) (Event, error) {
	return coachEvent(EventCoachProductShown, p)
}

// CoachProductTapped builds a coach_product_tapped event (a positive signal): the user tapped a
// product shown under an answer. Same rules as CoachProductShown.
func CoachProductTapped(p CoachProduct) (Event, error) {
	return coachEvent(EventCoachProductTapped, p)
}

func coachEvent(typ string, p CoachProduct) (Event, error) {
	op := typ
	if err := checkMode(op, p.Mode); err != nil {
		return Event{}, err
	}
	if err := checkIdentity(op, p.Mode, p.UserID, p.SessionID); err != nil {
		return Event{}, err
	}
	if p.Mode != ModeNonProfiled && p.UserID == "" {
		return Event{}, newError(CodeInvalidArgument, op, "%s events need the user id", p.Mode)
	}
	if strings.TrimSpace(p.ProductID) == "" || strings.TrimSpace(p.ConversationID) == "" {
		return Event{}, newError(CodeInvalidArgument, op, "product id and conversation id are required")
	}
	if p.Position < 0 {
		return Event{}, newError(CodeInvalidArgument, op, "position is 0-based and never negative")
	}
	e := Event{
		EventID: p.EventID, Type: typ, OccurredAt: p.OccurredAt, Source: p.Source, Context: p.Context,
		Props: map[string]any{"product_id": p.ProductID, "conversation_id": p.ConversationID, "position": p.Position},
	}
	if p.Mode == ModeNonProfiled {
		e.SessionID = p.SessionID
	} else {
		e.UserID = p.UserID
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
	return e, nil
}
