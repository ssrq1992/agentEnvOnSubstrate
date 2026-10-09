package dataplane

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"
)

// Transport pins the destination to an internal Substrate HTTPS ingress.
// New connections reload certificates and roots during rotation overlap.
func Transport(origin, rootsPath, bundlePath string) (*url.URL, *http.Transport, error) {
	target, err := url.Parse(origin)
	if err != nil || target.Scheme != "https" || target.Hostname() == "" || target.User != nil || target.Path != "" || target.RawQuery != "" || target.Fragment != "" {
		return nil, nil, fmt.Errorf("HTTPS ingress origin required")
	}
	load := func() (*tls.Config, error) {
		roots, err := os.ReadFile(rootsPath)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(roots) {
			return nil, fmt.Errorf("invalid ingress roots")
		}
		cert, err := tls.LoadX509KeyPair(bundlePath, bundlePath)
		if err != nil {
			return nil, err
		}
		return &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool, ServerName: target.Hostname(), Certificates: []tls.Certificate{cert}, NextProtos: []string{"h2", "http/1.1"}}, nil
	}
	if _, err = load(); err != nil {
		return nil, nil, err
	}
	address := target.Host
	if target.Port() == "" {
		address = net.JoinHostPort(target.Hostname(), "443")
	}
	transport := &http.Transport{ForceAttemptHTTP2: true, IdleConnTimeout: 30 * time.Second, MaxIdleConns: 128, ResponseHeaderTimeout: 2 * time.Minute,
		DialTLSContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			cfg, err := load()
			if err != nil {
				return nil, err
			}
			return (&tls.Dialer{NetDialer: &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}, Config: cfg}).DialContext(ctx, network, address)
		},
	}
	return target, transport, nil
}
