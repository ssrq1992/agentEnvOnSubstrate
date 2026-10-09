// Package auth binds SDK credentials to a server-configured tenant and
// Substrate namespace. Headers supplied by the client never select a tenant.
package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

type Tenant struct {
	ID           string `json:"id"`
	Atespace     string `json:"atespace"`
	APIKeySHA256 string `json:"apiKeySHA256"`
	// A short-lived OIDC access token authorized for this tenant's Atespace.
	// The projected/rotated file is read for each control-plane request.
	ControlTokenFile string `json:"controlTokenFile"`
}
type entry struct {
	tenant Tenant
	digest [32]byte
}
type Authenticator struct {
	entries           []entry
	gatewayIdentities map[string]bool
}

func New(tenants []Tenant, gatewayIdentities []string) (*Authenticator, error) {
	if len(tenants) == 0 || len(gatewayIdentities) == 0 {
		return nil, fmt.Errorf("tenant credentials and gateway identities required")
	}
	a := &Authenticator{gatewayIdentities: map[string]bool{}}
	for _, identity := range gatewayIdentities {
		parsed, err := url.Parse(identity)
		if err != nil || parsed.Scheme != "spiffe" || parsed.Host == "" || parsed.Port() != "" || parsed.User != nil || parsed.Path == "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" || parsed.String() != identity {
			return nil, fmt.Errorf("canonical gateway SPIFFE identity required")
		}
		a.gatewayIdentities[identity] = true
	}
	ids := map[string]bool{}
	keys := map[string]bool{}
	spaces := map[string]bool{}
	for _, t := range tenants {
		if t.ID == "" || t.Atespace == "" || t.ControlTokenFile == "" || strings.ContainsAny(t.ID+t.Atespace, "\x00\r\n") {
			return nil, fmt.Errorf("complete tenant configuration required")
		}
		raw, err := hex.DecodeString(t.APIKeySHA256)
		if err != nil || len(raw) != 32 || strings.ToLower(t.APIKeySHA256) != t.APIKeySHA256 {
			return nil, fmt.Errorf("canonical API key SHA256 required")
		}
		if ids[t.ID] || keys[t.APIKeySHA256] || spaces[t.Atespace] {
			return nil, fmt.Errorf("duplicate tenant, namespace, or credential")
		}
		ids[t.ID] = true
		keys[t.APIKeySHA256] = true
		spaces[t.Atespace] = true
		e := entry{tenant: t}
		copy(e.digest[:], raw)
		a.entries = append(a.entries, e)
	}
	return a, nil
}
func (a *Authenticator) trustedGateway(state *tls.ConnectionState) bool {
	if state == nil || len(state.VerifiedChains) == 0 || len(state.PeerCertificates) == 0 {
		return false
	}
	leaf := state.PeerCertificates[0]
	if len(leaf.URIs) != 1 {
		return false
	}
	// The HTTP server must require and verify client certificates. Verify that
	// the presented leaf is actually the leaf of a verified chain as well.
	verified := false
	for _, chain := range state.VerifiedChains {
		if len(chain) > 0 && chain[0].Equal(leaf) {
			verified = true
		}
	}
	return verified && a.gatewayIdentities[leaf.URIs[0].String()]
}
func (a *Authenticator) Authenticate(r *http.Request) (Tenant, error) {
	if !a.trustedGateway(r.TLS) {
		return Tenant{}, fmt.Errorf("untrusted gateway identity")
	}
	keys := r.Header.Values("X-API-Key")
	if len(keys) != 1 || len(keys[0]) == 0 || len(keys[0]) > 4096 {
		return Tenant{}, fmt.Errorf("API key required")
	}
	digest := sha256.Sum256([]byte(keys[0]))
	var match *Tenant
	// Scan all configured digests; no user-controlled tenant lookup or raw key
	// persistence, and no prefix matching of credentials.
	for i := range a.entries {
		if subtle.ConstantTimeCompare(digest[:], a.entries[i].digest[:]) == 1 {
			match = &a.entries[i].tenant
		}
	}
	if match == nil {
		return Tenant{}, fmt.Errorf("invalid API key")
	}
	return *match, nil
}
func (t Tenant) ControlToken() (string, error) {
	file, err := os.Open(t.ControlTokenFile)
	if err != nil {
		return "", fmt.Errorf("control credential unavailable")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 16384 {
		return "", fmt.Errorf("invalid control credential file")
	}
	data, err := io.ReadAll(io.LimitReader(file, 16385))
	if err != nil || len(data) == 0 || len(data) > 16384 {
		return "", fmt.Errorf("invalid control credential")
	}
	token := strings.TrimSpace(string(data))
	if token == "" || strings.ContainsAny(token, "\x00\r\n \t") {
		return "", fmt.Errorf("invalid control credential")
	}
	return token, nil
}

// TrustedGateway verifies transport identity without requiring an API key.
// Data requests additionally authenticate their sandbox-scoped credential.
func (a *Authenticator) TrustedGateway(r *http.Request) bool { return a.trustedGateway(r.TLS) }
