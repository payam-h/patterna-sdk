// Command patterna keeps a tenant's Patterna setup in the customer's own repository: apply flow
// configs from JSON files in CI, roll back, flip surface status, run holdouts and read the ledger.
//
//	go install github.com/payam-h/patterna-sdk/go/cmd/patterna@packages/sdk-go/v0.2.0
//
//	export PATTERNA_URL=https://patterna-api.orven.am/v1
//	export PATTERNA_ADMIN_KEY=...            # from your CI secrets; never a flag
//
//	patterna flow apply -f flows/more_to_read.json          # create + activate when it differs
//	patterna flow apply -f flows/ --dry-run                 # every *.json in a directory
//	patterna flow get --surface more_to_read > flows/more_to_read.json
//	patterna flow list --surface more_to_read
//	patterna flow rollback --surface more_to_read
//	patterna surface list
//	patterna surface status --surface more_to_read off
//	patterna surface testers --surface more_to_read u_1,u_2
//	patterna experiment start -f experiments/more_to_read.json
//	patterna experiment end <id>
//	patterna experiment list
//	patterna ledger --surface more_to_read [--from 2026-09-01 --to 2026-10-01] [--weekly] [--json]
//	patterna ledger export --from 2026-09-01 --to 2026-10-01 [--surface s] [--wait]
//	patterna signing-secret create
//
// A flow file is {"surface": "...", "note": "...", "config": {FlowConfig}}, or an array of them.
// Exit codes: 0 done, 1 an API or input error, 2 usage.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	patterna "github.com/payam-h/patterna-sdk/go"
	"github.com/payam-h/patterna-sdk/go/admin"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

type env struct {
	ctx       context.Context
	out       io.Writer
	errw      io.Writer
	getenv    func(string) string
	pollEvery time.Duration
}

type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

// exportPollEvery is how often `ledger export --wait` asks for the job state.
var exportPollEvery = 2 * time.Second

const usage = `usage: patterna <command> [flags]

  flow apply -f FILE|DIR [--activate=true] [--dry-run] [--note TEXT]
  flow get --surface KEY [--version N]
  flow list --surface KEY
  flow rollback --surface KEY [--note TEXT]
  surface list
  surface status --surface KEY shadow|testers|live|off
  surface testers --surface KEY ID[,ID...]
  experiment start -f FILE
  experiment end ID
  experiment list
  ledger [--surface KEY] [--from DATE] [--to DATE] [--weekly] [--json]
  ledger export --from DATE --to DATE [--surface KEY] [--wait]
  signing-secret create

Environment: PATTERNA_URL (base URL with /v1), PATTERNA_ADMIN_KEY, optional PATTERNA_TRACE_ID.
`

func run(ctx context.Context, args []string, getenv func(string) string, out, errw io.Writer) int {
	e := &env{ctx: ctx, out: out, errw: errw, getenv: getenv, pollEvery: exportPollEvery}
	err := e.dispatch(args)
	var ue usageError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ue):
		fmt.Fprintf(errw, "patterna: %s\n\n%s", ue.msg, usage)
		return 2
	default:
		fmt.Fprintf(errw, "patterna: %v\n", err)
		return 1
	}
}

func (e *env) client() (*admin.Client, func(), error) {
	base, key := e.getenv("PATTERNA_URL"), e.getenv("PATTERNA_ADMIN_KEY")
	if base == "" || key == "" {
		return nil, nil, usageError{"PATTERNA_URL and PATTERNA_ADMIN_KEY must be set"}
	}
	pc, err := patterna.New(patterna.Config{BaseURL: base, AdminKey: key})
	if err != nil {
		return nil, nil, err
	}
	if t := e.getenv("PATTERNA_TRACE_ID"); t != "" {
		e.ctx = patterna.ContextWithTraceID(e.ctx, t)
	}
	return admin.New(pc), func() { _ = pc.Close(context.Background()) }, nil
}

func (e *env) dispatch(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(e.out, usage)
		return nil
	}
	sub := ""
	if len(args) > 1 && !strings.HasPrefix(args[1], "-") {
		sub = args[1]
	}
	switch args[0] + " " + sub {
	case "flow apply":
		return e.flowApply(args[2:])
	case "flow get":
		return e.flowGet(args[2:])
	case "flow list":
		return e.flowList(args[2:])
	case "flow rollback":
		return e.flowRollback(args[2:])
	case "surface list":
		return e.surfaceList(args[2:])
	case "surface status":
		return e.surfaceStatus(args[2:])
	case "surface testers":
		return e.surfaceTesters(args[2:])
	case "experiment start":
		return e.experimentStart(args[2:])
	case "experiment end":
		return e.experimentEnd(args[2:])
	case "experiment list":
		return e.experimentList(args[2:])
	case "ledger export":
		return e.ledgerExport(args[2:])
	case "signing-secret create":
		return e.signingSecret(args[2:])
	}
	if args[0] == "ledger" {
		return e.ledger(args[1:])
	}
	return usageError{"unknown command " + strings.Join(args[:min(2, len(args))], " ")}
}

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func parse(fs *flag.FlagSet, args []string) error {
	// Allow flags after positional arguments (status --surface s off, or off --surface s).
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			pos = append(pos, a)
			continue
		}
		flags = append(flags, a)
		name := strings.TrimLeft(a, "-")
		if !strings.Contains(name, "=") {
			if f := fs.Lookup(name); f != nil && !isBool(f) && i+1 < len(args) {
				flags = append(flags, args[i+1])
				i++
			}
		}
	}
	if err := fs.Parse(append(flags, pos...)); err != nil {
		return usageError{fs.Name() + ": " + err.Error()}
	}
	return nil
}

func isBool(f *flag.Flag) bool {
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && b.IsBoolFlag()
}

func (e *env) printJSON(v any) error {
	enc := json.NewEncoder(e.out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// ---- flows ------------------------------------------------------------------------------------

type flowFile struct {
	Surface string          `json:"surface"`
	Note    string          `json:"note,omitempty"`
	Config  json.RawMessage `json:"config"`
}

func readFlowFiles(path string) ([]flowFile, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	paths := []string{path}
	if info.IsDir() {
		paths, err = filepath.Glob(filepath.Join(path, "*.json"))
		if err != nil {
			return nil, err
		}
		sort.Strings(paths)
	}
	var out []flowFile
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var many []flowFile
		trimmed := strings.TrimSpace(string(raw))
		if strings.HasPrefix(trimmed, "[") {
			err = json.Unmarshal(raw, &many)
		} else {
			var one flowFile
			err = json.Unmarshal(raw, &one)
			many = []flowFile{one}
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		for i, f := range many {
			if f.Surface == "" || len(f.Config) == 0 {
				return nil, fmt.Errorf("%s: entry %d needs surface and config", p, i)
			}
		}
		out = append(out, many...)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no flow files", path)
	}
	return out, nil
}

func (e *env) flowApply(args []string) error {
	fs := newFlags("flow apply")
	file := fs.String("f", "", "flow file or directory")
	activate := fs.Bool("activate", true, "activate the matching version")
	dry := fs.Bool("dry-run", false, "say what would change, change nothing")
	note := fs.String("note", "", "note on the version (default: the file's note)")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *file == "" {
		return usageError{"flow apply needs -f"}
	}
	files, err := readFlowFiles(*file)
	if err != nil {
		return err
	}
	a, done, err := e.client()
	if err != nil {
		return err
	}
	defer done()
	for _, f := range files {
		n := f.Note
		if *note != "" {
			n = *note
		}
		var res *admin.ApplyResult
		if *dry {
			res, err = a.PlanFlow(e.ctx, f.Surface, f.Config, *activate)
		} else {
			res, err = a.ApplyFlow(e.ctx, f.Surface, f.Config, n, *activate)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", f.Surface, err)
		}
		fmt.Fprintln(e.out, describe(res, *dry))
	}
	return nil
}

func describe(r *admin.ApplyResult, dry bool) string {
	verb := map[bool]string{true: "would ", false: ""}[dry]
	switch {
	case r.Unchanged:
		return fmt.Sprintf("%s: unchanged (version %d is active)", r.Surface, r.Version)
	case r.Created && r.Activated:
		if dry {
			return fmt.Sprintf("%s: would create a new version and activate it", r.Surface)
		}
		return fmt.Sprintf("%s: created and activated version %d", r.Surface, r.Version)
	case r.Created:
		if dry {
			return fmt.Sprintf("%s: would create a new version (not activated)", r.Surface)
		}
		return fmt.Sprintf("%s: created version %d (not activated)", r.Surface, r.Version)
	case r.Activated:
		return fmt.Sprintf("%s: %sactivate existing version %d", r.Surface, map[bool]string{true: "would ", false: "activated; "}[dry], r.Version)
	default:
		return fmt.Sprintf("%s: %smatch existing version %d (not activated)", r.Surface, verb, r.Version)
	}
}

func (e *env) flowGet(args []string) error {
	fs := newFlags("flow get")
	surface := fs.String("surface", "", "surface key")
	version := fs.Int("version", 0, "version (default: the active one)")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *surface == "" {
		return usageError{"flow get needs --surface"}
	}
	a, done, err := e.client()
	if err != nil {
		return err
	}
	defer done()
	f, ok, err := a.GetFlow(e.ctx, *surface, *version)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s: no such flow version (or no active flow)", *surface)
	}
	return e.printJSON(flowFile{Surface: *surface, Note: f.Note, Config: f.Config})
}

func (e *env) flowList(args []string) error {
	fs := newFlags("flow list")
	surface := fs.String("surface", "", "surface key")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *surface == "" {
		return usageError{"flow list needs --surface"}
	}
	a, done, err := e.client()
	if err != nil {
		return err
	}
	defer done()
	s, err := a.GetSurface(e.ctx, *surface)
	if err != nil {
		return err
	}
	flows, err := a.ListFlows(e.ctx, *surface)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(e.out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "VERSION\tACTIVE\tCREATED\tLAST ACTIVATED\tNOTE")
	for _, f := range flows {
		active := ""
		if s.ActiveFlowVersion != nil && *s.ActiveFlowVersion == f.Version {
			active = "*"
		}
		act := "-"
		if f.ActivatedAt != nil {
			act = f.ActivatedAt.UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n", f.Version, active, f.CreatedAt.UTC().Format(time.RFC3339), act, f.Note)
	}
	return tw.Flush()
}

func (e *env) flowRollback(args []string) error {
	fs := newFlags("flow rollback")
	surface := fs.String("surface", "", "surface key")
	note := fs.String("note", "rollback", "note on the activation")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *surface == "" {
		return usageError{"flow rollback needs --surface"}
	}
	a, done, err := e.client()
	if err != nil {
		return err
	}
	defer done()
	v, err := a.RollbackFlow(e.ctx, *surface, *note)
	if err != nil {
		return err
	}
	fmt.Fprintf(e.out, "%s: rolled back to version %d\n", *surface, v)
	return nil
}

// ---- surfaces ---------------------------------------------------------------------------------

func (e *env) surfaceList(args []string) error {
	if err := parse(newFlags("surface list"), args); err != nil {
		return err
	}
	a, done, err := e.client()
	if err != nil {
		return err
	}
	defer done()
	list, err := a.ListSurfaces(e.ctx)
	if err != nil {
		return err
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Key < list[j].Key })
	tw := tabwriter.NewWriter(e.out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "SURFACE\tSTATUS\tFLOW\tTESTERS")
	for _, s := range list {
		v := "-"
		if s.ActiveFlowVersion != nil {
			v = fmt.Sprint(*s.ActiveFlowVersion)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\n", s.Key, s.Status, v, len(s.Testers))
	}
	return tw.Flush()
}

func (e *env) surfaceStatus(args []string) error {
	fs := newFlags("surface status")
	surface := fs.String("surface", "", "surface key")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *surface == "" || fs.NArg() != 1 {
		return usageError{"surface status needs --surface and one of shadow, testers, live, off"}
	}
	a, done, err := e.client()
	if err != nil {
		return err
	}
	defer done()
	s, err := a.SetSurfaceStatus(e.ctx, *surface, fs.Arg(0))
	if err != nil {
		return err
	}
	fmt.Fprintf(e.out, "%s: status %s\n", s.Key, s.Status)
	return nil
}

func (e *env) surfaceTesters(args []string) error {
	fs := newFlags("surface testers")
	surface := fs.String("surface", "", "surface key")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *surface == "" || fs.NArg() > 1 {
		return usageError{"surface testers needs --surface and a comma-separated id list (empty clears)"}
	}
	ids := []string{}
	for _, id := range strings.Split(fs.Arg(0), ",") {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	a, done, err := e.client()
	if err != nil {
		return err
	}
	defer done()
	s, err := a.SetSurfaceTesters(e.ctx, *surface, ids)
	if err != nil {
		return err
	}
	fmt.Fprintf(e.out, "%s: %d testers\n", s.Key, len(s.Testers))
	return nil
}

// ---- experiments ------------------------------------------------------------------------------

func (e *env) experimentStart(args []string) error {
	fs := newFlags("experiment start")
	file := fs.String("f", "", "experiment file")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *file == "" {
		return usageError{"experiment start needs -f"}
	}
	raw, err := os.ReadFile(*file)
	if err != nil {
		return err
	}
	var in admin.ExperimentInput
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return fmt.Errorf("%s: %w", *file, err)
	}
	a, done, err := e.client()
	if err != nil {
		return err
	}
	defer done()
	x, err := a.StartExperiment(e.ctx, in)
	if err != nil {
		return err
	}
	fmt.Fprintf(e.out, "%s: started experiment %s\n", x.Surface, x.ID)
	return nil
}

func (e *env) experimentEnd(args []string) error {
	fs := newFlags("experiment end")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return usageError{"experiment end needs an id"}
	}
	a, done, err := e.client()
	if err != nil {
		return err
	}
	defer done()
	if err := a.EndExperiment(e.ctx, fs.Arg(0)); err != nil {
		return err
	}
	fmt.Fprintf(e.out, "ended experiment %s\n", fs.Arg(0))
	return nil
}

func (e *env) experimentList(args []string) error {
	if err := parse(newFlags("experiment list"), args); err != nil {
		return err
	}
	a, done, err := e.client()
	if err != nil {
		return err
	}
	defer done()
	list, err := a.ListExperiments(e.ctx)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(e.out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSURFACE\tNAME\tMETRIC\tSTARTED\tENDED")
	for _, x := range list {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", x.ID, x.Surface, x.Name, x.PrimaryMetric, ts(x.StartedAt), ts(x.EndedAt))
	}
	return tw.Flush()
}

func ts(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.UTC().Format("2006-01-02")
}

// ---- ledger -----------------------------------------------------------------------------------

func (e *env) ledger(args []string) error {
	fs := newFlags("ledger")
	surface := fs.String("surface", "", "surface key")
	from := fs.String("from", "", "first day, YYYY-MM-DD")
	to := fs.String("to", "", "last day, YYYY-MM-DD")
	weekly := fs.Bool("weekly", false, "weekly rows per arm")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return usageError{"unknown ledger argument " + fs.Arg(0)}
	}
	a, done, err := e.client()
	if err != nil {
		return err
	}
	defer done()
	q := admin.LedgerQuery{Surface: *surface, From: *from, To: *to}
	if *weekly {
		rows, err := a.LedgerWeeks(e.ctx, q)
		if err != nil {
			return err
		}
		return e.printJSON(rows)
	}
	rows, err := a.Ledger(e.ctx, q)
	if err != nil {
		return err
	}
	if *asJSON {
		return e.printJSON(rows)
	}
	tw := tabwriter.NewWriter(e.out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "SURFACE\tPERIOD\tMETRIC\tCONTROL\tTREATMENT\tDIFF\t95% CI\tUSERS C/T\tSRM P\tREADABLE")
	for _, r := range rows {
		ci := "-"
		if r.CILow != nil && r.CIHigh != nil {
			ci = fmt.Sprintf("[%+.4f, %+.4f]", *r.CILow, *r.CIHigh)
		}
		fmt.Fprintf(tw, "%s\t%s..%s\t%s\t%.4f\t%.4f\t%+.4f\t%s\t%d/%d\t%.3f\t%v\n",
			r.Surface, r.PeriodStart, r.PeriodEnd, r.Metric, r.ControlRate, r.TreatmentRate, r.Difference, ci,
			r.NUsersControl, r.NUsersTreatment, r.SRMP, r.Readable)
	}
	return tw.Flush()
}

func (e *env) ledgerExport(args []string) error {
	fs := newFlags("ledger export")
	surface := fs.String("surface", "", "surface key")
	from := fs.String("from", "", "first day, YYYY-MM-DD")
	to := fs.String("to", "", "last day, YYYY-MM-DD")
	wait := fs.Bool("wait", false, "poll until the export is done")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *from == "" || *to == "" {
		return usageError{"ledger export needs --from and --to"}
	}
	a, done, err := e.client()
	if err != nil {
		return err
	}
	defer done()
	job, err := a.ExportLedger(e.ctx, admin.LedgerQuery{Surface: *surface, From: *from, To: *to})
	if err != nil {
		return err
	}
	for *wait && job.Status != "done" {
		if job.Status == "failed" {
			return fmt.Errorf("export %s failed", job.JobID)
		}
		select {
		case <-e.ctx.Done():
			return e.ctx.Err()
		case <-time.After(e.pollEvery):
		}
		if job, err = a.ExportStatus(e.ctx, job.JobID); err != nil {
			return err
		}
	}
	return e.printJSON(job)
}

// ---- signing secret ---------------------------------------------------------------------------

func (e *env) signingSecret(args []string) error {
	if err := parse(newFlags("signing-secret create"), args); err != nil {
		return err
	}
	a, done, err := e.client()
	if err != nil {
		return err
	}
	defer done()
	s, err := a.CreateSigningSecret(e.ctx)
	if err != nil {
		return err
	}
	// The secret goes to stdout alone so it can be piped into a secret manager; the advice to stderr.
	fmt.Fprintln(e.out, s.SigningSecret)
	fmt.Fprintln(e.errw, "patterna: shown once. Store it now; sign with this string as it is (never hex-decoded).")
	return nil
}
