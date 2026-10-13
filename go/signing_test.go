package patterna

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"
)

// Shared with packages/sdk-ts/test/signing.test.ts: both SDKs must produce this signature.
const vectorSignature = "8baf729960f0d6390df4232b164a7bffab057e3c8a174148dedd0f324504aac9"

const testSigningKey = "test-signing-key" // a fixture string, not a secret

func vectorEvent() Event {
	nine := 9
	return Event{
		EventID:    "ev_42",
		Type:       "rec_click",
		OccurredAt: time.Date(2026, 10, 13, 9, 15, 0, 0, time.UTC),
		Source:     "backend",
		UserID:     "u_7f3a",
		SessionID:  "s_abc",
		RequestID:  "req_1",
		Context:    &EventContext{Platform: "ios", Locale: "nl", HourBucket: &nine},
		// Built from a rune so no editor turns the escape into the raw character.
		Props: map[string]any{"position": 2, "item_id": "doc_1", "title": "<b>Zon & zee</b>" + string(rune(0x2028)) + "x"},
	}
}

func TestCanonicalJSON(t *testing.T) {
	ls := `\u` + "2028"
	tests := []struct {
		name string
		in   any
		want string
	}{
		{"sorted keys, nested", map[string]any{"b": 1, "a": map[string]any{"d": []any{3, 1}, "c": nil}}, `{"a":{"c":null,"d":[3,1]},"b":1}`},
		{"html left alone, line separator escaped", "<a&b>" + string(rune(0x2028)), `"<a&b>` + ls + `"`},
		{"event drops empty optional fields", Event{EventID: "e", Type: "t", Source: "backend", OccurredAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
			`{"event_id":"e","occurred_at":"2026-01-02T03:04:05Z","source":"backend","type":"t"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CanonicalJSON(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
}

func TestEventSignature(t *testing.T) {
	e := vectorEvent()
	canon, err := CanonicalJSON(e)
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, []byte(testSigningKey))
	mac.Write(canon)
	independent := hex.EncodeToString(mac.Sum(nil))

	tests := []struct {
		name string
		in   Event
		want string
	}{
		{"matches an independent HMAC", e, independent},
		{"matches the cross-language vector", e, vectorSignature},
		{"ignores an existing signature", func() Event { x := vectorEvent(); x.Signature = "stale"; return x }(), vectorSignature},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := EventSignature(tt.in, testSigningKey)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("got %s, want %s", got, tt.want)
			}
		})
	}

	signed, err := SignEvent(e, testSigningKey)
	if err != nil || signed.Signature != vectorSignature || e.Signature != "" {
		t.Errorf("SignEvent = %+v, %v", signed.Signature, err)
	}
	if _, err := EventSignature(e, ""); err == nil {
		t.Error("empty secret accepted")
	}
}
