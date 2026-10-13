package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strconv"

	patterna "github.com/payam-h/patterna-sdk/go"
)

// Fallback kinds of a surface's editorial list.
const (
	FallbackNewest  = "newest"
	FallbackPopular = "popular"
	FallbackCurated = "curated"
)

// SurfaceFallback is a surface's editorial list: the control arm, the rollout gate, the safety
// net and the fill for short slates. Kind is newest, popular (7-day) or curated (ItemIDs, in
// order). ByAnchor (engine 0.3) maps an anchor's external id to the editor's list for that page,
// used when a request's anchor_item_id is a key; a request's context.control_item_ids wins over
// both.
type SurfaceFallback struct {
	Kind     string              `json:"kind"`
	ItemIDs  []string            `json:"item_ids,omitempty"`
	ByAnchor map[string][]string `json:"by_anchor,omitempty"`
}

// SurfaceSpec is the part of a surface a customer keeps in its repository and applies with
// ApplySurface (`patterna surface apply -f`). Nil and empty fields are left as they are. Status
// and testers are not part of it: they move with SetSurfaceStatus and SetSurfaceTesters.
type SurfaceSpec struct {
	Surface   string           `json:"surface"`
	ItemKinds []string         `json:"item_kinds,omitempty"`
	Slots     *int             `json:"slots,omitempty"`
	Fallback  *SurfaceFallback `json:"fallback,omitempty"`
	// MinSlots (engine 0.3): below this many eligible items the answer is shown=false with no
	// items, counted in its arm with zero taps. 0..50; 0 = always shown.
	MinSlots *int `json:"min_slots,omitempty"`
	// DeviceResort (engine 0.3): the app re-sorts the slate on the device; every interaction must
	// then carry displayed_position.
	DeviceResort *bool    `json:"device_resort,omitempty"`
	Notes        *string  `json:"notes,omitempty"`
	Exploration  *float64 `json:"exploration_share,omitempty"`
}

// Validate checks the spec the way the engine will, so a CI run fails before anything is sent.
func (s SurfaceSpec) Validate() error {
	const op = "applySurface"
	if s.Surface == "" {
		return invalid(op, "surface key is required")
	}
	if s.Slots != nil && (*s.Slots < 1 || *s.Slots > 50) {
		return invalid(op, "slots must be 1 to 50")
	}
	if s.MinSlots != nil && (*s.MinSlots < 0 || *s.MinSlots > 50) {
		return invalid(op, "min_slots must be 0 to 50")
	}
	if s.Exploration != nil && (*s.Exploration < 0 || *s.Exploration > 1) {
		return invalid(op, "exploration_share must be 0 to 1")
	}
	if s.Notes != nil && len(*s.Notes) > 4000 {
		return invalid(op, "notes holds at most 4000 characters")
	}
	for _, k := range s.ItemKinds {
		if !patterna.ItemKind(k).Valid() {
			return invalid(op, "unknown item kind "+strconv.Quote(k))
		}
	}
	if f := s.Fallback; f != nil {
		if !slices.Contains([]string{FallbackNewest, FallbackPopular, FallbackCurated}, f.Kind) {
			return invalid(op, "fallback.kind must be newest, popular or curated")
		}
		if f.Kind == FallbackCurated && len(f.ItemIDs) == 0 {
			return invalid(op, "a curated fallback needs item_ids")
		}
		for anchor, ids := range f.ByAnchor {
			if anchor == "" || len(ids) == 0 {
				return invalid(op, "fallback.by_anchor maps an anchor id to a non-empty list")
			}
		}
	}
	return nil
}

// body is the PATCH body for the fields the spec sets.
func (s SurfaceSpec) body() map[string]any {
	b := map[string]any{}
	if len(s.ItemKinds) > 0 {
		b["item_kinds"] = s.ItemKinds
	}
	if s.Slots != nil {
		b["slots"] = *s.Slots
	}
	if s.Fallback != nil {
		b["fallback"] = s.Fallback
	}
	if s.MinSlots != nil {
		b["min_slots"] = *s.MinSlots
	}
	if s.DeviceResort != nil {
		b["device_resort"] = *s.DeviceResort
	}
	if s.Notes != nil {
		b["notes"] = *s.Notes
	}
	if s.Exploration != nil {
		b["exploration_share"] = *s.Exploration
	}
	return b
}

// UpdateSurface (PATCH /surfaces/{key}) changes the fields spec sets and returns the surface
// after the change. spec.Surface is ignored; key names the surface.
func (c *Client) UpdateSurface(ctx context.Context, key string, spec SurfaceSpec, opts ...patterna.CallOption) (*Surface, error) {
	spec.Surface = key
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	b := spec.body()
	if len(b) == 0 {
		return nil, invalid("updateSurface", "nothing to change")
	}
	return c.patchSurface(ctx, "updateSurface", key, b, opts)
}

// SurfacePlan says what ApplySurface did or would do.
type SurfacePlan struct {
	Surface string   `json:"surface"`
	Changes []string `json:"changes"` // the fields that differ, in a fixed order; empty when unchanged
	Applied bool     `json:"applied"`
}

// Unchanged reports whether the surface already matched the spec.
func (p *SurfacePlan) Unchanged() bool { return len(p.Changes) == 0 }

// PlanSurface compares spec with the surface as it is and lists the fields that differ. Nothing
// is written.
func (c *Client) PlanSurface(ctx context.Context, spec SurfaceSpec, opts ...patterna.CallOption) (*SurfacePlan, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	cur, err := c.GetSurface(ctx, spec.Surface, opts...)
	if err != nil {
		return nil, err
	}
	curFallback, err := cur.FallbackConfig()
	if err != nil {
		return nil, &patterna.Error{Code: patterna.CodeBadResponse, Op: "planSurface", Message: "surface fallback is not an object", Err: err}
	}
	plan := &SurfacePlan{Surface: spec.Surface, Changes: []string{}}
	add := func(field string, differs bool) {
		if differs {
			plan.Changes = append(plan.Changes, field)
		}
	}
	add("item_kinds", len(spec.ItemKinds) > 0 && !sameSet(spec.ItemKinds, cur.ItemKinds))
	add("slots", spec.Slots != nil && *spec.Slots != cur.Slots)
	add("fallback", spec.Fallback != nil && !sameFallback(spec.Fallback, curFallback))
	add("min_slots", spec.MinSlots != nil && *spec.MinSlots != cur.MinSlots)
	add("device_resort", spec.DeviceResort != nil && *spec.DeviceResort != cur.DeviceResort)
	add("notes", spec.Notes != nil && *spec.Notes != cur.Notes)
	add("exploration_share", spec.Exploration != nil && (cur.ExplorationShare == nil || *spec.Exploration != *cur.ExplorationShare))
	return plan, nil
}

// ApplySurface makes the surface match spec, the way `patterna surface apply` does in CI: it
// patches only the fields that differ, and nothing when none do. The fallback is replaced as a
// whole (kind, item_ids and by_anchor together), so keep the full list in the file.
func (c *Client) ApplySurface(ctx context.Context, spec SurfaceSpec, opts ...patterna.CallOption) (*SurfacePlan, error) {
	plan, err := c.PlanSurface(ctx, spec, opts...)
	if err != nil || plan.Unchanged() {
		return plan, err
	}
	all := spec.body()
	b := make(map[string]any, len(plan.Changes))
	for _, f := range plan.Changes {
		b[f] = all[f]
	}
	if _, err := c.patchSurface(ctx, "applySurface", spec.Surface, b, opts); err != nil {
		return plan, err
	}
	plan.Applied = true
	return plan, nil
}

func sameSet(a, b []string) bool {
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(slices.Compact(x), slices.Compact(y))
}

func sameFallback(want, got *SurfaceFallback) bool {
	if got == nil {
		return false
	}
	norm := func(f *SurfaceFallback) SurfaceFallback {
		n := SurfaceFallback{Kind: f.Kind, ItemIDs: f.ItemIDs, ByAnchor: f.ByAnchor}
		if len(n.ItemIDs) == 0 {
			n.ItemIDs = nil
		}
		if len(n.ByAnchor) == 0 {
			n.ByAnchor = nil
		}
		return n
	}
	return reflect.DeepEqual(norm(want), norm(got))
}

// ---- surface metrics --------------------------------------------------------------------------

// MetricsQuery selects the days of SurfaceMetrics. Zero values use the engine's defaults.
type MetricsQuery struct {
	Days  int    // 1..180; default 28
	Block string // only slates seen in this block, and taps in it
}

// MetricsDay is one UTC day of a surface. Rates are nil when their denominator is 0.
type MetricsDay struct {
	Date          string   `json:"date"`
	Impressions   int      `json:"impressions"`
	Taps          int      `json:"taps"`
	TapRate       *float64 `json:"tap_rate"`
	FallbackRate  *float64 `json:"fallback_rate"` // safety-net fallbacks plus abandoned slates, over slates shown
	P99MS         *float64 `json:"p99_ms"`
	ExploredShare *float64 `json:"explored_share"`
	// UnmeasuredShare: slates requested with context.measured=false.
	UnmeasuredShare *float64 `json:"unmeasured_share"`
	// ClientAbandonedRate (engine 0.3): slates the client abandoned, over slates shown.
	ClientAbandonedRate *float64 `json:"client_abandoned_rate"`
	// NotShownShare (engine 0.3): slates answered shown=false over all slates (in the ledger with
	// zero taps, not in the other rates).
	NotShownShare *float64 `json:"not_shown_share"`
	Block         string   `json:"block,omitempty"`
}

// SurfaceMetrics returns a surface's daily metrics (UTC days, zero-filled).
func (c *Client) SurfaceMetrics(ctx context.Context, key string, q MetricsQuery, opts ...patterna.CallOption) ([]MetricsDay, error) {
	const op = "surfaceMetrics"
	if key == "" {
		return nil, invalid(op, "surface key is required")
	}
	if q.Days < 0 || q.Days > 180 {
		return nil, invalid(op, "days must be 1 to 180")
	}
	if err := checkBlock(op, q.Block); err != nil {
		return nil, err
	}
	v := url.Values{}
	if q.Days > 0 {
		v.Set("days", strconv.Itoa(q.Days))
	}
	if q.Block != "" {
		v.Set("block", q.Block)
	}
	var out struct {
		Days []MetricsDay `json:"days"`
	}
	err := c.call(ctx, patterna.AdminRequest{Op: op, Method: http.MethodGet, Path: surfacePath(key) + "/metrics", Query: v, Idempotent: true}, &out, opts)
	return out.Days, err
}

// ---- settings and health ----------------------------------------------------------------------

// Settings is the part of the tenant settings this package reads. Raw keeps the whole answer.
type Settings struct {
	Slug          string `json:"slug"`
	Name          string `json:"name"`
	DefaultMode   string `json:"default_mode"`
	TwoPersonRule bool   `json:"two_person_rule"`
	// PhaseTags (engine 0.3): slates carry phase_fit item metadata. Off by default.
	PhaseTags bool            `json:"phase_tags"`
	Paused    bool            `json:"paused"`
	Raw       json.RawMessage `json:"-"`
}

// GetSettings returns the tenant settings.
func (c *Client) GetSettings(ctx context.Context, opts ...patterna.CallOption) (*Settings, error) {
	var raw json.RawMessage
	if err := c.call(ctx, patterna.AdminRequest{Op: "getSettings", Method: http.MethodGet, Path: "/settings", Idempotent: true}, &raw, opts); err != nil {
		return nil, err
	}
	var s Settings
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, &patterna.Error{Code: patterna.CodeBadResponse, Op: "getSettings", Message: "settings are not an object", Err: err}
	}
	s.Raw = raw
	return &s, nil
}

// SettingsPatch changes tenant settings (PATCH /settings, owners and admin keys). Nil fields are
// left as they are.
type SettingsPatch struct {
	// PhaseTags (engine 0.3) turns phase_fit item metadata on in slates and the importer.
	PhaseTags *bool `json:"phase_tags,omitempty"`
}

// UpdateSettings applies p.
func (c *Client) UpdateSettings(ctx context.Context, p SettingsPatch, opts ...patterna.CallOption) error {
	if p.PhaseTags == nil {
		return invalid("updateSettings", "nothing to change")
	}
	return c.call(ctx, patterna.AdminRequest{Op: "updateSettings", Method: http.MethodPatch, Path: "/settings", Body: p, Idempotent: true}, nil, opts)
}

// SetPhaseTags turns phase_fit on or off for the tenant (engine 0.3; off by default).
func (c *Client) SetPhaseTags(ctx context.Context, on bool, opts ...patterna.CallOption) error {
	return c.UpdateSettings(ctx, SettingsPatch{PhaseTags: &on}, opts...)
}

// Health is GET /health. EngineVersion (engine 0.3) is the API version the engine serves (the
// contract's info.version, e.g. "0.2.0"); Version is the release (commit).
type Health struct {
	Status        string `json:"status"`
	Postgres      string `json:"postgres,omitempty"`
	Redis         string `json:"redis,omitempty"`
	Version       string `json:"version,omitempty"`
	EngineVersion string `json:"engine_version,omitempty"`
}

// Health returns the engine's liveness, release and engine_version.
func (c *Client) Health(ctx context.Context, opts ...patterna.CallOption) (*Health, error) {
	var out Health
	if err := c.call(ctx, patterna.AdminRequest{Op: "health", Method: http.MethodGet, Path: "/health", Idempotent: true}, &out, opts); err != nil {
		return nil, err
	}
	return &out, nil
}
