package patterna

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Dummy key strings; not secrets.
const (
	testServeKey  = "pk_serve_test"
	testIngestKey = "pk_ingest_test"
)

type recorded struct {
	Method string
	Path   string // escaped path
	Query  string
	Auth   string
	Header http.Header
	Body   map[string]any
}

// recorder is an httptest server that records requests and answers from a script of replies.
// The last reply repeats.
type recorder struct {
	t       *testing.T
	srv     *httptest.Server
	mu      sync.Mutex
	reqs    []recorded
	replies []reply
	// route, when set, answers by path first; a false second result uses the script.
	route func(path string) (reply, bool)
}

type reply struct {
	status    int
	body      string
	requestID string
	hang      bool // block until the client gives up
	drop      bool // close the connection without answering
	delay     time.Duration
}

func newRecorder(t *testing.T, replies ...reply) *recorder {
	t.Helper()
	return newRoutedRecorder(t, nil, replies...)
}

func newRoutedRecorder(t *testing.T, route func(string) (reply, bool), replies ...reply) *recorder {
	t.Helper()
	r := &recorder{t: t, replies: replies, route: route}
	r.srv = httptest.NewServer(http.HandlerFunc(r.handle))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *recorder) handle(w http.ResponseWriter, req *http.Request) {
	raw, _ := io.ReadAll(req.Body)
	var body map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	r.mu.Lock()
	n := len(r.reqs)
	r.reqs = append(r.reqs, recorded{
		Method: req.Method, Path: req.URL.EscapedPath(), Query: req.URL.RawQuery,
		Auth: req.Header.Get("Authorization"), Header: req.Header.Clone(), Body: body,
	})
	rep := r.replies[min(n, len(r.replies)-1)]
	if r.route != nil {
		if rr, ok := r.route(req.URL.Path); ok {
			rep = rr
		}
	}
	r.mu.Unlock()

	switch {
	case rep.hang:
		<-req.Context().Done()
		return
	case rep.drop:
		hj, ok := w.(http.Hijacker)
		if !ok {
			r.t.Fatal("cannot hijack")
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			r.t.Fatal(err)
		}
		_ = conn.Close()
		return
	}
	if rep.delay > 0 {
		select {
		case <-time.After(rep.delay):
		case <-req.Context().Done():
			return
		}
	}
	if rep.requestID != "" {
		w.Header().Set("X-Request-Id", rep.requestID)
	}
	w.Header().Set("Content-Type", "application/json")
	status := rep.status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, rep.body)
}

// requestsTo returns the recorded requests whose path has the given suffix.
func (r *recorder) requestsTo(suffix string) []recorded {
	var out []recorded
	for _, q := range r.requests() {
		if strings.HasSuffix(q.Path, suffix) {
			out = append(out, q)
		}
	}
	return out
}

func (r *recorder) requests() []recorded {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recorded(nil), r.reqs...)
}

func (r *recorder) client(t *testing.T, mutate ...func(*Config)) *Client {
	t.Helper()
	cfg := Config{
		BaseURL:    r.srv.URL + "/v1",
		ServeKey:   testServeKey,
		IngestKey:  testIngestKey,
		RetryDelay: -1, // no pause between attempts in tests
	}
	for _, m := range mutate {
		m(&cfg)
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(t.Context()) })
	return c
}

const slateJSON = `{"request_id":"req_1","surface":"home_articles","arm":"treatment","policy_version":"lin-1",
"items":[{"item_id":"doc_1","kind":"document","position":0,"reason_key":"similar_to_read",
"reason":{"nl":"Omdat je dit las","en":"Because you read this"}},
{"item_id":"doc_2","kind":"document","position":1,"reason_key":"trending","reason":{"nl":"Populair"}}]}`

func personalized() RecommendRequest {
	return RecommendRequest{Surface: "home_articles", Mode: ModePersonalized, UserID: "u_7f3a", Context: RecommendContext{Locale: "nl"}}
}

func ev(id string) Event {
	return Event{EventID: id, Type: "item_viewed", OccurredAt: time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC), Source: "backend", UserID: "u_7f3a"}
}
