package patterna

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Round-5 signals (engine round 5, API 0.3.0, docs/SIGNALS.md): the coarse request context, the
// editorial item attributes the engine reads and the reason keys a slot may carry.

// Contract limits of the coarse context.
const (
	maxSessionItems     = 20
	maxSessionItemLen   = 200
	maxRegionLen        = 64
	maxSubscriptionTier = 32
)

// Subscription tiers the engine knows by name; any other tier name of at most 32 characters is
// accepted as tenant-defined. For TierFree the premium policy and the paywall cooldown apply.
const (
	TierFree    = "free"
	TierPremium = "premium"
)

// Ptr returns a pointer to v, for the optional fields (HourBucket, Weekday, TenureDays and the
// numeric item attributes).
func Ptr[T any](v T) *T { return &v }

// LocalTime returns the hour (0 to 23) and the contract weekday (0 = Monday ... 6 = Sunday) of t
// in loc, the user's timezone. A nil loc keeps t's own location. Use it to fill hour_bucket and
// weekday, which select the time bucket the time_fit generator reads.
func LocalTime(t time.Time, loc *time.Location) (hourBucket, weekday int) {
	if loc != nil {
		t = t.In(loc)
	}
	return t.Hour(), (int(t.Weekday()) + 6) % 7
}

// SetLocalTime sets HourBucket and Weekday from t in the user's timezone (see LocalTime).
func (rc *RecommendContext) SetLocalTime(t time.Time, loc *time.Location) {
	h, w := LocalTime(t, loc)
	rc.HourBucket, rc.Weekday = &h, &w
}

// SetLocalTime sets HourBucket and Weekday from t in the user's timezone (see LocalTime).
func (ec *EventContext) SetLocalTime(t time.Time, loc *time.Location) {
	h, w := LocalTime(t, loc)
	ec.HourBucket, ec.Weekday = &h, &w
}

func validateCoarseContext(op, prefix string, hour, weekday, tenure *int, tier, region string) error {
	if hour != nil && (*hour < 0 || *hour > 23) {
		return newError(CodeInvalidArgument, op, "%s.hour_bucket is an hour from 0 to 23", prefix)
	}
	if weekday != nil && (*weekday < 0 || *weekday > 6) {
		return newError(CodeInvalidArgument, op, "%s.weekday is 0 (Monday) to 6 (Sunday)", prefix)
	}
	if tenure != nil && *tenure < 0 {
		return newError(CodeInvalidArgument, op, "%s.tenure_days is zero or more", prefix)
	}
	if len(strings.TrimSpace(tier)) > maxSubscriptionTier {
		return newError(CodeInvalidArgument, op, "%s.subscription_tier is a tier name of at most %d characters", prefix, maxSubscriptionTier)
	}
	if len(strings.TrimSpace(region)) > maxRegionLen {
		return newError(CodeInvalidArgument, op, "%s.region is a region name of at most %d characters", prefix, maxRegionLen)
	}
	return nil
}

func validateRecommendContext(op string, rc RecommendContext) error {
	if err := validateCoarseContext(op, "context", rc.HourBucket, rc.Weekday, rc.TenureDays, rc.SubscriptionTier, rc.Region); err != nil {
		return err
	}
	if len(rc.SessionItems) > maxSessionItems {
		return newError(CodeInvalidArgument, op, "context.session_items is limited to %d", maxSessionItems)
	}
	for _, id := range rc.SessionItems {
		if id == "" || len(id) > maxSessionItemLen {
			return newError(CodeInvalidArgument, op, "context.session_items holds item ids of 1 to %d characters", maxSessionItemLen)
		}
	}
	return nil
}

// ItemFormat is the editorial format of an item (attribute format).
type ItemFormat string

const (
	FormatText    ItemFormat = "text"
	FormatVideo   ItemFormat = "video"
	FormatAudio   ItemFormat = "audio"
	FormatRecipe  ItemFormat = "recipe"
	FormatWorkout ItemFormat = "workout"
	FormatTool    ItemFormat = "tool"
	FormatProduct ItemFormat = "product"
)

// Difficulty is the editorial difficulty of an item (attribute difficulty).
type Difficulty string

const (
	DifficultyEasy   Difficulty = "easy"
	DifficultyMedium Difficulty = "medium"
	DifficultyHard   Difficulty = "hard"
)

// ItemAttributes are the typed editorial attributes of a catalog item. Set ItemUpsert.Editorial
// and they are written into the upsert's attributes (a typed field wins over the same key in
// ItemUpsert.Attributes); every field is optional and a zero value is left out. The engine reads
// the first group (docs/SIGNALS.md §2); the panel shows the second.
type ItemAttributes struct {
	// Format feeds the format_fit generator and a ranker feature.
	Format ItemFormat
	// DurationMin (videos, audio, workouts) or ReadingTimeMin (text), in minutes: a ranker feature
	// and the time_fit heuristic (short items fit the day, long ones the evening).
	DurationMin    *float64
	ReadingTimeMin *float64
	Difficulty     Difficulty
	// Author is an author id or name, as you keep it (the author's tap rate is a ranker feature).
	Author string
	// Available (products): false is never served while the flow's hide_unavailable is on.
	Available *bool
	// ExpiresAt: never served after it. StartsAt (events): never served after it, boosted in the
	// 7 days before.
	ExpiresAt *time.Time
	StartsAt  *time.Time
	// RepurchaseDays (products): a buyer is not offered it again inside this window; it wins over
	// the flow's filters.repurchase_days. Consumables set a small number.
	RepurchaseDays *int
	// Region is one of the tenant's regions. The engine reads the upsert's top-level region, so the
	// SDK sends it there (ItemUpsert.Region); setting both to different names is an error.
	Region string
	// PhaseFit is only returned when the tenant's phase_tags setting is on; it never ranks.
	PhaseFit []string

	// Kept for the panel's product cards.
	PriceCents     *int
	CompareAtCents *int
	Currency       string // ISO 4217, e.g. EUR
	ImageURL       string
	URL            string
	ProductType    string
	// Categories are folded into tags at ingest.
	Categories []string
}

var currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)

// Validate checks the attributes against the contract before anything is sent.
func (a *ItemAttributes) Validate() error { return a.validate("upsertItems", "") }

func (a *ItemAttributes) validate(op, prefix string) error {
	if a == nil {
		return nil
	}
	switch a.Format {
	case "", FormatText, FormatVideo, FormatAudio, FormatRecipe, FormatWorkout, FormatTool, FormatProduct:
	default:
		return newError(CodeInvalidArgument, op, "%sattributes.format must be text, video, audio, recipe, workout, tool or product", prefix)
	}
	switch a.Difficulty {
	case "", DifficultyEasy, DifficultyMedium, DifficultyHard:
	default:
		return newError(CodeInvalidArgument, op, "%sattributes.difficulty must be easy, medium or hard", prefix)
	}
	for name, v := range map[string]*float64{"duration_min": a.DurationMin, "reading_time_min": a.ReadingTimeMin} {
		if v != nil && *v < 0 {
			return newError(CodeInvalidArgument, op, "%sattributes.%s is never negative", prefix, name)
		}
	}
	for name, v := range map[string]*int{"repurchase_days": a.RepurchaseDays, "price_cents": a.PriceCents, "compare_at_cents": a.CompareAtCents} {
		if v != nil && *v < 0 {
			return newError(CodeInvalidArgument, op, "%sattributes.%s is never negative", prefix, name)
		}
	}
	if a.Currency != "" && !currencyCode.MatchString(a.Currency) {
		return newError(CodeInvalidArgument, op, "%sattributes.currency is an ISO 4217 code such as EUR", prefix)
	}
	if len(a.Region) > maxRegionLen {
		return newError(CodeInvalidArgument, op, "%sregion is a region name of at most %d characters", prefix, maxRegionLen)
	}
	return nil
}

// Map returns the attributes as the upsert's attributes object (Region excluded: it travels as
// the top-level region). Times are RFC 3339 in UTC.
func (a *ItemAttributes) Map() map[string]any {
	m := map[string]any{}
	if a == nil {
		return m
	}
	str := func(k, v string) {
		if v != "" {
			m[k] = v
		}
	}
	str("format", string(a.Format))
	str("difficulty", string(a.Difficulty))
	str("author", a.Author)
	str("currency", a.Currency)
	str("image_url", a.ImageURL)
	str("url", a.URL)
	str("product_type", a.ProductType)
	if a.DurationMin != nil {
		m["duration_min"] = *a.DurationMin
	}
	if a.ReadingTimeMin != nil {
		m["reading_time_min"] = *a.ReadingTimeMin
	}
	if a.Available != nil {
		m["available"] = *a.Available
	}
	if a.ExpiresAt != nil {
		m["expires_at"] = a.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if a.StartsAt != nil {
		m["starts_at"] = a.StartsAt.UTC().Format(time.RFC3339)
	}
	if a.RepurchaseDays != nil {
		m["repurchase_days"] = *a.RepurchaseDays
	}
	if a.PriceCents != nil {
		m["price_cents"] = *a.PriceCents
	}
	if a.CompareAtCents != nil {
		m["compare_at_cents"] = *a.CompareAtCents
	}
	if len(a.PhaseFit) > 0 {
		m["phase_fit"] = a.PhaseFit
	}
	if len(a.Categories) > 0 {
		m["categories"] = a.Categories
	}
	return m
}

// MarshalJSON writes Editorial into attributes and its Region into the top-level region.
func (u ItemUpsert) MarshalJSON() ([]byte, error) {
	type plain ItemUpsert
	p := plain(u)
	if u.Editorial != nil {
		merged := make(map[string]any, len(u.Attributes)+8)
		for k, v := range u.Attributes {
			merged[k] = v
		}
		for k, v := range u.Editorial.Map() {
			merged[k] = v
		}
		if len(merged) > 0 {
			p.Attributes = merged
		}
		if p.Region == "" {
			p.Region = u.Editorial.Region
		}
	}
	return json.Marshal(p)
}

func validateItem(op string, i int, it ItemUpsert) error {
	prefix := fmt.Sprintf("item %d: ", i)
	if err := it.Editorial.validate(op, prefix); err != nil {
		return err
	}
	if len(it.Region) > maxRegionLen {
		return newError(CodeInvalidArgument, op, "item %d: region is a region name of at most %d characters", i, maxRegionLen)
	}
	if it.Editorial != nil && it.Editorial.Region != "" && it.Region != "" && it.Editorial.Region != it.Region {
		return newError(CodeInvalidArgument, op, "item %d: Region and Editorial.Region name different regions", i)
	}
	return nil
}

// Reason keys a slot may carry (SlateItem.ReasonKey). The set may grow; treat an unknown key as
// ReasonRecommended.
const (
	ReasonSimilarToRead = "similar_to_read"
	ReasonSimilar       = "similar"
	ReasonPairsWith     = "pairs_with"
	ReasonMatchesGoals  = "matches_goals"
	ReasonMoreAboutTag  = "more_about_tag"
	ReasonPeopleLikeYou = "people_like_you"
	ReasonTrending      = "trending"
	ReasonEditorsPick   = "editors_pick"
	ReasonNew           = "new"
	ReasonPopular       = "popular"
	// ReasonRecommended (engine round 5): nothing true could be said about the slot (the winning
	// signal was not positive, or it was a silent generator such as time_fit); the text is a
	// plain "Recommended".
	ReasonRecommended = "recommended"
)

// ReasonKeys lists every reason key in the contract, in the contract's order.
var ReasonKeys = []string{
	ReasonSimilarToRead, ReasonSimilar, ReasonPairsWith, ReasonMatchesGoals, ReasonMoreAboutTag,
	ReasonPeopleLikeYou, ReasonTrending, ReasonEditorsPick, ReasonNew, ReasonPopular, ReasonRecommended,
}
