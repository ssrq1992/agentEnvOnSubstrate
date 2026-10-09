// Package dataplane authenticates SDK traffic before routing it through Substrate.
package dataplane

import (
	"agentenv/services/aenv-api-bridge/internal/metadata"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
)

type Tokens struct{ key []byte }

func NewTokens(key []byte) (*Tokens, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("32-byte data credential key required")
	}
	return &Tokens{key: append([]byte(nil), key...)}, nil
}
func LoadTokens(path string) (*Tokens, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("data credential key unavailable")
	}
	return NewTokens(data)
}

// Token is independent of a VM allocation, so an authorized SDK can wake a
// paused sandbox. The runtime token is injected only after allocation lookup.
func (t *Tokens) Token(b metadata.Sandbox, purpose string) string {
	payload, _ := json.Marshal([]string{"aenv-bridge-v1", purpose, b.Tenant, b.ExternalID, b.ActorUID})
	mac := hmac.New(sha256.New, t.key)
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (t *Tokens) Valid(b metadata.Sandbox, purpose, candidate string) bool {
	return b.ActorUID != "" && hmac.Equal([]byte(t.Token(b, purpose)), []byte(candidate))
}
