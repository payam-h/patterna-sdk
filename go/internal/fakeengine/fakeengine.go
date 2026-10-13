// Package fakeengine is an in-memory stand-in for the engine's admin endpoints, for the admin
// package and CLI tests. It implements only what those tests need.
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

// AdminKey is the key the fake accepts.
const AdminKey = "pk_a_test"

type flow struct {
	Version     int             `json:"version"`
	Config      json.RawMessage `json:"config"`
	Note        string          `json:"note,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	ActivatedAt *time.Time      `json:"activated_at"`
}

type surface struct {
	Key               string   `json:"key"`
	Status            string   `json:"status"`
	Testers           []string `json:"testers,omitempty"`
	ActiveFlowVersion *int     `json:"active_flow_version"`
}

// Engine is the fake. Lock Mu before reading its maps from a test.
type Engine struct {
	Mu          sync.Mutex
	Surfaces    map[string]*surface
	Flows       map[string][]*flow
	Experiments map[string]map[string]any
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
		clock:       time.Date(2026, 10, 13, 8, 0, 0, 0, time.UTC),
	}
	for _, k := range surfaceKeys {
		e.Surfaces[k] = &surface{Key: k, Status: "shadow"}
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
	if r.Header.Get("Authorization") != "Bearer "+AdminKey {
		fail(w, 401, "unauthorized")
		return
	}
	var body map[string]json.RawMessage
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/"), "/")
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
		writeJSON(w, 200, s)
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
		writeJSON(w, 200, []map[string]any{{
			"period_start": "2026-09-14", "period_end": "2026-10-12", "surface": surf, "metric": "tap_rate",
			"control_rate": 0.1, "treatment_rate": 0.13, "difference": 0.03, "ci_low": lo, "ci_high": hi,
			"n_control": 1000, "n_treatment": 1010, "n_users_control": 400, "n_users_treatment": 410,
			"srm_p": 0.8, "readable": true,
		}})
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
