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

// Package podidentityissuer issues Pod identities using stable Kubernetes APIs.
package podidentityissuer

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/agent-substrate/substrate/internal/localca"
	"github.com/agent-substrate/substrate/internal/substratex509"
	authv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
)

const Audience = "podidentity.ate.dev"

// Issuer requires an explicit namespace/service-account allowlist. Identity and
// DNS names are derived from current API objects, never from CSR subject fields.
type Issuer struct {
	Client                 kubernetes.Interface
	Pool                   localca.Pool
	AllowedServiceAccounts map[string]bool
}

type Request struct {
	CSR string `json:"csr"`
}
type Response struct {
	CertificateChain string    `json:"certificate_chain"`
	RefreshAt        time.Time `json:"refresh_at"`
}

var errUnauthenticated = errors.New("invalid Pod identity")

func (s *Issuer) identify(ctx context.Context, token string) (*substratex509.PodIdentity, []string, error) {
	if token == "" {
		return nil, nil, errUnauthenticated
	}
	review, err := s.Client.AuthenticationV1().TokenReviews().Create(ctx, &authv1.TokenReview{
		Spec: authv1.TokenReviewSpec{Token: token, Audiences: []string{Audience}},
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("TokenReview: %w", err)
	}
	u := review.Status.User
	parts := strings.Split(u.Username, ":")
	if !review.Status.Authenticated || review.Status.Error != "" || !slices.Contains(review.Status.Audiences, Audience) ||
		len(parts) != 4 || parts[0] != "system" || parts[1] != "serviceaccount" ||
		!s.AllowedServiceAccounts[parts[2]+"/"+parts[3]] {
		return nil, nil, errUnauthenticated
	}
	names, uids := u.Extra["authentication.kubernetes.io/pod-name"], u.Extra["authentication.kubernetes.io/pod-uid"]
	if len(names) != 1 || len(uids) != 1 || names[0] == "" || uids[0] == "" {
		return nil, nil, errUnauthenticated
	}
	pod, err := s.Client.CoreV1().Pods(parts[2]).Get(ctx, names[0], metav1.GetOptions{})
	if err != nil {
		return nil, nil, errUnauthenticated
	}
	if string(pod.UID) != uids[0] || pod.DeletionTimestamp != nil || pod.Spec.ServiceAccountName != parts[3] || pod.Spec.NodeName == "" {
		return nil, nil, errUnauthenticated
	}
	sa, err := s.Client.CoreV1().ServiceAccounts(parts[2]).Get(ctx, parts[3], metav1.GetOptions{})
	if err != nil || string(sa.UID) != u.UID || sa.DeletionTimestamp != nil {
		return nil, nil, errUnauthenticated
	}
	node, err := s.Client.CoreV1().Nodes().Get(ctx, pod.Spec.NodeName, metav1.GetOptions{})
	if err != nil || node.UID == "" || node.DeletionTimestamp != nil {
		return nil, nil, errUnauthenticated
	}
	// Recent apiservers also attest the Node. Check it when supplied; the live
	// Pod -> Node lookup remains mandatory on every supported apiserver.
	for key, expected := range map[string]string{"node-name": node.Name, "node-uid": string(node.UID)} {
		if values := u.Extra["authentication.kubernetes.io/"+key]; len(values) != 0 && (len(values) != 1 || values[0] != expected) {
			return nil, nil, errUnauthenticated
		}
	}
	services, err := s.Client.CoreV1().Services(parts[2]).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("list identity services: %w", err)
	}
	var dns []string
	for _, svc := range services.Items {
		if svc.DeletionTimestamp != nil || len(svc.Spec.Selector) == 0 || !labels.SelectorFromSet(svc.Spec.Selector).Matches(labels.Set(pod.Labels)) {
			continue
		}
		dns = append(dns, svc.Name, svc.Name+"."+pod.Namespace, svc.Name+"."+pod.Namespace+".svc", svc.Name+"."+pod.Namespace+".svc.cluster.local")
	}
	slices.Sort(dns)
	return &substratex509.PodIdentity{
		Namespace: pod.Namespace, ServiceAccountName: sa.Name, ServiceAccountUID: string(sa.UID),
		PodName: pod.Name, PodUID: string(pod.UID), NodeName: node.Name, NodeUID: string(node.UID),
	}, slices.Compact(dns), nil
}

func (s *Issuer) Issue(ctx context.Context, token, csrPEM string) (*Response, error) {
	block, rest := pem.Decode([]byte(csrPEM))
	if block == nil || block.Type != "CERTIFICATE REQUEST" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, fmt.Errorf("invalid CSR PEM")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse CSR: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("CSR proof of possession: %w", err)
	}
	switch key := csr.PublicKey.(type) {
	case ed25519.PublicKey:
	case *ecdsa.PublicKey:
		if key.Curve.Params().BitSize < 256 {
			return nil, fmt.Errorf("weak CSR key")
		}
	case *rsa.PublicKey:
		if key.N.BitLen() < 2048 {
			return nil, fmt.Errorf("weak CSR key")
		}
	default:
		return nil, fmt.Errorf("unsupported CSR key")
	}
	identity, dns, err := s.identify(ctx, token)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	template := &x509.Certificate{
		Subject: pkix.Name{CommonName: rand.Text()}, BasicConstraintsValid: true,
		NotBefore: now.Add(-2 * time.Minute), NotAfter: now.Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		DNSNames:    dns,
		URIs:        []*url.URL{{Scheme: "spiffe", Host: "cluster.local", Path: "/ns/" + identity.Namespace + "/sa/" + identity.ServiceAccountName}},
	}
	if err := substratex509.AddPodIdentityToCertificate(identity, template); err != nil {
		return nil, err
	}
	chain, err := s.Pool.CreateCertificate(template, csr.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("sign Pod identity: %w", err)
	}
	var out bytes.Buffer
	for _, der := range chain {
		if err := pem.Encode(&out, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
			return nil, err
		}
	}
	return &Response{CertificateChain: out.String(), RefreshAt: now.Add(40 * time.Minute)}, nil
}

func (s *Issuer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost || r.URL.Path != "/v1/issue" {
		http.NotFound(w, r)
		return
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request Request
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		http.Error(w, "trailing request data", http.StatusBadRequest)
		return
	}
	response, err := s.Issue(r.Context(), strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), request.CSR)
	if err != nil {
		// Never put tokens, CSR contents, API errors or signing state in a response.
		if errors.Is(err, errUnauthenticated) {
			http.Error(w, "invalid Pod identity", http.StatusForbidden)
		} else {
			http.Error(w, "certificate issuance failed", http.StatusServiceUnavailable)
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}
