package admin

import (
	"encoding/json"
	"strings"
	"testing"

	patterna "github.com/payam-h/patterna-sdk/go"
)

// The round-5 flow: the two silent generators and every new filter, as SIGNALS.md §6 shows it.
const cfgRound5 = `{"generators":[{"kind":"item_to_item","weight":0.5},{"kind":"session_items","weight":0.3},
{"kind":"time_fit","weight":0.15},{"kind":"format_fit","weight":0.15},{"kind":"trending","weight":0.2}],
"filters":{"diversity":0.2,"exclude_dismissed_days":30,"exclude_consumed_days":30,"paywall_cooldown_days":7,
"repurchase_days":45,"hide_unavailable":false,"relevance_floor":0.35,"region_boost":0.3},
"ranker":{"kind":"gbdt","bandit":false},"exploration":{"kind":"plackett_luce","share":0.2},
"reasons":{"recommended":{"nl":"Voor jou","en":"For you"},"time_fit":{"en":"Fits the evening"}}}`

func TestFlowConfigRound5(t *testing.T) {
	if err := ValidateFlowConfig(json.RawMessage(cfgRound5)); err != nil {
		t.Fatal(err)
	}
	var c FlowConfig
	if err := json.Unmarshal([]byte(cfgRound5), &c); err != nil {
		t.Fatal(err)
	}
	f := c.Filters
	if *f.ExcludeDismissedDays != 30 || *f.ExcludeConsumedDays != 30 || *f.PaywallCooldownDays != 7 || *f.RepurchaseDays != 45 ||
		*f.HideUnavailable || *f.RelevanceFloor != 0.35 || c.Generators[2].Kind != GenTimeFit || c.Generators[3].Kind != GenFormatFit {
		t.Fatalf("decoded %+v %+v", c, *f)
	}
	// typed config out: only what is set, under the contract names
	raw, err := FlowConfig{
		Generators:  []GeneratorSpec{{Kind: GenTimeFit, Weight: 0.2}, {Kind: GenFormatFit, Weight: 0.1}},
		Filters:     &FlowFilters{ExcludeDismissedDays: ptr(0), HideUnavailable: ptr(true), RelevanceFloor: ptr(0.5), PaywallCooldownDays: ptr(14)},
		Ranker:      RankerSpec{Kind: "linear"},
		Exploration: ExplorationSpec{Kind: "none"},
		Reasons:     map[string]map[string]string{patterna.ReasonRecommended: {"nl": "Aanbevolen"}},
	}.JSON()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"generators":[{"kind":"time_fit","weight":0.2},{"kind":"format_fit","weight":0.1}],"filters":{"exclude_dismissed_days":0,"paywall_cooldown_days":14,"hide_unavailable":true,"relevance_floor":0.5},"ranker":{"kind":"linear"},"exploration":{"kind":"none"},"reasons":{"recommended":{"nl":"Aanbevolen"}}}`
	if string(raw) != want {
		t.Fatalf("JSON = %s\nwant %s", raw, want)
	}
	if _, err := (FlowConfig{Ranker: RankerSpec{Kind: "linear"}}).JSON(); err == nil {
		t.Fatal("a config without generators must not pass")
	}
}

func TestValidateFlowConfigRejects(t *testing.T) {
	base := func(filters string) string {
		return `{"generators":[{"kind":"time_fit","weight":1}],"ranker":{"kind":"linear"},"exploration":{"kind":"none"},"filters":` + filters + `}`
	}
	cases := []struct{ cfg, msg string }{
		{`[]`, "JSON object"},
		{`{"generators":[],"ranker":{"kind":"linear"}}`, "at least one generator"},
		{`{"generators":[{"kind":"time_fits","weight":1}]}`, "unknown generator kind \"time_fits\""},
		{`{"generators":[{"kind":"time_fit","weight":1}],"ranker":{"kind":"lambdamart"}}`, "ranker.kind"},
		{`{"generators":[{"kind":"time_fit","weight":1}],"exploration":{"kind":"thompson"}}`, "exploration.kind"},
		{`{"generators":[{"kind":"time_fit","weight":1}],"exploration":{"kind":"plackett_luce","share":0.6}}`, "exploration.share"},
		{`{"generators":[{"kind":"time_fit","weight":1}],"rankers":{}}`, "unknown config key rankers"},
		{base(`{"exclude_dismissed_days":-1}`), "filters.exclude_dismissed_days"},
		{base(`{"exclude_dismissed_days":366}`), "filters.exclude_dismissed_days"},
		{base(`{"exclude_consumed_days":400}`), "filters.exclude_consumed_days"},
		{base(`{"paywall_cooldown_days":-7}`), "filters.paywall_cooldown_days"},
		{base(`{"repurchase_days":1000}`), "filters.repurchase_days"},
		{base(`{"relevance_floor":1.5}`), "filters.relevance_floor"},
		{base(`{"relevance_floor":-0.1}`), "filters.relevance_floor"},
		{base(`{"region_boost":2}`), "filters.region_boost"},
		{base(`{"diversity":-1}`), "filters.diversity"},
		{base(`{"hide_unavailable":"no"}`), "flow model"},
		{base(`{"exclude_dismissed_days":7.5}`), "flow model"},
		{base(`{"premium_policy":"hide"}`), "filters.premium_policy"},
		{base(`{"exclude_dismised_days":30}`), "unknown filter exclude_dismised_days"},
		{`{"generators":[{"kind":"time_fit","weight":1}],"reasons":{"recomended":{"nl":"x"}}}`, "unknown reason key or generator recomended"},
		{`{"generators":[{"kind":"time_fit","weight":1}],"reasons":{"recommended":{"NL":"x"}}}`, "locale NL"},
		{`{"generators":[{"kind":"time_fit","weight":1}],"reasons":{"recommended":{"nl":"{user}"}}}`, "unknown placeholder {user}"},
		{`{"generators":[{"kind":"time_fit","weight":1}],"reasons":{"recommended":{"nl":"{tag"}}}`, "unbalanced brace"},
		{`{"generators":[{"kind":"time_fit","weight":1}],"reasons":{"recommended":{"nl":"` + strings.Repeat("x", 201) + `"}}}`, "longer than 200"},
	}
	for _, tc := range cases {
		err := ValidateFlowConfig(json.RawMessage(tc.cfg))
		var pe *patterna.Error
		if err == nil || !strings.Contains(err.Error(), tc.msg) || !asErr(err, &pe) || pe.Code != patterna.CodeInvalidArgument {
			t.Errorf("%s: err = %v, want %q", tc.cfg, err, tc.msg)
		}
	}
	// the edges and the legacy top-level diversity pass
	for _, cfg := range []string{
		base(`{"exclude_dismissed_days":0,"exclude_consumed_days":365,"paywall_cooldown_days":0,"repurchase_days":365,"relevance_floor":1,"hide_unavailable":true,"freshness_days":null}`),
		`{"generators":[{"kind":"curated","weight":1}],"diversity":{"mmr_lambda":0.3}}`,
		`{"generators":[{"kind":"format_fit","weight":1}],"filters":null}`,
	} {
		if err := ValidateFlowConfig(json.RawMessage(cfg)); err != nil {
			t.Errorf("%s: %v", cfg, err)
		}
	}
}

func asErr(err error, pe **patterna.Error) bool {
	e, ok := err.(*patterna.Error)
	*pe = e
	return ok
}

// ApplyFlow, PlanFlow and CreateFlow refuse an invalid config before anything is sent.
func TestFlowCallsValidate(t *testing.T) {
	a, e := setup(t)
	bad := json.RawMessage(`{"generators":[{"kind":"time_fit","weight":1}],"filters":{"relevance_floor":3}}`)
	if _, err := a.ApplyFlow(t.Context(), "more_to_read", bad, "", true); err == nil {
		t.Fatal("ApplyFlow accepted an invalid config")
	}
	if _, err := a.PlanFlow(t.Context(), "more_to_read", bad, true); err == nil {
		t.Fatal("PlanFlow accepted an invalid config")
	}
	if _, err := a.CreateFlow(t.Context(), "more_to_read", bad, ""); err == nil {
		t.Fatal("CreateFlow accepted an invalid config")
	}
	e.Mu.Lock()
	n := len(e.Calls)
	e.Mu.Unlock()
	if n != 0 {
		t.Fatalf("calls reached the engine: %d", n)
	}
	res, err := a.ApplyFlow(t.Context(), "more_to_read", json.RawMessage(cfgRound5), "round 5", true)
	if err != nil || !res.Created || !res.Activated {
		t.Fatalf("apply round-5 flow: %+v, %v", res, err)
	}
}
