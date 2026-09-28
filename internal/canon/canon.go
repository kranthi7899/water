// Package canon produces the canonical JSON form that approvals and the audit
// log hash. Keys are sorted at every depth and numbers keep their literal
// text, so two payloads hash equal only when they would execute identically.
package canon

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// JSON returns v as compact JSON with sorted object keys.
func JSON(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return nil, err
	}
	return json.Marshal(generic)
}

// Hash returns the hex sha256 of JSON(v).
func Hash(v any) (string, error) {
	b, err := JSON(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
