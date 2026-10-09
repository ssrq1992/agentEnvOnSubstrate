package servertls

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func material(t *testing.T, dir string, serial int64) (string, string, string) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath, rootsPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem"), filepath.Join(dir, "roots.pem")
	for path, data := range map[string][]byte{certPath: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), keyPath: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: secret}), rootsPath: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})} {
		if err = os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return certPath, keyPath, rootsPath
}
func TestRotationReloadsAndRejectsBrokenMaterial(t *testing.T) {
	dir := t.TempDir()
	cert, key, roots := material(t, dir, 1)
	config, err := Rotating(cert, key, roots)
	if err != nil {
		t.Fatal(err)
	}
	if config.MinVersion != tls.VersionTLS13 || config.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Fatal("client verification disabled")
	}
	material(t, dir, 2)
	next, err := config.GetConfigForClient(nil)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(next.Certificates[0].Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if parsed.SerialNumber.Int64() != 2 || next.GetConfigForClient != nil {
		t.Fatal("rotation not loaded")
	}
	if err = os.WriteFile(roots, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = config.GetConfigForClient(nil); err == nil {
		t.Fatal("invalid trust update accepted")
	}
}
