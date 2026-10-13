package patterna

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestConsentGate(t *testing.T) {
	base := RecommendContext{Locale: "nl"}
	tests := []struct {
		name    string
		req     RecommendRequest
		wantErr error
	}{
		{"missing mode", RecommendRequest{Surface: "s", UserID: "u_1", Context: base}, ErrConsentRequired},
		{"unknown mode", RecommendRequest{Surface: "s", Mode: "everything", Context: base}, ErrConsentRequired},
		{"non_profiled with a user id", RecommendRequest{Surface: "s", Mode: ModeNonProfiled, UserID: "u_1", SessionID: "s_1", Context: base}, ErrConsentViolation},
		{"non_profiled without a session id", RecommendRequest{Surface: "s", Mode: ModeNonProfiled, Context: base}, ErrConsentViolation},
		{"missing surface", RecommendRequest{Mode: ModeCohort, Context: base}, ErrInvalidArgument},
		{"missing locale", RecommendRequest{Surface: "s", Mode: ModeCohort}, ErrInvalidArgument},
		{"non_profiled with session and context only", RecommendRequest{Surface: "s", Mode: ModeNonProfiled, SessionID: "s_1", Context: base}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRecorder(t, reply{body: slateJSON})
			_, err := r.client(t).Recommend(t.Context(), tt.req)
			if !errors.Is(err, tt.wantErr) && !(tt.wantErr == nil && err == nil) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			want := 0
			if tt.wantErr == nil {
				want = 1
			}
			reqs := r.requests()
			if len(reqs) != want {
				t.Fatalf("requests = %d, want %d", len(reqs), want)
			}
			if want == 1 && tt.req.Mode == ModeNonProfiled {
				if _, ok := reqs[0].Body["user_id"]; ok {
					t.Error("non_profiled body carries user_id")
				}
			}
		})
	}
}

// No PII: emails and identity-provider subjects are refused everywhere a user id goes, and the
// refused id never appears in the error text (errors end up in logs and Sentry).
func TestNoPIIUserIDs(t *testing.T) {
	ids := []string{"anna@example.com", "auth0|64f1c2", "google-oauth2|1093", "sms|+31612345678", " "}
	calls := map[string]func(context.Context, *Client, string) error{
		"Recommend": func(ctx context.Context, c *Client, id string) error {
			_, err := c.Recommend(ctx, RecommendRequest{Surface: "s", Mode: ModePersonalized, UserID: id, Context: RecommendContext{Locale: "nl"}})
			return err
		},
		"SetUserMode":    func(ctx context.Context, c *Client, id string) error { return c.SetUserMode(ctx, id, ModeCohort) },
		"SetUserProfile": func(ctx context.Context, c *Client, id string) error { return c.SetUserProfile(ctx, id, UserProfile{}) },
		"DeleteUser": func(ctx context.Context, c *Client, id string) error {
			_, err := c.DeleteUser(ctx, id)
			return err
		},
		"IngestEvents": func(ctx context.Context, c *Client, id string) error {
			e := ev("e1")
			e.UserID = id
			_, err := c.IngestEvents(ctx, []Event{e})
			return err
		},
	}
	r := newRecorder(t, reply{status: 200, body: `{}`})
	c := r.client(t)
	for name, call := range calls {
		for _, id := range ids {
			t.Run(name+"/"+id, func(t *testing.T) {
				err := call(t.Context(), c, id)
				if !errors.Is(err, ErrInvalidArgument) {
					t.Fatalf("err = %v", err)
				}
				if strings.TrimSpace(id) != "" && strings.Contains(err.Error(), id) {
					t.Errorf("error text leaks the id: %v", err)
				}
			})
		}
	}
	if n := len(r.requests()); n != 0 {
		t.Errorf("requests = %d, want 0", n)
	}
}
