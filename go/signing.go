package patterna

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

// Event signing, identical to @patterna/sdk (both test suites share one vector):
// the event without `signature`, as JSON with object keys sorted, no whitespace, HTML characters
// unescaped, U+2028/U+2029 escaped; the signature is lowercase hex HMAC-SHA256 over it.
// Backend keys already mark events as signed; sign when an event travels through an untrusted
// hop (an app, a queue) before it reaches Patterna.
//
// The key is the bytes of the secret string exactly as the API returned it (CreateSigningSecret,
// the panel). Never hex-decode it first: all three SDKs and the engine (0.2) use the string's
// bytes, and the shared vector uses the key "test-signing-key".

// CanonicalJSON returns the canonical form of v used for signing.
func CanonicalJSON(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // keep numbers exactly as written
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	// encoding/json writes map keys sorted, which is the canonical order.
	if err := enc.Encode(generic); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// EventSignature returns the hex HMAC-SHA256 of the event's canonical form.
func EventSignature(e Event, secret string) (string, error) {
	if len(secret) == 0 {
		return "", errors.New("patterna: signing secret is empty")
	}
	e.Signature = ""
	canon, err := CanonicalJSON(e)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(canon)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// SignEvent returns a copy of e with Signature set.
func SignEvent(e Event, secret string) (Event, error) {
	sig, err := EventSignature(e, secret)
	if err != nil {
		return Event{}, err
	}
	e.Signature = sig
	return e, nil
}
