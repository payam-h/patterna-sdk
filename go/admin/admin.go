// Package admin wraps the admin-scope endpoints a customer needs to keep its Patterna setup in
// its own repository: flows (get, create, activate, rollback, apply), surfaces (status, testers,
// and the settings in a surface file: slots, item kinds, the editorial fallback with by_anchor,
// min_slots, device_resort), surface metrics, experiments (start, end), the proof ledger (per
// block too) and its export, tenant settings (phase_tags), the signing secret and health.
//
// It runs on a *patterna.Client configured with an AdminKey, so it shares that client's
// transport: deadlines (Config.AdminTimeout, default 30 s), one retry for idempotent calls, the
// "admin" circuit breaker, request and trace ids, and *patterna.Error for every failure.
//
// The admin key can change what users see. Keep it in CI secrets, never in the app backend's
// request path.
package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	patterna "github.com/payam-h/patterna-sdk/go"
)

// Client is the admin API on top of a patterna.Client.
type Client struct {
	pc *patterna.Client
}

// New returns an admin client. pc must have an AdminKey; calls fail with
// patterna.ErrNotConfigured otherwise.
func New(pc *patterna.Client) *Client { return &Client{pc: pc} }

func (c *Client) call(ctx context.Context, r patterna.AdminRequest, out any, opts []patterna.CallOption) error {
	return c.pc.AdminCall(ctx, r, out, opts...)
}

func invalid(op, msg string) error {
	return &patterna.Error{Code: patterna.CodeInvalidArgument, Op: op, Message: msg}
}

func surfacePath(key string) string { return "/surfaces/" + url.PathEscape(key) }

// ---- surfaces ---------------------------------------------------------------------------------

// Surface status values.
const (
	StatusShadow  = "shadow"
	StatusTesters = "testers"
	StatusLive    = "live"
	StatusOff     = "off"
)

// Surface is a placement with its status and active flow version.
type Surface struct {
	Key               string          `json:"key"`
	Name              string          `json:"name,omitempty"`
	ItemKinds         []string        `json:"item_kinds,omitempty"`
	Slots             int             `json:"slots,omitempty"`
	Fallback          json.RawMessage `json:"fallback,omitempty"`
	ExplorationShare  *float64        `json:"exploration_share,omitempty"`
	Status            string          `json:"status,omitempty"`
	Testers           []string        `json:"testers,omitempty"`
	Notes             string          `json:"notes,omitempty"`
	ActiveFlowVersion *int            `json:"active_flow_version,omitempty"`
	Impressions7d     int             `json:"impressions_7d,omitempty"`
	TapRate7d         *float64        `json:"tap_rate_7d,omitempty"`
	ExperimentID      *string         `json:"experiment_id,omitempty"`
	// DeviceResort (engine 0.3): the app re-sorts the slate on the device; every interaction must
	// then carry displayed_position.
	DeviceResort bool `json:"device_resort,omitempty"`
	// MinSlots (engine 0.3): below this many eligible items the answer is shown=false with no
	// items. 0 = always shown.
	MinSlots         int `json:"min_slots,omitempty"`
	TesterRequests7d int `json:"tester_requests_7d,omitempty"`
}

// FallbackConfig decodes Surface.Fallback.
func (s *Surface) FallbackConfig() (*SurfaceFallback, error) {
	if s == nil || len(s.Fallback) == 0 || string(s.Fallback) == "null" {
		return nil, nil
	}
	var f SurfaceFallback
	if err := json.Unmarshal(s.Fallback, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// ListSurfaces returns every surface of the tenant.
func (c *Client) ListSurfaces(ctx context.Context, opts ...patterna.CallOption) ([]Surface, error) {
	var out []Surface
	err := c.call(ctx, patterna.AdminRequest{Op: "listSurfaces", Method: http.MethodGet, Path: "/surfaces", Idempotent: true}, &out, opts)
	return out, err
}

// GetSurface returns one surface, or patterna.ErrUnknownSurface.
func (c *Client) GetSurface(ctx context.Context, key string, opts ...patterna.CallOption) (*Surface, error) {
	all, err := c.ListSurfaces(ctx, opts...)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].Key == key {
			return &all[i], nil
		}
	}
	return nil, &patterna.Error{Code: patterna.CodeUnknownSurface, Op: "getSurface", Message: "no surface " + strconv.Quote(key)}
}

func (c *Client) patchSurface(ctx context.Context, op, key string, body map[string]any, opts []patterna.CallOption) (*Surface, error) {
	if key == "" {
		return nil, invalid(op, "surface key is required")
	}
	var out Surface
	err := c.call(ctx, patterna.AdminRequest{Op: op, Method: http.MethodPatch, Path: surfacePath(key), Body: body, Idempotent: true}, &out, opts)
	if err != nil {
		return nil, err
	}
	if out.Status == "" {
		// The engine answers {"key", "ok"} rather than the surface the contract describes: read
		// the surface back so callers see its state after the change.
		return c.GetSurface(ctx, key, opts...)
	}
	return &out, nil
}

// SetSurfaceStatus moves a surface between shadow, testers, live and off. off is the kill switch:
// Recommend answers surface_off and RecommendWithFallback shows your own list.
func (c *Client) SetSurfaceStatus(ctx context.Context, key, status string, opts ...patterna.CallOption) (*Surface, error) {
	if !slices.Contains([]string{StatusShadow, StatusTesters, StatusLive, StatusOff}, status) {
		return nil, invalid("setSurfaceStatus", "status must be shadow, testers, live or off")
	}
	return c.patchSurface(ctx, "setSurfaceStatus", key, map[string]any{"status": status}, opts)
}

// SetSurfaceTesters replaces the tester roster of a surface: at most 200 random user ids, never
// emails. With engine 0.2 you can also flag a request with patterna.WithTester() instead.
func (c *Client) SetSurfaceTesters(ctx context.Context, key string, userIDs []string, opts ...patterna.CallOption) (*Surface, error) {
	const op = "setSurfaceTesters"
	if len(userIDs) > 200 {
		return nil, invalid(op, "at most 200 testers")
	}
	for _, id := range userIDs {
		if err := patterna.CheckUserID(op, id); err != nil {
			return nil, err
		}
	}
	if userIDs == nil {
		userIDs = []string{}
	}
	return c.patchSurface(ctx, op, key, map[string]any{"testers": userIDs}, opts)
}

// ---- flows ------------------------------------------------------------------------------------

// Flow is one version of a surface's recipe. Config is kept as raw JSON so fields this SDK does
// not know survive a round trip.
type Flow struct {
	Version     int             `json:"version"`
	Config      json.RawMessage `json:"config"`
	Note        string          `json:"note,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	ActivatedAt *time.Time      `json:"activated_at,omitempty"`
	CreatedBy   *string         `json:"created_by,omitempty"`
}

// ListFlows returns every flow version of a surface.
func (c *Client) ListFlows(ctx context.Context, surface string, opts ...patterna.CallOption) ([]Flow, error) {
	if surface == "" {
		return nil, invalid("listFlows", "surface key is required")
	}
	var out []Flow
	err := c.call(ctx, patterna.AdminRequest{Op: "listFlows", Method: http.MethodGet, Path: surfacePath(surface) + "/flows", Idempotent: true}, &out, opts)
	return out, err
}

// GetFlow returns one version; version 0 means the active one. ok is false when there is none.
func (c *Client) GetFlow(ctx context.Context, surface string, version int, opts ...patterna.CallOption) (flow *Flow, ok bool, err error) {
	if version == 0 {
		s, err := c.GetSurface(ctx, surface, opts...)
		if err != nil {
			return nil, false, err
		}
		if s.ActiveFlowVersion == nil {
			return nil, false, nil
		}
		version = *s.ActiveFlowVersion
	}
	flows, err := c.ListFlows(ctx, surface, opts...)
	if err != nil {
		return nil, false, err
	}
	for i := range flows {
		if flows[i].Version == version {
			return &flows[i], true, nil
		}
	}
	return nil, false, nil
}

// CreateFlow stores a new, inactive version.
func (c *Client) CreateFlow(ctx context.Context, surface string, config json.RawMessage, note string, opts ...patterna.CallOption) (*Flow, error) {
	const op = "createFlow"
	if surface == "" {
		return nil, invalid(op, "surface key is required")
	}
	if !json.Valid(config) || len(bytes.TrimSpace(config)) == 0 || bytes.TrimSpace(config)[0] != '{' {
		return nil, invalid(op, "config must be a JSON object")
	}
	if err := ValidateFlowConfig(config); err != nil {
		return nil, err
	}
	body := map[string]any{"config": config}
	if note != "" {
		body["note"] = note
	}
	var out Flow
	// Not idempotent: a retry after a lost answer would create a second version.
	err := c.call(ctx, patterna.AdminRequest{Op: op, Method: http.MethodPost, Path: surfacePath(surface) + "/flows", Body: body}, &out, opts)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ActivateFlow makes a version live. On a tenant with the two-person rule the engine answers 409
// approval_required; request one in the panel.
func (c *Client) ActivateFlow(ctx context.Context, surface string, version int, note string, opts ...patterna.CallOption) error {
	if surface == "" || version <= 0 {
		return invalid("activateFlow", "surface key and a positive version are required")
	}
	var body any
	if note != "" {
		body = map[string]any{"note": note}
	}
	return c.call(ctx, patterna.AdminRequest{
		Op: "activateFlow", Method: http.MethodPost,
		Path: surfacePath(surface) + "/flows/" + strconv.Itoa(version) + "/activate", Body: body, Idempotent: true,
	}, nil, opts)
}

// ErrNothingToRollBack: the surface has no previously activated version.
var ErrNothingToRollBack = errors.New("admin: no earlier activated flow version to roll back to")

// RollbackFlow re-activates the version that was active before the current one (the most
// recently activated version other than the active one) and returns its number.
func (c *Client) RollbackFlow(ctx context.Context, surface, note string, opts ...patterna.CallOption) (int, error) {
	s, err := c.GetSurface(ctx, surface, opts...)
	if err != nil {
		return 0, err
	}
	flows, err := c.ListFlows(ctx, surface, opts...)
	if err != nil {
		return 0, err
	}
	target := previousVersion(flows, s.ActiveFlowVersion)
	if target == 0 {
		return 0, ErrNothingToRollBack
	}
	if note == "" {
		note = "rollback"
	}
	return target, c.ActivateFlow(ctx, surface, target, note, opts...)
}

func previousVersion(flows []Flow, active *int) int {
	var best *Flow
	for i := range flows {
		f := &flows[i]
		if f.ActivatedAt == nil || (active != nil && f.Version == *active) {
			continue
		}
		if best == nil || f.ActivatedAt.After(*best.ActivatedAt) {
			best = f
		}
	}
	if best == nil {
		return 0
	}
	return best.Version
}

// ApplyResult says what ApplyFlow did.
type ApplyResult struct {
	Surface   string `json:"surface"`
	Version   int    `json:"version"`   // the version that matches the file
	Created   bool   `json:"created"`   // a new version was stored
	Activated bool   `json:"activated"` // it was activated by this call
	Unchanged bool   `json:"unchanged"` // the active version already had this config
}

// ApplyFlow makes the surface's flow match config, the way `patterna flow apply` does in CI:
//
//   - if the active version's config is equal (compared as canonical JSON), nothing happens;
//   - else if an existing version has the same config, that one is reused;
//   - else a new version is created;
//   - with activate, the matching version is then activated.
//
// If the engine normalises a config (fills defaults), write the normalised form into the file
// (`patterna flow get`) or every apply will create a new version.
func (c *Client) ApplyFlow(ctx context.Context, surface string, config json.RawMessage, note string, activate bool, opts ...patterna.CallOption) (*ApplyResult, error) {
	res, err := c.PlanFlow(ctx, surface, config, activate, opts...)
	if err != nil || res.Unchanged {
		return res, err
	}
	if res.Created {
		f, err := c.CreateFlow(ctx, surface, config, note, opts...)
		if err != nil {
			return nil, err
		}
		res.Version = f.Version
	}
	if res.Activated {
		if err := c.ActivateFlow(ctx, surface, res.Version, note, opts...); err != nil {
			res.Activated = false
			return res, err
		}
	}
	return res, nil
}

// PlanFlow is ApplyFlow without writing anything: it says what ApplyFlow would do. For a version
// it would create, Version is 0.
func (c *Client) PlanFlow(ctx context.Context, surface string, config json.RawMessage, activate bool, opts ...patterna.CallOption) (*ApplyResult, error) {
	want, err := canonical(config)
	if err != nil || len(want) == 0 || want[0] != '{' {
		return nil, invalid("applyFlow", "config must be a JSON object")
	}
	if err := ValidateFlowConfig(config); err != nil {
		return nil, err
	}
	s, err := c.GetSurface(ctx, surface, opts...)
	if err != nil {
		return nil, err
	}
	flows, err := c.ListFlows(ctx, surface, opts...)
	if err != nil {
		return nil, err
	}
	res := &ApplyResult{Surface: surface}
	for _, f := range flows {
		got, err := canonical(f.Config)
		if err != nil || got != want {
			continue
		}
		if s.ActiveFlowVersion != nil && f.Version == *s.ActiveFlowVersion {
			return &ApplyResult{Surface: surface, Version: f.Version, Unchanged: true}, nil
		}
		res.Version = max(res.Version, f.Version)
	}
	res.Created = res.Version == 0
	res.Activated = activate
	return res, nil
}

func canonical(raw json.RawMessage) (string, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", err
	}
	b, err := patterna.CanonicalJSON(v)
	return string(b), err
}

// ---- experiments ------------------------------------------------------------------------------

// ExperimentInput starts a holdout on a surface.
type ExperimentInput struct {
	Surface        string   `json:"surface"`
	Name           string   `json:"name"`
	PrimaryMetric  string   `json:"primary_metric"` // tap_rate (the only one the engine accepts today)
	TreatmentShare *float64 `json:"treatment_share,omitempty"`
	StratifyBy     string   `json:"stratify_by,omitempty"`
	BaselineRate   *float64 `json:"baseline_rate,omitempty"`
	RelativeChange *float64 `json:"relative_change,omitempty"`
}

// Experiment is a holdout.
type Experiment struct {
	ExperimentInput
	ID             string     `json:"id"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	EndedAt        *time.Time `json:"ended_at,omitempty"`
	ReadableSample int        `json:"readable_sample,omitempty"`
	NControl       int        `json:"n_control,omitempty"`
	NTreatment     int        `json:"n_treatment,omitempty"`
	ReadableAt     *string    `json:"readable_at,omitempty"`
}

// ListExperiments returns every experiment of the tenant.
func (c *Client) ListExperiments(ctx context.Context, opts ...patterna.CallOption) ([]Experiment, error) {
	var out []Experiment
	err := c.call(ctx, patterna.AdminRequest{Op: "listExperiments", Method: http.MethodGet, Path: "/experiments", Idempotent: true}, &out, opts)
	return out, err
}

// StartExperiment starts a holdout. Not retried: a lost answer would start a second one.
func (c *Client) StartExperiment(ctx context.Context, in ExperimentInput, opts ...patterna.CallOption) (*Experiment, error) {
	if in.Surface == "" || in.Name == "" || in.PrimaryMetric == "" {
		return nil, invalid("startExperiment", "surface, name and primary_metric are required")
	}
	var out Experiment
	if err := c.call(ctx, patterna.AdminRequest{Op: "startExperiment", Method: http.MethodPost, Path: "/experiments", Body: in}, &out, opts); err != nil {
		return nil, err
	}
	return &out, nil
}

// EndExperiment ends a running holdout. An unknown or already ended id answers 404.
func (c *Client) EndExperiment(ctx context.Context, id string, opts ...patterna.CallOption) error {
	if id == "" {
		return invalid("endExperiment", "experiment id is required")
	}
	return c.call(ctx, patterna.AdminRequest{Op: "endExperiment", Method: http.MethodPost, Path: "/experiments/" + url.PathEscape(id) + "/end", Idempotent: true}, nil, opts)
}

// ---- ledger -----------------------------------------------------------------------------------

// LedgerQuery filters the ledger. From and To are UTC dates (YYYY-MM-DD); empty means the
// engine's default window.
type LedgerQuery struct {
	Surface string
	From    string
	To      string
	// Block (engine 0.3) restricts the rows to one UI block (at most 32 characters). For every
	// block use LedgerByBlock.
	Block string
}

// LedgerRow is one surface's proof: treatment against control with a user-clustered interval.
type LedgerRow struct {
	PeriodStart     string   `json:"period_start"`
	PeriodEnd       string   `json:"period_end"`
	Surface         string   `json:"surface"`
	Metric          string   `json:"metric"`
	ControlRate     float64  `json:"control_rate"`
	TreatmentRate   float64  `json:"treatment_rate"`
	Difference      float64  `json:"difference"`
	CILow           *float64 `json:"ci_low"`
	CIHigh          *float64 `json:"ci_high"`
	NControl        int      `json:"n_control"`
	NTreatment      int      `json:"n_treatment"`
	NUsersControl   int      `json:"n_users_control"`
	NUsersTreatment int      `json:"n_users_treatment"`
	SRMP            float64  `json:"srm_p"`
	Note            string   `json:"note,omitempty"`
	Readable        bool     `json:"readable"`
	// ReadableSample is the units per arm the row needs to be readable (the power calculation).
	ReadableSample int `json:"readable_sample,omitempty"`
	// Block (engine 0.3) is set on per-block rows (LedgerByBlock, LedgerQuery.Block): slates the
	// client reported seen in that block and taps in it. Descriptive; the whole-slate row decides.
	Block                 string  `json:"block,omitempty"`
	RevenueCentsControl   *int64  `json:"revenue_cents_control,omitempty"`
	RevenueCentsTreatment *int64  `json:"revenue_cents_treatment,omitempty"`
	ExportURI             *string `json:"export_uri,omitempty"`
}

func (q LedgerQuery) values() url.Values {
	v := url.Values{}
	if q.Surface != "" {
		v.Set("surface", q.Surface)
	}
	if q.From != "" {
		v.Set("from", q.From)
	}
	if q.To != "" {
		v.Set("to", q.To)
	}
	if q.Block != "" {
		v.Set("block", q.Block)
	}
	return v
}

func checkBlock(op, block string) error {
	if len(block) > 32 {
		return invalid(op, "block is a short name of at most 32 characters")
	}
	return nil
}

// Ledger returns one summary row per surface (one row for q.Block when it is set).
func (c *Client) Ledger(ctx context.Context, q LedgerQuery, opts ...patterna.CallOption) ([]LedgerRow, error) {
	if err := checkBlock("ledger", q.Block); err != nil {
		return nil, err
	}
	var out []LedgerRow
	err := c.call(ctx, patterna.AdminRequest{Op: "ledger", Method: http.MethodGet, Path: "/proof/ledger", Query: q.values(), Idempotent: true}, &out, opts)
	return out, err
}

// LedgerByBlock (engine 0.3: by=block) returns one row per surface and block, over the slates
// the client reported seen in that block (Interaction.Block) and the taps in it. Descriptive: the
// whole-slate row from Ledger decides. q.Block is ignored.
func (c *Client) LedgerByBlock(ctx context.Context, q LedgerQuery, opts ...patterna.CallOption) ([]LedgerRow, error) {
	q.Block = ""
	v := q.values()
	v.Set("by", "block")
	var out []LedgerRow
	err := c.call(ctx, patterna.AdminRequest{Op: "ledgerByBlock", Method: http.MethodGet, Path: "/proof/ledger", Query: v, Idempotent: true}, &out, opts)
	return out, err
}

// LedgerWeeks returns the weekly rows per arm (bucket=week), as raw JSON objects.
func (c *Client) LedgerWeeks(ctx context.Context, q LedgerQuery, opts ...patterna.CallOption) ([]map[string]any, error) {
	v := q.values()
	v.Set("bucket", "week")
	var out []map[string]any
	err := c.call(ctx, patterna.AdminRequest{Op: "ledgerWeeks", Method: http.MethodGet, Path: "/proof/ledger", Query: v, Idempotent: true}, &out, opts)
	return out, err
}

// ExportJob is a proof export: impressions and interactions as files, so the ledger can be
// recomputed outside Patterna.
type ExportJob struct {
	JobID  string   `json:"job_id"`
	Status string   `json:"status,omitempty"`
	URI    string   `json:"uri,omitempty"`
	Files  []string `json:"files,omitempty"`
}

// ExportLedger queues an export for [from, to] (UTC dates) and returns the job id. Poll
// ExportStatus until Status is "done".
func (c *Client) ExportLedger(ctx context.Context, q LedgerQuery, opts ...patterna.CallOption) (*ExportJob, error) {
	if q.From == "" || q.To == "" {
		return nil, invalid("exportLedger", "from and to are required")
	}
	body := map[string]any{"from": q.From, "to": q.To}
	if q.Surface != "" {
		body["surface"] = q.Surface
	}
	var out ExportJob
	if err := c.call(ctx, patterna.AdminRequest{Op: "exportLedger", Method: http.MethodPost, Path: "/proof/export", Body: body}, &out, opts); err != nil {
		return nil, err
	}
	return &out, nil
}

// ExportStatus returns the state of an export job.
func (c *Client) ExportStatus(ctx context.Context, jobID string, opts ...patterna.CallOption) (*ExportJob, error) {
	if jobID == "" {
		return nil, invalid("exportStatus", "job id is required")
	}
	var out ExportJob
	if err := c.call(ctx, patterna.AdminRequest{Op: "exportStatus", Method: http.MethodGet, Path: "/proof/exports/" + url.PathEscape(jobID), Idempotent: true}, &out, opts); err != nil {
		return nil, err
	}
	return &out, nil
}

// ---- signing secret ---------------------------------------------------------------------------

// CreateSigningSecret issues the first signing secret (engine 0.2). See
// patterna.Client.CreateSigningSecret.
func (c *Client) CreateSigningSecret(ctx context.Context, opts ...patterna.CallOption) (*patterna.SigningSecret, error) {
	return c.pc.CreateSigningSecret(ctx, opts...)
}

// RotateSigningSecret replaces the signing secret (owner keys only). Events signed with the old
// one stop verifying at once: roll it out to your signers first.
func (c *Client) RotateSigningSecret(ctx context.Context, opts ...patterna.CallOption) (*patterna.SigningSecret, error) {
	var out patterna.SigningSecret
	if err := c.call(ctx, patterna.AdminRequest{Op: "rotateSigningSecret", Method: http.MethodPost, Path: "/settings/signing-secret/rotate"}, &out, opts); err != nil {
		return nil, err
	}
	return &out, nil
}
