package control

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc/credentials"
)

func testCertificate(t *testing.T, serial int64) (tls.Certificate, []byte) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(serial), DNSNames: []string{"control.test"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
func handshake(t *testing.T, c credentials.TransportCredentials, cert tls.Certificate) error {
	t.Helper()
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	deadline := time.Now().Add(3 * time.Second)
	_ = client.SetDeadline(deadline)
	_ = server.SetDeadline(deadline)
	done := make(chan error, 1)
	go func() {
		done <- tls.Server(server, &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, NextProtos: []string{"h2"}}).Handshake()
	}()
	ctx, cancel := context.WithDeadline(t.Context(), deadline)
	defer cancel()
	_, _, err := c.ClientHandshake(ctx, "control.test", client)
	// Close the pipe before joining a server whose peer rejected its certificate.
	client.Close()
	<-done
	return err
}
func TestControlTrustRotationOnClonedTransport(t *testing.T) {
	cert, roots := testCertificate(t, 1)
	path := filepath.Join(t.TempDir(), "roots.pem")
	if err := os.WriteFile(path, roots, 0600); err != nil {
		t.Fatal(err)
	}
	c, err := controlCredentials("control.test", path)
	if err != nil {
		t.Fatal(err)
	}
	clone := c.Clone()
	if err = handshake(t, clone, cert); err != nil {
		t.Fatal(err)
	}
	next, nextRoots := testCertificate(t, 2)
	if err = handshake(t, clone, next); err == nil {
		t.Fatal("untrusted server accepted")
	}
	if err = os.WriteFile(path, append(roots, nextRoots...), 0600); err != nil {
		t.Fatal(err)
	}
	if err = handshake(t, clone, next); err != nil {
		t.Fatal("overlapping roots were not reloaded", err)
	}
	if err = os.WriteFile(path, nextRoots, 0600); err != nil {
		t.Fatal(err)
	}
	if err = handshake(t, clone, cert); err == nil {
		t.Fatal("removed root still trusted")
	}
	if err = clone.OverrideServerName("other.test"); err != nil {
		t.Fatal(err)
	}
	if err = handshake(t, clone, next); err == nil {
		t.Fatal("hostname validation bypassed")
	}
	if err = os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = clone.ClientHandshake(t.Context(), "control.test", nil); err == nil {
		t.Fatal("invalid roots accepted")
	}
}
