package control

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"

	"google.golang.org/grpc/credentials"
)

// Each reconnect reads the current overlapping trust bundle. Clone retains
// this behavior when gRPC creates transports or overrides a server name.
type rotatingCredentials struct {
	credentials.TransportCredentials
	rootsPath string
}

func controlCredentials(serverName, rootsPath string) (credentials.TransportCredentials, error) {
	current, err := loadControlCredentials(serverName, rootsPath)
	if err != nil {
		return nil, err
	}
	return &rotatingCredentials{TransportCredentials: current, rootsPath: rootsPath}, nil
}
func loadControlCredentials(serverName, rootsPath string) (credentials.TransportCredentials, error) {
	pem, err := os.ReadFile(rootsPath)
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("empty control trust bundle")
	}
	return credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: serverName}), nil
}
func (c *rotatingCredentials) ClientHandshake(ctx context.Context, authority string, raw net.Conn) (net.Conn, credentials.AuthInfo, error) {
	current, err := loadControlCredentials(c.Info().ServerName, c.rootsPath)
	if err != nil {
		return nil, nil, err
	}
	// The configured control identity is authoritative, even when the dial
	// target uses a different service alias or IP address.
	return current.ClientHandshake(ctx, c.Info().ServerName, raw)
}
func (c *rotatingCredentials) Clone() credentials.TransportCredentials {
	return &rotatingCredentials{TransportCredentials: c.TransportCredentials.Clone(), rootsPath: c.rootsPath}
}
