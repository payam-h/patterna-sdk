// Package fakeengine is an in-memory stand-in for the engine, for the admin package, CLI and
// client tests. It implements only what those tests need: the admin endpoints, and recommend and
// abandon with engine 0.3's min_slots rule (shown=false with no items; abandoning such a slate is
// 409 not_shown).
package fakeengine

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"
)

// AdminKey is the key the fake accepts for admin calls; ServeKey for recommend and abandon.
const (
	AdminKey = "pk_a_test"
	ServeKey = "pk_s_test"
)

type flow struct {
	Version     int             `json:"version"`
	Config      json.RawMessage `json:"config"`
	Note        string          `json:"note,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	ActivatedAt *time.Time      `json:"activated_at"`
}

type surface struct {
	Key               string          `json:"key"`
	Status            string          `json:"status"`
	Testers           []string        `json:"testers,omitempty"`
	ActiveFlowVersion *int            `json:"active_flow_version"`
	ItemKinds         []string        `json:"item_kinds"`
	Slots             int             `json:"slots"`
	Fallback          json.RawMessage `json:"fallback"`
	MinSlots          int             `json:"min_slots"`
	DeviceResort      bool            `json:"device_resort"`
	// Items is the fake's catalog for the surface: what recommend ranks (in order).
	Items []string `json:"-"`
}

// Engine is the fake. Lock Mu before reading its maps from a test.
type Engine struct {
	Mu          sync.Mutex
	Surfaces    map[string]*surface
	Flows       map[string][]*flow
	Experiments map[string]map[string]any
	Settings    map[string]any
	Served      map[string]bool // request id -> shown
	Abandoned   map[string]string
	Calls       []string // "METHOD path?query"
	clock       time.Time
	Server      *httptest.Server
}

// New starts a fake with the given surfaces (status shadow, no flows).
func New(surfaceKeys ...string) *Engine {
	e := &Engine{
		Surfaces:    map[string]*surface{},
		Flows:       map[string][]*flow{},
		Experiments: map[string]map[string]any{},
		Settings:    map[string]any{"slug": "fake", "name": "Fake", "phase_tags": false},
		Served:      map[string]bool{},
		Abandoned:   map[string]string{},
		clock:       time.Date(2026, 10, 13, 8, 0, 0, 0, time.UTC),
	}
	for _, k := range surfaceKeys {
		e.Surfaces[k] = &surface{Key: k, Status: "shadow", ItemKinds: []string{"document"}, Slots: 5, Fallback: json.RawMessage(`{"kind":"newest"}`)}
	}
	e.Server = httptest.NewServer(http.HandlerFunc(e.handle))
	return e
}

// URL is the base URL including /v1.
func (e *Engine) URL() string { return e.Server.URL + "/v1" }

// Close stops the server.
func (e *Engine) Close() { e.Server.Close() }

func (e *Engine) tick() time.Time {
	e.clock = e.clock.Add(time.Minute)
	return e.clock
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]any{"code": code, "message": code, "request_id": "req_fake"})
}

func (e *Engine) handle(w http.ResponseWriter, r *http.Request) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	call := r.Method + " " + r.URL.Path
	if r.URL.RawQuery != "" {
		call += "?" + r.URL.RawQuery
	}
	e.Calls = append(e.Calls, call)
	var body map[string]json.RawMessage
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/"), "/")
	if parts[0] == "recommendations" {
		if r.Header.Get("Authorization") != "Bearer "+ServeKey {
			fail(w, 401, "unauthorized")
			return
		}
		e.serve(w, r, parts, body)
		return
	}
	if r.URL.Path == "/v1/health" {
		writeJSON(w, 200, map[string]any{"status": "ok", "postgres": "ok", "redis": "not configured", "version": "fake", "engine_version": "0.2.0"})
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+AdminKey {
		fail(w, 401, "unauthorized")
		return
	}
	switch {
	case r.Method == "GET" && r.URL.Path == "/v1/surfaces":
		out := []*surface{}
		for _, s := range e.Surfaces {
			out = append(out, s)
		}
		writeJSON(w, 200, out)
	case r.Method == "PATCH" && len(parts) == 2 && parts[0] == "surfaces":
		s, ok := e.Surfaces[parts[1]]
		if !ok {
			fail(w, 404, "unknown_surface")
			return
		}
		if v, ok := body["status"]; ok {
			_ = json.Unmarshal(v, &s.Status)
		}
		if v, ok := body["testers"]; ok {
			_ = json.Unmarshal(v, &s.Testers)
		}
		if v, ok := body["min_slots"]; ok {
			_ = json.Unmarshal(v, &s.MinSlots)
			if s.MinSlots < 0 || s.MinSlots > 50 {
				fail(w, 400, "bad_request")
				return
			}
		}
		for k, dst := range map[string]any{"item_kinds": &s.ItemKinds, "slots": &s.Slots, "device_resort": &s.DeviceResort, "fallback": &s.Fallback} {
			if v, ok := body[k]; ok {
				_ = json.Unmarshal(v, dst)
			}
		}
		// like the engine: the answer is not the surface
		writeJSON(w, 200, map[string]any{"key": s.Key, "ok": true})
	case r.Method == "GET" && len(parts) == 3 && parts[0] == "surfaces" && parts[2] == "metrics":
		if _, ok := e.Surfaces[parts[1]]; !ok {
			fail(w, 404, "unknown_surface")
			return
		}
		day := map[string]any{"date": "2026-10-12", "impressions": 10, "taps": 1, "tap_rate": 0.1, "fallback_rate": 0.2,
			"client_abandoned_rate": 0.1, "not_shown_share": 0.25, "unmeasured_share": nil, "p99_ms": 80, "explored_share": 0.1}
		if b := r.URL.Query().Get("block"); b != "" {
			day["block"] = b
		}
		writeJSON(w, 200, map[string]any{"days": []any{day}})
	case r.Method == "GET" && r.URL.Path == "/v1/settings":
		writeJSON(w, 200, e.Settings)
	case r.Method == "PATCH" && r.URL.Path == "/v1/settings":
		for k, v := range body {
			var a any
			_ = json.Unmarshal(v, &a)
			e.Settings[k] = a
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	case len(parts) >= 3 && parts[0] == "surfaces" && parts[2] == "flows":
		s, ok := e.Surfaces[parts[1]]
		if !ok {
			fail(w, 404, "unknown_surface")
			return
		}
		switch {
		case r.Method == "GET" && len(parts) == 3:
			out := e.Flows[s.Key]
			if out == nil {
				out = []*flow{}
			}
			writeJSON(w, 200, out)
		case r.Method == "POST" && len(parts) == 3:
			f := &flow{Version: len(e.Flows[s.Key]) + 1, Config: body["config"], CreatedAt: e.tick()}
			if v, ok := body["note"]; ok {
				_ = json.Unmarshal(v, &f.Note)
			}
			e.Flows[s.Key] = append(e.Flows[s.Key], f)
			writeJSON(w, 201, f)
		case r.Method == "POST" && len(parts) == 5 && parts[4] == "activate":
			v, _ := strconv.Atoi(parts[3])
			if v < 1 || v > len(e.Flows[s.Key]) {
				fail(w, 404, "not_found")
				return
			}
			at := e.tick()
			e.Flows[s.Key][v-1].ActivatedAt = &at
			s.ActiveFlowVersion = &v
			writeJSON(w, 200, map[string]any{"ok": true})
		default:
			fail(w, 404, "not_found")
		}
	case r.Method == "GET" && r.URL.Path == "/v1/experiments":
		out := []map[string]any{}
		for _, x := range e.Experiments {
			out = append(out, x)
		}
		writeJSON(w, 200, out)
	case r.Method == "POST" && r.URL.Path == "/v1/experiments":
		x := map[string]any{}
		for k, v := range body {
			var a any
			_ = json.Unmarshal(v, &a)
			x[k] = a
		}
		x["id"] = "exp_" + strconv.Itoa(len(e.Experiments)+1)
		x["started_at"] = e.tick()
		e.Experiments[x["id"].(string)] = x
		writeJSON(w, 201, x)
	case r.Method == "POST" && len(parts) == 3 && parts[0] == "experiments" && parts[2] == "end":
		x, ok := e.Experiments[parts[1]]
		if !ok || x["ended_at"] != nil {
			fail(w, 404, "not_found")
			return
		}
		x["ended_at"] = e.tick()
		writeJSON(w, 200, map[string]any{"ok": true})
	case r.Method == "GET" && r.URL.Path == "/v1/proof/ledger":
		surf := r.URL.Query().Get("surface")
		if r.URL.Query().Get("bucket") == "week" {
			writeJSON(w, 200, []map[string]any{{"week_start": "2026-10-05", "arm": "treatment", "surface": surf}})
			return
		}
		lo, hi := 0.01, 0.05
		row := func(block string) map[string]any {
			m := map[string]any{
				"period_start": "2026-09-14", "period_end": "2026-10-12", "surface": surf, "metric": "tap_rate",
				"control_rate": 0.1, "treatment_rate": 0.13, "difference": 0.03, "ci_low": lo, "ci_high": hi,
				"n_control": 1000, "n_treatment": 1010, "n_users_control": 400, "n_users_treatment": 410,
				"srm_p": 0.8, "readable": true, "readable_sample": 900,
			}
			if block != "" {
				m["block"] = block
			}
			return m
		}
		q := r.URL.Query()
		switch {
		case q.Get("by") == "block":
			writeJSON(w, 200, []map[string]any{row("top"), row("end_carousel")})
		case q.Get("by") != "":
			fail(w, 400, "bad_request")
		case q.Get("block") != "":
			writeJSON(w, 200, []map[string]any{row(q.Get("block"))})
		default:
			writeJSON(w, 200, []map[string]any{row("")})
		}
	case r.Method == "POST" && r.URL.Path == "/v1/proof/export":
		writeJSON(w, 202, map[string]any{"job_id": "job_1"})
	case r.Method == "GET" && r.URL.Path == "/v1/proof/exports/job_1":
		writeJSON(w, 200, map[string]any{"job_id": "job_1", "status": "done", "uri": "file:///exports/job_1", "files": []string{"impressions.csv", "interactions.csv"}})
	case r.Method == "POST" && r.URL.Path == "/v1/settings/signing-secret":
		writeJSON(w, 201, map[string]any{"signing_secret": "s3cr3t-hex", "note": "shown once"})
	default:
		fail(w, 404, "not_found")
	}
}

// serve answers POST /recommendations and POST /recommendations/{id}/abandon. Recommend ranks the
// surface's Items minus exclusions; fewer than min_slots eligible answers shown=false with no
// items. A request's control_item_ids become the slate in arm control.
func (e *Engine) serve(w http.ResponseWriter, r *http.Request, parts []string, body map[string]json.RawMessage) {
	switch {
	case r.Method == "POST" && len(parts) == 1:
		var req struct {
			Surface string `json:"surface"`
			Slots   int    `json:"slots"`
			Context struct {
				ControlItemIDs []string `json:"control_item_ids"`
			} `json:"context"`
			ExcludeItemIDs []string `json:"exclude_item_ids"`
		}
		raw, _ := json.Marshal(body)
		_ = json.Unmarshal(raw, &req)
		sf, ok := e.Surfaces[req.Surface]
		if !ok {
			fail(w, 404, "unknown_surface")
			return
		}
		rid := r.Header.Get("X-Request-Id")
		if rid == "" {
			rid = "req_" + strconv.Itoa(len(e.Served)+1)
		}
		excluded := map[string]bool{}
		for _, id := range req.ExcludeItemIDs {
			excluded[id] = true
		}
		var eligible []string
		for _, id := range sf.Items {
			if !excluded[id] {
				eligible = append(eligible, id)
			}
		}
		slots := req.Slots
		if slots <= 0 {
			slots = sf.Slots
		}
		shown := sf.MinSlots <= 0 || len(eligible) >= sf.MinSlots
		e.Served[rid] = shown
		out := map[string]any{"request_id": rid, "surface": sf.Key, "arm": "treatment", "policy_version": "fake", "flow_version": 1,
			"fallback": false, "shown": shown, "latency_ms": 1, "items": []any{}}
		if !shown {
			writeJSON(w, 200, out)
			return
		}
		ids, reason := eligible, "popular"
		if len(req.Context.ControlItemIDs) > 0 {
			ids, reason = req.Context.ControlItemIDs, "editors_pick"
			out["arm"], out["policy_version"], out["control_source"] = "control", "editorial", "request"
		}
		ids = ids[:min(slots, len(ids))]
		items := make([]any, len(ids))
		for i, id := range ids {
			items[i] = map[string]any{"item_id": id, "kind": "document", "position": i, "reason_key": reason, "reason": map[string]string{"nl": "Populair"}, "score": 1}
		}
		out["items"] = items
		writeJSON(w, 200, out)
	case r.Method == "POST" && len(parts) == 3 && parts[2] == "abandon":
		shown, ok := e.Served[parts[1]]
		if !ok {
			fail(w, 404, "unknown_request")
			return
		}
		if !shown {
			fail(w, 409, "not_shown")
			return
		}
		var reason string
		_ = json.Unmarshal(body["reason"], &reason)
		e.Abandoned[parts[1]] = reason
		writeJSON(w, 200, map[string]any{"request_id": parts[1], "arm": "treatment", "fallback": true, "client_abandoned": true, "reason": reason})
	default:
		fail(w, 404, "not_found")
	}
}
