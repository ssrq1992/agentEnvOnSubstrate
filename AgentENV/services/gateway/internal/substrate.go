package gateway

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"time"
)

// SubstrateOptions selects the API bridge as the sole routing/lifecycle backend.
// The bridge authenticates both API keys and per-sandbox traffic credentials.
type SubstrateOptions struct {
	URL                   string
	CredentialBundle      string
	TrustBundle           string
	ResponseHeaderTimeout time.Duration
}

func NewSubstrateHandler(options SubstrateOptions) (http.Handler, error) {
	target, err := url.Parse(options.URL)
	if err != nil || target.Scheme != "https" || target.Hostname() == "" || target.User != nil || target.Path != "" || target.RawQuery != "" || target.Fragment != "" {
		return nil, fmt.Errorf("bridge URL must be an HTTPS origin")
	}
	load := func() (*tls.Config, error) {
		certificate, err := tls.LoadX509KeyPair(options.CredentialBundle, options.CredentialBundle)
		if err != nil {
			return nil, err
		}
		roots, err := os.ReadFile(options.TrustBundle)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(roots) {
			return nil, errors.New("invalid bridge trust bundle")
		}
		return &tls.Config{MinVersion: tls.VersionTLS13, NextProtos: []string{"h2", "http/1.1"}, ServerName: target.Hostname(), RootCAs: pool, Certificates: []tls.Certificate{certificate}}, nil
	}
	if _, err := load(); err != nil {
		return nil, err
	}
	transport := &http.Transport{ForceAttemptHTTP2: true, ResponseHeaderTimeout: options.ResponseHeaderTimeout, IdleConnTimeout: 30 * time.Second, MaxIdleConns: 100,
		DialTLSContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			cfg, err := load()
			if err != nil {
				return nil, err
			}
			return (&tls.Dialer{NetDialer: &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}, Config: cfg}).DialContext(ctx, network, address)
		},
	}
	return newSubstrateProxy(target, transport), nil
}
func newSubstrateProxy(target *url.URL, transport http.RoundTripper) http.Handler {
	proxy := &httputil.ReverseProxy{Transport: transport, FlushInterval: -1,
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			// Host selects an SDK sandbox/port. Destination routing remains pinned to
			// target; the bridge resolves and authorizes the original Host itself.
			r.Out.Host = r.In.Host
			r.Out.Header.Del("X-Agentenv-Tenant")
			r.Out.Header.Del("X-Substrate-Actor")
			r.Out.Header.Del("X-Agentenv-Node-Id")
			r.Out.Header.Del("Ate-Target-Actor")
			r.Out.Header.Del("Ate-Target-Actor-Uid")
			r.Out.Header.Del("Ate-Target-Assignment-Generation")
			r.Out.Header.Del("X-Ate-Target-Port")
			r.SetXForwarded()
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, "Substrate bridge unavailable", http.StatusBadGateway)
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = http.NewResponseController(w).EnableFullDuplex()
		proxy.ServeHTTP(w, r)
	})
}
