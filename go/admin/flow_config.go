package admin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	patterna "github.com/payam-h/patterna-sdk/go"
)

// Generator kinds (FlowConfig.generators[].kind).
const (
	GenTrending        = "trending"
	GenCurated         = "curated"
	GenItemToItem      = "item_to_item"
	GenContentAffinity = "content_affinity"
	GenPersonaBayes    = "persona_bayes"
	GenUserBayes       = "user_bayes"
	GenSurfaceSpecific = "surface_specific"
	GenSessionItems    = "session_items"
	// GenTimeFit (engine round 5): the item's tap rate in the request's time bucket (hour_bucket and
	// weekday), else a duration heuristic; every mode. Silent: never names a reason.
	GenTimeFit = "time_fit"
	// GenFormatFit (engine round 5): the user's learned format preference against the item's format
	// attribute; personalized only. Silent: never names a reason.
	GenFormatFit = "format_fit"
)

// GeneratorKinds lists every generator kind in the contract.
var GeneratorKinds = []string{
	GenTrending, GenCurated, GenItemToItem, GenContentAffinity, GenPersonaBayes, GenUserBayes,
	GenSurfaceSpecific, GenSessionItems, GenTimeFit, GenFormatFit,
}

// FlowConfig is the typed form of a surface's recipe (contracts/openapi.yaml FlowConfig). The
// flow calls take the config as raw JSON, so keys this SDK does not know survive a round trip;
// build one here and pass it through JSON, or check a raw config with ValidateFlowConfig.
type FlowConfig struct {
	Generators  []GeneratorSpec              `json:"generators"`
	Filters     *FlowFilters                 `json:"filters,omitempty"`
	Ranker      RankerSpec                   `json:"ranker"`
	Exploration ExplorationSpec              `json:"exploration"`
	Reasons     map[string]map[string]string `json:"reasons,omitempty"`
}

// GeneratorSpec is one generator and its weight.
type GeneratorSpec struct {
	Kind   string         `json:"kind"`
	Weight float64        `json:"weight"`
	Params map[string]any `json:"params,omitempty"`
}

// FlowFilters are the hard rules and preferences applied after the generators. A nil field is
// left out, so the engine's default applies.
type FlowFilters struct {
	ExcludeSeenDays *int `json:"exclude_seen_days,omitempty"`
	// ExcludeDismissedDays (engine round 5, default 30, 0 to 365, 0 = off): items this person
	// dismissed or unpinned stay out; relaxed when nothing else is left.
	ExcludeDismissedDays *int `json:"exclude_dismissed_days,omitempty"`
	// ExcludeConsumedDays (default 30): items the user completed stay out; personalized mode.
	ExcludeConsumedDays *int `json:"exclude_consumed_days,omitempty"`
	// PaywallCooldownDays (default 7): an item that showed a free user a paywall stays out.
	PaywallCooldownDays *int `json:"paywall_cooldown_days,omitempty"`
	// RepurchaseDays (default 30): a bought product is not offered again inside this window;
	// the item's own repurchase_days attribute wins.
	RepurchaseDays *int `json:"repurchase_days,omitempty"`
	// HideUnavailable (default true): products with available=false are never served (a hard
	// rule, both arms).
	HideUnavailable *bool `json:"hide_unavailable,omitempty"`
	// RelevanceFloor (0 to 1, default 0 = off): on an anchored request only items at least this
	// similar to the anchor count towards min_slots, so the block hides when nothing is relevant.
	RelevanceFloor   *float64 `json:"relevance_floor,omitempty"`
	Locales          []string `json:"locales,omitempty"`
	PremiumPolicy    string   `json:"premium_policy,omitempty"` // allow | cap | exclude
	PremiumCap       *int     `json:"premium_cap,omitempty"`
	PerCollectionCap *int     `json:"per_collection_cap,omitempty"`
	FreshnessDays    *int     `json:"freshness_days,omitempty"`
	Diversity        *float64 `json:"diversity,omitempty"`    // 0 to 1
	RegionBoost      *float64 `json:"region_boost,omitempty"` // 0 to 1
}

// RankerSpec picks the ranker.
type RankerSpec struct {
	Kind          string `json:"kind"` // linear | learned_linear | gbdt
	PolicyVersion string `json:"policy_version,omitempty"`
	Bandit        *bool  `json:"bandit,omitempty"`
}

// ExplorationSpec sets the exploration.
type ExplorationSpec struct {
	Kind        string   `json:"kind"` // none | plackett_luce
	Temperature *float64 `json:"temperature,omitempty"`
	Share       *float64 `json:"share,omitempty"` // 0 to 0.5
}

// JSON returns the config as the raw JSON the flow calls take, after validating it.
func (f FlowConfig) JSON() (json.RawMessage, error) {
	b, err := json.Marshal(f)
	if err != nil {
		return nil, invalid("flowConfig", err.Error())
	}
	if err := ValidateFlowConfig(b); err != nil {
		return nil, err
	}
	return b, nil
}

var filterKeys = []string{
	"exclude_seen_days", "exclude_dismissed_days", "exclude_consumed_days", "paywall_cooldown_days",
	"repurchase_days", "hide_unavailable", "relevance_floor", "locales", "premium_policy",
	"premium_cap", "per_collection_cap", "freshness_days", "diversity", "region_boost",
}

var placeholderRe = regexp.MustCompile(`\{([^{}]*)\}`)

// ValidateFlowConfig checks a flow config the way the engine does on save, plus unknown filter
// keys (a misspelt filter would otherwise do nothing), so `patterna flow apply` fails in CI
// before anything is sent. The error names the field.
func ValidateFlowConfig(raw json.RawMessage) error {
	const op = "validateFlow"
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return invalid(op, "config must be a JSON object")
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &top); err != nil {
		return invalid(op, "config must be a JSON object: "+err.Error())
	}
	for k := range top {
		switch k {
		case "generators", "filters", "ranker", "exploration", "reasons", "diversity":
		default:
			return invalid(op, "unknown config key "+k+" (generators, filters, ranker, exploration, reasons)")
		}
	}
	if fr, ok := top["filters"]; ok && !isNull(fr) {
		var fm map[string]json.RawMessage
		if err := json.Unmarshal(fr, &fm); err != nil {
			return invalid(op, "filters must be an object")
		}
		for k := range fm {
			if !slices.Contains(filterKeys, k) {
				return invalid(op, "unknown filter "+k+" ("+strings.Join(filterKeys, ", ")+")")
			}
		}
	}
	var c FlowConfig
	if err := json.Unmarshal(trimmed, &c); err != nil {
		return invalid(op, "config does not match the flow model: "+err.Error())
	}
	if len(c.Generators) == 0 {
		return invalid(op, "generators must hold at least one generator")
	}
	for i, g := range c.Generators {
		if !slices.Contains(GeneratorKinds, g.Kind) {
			return invalid(op, fmt.Sprintf("generators[%d]: unknown generator kind %q (%s)", i, g.Kind, strings.Join(GeneratorKinds, ", ")))
		}
	}
	switch c.Ranker.Kind {
	case "", "linear", "learned_linear", "gbdt":
	default:
		return invalid(op, "ranker.kind must be linear, learned_linear or gbdt")
	}
	switch c.Exploration.Kind {
	case "", "none", "plackett_luce":
	default:
		return invalid(op, "exploration.kind must be none or plackett_luce")
	}
	if s := c.Exploration.Share; s != nil && (*s < 0 || *s > 0.5) {
		return invalid(op, "exploration.share must be between 0 and 0.5")
	}
	if f := c.Filters; f != nil {
		for _, r := range []struct {
			name string
			v    *float64
		}{{"diversity", f.Diversity}, {"relevance_floor", f.RelevanceFloor}, {"region_boost", f.RegionBoost}} {
			if r.v != nil && (*r.v < 0 || *r.v > 1) {
				return invalid(op, "filters."+r.name+" must be between 0 and 1")
			}
		}
		for _, d := range []struct {
			name string
			v    *int
		}{{"exclude_dismissed_days", f.ExcludeDismissedDays}, {"exclude_consumed_days", f.ExcludeConsumedDays},
			{"paywall_cooldown_days", f.PaywallCooldownDays}, {"repurchase_days", f.RepurchaseDays}} {
			if d.v != nil && (*d.v < 0 || *d.v > 365) {
				return invalid(op, "filters."+d.name+" must be between 0 and 365 days")
			}
		}
		switch f.PremiumPolicy {
		case "", "allow", "cap", "exclude":
		default:
			return invalid(op, "filters.premium_policy must be allow, cap or exclude")
		}
	}
	return validateReasons(op, c.Reasons)
}

func validateReasons(op string, reasons map[string]map[string]string) error {
	keys := make([]string, 0, len(reasons))
	for k := range reasons {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !slices.Contains(patterna.ReasonKeys, k) && !slices.Contains(GeneratorKinds, k) {
			return invalid(op, "reasons: unknown reason key or generator "+k+" (reason keys: "+strings.Join(patterna.ReasonKeys, ", ")+"; or a generator kind)")
		}
		for loc, t := range reasons[k] {
			if !localeKey(loc) {
				return invalid(op, "reasons."+k+": locale "+loc+" is not a language code such as nl or en")
			}
			if len(t) > 200 {
				return invalid(op, "reasons."+k+"."+loc+" is longer than 200 characters")
			}
			if strings.Count(t, "{") != strings.Count(t, "}") {
				return invalid(op, "reasons."+k+"."+loc+" has an unbalanced brace")
			}
			for _, m := range placeholderRe.FindAllStringSubmatch(t, -1) {
				if m[1] != "anchor_title" && m[1] != "tag" && m[1] != "segment" {
					return invalid(op, "reasons."+k+"."+loc+": unknown placeholder {"+m[1]+"} (allowed: {anchor_title}, {tag}, {segment})")
				}
			}
		}
	}
	return nil
}

func localeKey(l string) bool {
	if len(l) < 2 || len(l) > 3 {
		return false
	}
	for _, c := range l {
		if c < 'a' || c > 'z' {
			return false
		}
	}
	return true
}

func isNull(b json.RawMessage) bool { return string(bytes.TrimSpace(b)) == "null" }
