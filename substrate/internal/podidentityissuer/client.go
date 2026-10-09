// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
// http://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package podidentityissuer

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Renew rereads both the projected token and trust roots on each attempt. It
// replaces one combined PEM file so readers cannot observe mismatched key/cert.
func Renew(ctx context.Context, endpoint, tokenPath, rootsPath, bundlePath string, mode os.FileMode) (time.Time, error) {
	if mode != 0600 && mode != 0640 {
		return time.Time{}, fmt.Errorf("credential mode must be 0600 or 0640")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return time.Time{}, fmt.Errorf("issuer must be an HTTPS URL without credentials or query")
	}
	token, err := os.ReadFile(tokenPath)
	if err != nil {
		return time.Time{}, err
	}
	rootPEM, err := os.ReadFile(rootsPath)
	if err != nil {
		return time.Time{}, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(rootPEM) {
		return time.Time{}, fmt.Errorf("issuer trust roots are empty")
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return time.Time{}, err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		return time.Time{}, err
	}
	input, err := json.Marshal(Request{CSR: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr}))})
	if err != nil {
		return time.Time{}, err
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/v1/issue"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(input))
	if err != nil {
		return time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("issuer redirects are forbidden") }}
	res, err := client.Do(req)
	if err != nil {
		return time.Time{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return time.Time{}, fmt.Errorf("issuer returned HTTP %d", res.StatusCode)
	}
	var response Response
	if err := json.NewDecoder(io.LimitReader(res.Body, 128<<10)).Decode(&response); err != nil {
		return time.Time{}, err
	}
	block, _ := pem.Decode([]byte(response.CertificateChain))
	if block == nil || block.Type != "CERTIFICATE" {
		return time.Time{}, fmt.Errorf("issuer returned no certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Time{}, err
	}
	issuedKey, ok := cert.PublicKey.(ed25519.PublicKey)
	if !ok || !bytes.Equal(issuedKey, pub) || !response.RefreshAt.After(time.Now()) || !response.RefreshAt.Before(cert.NotAfter) {
		return time.Time{}, fmt.Errorf("invalid issued key or renewal interval")
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return time.Time{}, err
	}
	bundle := append(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), []byte(response.CertificateChain)...)
	if _, err := tls.X509KeyPair(bundle, bundle); err != nil {
		return time.Time{}, err
	}
	if err := writeBundle(bundlePath, bundle, mode); err != nil {
		return time.Time{}, err
	}
	return response.RefreshAt, nil
}

func writeBundle(path string, bundle []byte, mode os.FileMode) error {
	if mode != 0600 && mode != 0640 {
		return fmt.Errorf("credential mode must be 0600 or 0640")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".credential-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(bundle); err != nil {
		return err
	}
	if err := f.Chmod(mode); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
