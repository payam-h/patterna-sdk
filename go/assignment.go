package patterna

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// AssignmentUnit returns the value for RecommendRequest.AssignmentUnit: the lowercase hex
// HMAC-SHA256 of userID under secret (the UTF-8 bytes of both strings).
//
// Use it for non_profiled traffic under a holdout, so one person lands in one arm across
// rotating sessions. The secret is yours, per experiment, and never sent to Patterna; the engine
// uses the value only for arm assignment and the ledger's unit hash, never stores it raw, never
// joins it to events and never ranks with it (engine 0.2). The same function exists in
// @patterna/sdk and patterna-sdk (Python) with a shared test vector.
func AssignmentUnit(secret, userID string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(userID))
	return hex.EncodeToString(mac.Sum(nil))
}
