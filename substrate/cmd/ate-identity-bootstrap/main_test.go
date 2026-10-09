// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"github.com/agent-substrate/substrate/internal/localca"
	corev1 "k8s.io/api/core/v1"
	"os"
	"path/filepath"
	"testing"
)

func TestBootstrapCertificateAndTrustIsolation(t *testing.T) {
	data, err := bootstrap("ate-system", "podidentity-issuer")
	if err != nil {
		t.Fatal(err)
	}
	var list struct{ Items []json.RawMessage }
	if err = json.Unmarshal(data, &list); err != nil {
		t.Fatal(err)
	}
	var secret corev1.Secret
	var roots corev1.ConfigMap
	if err = json.Unmarshal(list.Items[0], &secret); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(list.Items[1], &roots); err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(secret.Data["tls.crt"], secret.Data["tls.key"])
	if err != nil {
		t.Fatal(err)
	}
	anchors := x509.NewCertPool()
	if !anchors.AppendCertsFromPEM([]byte(roots.Data["issuer-ca.crt"])) {
		t.Fatal("issuer roots missing")
	}
	for _, dns := range []string{"podidentity-issuer.ate-system.svc", "podidentity-issuer.ate-system.svc.cluster.local"} {
		if _, err = cert.Leaf.Verify(x509.VerifyOptions{Roots: anchors, DNSName: dns}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = cert.Leaf.Verify(x509.VerifyOptions{Roots: anchors, DNSName: "attacker.ate-system.svc"}); err == nil {
		t.Fatal("unexpected DNS identity")
	}
	if roots.Data["issuer-ca.crt"] == roots.Data["podidentity-ca.crt"] {
		t.Fatal("issuer TLS and Pod identity authorities are not separated")
	}
	for _, name := range []string{"pool.json", "tls-ca-pool.json"} {
		pool, err := localca.Unmarshal(secret.Data[name])
		if err != nil || pool.ActiveForSigning == "" {
			t.Fatalf("invalid signing pool: %v", err)
		}
	}
	if roots.Data["podidentity-ca.crt"] != roots.Data["servicedns-ca.crt"] {
		t.Fatal("combined Pod certificate root mismatch")
	}
	if _, ok := roots.Data["tls.key"]; ok {
		t.Fatal("private key in public ConfigMap")
	}
}
func TestPrivateOutputNeverOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bootstrap.json")
	if err := writeNew(path, []byte("original")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("bootstrap key file is not private")
	}
	if err = writeNew(path, []byte("replacement")); err == nil {
		t.Fatal("existing keys overwritten")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "original" {
		t.Fatal("original output changed")
	}
	link := filepath.Join(t.TempDir(), "link")
	if err = os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if err = writeNew(link, []byte("replacement")); err == nil {
		t.Fatal("followed output symlink")
	}
	if _, err = bootstrap("../other", "issuer"); err == nil {
		t.Fatal("invalid namespace allowed")
	}
}
