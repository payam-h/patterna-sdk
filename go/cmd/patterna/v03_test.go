package main

import (
	"strings"
	"testing"

	"github.com/payam-h/patterna-sdk/go/internal/fakeengine"
)

const surfaceFile = `{"surface":"more_to_read","slots":7,"item_kinds":["document"],"min_slots":3,"device_resort":true,
"fallback":{"kind":"curated","item_ids":["doc_1"],"by_anchor":{"doc_9":["p_1","p_2"]}}}`

func TestSurfaceApply(t *testing.T) {
	e := fakeengine.New("more_to_read", "home_products")
	defer e.Close()
	dir := t.TempDir()
	f := write(t, dir, "more_to_read.json", surfaceFile)
	steps := []struct {
		args []string
		code int
		want string
	}{
		{[]string{"surface", "apply", "-f", f, "--dry-run"}, 0, "more_to_read: would change slots, fallback, min_slots, device_resort"},
		{[]string{"surface", "apply", "-f", f}, 0, "more_to_read: changed slots, fallback, min_slots, device_resort"},
		{[]string{"surface", "apply", "-f", dir}, 0, "more_to_read: unchanged"},
		{[]string{"surface", "apply"}, 2, "surface apply needs -f"},
	}
	for _, s := range steps {
		r := cli(t, e, s.args...)
		if r.code != s.code || !strings.Contains(r.out+r.err, s.want) {
			t.Fatalf("%v: code %d, out %q, err %q; want %d and %q", s.args, r.code, r.out, r.err, s.code, s.want)
		}
	}
	e.Mu.Lock()
	sf := e.Surfaces["more_to_read"]
	if sf.MinSlots != 3 || !sf.DeviceResort || sf.Slots != 7 || !strings.Contains(string(sf.Fallback), `"by_anchor":{"doc_9":["p_1","p_2"]}`) {
		t.Errorf("surface %+v %s", sf, sf.Fallback)
	}
	e.Mu.Unlock()

	// a typo or an invalid value fails before anything is sent
	for _, body := range []string{`{"surface":"more_to_read","min_slot":3}`, `{"surface":"more_to_read","min_slots":60}`} {
		bad := write(t, t.TempDir(), "bad.json", body)
		e.Mu.Lock()
		n := len(e.Calls)
		e.Mu.Unlock()
		if r := cli(t, e, "surface", "apply", "-f", bad); r.code != 1 {
			t.Errorf("%s: code %d, err %q", body, r.code, r.err)
		}
		e.Mu.Lock()
		if len(e.Calls) != n {
			t.Errorf("%s: sent %v", body, e.Calls[n:])
		}
		e.Mu.Unlock()
	}
}

func TestSettingsPhaseTags(t *testing.T) {
	e := fakeengine.New("more_to_read")
	defer e.Close()
	steps := []struct {
		args []string
		code int
		want string
	}{
		{[]string{"settings", "phase-tags"}, 0, "phase_tags: off"},
		{[]string{"settings", "phase-tags", "on"}, 0, "phase_tags: on"},
		{[]string{"settings", "phase-tags"}, 0, "phase_tags: on"},
		{[]string{"settings", "phase-tags", "off"}, 0, "phase_tags: off"},
		{[]string{"settings", "phase-tags", "maybe"}, 2, "takes on, off"},
	}
	for _, s := range steps {
		r := cli(t, e, s.args...)
		if r.code != s.code || !strings.Contains(r.out+r.err, s.want) {
			t.Fatalf("%v: code %d, out %q, err %q; want %d and %q", s.args, r.code, r.out, r.err, s.code, s.want)
		}
	}
}

func TestLedgerByBlock(t *testing.T) {
	e := fakeengine.New("more_to_read")
	defer e.Close()
	r := cli(t, e, "ledger", "--by", "block", "--surface", "more_to_read")
	if r.code != 0 || !strings.Contains(r.out, "BLOCK") || !strings.Contains(r.out, "end_carousel") || !strings.Contains(r.out, "top") {
		t.Fatalf("code %d, out %q, err %q", r.code, r.out, r.err)
	}
	r = cli(t, e, "ledger", "--block", "top", "--json")
	if r.code != 0 || !strings.Contains(r.out, `"block": "top"`) {
		t.Fatalf("code %d, out %q, err %q", r.code, r.out, r.err)
	}
	for _, args := range [][]string{{"ledger", "--by", "day"}, {"ledger", "--weekly", "--by", "block"}} {
		if r := cli(t, e, args...); r.code != 2 {
			t.Errorf("%v: code %d", args, r.code)
		}
	}
	e.Mu.Lock()
	defer e.Mu.Unlock()
	if e.Calls[0] != "GET /v1/proof/ledger?by=block&surface=more_to_read" {
		t.Errorf("calls %v", e.Calls)
	}
}

// `flow apply` checks every file before the first is applied: a typo in the second file leaves
// the first unapplied too.
func TestFlowApplyValidatesRound5(t *testing.T) {
	e := fakeengine.New("more_to_read", "home_products")
	defer e.Close()
	dir := t.TempDir()
	write(t, dir, "a.json", `{"surface":"more_to_read","config":{"generators":[{"kind":"time_fit","weight":0.2},{"kind":"format_fit","weight":0.2}],
"filters":{"exclude_dismissed_days":30,"exclude_consumed_days":30,"paywall_cooldown_days":7,"repurchase_days":30,"hide_unavailable":true,"relevance_floor":0.3},
"ranker":{"kind":"linear"},"exploration":{"kind":"none"},"reasons":{"recommended":{"nl":"Aanbevolen"}}}}`)
	write(t, dir, "b.json", `{"surface":"home_products","config":{"generators":[{"kind":"trending","weight":1}],"filters":{"relevance_flor":0.3},"ranker":{"kind":"linear"},"exploration":{"kind":"none"}}}`)
	r := cli(t, e, "flow", "apply", "-f", dir)
	if r.code != 1 || !strings.Contains(r.err, "b.json: home_products") || !strings.Contains(r.err, "unknown filter relevance_flor") {
		t.Fatalf("code %d, out %q, err %q", r.code, r.out, r.err)
	}
	e.Mu.Lock()
	n := len(e.Calls)
	e.Mu.Unlock()
	if n != 0 {
		t.Fatalf("calls reached the engine: %d", n)
	}
	write(t, dir, "b.json", `{"surface":"home_products","config":{"generators":[{"kind":"trending","weight":1}],"filters":{"relevance_floor":0.3},"ranker":{"kind":"linear"},"exploration":{"kind":"none"}}}`)
	r = cli(t, e, "flow", "apply", "-f", dir)
	if r.code != 0 || !strings.Contains(r.out, "more_to_read: created and activated version 1") || !strings.Contains(r.out, "home_products: created and activated version 1") {
		t.Fatalf("code %d, out %q, err %q", r.code, r.out, r.err)
	}
}
