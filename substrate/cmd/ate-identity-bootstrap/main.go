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

// Command ate-identity-bootstrap writes fresh issuer credentials to a private
// manifest file. It does not connect to Kubernetes or overwrite existing keys.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"math/big"
	"os"
	"time"

	"github.com/agent-substrate/substrate/internal/localca"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

func bootstrap(namespace, service string) ([]byte, error) {
	if len(validation.IsDNS1123Label(namespace)) != 0 || len(validation.IsDNS1123Label(service)) != 0 {
		return nil, fmt.Errorf("DNS namespace and service names required")
	}
	identity, err := localca.GenerateCA("identity-1", localca.KeyTypeED25519, 365*24*time.Hour)
	if err != nil {
		return nil, err
	}
	tlsCA, err := localca.GenerateCA("issuer-tls-1", localca.KeyTypeED25519, 365*24*time.Hour)
	if err != nil {
		return nil, err
	}
	identities := &localca.ConcretePool{CAs: []*localca.CA{identity}, ActiveForSigning: identity.ID}
	issuerTLS := &localca.ConcretePool{CAs: []*localca.CA{tlsCA}, ActiveForSigning: tlsCA.ID}
	pool, err := localca.Marshal(identities)
	if err != nil {
		return nil, err
	}
	tlsPool, err := localca.Marshal(issuerTLS)
	if err != nil {
		return nil, err
	}
	identityRoot, err := identity.TLSCertificateChainPEM()
	if err != nil {
		return nil, err
	}
	tlsRoot, err := tlsCA.TLSCertificateChainPEM()
	if err != nil {
		return nil, err
	}
	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	now := time.Now()
	chain, err := issuerTLS.CreateCertificate(&x509.Certificate{SerialNumber: serial, DNSNames: []string{service, service + "." + namespace, service + "." + namespace + ".svc", service + "." + namespace + ".svc.cluster.local"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(90 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}, public)
	if err != nil {
		return nil, err
	}
	cert := []byte{}
	for _, der := range chain {
		cert = append(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	secret := corev1.Secret{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"}, ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "podidentity-issuer-credentials"}, Type: corev1.SecretTypeOpaque, Data: map[string][]byte{"pool.json": pool, "tls-ca-pool.json": tlsPool, "tls.crt": cert, "tls.key": pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})}}
	roots := corev1.ConfigMap{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"}, ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "ate-identity-roots"}, Data: map[string]string{"issuer-ca.crt": string(tlsRoot), "podidentity-ca.crt": string(identityRoot), "servicedns-ca.crt": string(identityRoot)}}
	return json.MarshalIndent(map[string]any{"apiVersion": "v1", "kind": "List", "items": []any{secret, roots}}, "", "  ")
}
func writeNew(path string, data []byte) error {
	if path == "" {
		return fmt.Errorf("explicit private output file required")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(path)
		}
	}()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}
func main() {
	namespace := flag.String("namespace", "ate-system", "namespace of the issuer")
	service := flag.String("service", "podidentity-issuer", "issuer Service name")
	output := flag.String("output", "", "new private JSON manifest file (never stdout)")
	flag.Parse()
	if *output == "" {
		fmt.Fprintln(os.Stderr, "--output is required")
		os.Exit(1)
	}
	data, err := bootstrap(*namespace, *service)
	if err == nil {
		err = writeNew(*output, data)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
