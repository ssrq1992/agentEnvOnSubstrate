package auth

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestGatewayAndTenantBothRequired(t *testing.T) {
	key := "tenant-a-secret-api-key"
	digest := sha256.Sum256([]byte(key))
	a, err := New([]Tenant{{ID: "a", Atespace: "space-a", APIKeySHA256: hex.EncodeToString(digest[:]), ControlTokenFile: "/projected/token"}}, []string{"spiffe://ate/gateway"})
	if err != nil {
		t.Fatal(err)
	}
	uri, _ := url.Parse("spiffe://ate/gateway")
	cert := &x509.Certificate{Raw: []byte("gateway certificate"), URIs: []*url.URL{uri}}
	for _, name := range []string{"valid", "spoofed tenant", "no tls", "no verified chain", "wrong gateway", "wrong key", "duplicate key"} {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "https://bridge/sandboxes", nil)
			r.Header.Set("X-API-Key", key)
			r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}, VerifiedChains: [][]*x509.Certificate{{cert}}}
			switch name {
			case "spoofed tenant":
				r.Header.Set("X-Agentenv-Tenant", "another-tenant")
			case "no tls":
				r.TLS = nil
			case "no verified chain":
				r.TLS.VerifiedChains = nil
			case "wrong gateway":
				other, _ := url.Parse("spiffe://ate/attacker")
				c := &x509.Certificate{Raw: []byte("other"), URIs: []*url.URL{other}}
				r.TLS.PeerCertificates = []*x509.Certificate{c}
				r.TLS.VerifiedChains = [][]*x509.Certificate{{c}}
			case "wrong key":
				r.Header.Set("X-API-Key", "unknown-secret-api-key")
			case "duplicate key":
				r.Header.Add("X-API-Key", key)
			}
			tenant, err := a.Authenticate(r)
			valid := name == "valid" || name == "spoofed tenant"
			if (err == nil) != valid {
				t.Fatalf("unexpected authentication: %v", err)
			}
			if valid && (tenant.ID != "a" || tenant.Atespace != "space-a") {
				t.Fatal("client changed tenant binding")
			}
		})
	}
}
func TestControlCredentialRotationAndBounds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	tenant := Tenant{ControlTokenFile: path}
	for _, token := range []string{"first-token", "second-token"} {
		if err := os.WriteFile(path, []byte(token+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		got, err := tenant.ControlToken()
		if err != nil || got != token {
			t.Fatalf("rotation: %q %v", got, err)
		}
	}
	if err := os.WriteFile(path, []byte("two\ncredentials"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.ControlToken(); err == nil {
		t.Fatal("header injection accepted")
	}
	if err := os.WriteFile(path, make([]byte, 16385), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.ControlToken(); err == nil {
		t.Fatal("oversized credential accepted")
	}
}
