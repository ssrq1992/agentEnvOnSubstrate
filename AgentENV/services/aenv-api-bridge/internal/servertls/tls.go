// Package servertls reloads atomically published certificates and overlapping
// trust bundles for each new connection. Existing connections retain their
// authenticated identity until they close.
package servertls

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

func Load(certPath, keyPath, rootsPath string) (*tls.Config, error) {
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("load bridge server certificate: %w", err)
	}
	pem, err := os.ReadFile(rootsPath)
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("empty gateway trust bundle")
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert, NextProtos: []string{"h2", "http/1.1"}}, nil
}
func Rotating(certPath, keyPath, rootsPath string) (*tls.Config, error) {
	initial, err := Load(certPath, keyPath, rootsPath)
	if err != nil {
		return nil, err
	}
	initial.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) { return Load(certPath, keyPath, rootsPath) }
	return initial, nil
}
