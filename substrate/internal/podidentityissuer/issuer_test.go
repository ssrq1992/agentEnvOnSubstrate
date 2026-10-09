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
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agent-substrate/substrate/internal/localca"
	"github.com/agent-substrate/substrate/internal/substratex509"
	authv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func fixture(t *testing.T) (*Issuer, *fake.Clientset, *authv1.TokenReviewStatus) {
	t.Helper()
	client := fake.NewSimpleClientset(
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "ate", UID: "pod-1", Labels: map[string]string{"app": "worker"}}, Spec: corev1.PodSpec{ServiceAccountName: "worker", NodeName: "node"}},
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "ate", UID: "sa-1"}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node", UID: "node-1"}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "ate"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "worker"}}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "ate"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "other"}}},
	)
	status := &authv1.TokenReviewStatus{Authenticated: true, Audiences: []string{Audience}, User: authv1.UserInfo{
		Username: "system:serviceaccount:ate:worker", UID: "sa-1", Extra: map[string]authv1.ExtraValue{
			"authentication.kubernetes.io/pod-name": {"worker"}, "authentication.kubernetes.io/pod-uid": {"pod-1"},
			"authentication.kubernetes.io/node-name": {"node"}, "authentication.kubernetes.io/node-uid": {"node-1"},
		},
	}}
	client.PrependReactor("create", "tokenreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
		req := action.(ktesting.CreateAction).GetObject().(*authv1.TokenReview)
		if req.Spec.Token != "test-token" || len(req.Spec.Audiences) != 1 || req.Spec.Audiences[0] != Audience {
			t.Fatal("unexpected TokenReview request")
		}
		return true, &authv1.TokenReview{Status: *status}, nil
	})
	ca, err := localca.GenerateCA("issuer-test", localca.KeyTypeED25519, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return &Issuer{Client: client, Pool: &localca.ConcretePool{CAs: []*localca.CA{ca}}, AllowedServiceAccounts: map[string]bool{"ate/worker": true}}, client, status
}

func maliciousCSR(t *testing.T) string {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{DNSNames: []string{"admin.example"}, URIs: []*url.URL{{Scheme: "spiffe", Host: "cluster.local", Path: "/ns/kube-system/sa/admin"}}}, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
}

func TestIssue(t *testing.T) {
	for _, tc := range []struct {
		name    string
		alter   func(*Issuer, *authv1.TokenReviewStatus)
		invalid bool
	}{
		{"derived identity", func(*Issuer, *authv1.TokenReviewStatus) {}, false},
		{"unauthenticated", func(_ *Issuer, s *authv1.TokenReviewStatus) { s.Authenticated = false }, true},
		{"audience", func(_ *Issuer, s *authv1.TokenReviewStatus) { s.Audiences = []string{"other"} }, true},
		{"unbound token", func(_ *Issuer, s *authv1.TokenReviewStatus) {
			delete(s.User.Extra, "authentication.kubernetes.io/pod-uid")
		}, true},
		{"recreated Pod", func(_ *Issuer, s *authv1.TokenReviewStatus) {
			s.User.Extra["authentication.kubernetes.io/pod-uid"] = authv1.ExtraValue{"old"}
		}, true},
		{"recreated SA", func(_ *Issuer, s *authv1.TokenReviewStatus) { s.User.UID = "old" }, true},
		{"recreated Node", func(_ *Issuer, s *authv1.TokenReviewStatus) {
			s.User.Extra["authentication.kubernetes.io/node-uid"] = authv1.ExtraValue{"old"}
		}, true},
		{"allowlist", func(s *Issuer, _ *authv1.TokenReviewStatus) { s.AllowedServiceAccounts = nil }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, status := fixture(t)
			tc.alter(s, status)
			response, err := s.Issue(context.Background(), "test-token", maliciousCSR(t))
			if tc.invalid {
				if err == nil {
					t.Fatal("accepted invalid identity")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			block, _ := pem.Decode([]byte(response.CertificateChain))
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				t.Fatal(err)
			}
			if len(cert.URIs) != 1 || cert.URIs[0].String() != "spiffe://cluster.local/ns/ate/sa/worker" {
				t.Fatal("CSR identity leaked")
			}
			if strings.Contains(strings.Join(cert.DNSNames, ","), "admin") || strings.Contains(strings.Join(cert.DNSNames, ","), "other") {
				t.Fatal("unrelated DNS identity issued")
			}
			if err := cert.VerifyHostname("worker.ate.svc.cluster.local"); err != nil {
				t.Fatal(err)
			}
			identity, err := substratex509.PodIdentityFromCertificate(cert)
			if err != nil || identity.PodUID != "pod-1" || identity.NodeUID != "node-1" {
				t.Fatalf("identity: %v %v", identity, err)
			}
		})
	}
}

func TestRenewAndAtomicBundle(t *testing.T) {
	s, _, _ := fixture(t)
	server := httptest.NewTLSServer(s)
	defer server.Close()
	dir := t.TempDir()
	roots := filepath.Join(dir, "roots.pem")
	token := filepath.Join(dir, "token")
	bundle := filepath.Join(dir, "identity", "bundle.pem")
	if err := os.WriteFile(roots, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(token, []byte("test-token"), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := Renew(context.Background(), server.URL, token, roots, bundle, 0600); err != nil {
			t.Fatal(err)
		}
	}
	stat, err := os.Stat(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if stat.Mode().Perm() != 0600 {
		t.Fatalf("unsafe permissions: %v", stat.Mode())
	}
	before, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Renew(context.Background(), "http://untrusted", token, roots, bundle, 0600); err == nil {
		t.Fatal("accepted cleartext issuer")
	}
	after, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("failed renewal damaged active certificate")
	}
}

func TestHTTPRejectsUnauthenticatedAndMalformedRequests(t *testing.T) {
	s, _, _ := fixture(t)
	for _, tc := range []struct {
		body, auth string
		code       int
	}{
		{`{}`, "", http.StatusUnauthorized},
		{`{"unknown":1}`, "Bearer test-token", http.StatusBadRequest},
		{`{} {}`, "Bearer test-token", http.StatusBadRequest},
	} {
		r := httptest.NewRequest(http.MethodPost, "/v1/issue", strings.NewReader(tc.body))
		r.Header.Set("Authorization", tc.auth)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Fatalf("got %d want %d", w.Code, tc.code)
		}
	}
}

func TestBundlePublicationPermissions(t *testing.T) {
	for _, mode := range []os.FileMode{0600, 0640} {
		path := filepath.Join(t.TempDir(), "credential.pem")
		for _, content := range []string{"first", "rotated"} {
			if err := writeBundle(path, []byte(content), mode); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != mode {
				t.Fatalf("mode %o, want %o", info.Mode().Perm(), mode)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != content {
				t.Fatal("non-atomic bundle content")
			}
		}
	}
	path := filepath.Join(t.TempDir(), "unsafe.pem")
	if err := writeBundle(path, []byte("key"), 0644); err == nil {
		t.Fatal("world-readable key permitted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("invalid mode wrote credential")
	}
}
