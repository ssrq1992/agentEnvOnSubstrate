package gateway

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type bridgeTransport func(*http.Request) (*http.Response, error)

func (f bridgeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestSubstrateProxyPreservesSDKRoutingAndStreaming(t *testing.T) {
	target, _ := url.Parse("https://bridge.internal")
	called := false
	handler := newSubstrateProxy(target, bridgeTransport(func(r *http.Request) (*http.Response, error) {
		called = true
		if r.URL.Host != "bridge.internal" || r.Host != "49983-sandbox.example.com" || r.URL.EscapedPath() != "/files/%2Ftmp%2Fa" || r.URL.RawQuery != "token=a%2Bb" {
			t.Fatalf("routing changed: host=%s URL=%s", r.Host, r.URL)
		}
		if r.Header.Get("X-API-Key") != "api-key" || r.Header.Get("X-Access-Token") != "envd-token" {
			t.Fatal("SDK credentials lost")
		}
		if r.Header.Get("X-Agentenv-Tenant") != "" || r.Header.Get("X-Substrate-Actor") != "" || strings.Contains(r.Header.Get("X-Forwarded-For"), "spoofed") {
			t.Fatal("untrusted identity headers forwarded")
		}
		for _, key := range []string{"Ate-Target-Actor", "Ate-Target-Actor-Uid", "X-Ate-Target-Port"} {
			if r.Header.Get(key) != "" {
				t.Fatalf("untrusted route forwarded: %s", key)
			}
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/connect+proto"}}, Body: io.NopCloser(strings.NewReader("stream"))}, nil
	}))
	req := httptest.NewRequest("POST", "https://49983-sandbox.example.com/files/%2Ftmp%2Fa?token=a%2Bb", strings.NewReader("body"))
	req.Header.Set("X-API-Key", "api-key")
	req.Header.Set("X-Access-Token", "envd-token")
	req.Header.Set("X-Agentenv-Tenant", "spoofed")
	req.Header.Set("X-Substrate-Actor", "spoofed")
	req.Header.Set("X-Forwarded-For", "spoofed")
	for _, key := range []string{"Ate-Target-Actor", "Ate-Target-Actor-Uid", "X-Ate-Target-Port"} {
		req.Header.Set(key, "spoofed")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if !called || response.Code != 200 || response.Body.String() != "stream" {
		t.Fatal("proxy failed")
	}
}
func TestSubstrateProxyDoesNotLeakBackendErrors(t *testing.T) {
	target, _ := url.Parse("https://bridge.internal")
	handler := newSubstrateProxy(target, bridgeTransport(func(*http.Request) (*http.Response, error) { return nil, errors.New("secret endpoint credentials") }))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "https://public.example/sandboxes", nil))
	if response.Code != 502 || strings.Contains(response.Body.String(), "credentials") {
		t.Fatal("backend error leaked")
	}
}
func TestSubstrateRequiresPinnedHTTPSOrigin(t *testing.T) {
	for _, target := range []string{"http://bridge", "https://user:pass@bridge", "https://bridge/prefix", "https://bridge?query", "https://bridge#fragment"} {
		if _, err := NewSubstrateHandler(SubstrateOptions{URL: target}); err == nil {
			t.Fatalf("unsafe bridge URL accepted: %s", target)
		}
	}
}
