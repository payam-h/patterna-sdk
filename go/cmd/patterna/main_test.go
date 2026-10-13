package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/payam-h/patterna-sdk/go/internal/fakeengine"
)

type result struct {
	code     int
	out, err string
}

func cli(t *testing.T, e *fakeengine.Engine, args ...string) result {
	t.Helper()
	vars := map[string]string{"PATTERNA_URL": e.URL(), "PATTERNA_ADMIN_KEY": fakeengine.AdminKey}
	var out, errw bytes.Buffer
	code := run(t.Context(), args, func(k string) string { return vars[k] }, &out, &errw)
	return result{code, out.String(), errw.String()}
}

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const flowA = `{"surface":"more_to_read","note":"item to item first","config":{"generators":[{"kind":"item_to_item","weight":1}],"ranker":{"kind":"linear"},"exploration":{"kind":"none"}}}`
const flowB = `{"surface":"more_to_read","config":{"generators":[{"kind":"trending","weight":1}],"ranker":{"kind":"linear"},"exploration":{"kind":"none"}}}`

func TestFlowCommands(t *testing.T) {
	e := fakeengine.New("more_to_read", "home_products")
	defer e.Close()
	dir := t.TempDir()
	a := write(t, dir, "a.json", flowA)

	steps := []struct {
		args []string
		code int
		want string
	}{
		{[]string{"flow", "apply", "-f", a, "--dry-run"}, 0, "more_to_read: would create a new version and activate it"},
		{[]string{"flow", "apply", "-f", a}, 0, "more_to_read: created and activated version 1"},
		{[]string{"flow", "apply", "-f", a}, 0, "more_to_read: unchanged (version 1 is active)"},
		{[]string{"flow", "get", "--surface", "more_to_read"}, 0, `"note": "item to item first"`},
		{[]string{"flow", "rollback", "--surface", "more_to_read"}, 1, "no earlier activated flow version"},
	}
	for _, s := range steps {
		r := cli(t, e, s.args...)
		if r.code != s.code || !strings.Contains(r.out+r.err, s.want) {
			t.Fatalf("%v: code %d, out %q, err %q; want %d and %q", s.args, r.code, r.out, r.err, s.code, s.want)
		}
	}

	// A directory of files, applied in name order; then roll back.
	write(t, dir, "b.json", strings.Replace(flowB, `"more_to_read"`, `"home_products"`, 1))
	r := cli(t, e, "flow", "apply", "-f", dir, "--note", "ci")
	if r.code != 0 || !strings.Contains(r.out, "more_to_read: unchanged") || !strings.Contains(r.out, "home_products: created and activated version 1") {
		t.Fatalf("dir apply: %+v", r)
	}
	b := write(t, t.TempDir(), "b.json", flowB)
	if r := cli(t, e, "flow", "apply", "-f", b, "--activate=false"); r.code != 0 || !strings.Contains(r.out, "created version 2 (not activated)") {
		t.Fatalf("no-activate: %+v", r)
	}
	if r := cli(t, e, "flow", "apply", "-f", b); r.code != 0 || !strings.Contains(r.out, "activated; activate existing version 2") {
		t.Fatalf("activate existing: %+v", r)
	}
	if r := cli(t, e, "flow", "rollback", "--surface", "more_to_read"); r.code != 0 || !strings.Contains(r.out, "rolled back to version 1") {
		t.Fatalf("rollback: %+v", r)
	}
	if r := cli(t, e, "flow", "list", "--surface", "more_to_read"); r.code != 0 || !strings.Contains(r.out, "VERSION") || strings.Count(r.out, "\n") != 3 {
		t.Fatalf("list: %+v", r)
	}

	// The output of flow get is a valid flow file.
	got := cli(t, e, "flow", "get", "--surface", "more_to_read")
	again := write(t, t.TempDir(), "again.json", got.out)
	if r := cli(t, e, "flow", "apply", "-f", again); !strings.Contains(r.out, "unchanged") {
		t.Fatalf("round trip: %+v", r)
	}
}

func TestOtherCommands(t *testing.T) {
	exportPollEvery = 0
	e := fakeengine.New("more_to_read")
	defer e.Close()
	exp := write(t, t.TempDir(), "exp.json", `{"surface":"more_to_read","name":"holdout 1","primary_metric":"tap_rate"}`)
	steps := []struct {
		args []string
		code int
		want string
	}{
		{[]string{"surface", "status", "--surface", "more_to_read", "off"}, 0, "more_to_read: status off"},
		{[]string{"surface", "status", "live", "--surface", "more_to_read"}, 0, "more_to_read: status live"},
		{[]string{"surface", "status", "--surface", "more_to_read", "on"}, 1, "invalid_argument"},
		{[]string{"surface", "testers", "--surface", "more_to_read", "u_1, u_2"}, 0, "more_to_read: 2 testers"},
		{[]string{"surface", "testers", "--surface", "more_to_read", "a@b.nl"}, 1, "looks like an email"},
		{[]string{"surface", "list"}, 0, "more_to_read  live"},
		{[]string{"experiment", "start", "-f", exp}, 0, "more_to_read: started experiment exp_1"},
		{[]string{"experiment", "list"}, 0, "holdout 1"},
		{[]string{"experiment", "end", "exp_1"}, 0, "ended experiment exp_1"},
		{[]string{"experiment", "end", "exp_1"}, 1, "not_found"},
		{[]string{"ledger", "--surface", "more_to_read"}, 0, "[+0.0100, +0.0500]"},
		{[]string{"ledger", "--surface", "more_to_read", "--json"}, 0, `"srm_p": 0.8`},
		{[]string{"ledger", "--weekly", "--surface", "more_to_read"}, 0, `"week_start"`},
		{[]string{"ledger", "export", "--from", "2026-09-01", "--to", "2026-10-01", "--wait"}, 0, `"status": "done"`},
		{[]string{"ledger", "export", "--from", "2026-09-01"}, 2, "needs --from and --to"},
		{[]string{"signing-secret", "create"}, 0, "s3cr3t-hex"},
		{[]string{"flow", "explode"}, 2, "unknown command"},
		{[]string{"flow", "apply"}, 2, "needs -f"},
		{[]string{}, 0, "usage: patterna"},
	}
	for _, s := range steps {
		r := cli(t, e, s.args...)
		if r.code != s.code || !strings.Contains(r.out+r.err, s.want) {
			t.Errorf("%v: code %d, out %q, err %q; want %d and %q", s.args, r.code, r.out, r.err, s.code, s.want)
		}
	}
}

func TestMissingEnv(t *testing.T) {
	var out, errw bytes.Buffer
	code := run(t.Context(), []string{"surface", "list"}, func(string) string { return "" }, &out, &errw)
	if code != 2 || !strings.Contains(errw.String(), "PATTERNA_ADMIN_KEY") {
		t.Errorf("code %d, err %q", code, errw.String())
	}
}
