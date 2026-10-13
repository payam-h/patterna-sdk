package patterna

import (
	"regexp"
	"strings"
)

// Identity-provider subjects ("auth0|...", "google-oauth2|...") and emails are personal data.
// The contract wants a random per-tenant id minted by the customer.
var (
	emailLike  = regexp.MustCompile(`[^\s@]+@[^\s@]+`)
	idpSubject = regexp.MustCompile(`(?i)^[a-z0-9-]+\|`)
	hmacHex    = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// CheckUserID returns an error when id is empty or looks like an email or an identity-provider
// subject. The id itself never appears in the error.
func CheckUserID(op, id string) error {
	if strings.TrimSpace(id) == "" {
		return newError(CodeInvalidArgument, op, "user id is empty")
	}
	if emailLike.MatchString(id) || idpSubject.MatchString(id) {
		return newError(CodeInvalidArgument, op,
			"user id looks like an email or an identity-provider subject; send the random per-tenant id you minted")
	}
	return nil
}

func checkMode(op string, m Mode) error {
	if m == "" {
		return newError(CodeConsentRequired, op, "consent mode is required (personalized, cohort or non_profiled)")
	}
	if !m.Valid() {
		return newError(CodeConsentRequired, op, "unknown consent mode %q", string(m))
	}
	return nil
}

// checkIdentity: in non_profiled mode Patterna must never learn who the user is.
func checkIdentity(op string, m Mode, userID, sessionID string) error {
	if m == ModeNonProfiled {
		if userID != "" {
			return newError(CodeConsentViolation, op, "non_profiled requests must not carry a user id")
		}
		if sessionID == "" {
			return newError(CodeConsentViolation, op, "non_profiled requests need a rotating session id")
		}
		return nil
	}
	if userID != "" {
		return CheckUserID(op, userID)
	}
	return nil
}

func validateRecommend(req RecommendRequest) error {
	const op = "recommend"
	if err := checkMode(op, req.Mode); err != nil {
		return err
	}
	if err := checkIdentity(op, req.Mode, req.UserID, req.SessionID); err != nil {
		return err
	}
	if req.Surface == "" {
		return newError(CodeInvalidArgument, op, "surface is required")
	}
	if req.Context.Locale == "" {
		return newError(CodeInvalidArgument, op, "context.locale is required")
	}
	if len(req.ExcludeItemIDs) > maxExcludes {
		return newError(CodeInvalidArgument, op, "exclude_item_ids is limited to %d", maxExcludes)
	}
	if len(req.Context.ControlItemIDs) > maxControlItems {
		return newError(CodeInvalidArgument, op, "context.control_item_ids is limited to %d", maxControlItems)
	}
	if len(req.Context.MemberOf) > maxMemberOf {
		return newError(CodeInvalidArgument, op, "context.member_of is limited to %d", maxMemberOf)
	}
	if err := validateRecommendContext(op, req.Context); err != nil {
		return err
	}
	if req.AssignmentUnit != "" && !hmacHex.MatchString(req.AssignmentUnit) {
		return newError(CodeInvalidArgument, op,
			"assignment_unit must be AssignmentUnit(secret, userID): 64 lowercase hex characters, never a raw id")
	}
	return nil
}

func validateEvent(op string, e Event) error {
	if e.EventID == "" || e.Type == "" || e.Source == "" || e.OccurredAt.IsZero() {
		return newError(CodeInvalidArgument, op, "event_id, type, source and occurred_at are required")
	}
	if c := e.Context; c != nil {
		if err := validateCoarseContext(op, "event context", c.HourBucket, c.Weekday, c.TenureDays, c.SubscriptionTier, c.Region); err != nil {
			return err
		}
	}
	if e.UserID != "" {
		return CheckUserID(op, e.UserID)
	}
	return nil
}
