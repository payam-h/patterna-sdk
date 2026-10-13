package patterna

import (
	"errors"
	"slices"
	"testing"
	"time"
)

func TestRecommendWithFallback(t *testing.T) {
	mine := func() []string { return []string{"newest_1", "newest_2"} }
	tests := []struct {
		name         string
		replies      []reply
		req          RecommendRequest
		fallback     func() []string
		openBreaker  bool
		wantErr      error
		wantFallback bool
		wantIDs      []string
		wantCause    error
	}{
		{"Patterna answers", []reply{{body: slateJSON}}, personalized(), mine, false, nil, false, []string{"doc_1", "doc_2"}, nil},
		{"timeout returns the customer's list", []reply{{hang: true}}, personalized(), mine, false, nil, true, []string{"newest_1", "newest_2"}, ErrTimeout},
		{"5xx returns the customer's list", []reply{{status: 503, body: `{"code":"unavailable"}`}}, personalized(), mine, false, nil, true, []string{"newest_1", "newest_2"}, nil},
		{"open breaker returns the customer's list without a call", []reply{{status: 500}}, personalized(), mine, true, nil, true, []string{"newest_1", "newest_2"}, ErrCircuitOpen},
		{"nil fallback gives an empty list", []reply{{hang: true}}, personalized(), nil, false, nil, true, nil, ErrTimeout},
		{"consent violation is returned, never hidden", []reply{{body: slateJSON}}, RecommendRequest{Surface: "s", Mode: ModeNonProfiled, UserID: "u_1", SessionID: "s", Context: RecommendContext{Locale: "nl"}}, mine, false, ErrConsentViolation, false, nil, nil},
		{"invalid input is returned, never hidden", []reply{{body: slateJSON}}, RecommendRequest{Mode: ModeCohort, Context: RecommendContext{Locale: "nl"}}, mine, false, ErrInvalidArgument, false, nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRecorder(t, tt.replies...)
			c := r.client(t, func(cfg *Config) { cfg.BreakerThreshold = 1 })
			if tt.openBreaker {
				_, _ = c.Recommend(t.Context(), personalized())
				if !c.BreakerOpen("serve") {
					t.Fatal("setup: breaker not open")
				}
			}
			start := time.Now()
			rec, err := c.RecommendWithFallback(t.Context(), tt.req, tt.fallback)
			if el := time.Since(start); el > 400*time.Millisecond {
				t.Errorf("took %v", el)
			}
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if rec.Fallback != tt.wantFallback || !slices.Equal(rec.ItemIDs, tt.wantIDs) {
				t.Errorf("got %+v", rec)
			}
			if tt.wantFallback && rec.Slate != nil {
				t.Error("fallback must not carry a slate")
			}
			if tt.wantCause != nil && !errors.Is(rec.Cause, tt.wantCause) {
				t.Errorf("cause = %v, want %v", rec.Cause, tt.wantCause)
			}
		})
	}
}

func TestRecommendationReason(t *testing.T) {
	r := newRecorder(t, reply{body: slateJSON})
	rec, err := r.client(t).RecommendWithFallback(t.Context(), personalized(), nil)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct{ item, locale, want string }{
		{"doc_1", "en", "Because you read this"},
		{"doc_2", "en", "Populair"}, // falls back to Dutch
		{"missing", "nl", ""},
	}
	for _, tt := range tests {
		if got := rec.Reason(tt.item, tt.locale); got != tt.want {
			t.Errorf("Reason(%s,%s) = %q, want %q", tt.item, tt.locale, got, tt.want)
		}
	}
	if (Recommendation{Fallback: true}).Reason("x", "nl") != "" {
		t.Error("fallback has no reasons")
	}
}
